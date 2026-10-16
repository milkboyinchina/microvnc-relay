package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"github.com/milkboyinchina/microvnc-relay/internal/logger"
	"github.com/milkboyinchina/microvnc-relay/internal/proxy"
	"github.com/milkboyinchina/microvnc-relay/internal/telemetry"
)

// APIHandler coordinates REST and WebSocket endpoints for WebUI management.
type APIHandler struct {
	cfg            *config.Config
	configPath     string
	meter          *proxy.TrafficMeter
	pinger         *telemetry.Pinger
	appLogger      *logger.Logger
	auth           *AuthManager
	wsHub          *WSHub
	startTime      time.Time
	mode1Reloader  func([]config.DeviceConfig) error
	version        string
}

// NewAPIHandler initializes the API router and telemetry hub.
func NewAPIHandler(
	cfg *config.Config,
	configPath string,
	meter *proxy.TrafficMeter,
	pinger *telemetry.Pinger,
	appLogger *logger.Logger,
	auth *AuthManager,
	wsHub *WSHub,
	mode1Reloader func([]config.DeviceConfig) error,
	version string,
) *APIHandler {
	return &APIHandler{
		cfg:           cfg,
		configPath:    configPath,
		meter:         meter,
		pinger:        pinger,
		appLogger:     appLogger,
		auth:          auth,
		wsHub:         wsHub,
		startTime:     time.Now(),
		mode1Reloader: mode1Reloader,
		version:       version,
	}
}

// RegisterRoutes registers all REST and WebSocket endpoints onto mux.
func (a *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	// Public auth endpoints
	mux.HandleFunc("/api/v1/auth/status", a.handleAuthStatus)
	mux.HandleFunc("/api/v1/auth/setup", a.handleAuthSetup)
	mux.HandleFunc("/api/v1/login", a.handleLogin)
	mux.HandleFunc("/api/v1/logout", a.handleLogout)

	// Protected management endpoints (wrapped by RequireAuth)
	wrap := func(h http.HandlerFunc) http.Handler {
		return a.auth.RequireAuth(h)
	}

	mux.Handle("/api/v1/devices", wrap(a.handleDevices))
	mux.Handle("/api/v1/devices/", wrap(a.handleDeviceByID))
	mux.Handle("/api/v1/sessions", wrap(a.handleSessions))
	mux.Handle("/api/v1/sessions/", wrap(a.handleSessionByID))
	mux.Handle("/api/v1/metrics", wrap(a.handleMetrics))
	mux.Handle("/api/v1/logs", wrap(a.handleLogs))
	mux.Handle("/api/v1/system/reset-counters", wrap(a.handleResetCounters))
	mux.Handle("/api/v1/ws/telemetry", wrap(a.handleTelemetryWS))
}

func (a *APIHandler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie(sessionCookieName)
	authenticated := !a.cfg.Security.AuthEnabled || (err == nil && a.auth.ValidateSession(cookie.Value))

	writeJSON(w, http.StatusOK, map[string]any{
		"auth_enabled":   a.cfg.Security.AuthEnabled,
		"authenticated":  authenticated,
		"setup_required": a.auth.SetupRequired(),
		"version":        a.version,
	})
}

func (a *APIHandler) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.auth.SetupRequired() {
		writeError(w, http.StatusBadRequest, "Administrator credentials have already been configured")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	if err := a.auth.SetAdminCredentials(req.Username, req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	token := a.auth.CreateSession(req.Username)
	a.auth.SetSessionCookie(w, token)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	if !a.auth.VerifyCredentials(req.Username, req.Password) {
		writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	token := a.auth.CreateSession(req.Username)
	a.auth.SetSessionCookie(w, token)

	// Persist potentially auto-upgraded bcrypt password
	if a.configPath != "" {
		_ = a.cfg.Save(a.configPath)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		a.auth.RevokeSession(cookie.Value)
	}
	a.auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIHandler) handleDevices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		devices := a.cfg.GetDevices()
		var statuses map[string]telemetry.DeviceProbeStatus
		if a.pinger != nil {
			statuses = a.pinger.GetAllStatuses()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"devices":  devices,
			"statuses": statuses,
		})

	case http.MethodPost:
		var dev config.DeviceConfig
		if err := json.NewDecoder(r.Body).Decode(&dev); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON payload")
			return
		}

		if _, exists := a.cfg.FindDevice(dev.ID); exists {
			writeError(w, http.StatusBadRequest, "device ID already exists")
			return
		}

		if err := a.cfg.AddOrUpdateDevice(dev); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		if a.configPath != "" {
			if err := a.cfg.Save(a.configPath); err != nil {
				slog.Error("Failed to save config after adding device", "err", err)
			}
		}

		if a.mode1Reloader != nil {
			_ = a.mode1Reloader(a.cfg.GetDevices())
		}
		if a.pinger != nil && dev.Enabled {
			go a.pinger.ProbeDevice(dev)
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"ok":     true,
			"device": dev,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *APIHandler) handleDeviceByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/devices/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, "Missing device ID")
		return
	}

	// Route: POST /api/v1/devices/{id}/test
	if len(parts) == 2 && parts[1] == "test" {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dev, found := a.findDevice(id)
		if !found {
			writeError(w, http.StatusNotFound, "Device not found")
			return
		}
		if a.pinger == nil {
			writeError(w, http.StatusBadRequest, "Latency pinger disabled")
			return
		}
		status := a.pinger.ProbeDevice(dev)
		writeJSON(w, http.StatusOK, status)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var dev config.DeviceConfig
		if err := json.NewDecoder(r.Body).Decode(&dev); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON payload")
			return
		}
		if _, exists := a.cfg.FindDevice(id); !exists {
			writeError(w, http.StatusNotFound, "Device not found")
			return
		}

		if err := a.cfg.AddOrUpdateDevice(dev); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		if a.configPath != "" {
			_ = a.cfg.Save(a.configPath)
		}
		if a.mode1Reloader != nil {
			_ = a.mode1Reloader(a.cfg.GetDevices())
		}
		if a.pinger != nil && dev.Enabled {
			go a.pinger.ProbeDevice(dev)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"device": dev,
		})

	case http.MethodDelete:
		if !a.cfg.DeleteDevice(id) {
			writeError(w, http.StatusNotFound, "Device not found")
			return
		}

		if a.configPath != "" {
			_ = a.cfg.Save(a.configPath)
		}
		if a.mode1Reloader != nil {
			_ = a.mode1Reloader(a.cfg.GetDevices())
		}
		if a.pinger != nil {
			go a.pinger.ProbeAll()
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *APIHandler) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sessions []proxy.SessionSnapshot
	if a.meter != nil {
		sessions = a.meter.GetSessions()
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (a *APIHandler) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "Missing session ID")
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if a.meter == nil || !a.meter.DisconnectSession(id) {
		writeError(w, http.StatusNotFound, "Session not found or already closed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIHandler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	metrics := a.CollectMetrics()
	writeJSON(w, http.StatusOK, metrics)
}

func (a *APIHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var entries []logger.LogEntry
	if a.appLogger != nil {
		entries = a.appLogger.Entries()
	}
	writeJSON(w, http.StatusOK, entries)
}

func (a *APIHandler) handleResetCounters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if a.meter != nil {
		a.meter.ResetCounters()
	}
	if a.appLogger != nil {
		a.appLogger.Reset()
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIHandler) handleTelemetryWS(w http.ResponseWriter, r *http.Request) {
	if a.wsHub == nil {
		http.Error(w, "WebSocket hub disabled", http.StatusServiceUnavailable)
		return
	}

	if err := a.wsHub.HandleUpgrade(w, r); err != nil {
		slog.Warn("WebSocket upgrade failed", "err", err)
	}
}

// CollectMetrics gathers process RSS, Go runtime memory, uptime, and traffic totals.
func (a *APIHandler) CollectMetrics() map[string]any {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	rssBytes := readProcessRSSBytes()
	if rssBytes == 0 {
		rssBytes = memStats.Sys
	}

	var traffic proxy.TrafficSnapshot
	if a.meter != nil {
		traffic = a.meter.Snapshot(false)
	}

	return map[string]any{
		"uptime_seconds":        int64(time.Since(a.startTime).Seconds()),
		"memory_rss_bytes":      rssBytes,
		"memory_rss_mb":         float64(rssBytes) / (1024 * 1024),
		"heap_alloc_mb":         float64(memStats.Alloc) / (1024 * 1024),
		"goroutines":            runtime.NumGoroutine(),
		"total_rx_bytes_sec":    traffic.TotalRxBytesSec,
		"total_tx_bytes_sec":    traffic.TotalTxBytesSec,
		"total_cumulative_rx":   traffic.TotalCumulativeRx,
		"total_cumulative_tx":   traffic.TotalCumulativeTx,
		"active_sessions_count": len(a.meter.GetSessions()),
	}
}

// BuildTelemetryPayload creates the 1Hz JSON snapshot pushed to connected WebUI clients.
func (a *APIHandler) BuildTelemetryPayload() []byte {
	metrics := a.CollectMetrics()
	var sessions []proxy.SessionSnapshot
	if a.meter != nil {
		sessions = a.meter.GetSessions()
	}
	var statuses map[string]telemetry.DeviceProbeStatus
	if a.pinger != nil {
		statuses = a.pinger.GetAllStatuses()
	}

	payload := map[string]any{
		"metrics":         metrics,
		"sessions":        sessions,
		"device_statuses": statuses,
	}

	data, _ := json.Marshal(payload)
	return data
}

func (a *APIHandler) findDevice(id string) (config.DeviceConfig, bool) {
	for _, dev := range a.cfg.GetDevices() {
		if dev.ID == id {
			return dev, true
		}
	}
	return config.DeviceConfig{}, false
}

func readProcessRSSBytes() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 2 {
		residentPages, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			return residentPages * uint64(os.Getpagesize())
		}
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
