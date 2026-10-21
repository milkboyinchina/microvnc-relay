package web

import (
	"encoding/json"
	"errors"
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
	cfg              *config.Config
	configPath       string
	meter            *proxy.TrafficMeter
	pinger           *telemetry.Pinger
	appLogger        *logger.Logger
	auth             *AuthManager
	wsHub            *WSHub
	startTime        time.Time
	mode1Reloader    func([]config.DeviceConfig) error
	bindErrorsGetter func() map[string]string
	admission        *proxy.AdmissionController
	version          string
	limiter          *LoginRateLimiter
}

// SetAdmissionController attaches the shared session admission controller.
func (a *APIHandler) SetAdmissionController(admission *proxy.AdmissionController) {
	a.admission = admission
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
		limiter:       NewLoginRateLimiter(),
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

const (
	maxAuthBodyBytes   = 64 << 10
	maxDeviceBodyBytes = 1 << 20
)

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
		"auth_broken":    a.auth.AuthBroken(),
		"version":        a.version,
	})
}

func (a *APIHandler) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if a.auth.AuthBroken() {
		writeError(w, http.StatusForbidden, "auth.json exists but is invalid; delete it and restart to reset credentials")
		return
	}
	if !a.auth.SetupRequired() {
		writeError(w, http.StatusForbidden, "Administrator credentials have already been configured")
		return
	}

	clientIP := ExtractClientIP(r)
	if locked, remaining, delay := a.limiter.Check(clientIP); locked {
		retryAfter := int(remaining.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":               "Too many failed setup attempts. Try again later.",
			"retry_after_seconds": retryAfter,
		})
		return
	} else if delay > 0 {
		retryAfter := int(delay.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":               "Too many failed setup attempts. Try again shortly.",
			"retry_after_seconds": retryAfter,
		})
		return
	}

	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		SetupToken string `json:"setup_token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if req.SetupToken == "" {
		req.SetupToken = strings.TrimSpace(r.Header.Get("X-Setup-Token"))
	}

	if err := a.auth.SetAdminCredentials(req.Username, req.Password, req.SetupToken); err != nil {
		switch {
		case errors.Is(err, ErrInvalidSetupToken):
			a.limiter.RecordFailure(clientIP)
			writeError(w, http.StatusForbidden, "Invalid or expired setup token")
		case errors.Is(err, ErrAlreadyConfigured):
			writeError(w, http.StatusForbidden, "Administrator credentials have already been configured")
		case errors.Is(err, ErrBcryptBusy):
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               "Credential hashing is busy. Retry shortly.",
				"retry_after_seconds": 2,
			})
		case strings.Contains(err.Error(), "username"), strings.Contains(err.Error(), "password"):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			slog.Error("failed to complete first-run setup", "err", err)
			writeError(w, http.StatusInternalServerError, "Failed to persist administrator credentials")
		}
		return
	}

	a.limiter.RecordSuccess(clientIP)
	token, err := a.auth.CreateSession(req.Username)
	if err != nil {
		slog.Error("credentials saved but session creation failed", "err", err)
		writeError(w, http.StatusInternalServerError, "Credentials saved, but session creation failed; please log in")
		return
	}
	a.auth.SetSessionCookie(w, token)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := ExtractClientIP(r)

	// Check if this IP is currently locked out or subject to progressive delay.
	locked, remaining, delay := a.limiter.Check(clientIP)
	if locked {
		retryAfter := int(remaining.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":               "Too many failed login attempts. Account temporarily locked for security.",
			"retry_after_seconds": retryAfter,
		})
		return
	}

	// Progressive-delay gate: reject immediately with 429 instead of sleeping.
	// A sleeping handler pins a goroutine and socket per attempt — a
	// self-inflicted slowloris on a single-core Pi (audit C12 / Gate 0).
	if delay > 0 {
		retryAfter := int(delay.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":               "Too many failed login attempts. Please retry shortly.",
			"retry_after_seconds": retryAfter,
		})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	ok, err := a.auth.VerifyCredentials(req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, ErrBcryptBusy):
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               "Credential verification is busy. Retry shortly.",
				"retry_after_seconds": 2,
			})
		case errors.Is(err, ErrAuthFileBroken):
			writeError(w, http.StatusServiceUnavailable, "auth.json is invalid; delete it and restart to reset credentials")
		default:
			slog.Error("credential verification failed", "err", err)
			writeError(w, http.StatusInternalServerError, "Authentication is temporarily unavailable")
		}
		return
	}

	if !ok {
		lockedNow, _, lockoutDur := a.limiter.RecordFailure(clientIP)
		if lockedNow {
			retryAfter := int(lockoutDur.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               "Too many failed login attempts. Locked out for 15 minutes.",
				"retry_after_seconds": retryAfter,
			})
			return
		}
		writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	a.limiter.RecordSuccess(clientIP)
	token, err := a.auth.CreateSession(req.Username)
	if err != nil {
		slog.Error("failed to create session", "err", err)
		writeError(w, http.StatusInternalServerError, "Session creation failed")
		return
	}
	a.auth.SetSessionCookie(w, token)

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
			"devices":     devices,
			"statuses":    statuses,
			"bind_errors": a.getBindErrors(),
		})

	case http.MethodPost:
		var dev config.DeviceConfig
		r.Body = http.MaxBytesReader(w, r.Body, maxDeviceBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&dev); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON payload")
			return
		}

		before := a.cfg.GetDevices()
		err := a.cfg.ApplyDeviceTransaction(func(devices []config.DeviceConfig) ([]config.DeviceConfig, error) {
			for _, existing := range devices {
				if existing.ID == dev.ID {
					return nil, errDeviceExists
				}
			}
			return append(devices, dev), nil
		})
		if err != nil {
			slog.Error("failed to add device", "device_id", dev.ID, "err", err)
			writeDeviceTransactionError(w, err)
			return
		}

		reloadErr := a.reloadMode1()
		if a.pinger != nil {
			a.pinger.TriggerSweep()
		}

		bindErrs := a.getBindErrors()
		devBindErr := bindErrs[dev.ID]
		if devBindErr == "" && reloadErr != nil {
			devBindErr = reloadErr.Error()
		}
		if devBindErr != "" {
			if rbErr := a.rollbackDevices(before); rbErr != nil {
				slog.Error("failed to roll back device add after activation failure", "device_id", dev.ID, "err", rbErr)
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":          false,
				"error":       "device activation failed; change rolled back",
				"device":      dev,
				"bind_error":  devBindErr,
				"bind_errors": a.getBindErrors(),
			})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"ok":          true,
			"device":      dev,
			"bind_error":  "",
			"bind_errors": bindErrs,
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
		status, err := a.pinger.ProbeDeviceNow(dev)
		if err != nil {
			switch {
			case errors.Is(err, telemetry.ErrProbeBusy):
				w.Header().Set("Retry-After", "1")
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":               "Probe scheduler is busy. Retry shortly.",
					"retry_after_seconds": 1,
				})
			case errors.Is(err, telemetry.ErrProbeStopped):
				writeError(w, http.StatusServiceUnavailable, "Probe scheduler is shutting down")
			default:
				writeError(w, http.StatusInternalServerError, "Probe failed")
			}
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var dev config.DeviceConfig
		r.Body = http.MaxBytesReader(w, r.Body, maxDeviceBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&dev); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON payload")
			return
		}
		if dev.ID == "" {
			dev.ID = id
		}
		if dev.ID != id {
			writeError(w, http.StatusBadRequest, "device ID cannot be changed")
			return
		}

		before := a.cfg.GetDevices()
		err := a.cfg.ApplyDeviceTransaction(func(devices []config.DeviceConfig) ([]config.DeviceConfig, error) {
			for i := range devices {
				if devices[i].ID == id {
					devices[i] = dev
					return devices, nil
				}
			}
			return nil, errDeviceNotFound
		})
		if err != nil {
			slog.Error("failed to update device", "device_id", id, "err", err)
			writeDeviceTransactionError(w, err)
			return
		}

		reloadErr := a.reloadMode1()
		if a.pinger != nil {
			a.pinger.TriggerSweep()
		}

		bindErrs := a.getBindErrors()
		devBindErr := bindErrs[dev.ID]
		if devBindErr == "" && reloadErr != nil {
			devBindErr = reloadErr.Error()
		}
		if devBindErr != "" {
			if rbErr := a.rollbackDevices(before); rbErr != nil {
				slog.Error("failed to roll back device update after activation failure", "device_id", dev.ID, "err", rbErr)
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":          false,
				"error":       "device activation failed; change rolled back",
				"device":      dev,
				"bind_error":  devBindErr,
				"bind_errors": a.getBindErrors(),
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"device":      dev,
			"bind_error":  "",
			"bind_errors": bindErrs,
		})

	case http.MethodDelete:
		before := a.cfg.GetDevices()
		err := a.cfg.ApplyDeviceTransaction(func(devices []config.DeviceConfig) ([]config.DeviceConfig, error) {
			for i := range devices {
				if devices[i].ID == id {
					return append(devices[:i], devices[i+1:]...), nil
				}
			}
			return nil, errDeviceNotFound
		})
		if err != nil {
			slog.Error("failed to delete device", "device_id", id, "err", err)
			writeDeviceTransactionError(w, err)
			return
		}

		if reloadErr := a.reloadMode1(); reloadErr != nil {
			if rbErr := a.rollbackDevices(before); rbErr != nil {
				slog.Error("failed to roll back device delete after activation failure", "device_id", id, "err", rbErr)
			}
			writeError(w, http.StatusInternalServerError, "device delete activation failed; change rolled back: "+reloadErr.Error())
			return
		}
		if a.pinger != nil {
			a.pinger.TriggerSweep()
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

	var activeSessionsCount int
	if a.meter != nil {
		activeSessionsCount = len(a.meter.GetSessions())
	}

	metrics := map[string]any{
		"uptime_seconds":        int64(time.Since(a.startTime).Seconds()),
		"memory_rss_bytes":      rssBytes,
		"memory_rss_mb":         float64(rssBytes) / (1024 * 1024),
		"heap_alloc_mb":         float64(memStats.Alloc) / (1024 * 1024),
		"goroutines":            runtime.NumGoroutine(),
		"total_rx_bytes_sec":    traffic.TotalRxBytesSec,
		"total_tx_bytes_sec":    traffic.TotalTxBytesSec,
		"total_cumulative_rx":   traffic.TotalCumulativeRx,
		"total_cumulative_tx":   traffic.TotalCumulativeTx,
		"active_sessions_count": activeSessionsCount,
	}

	if a.wsHub != nil {
		ws := a.wsHub.Stats()
		metrics["ws_clients"] = ws.Clients
		metrics["ws_max_clients"] = ws.MaxClients
		metrics["ws_evictions"] = ws.Evicted
		metrics["ws_origin_rejections"] = ws.OriginRejected
		metrics["ws_version_rejections"] = ws.VersionRejected
	}
	if a.admission != nil {
		metrics["admission"] = a.admission.Snapshot()
	}

	spliceDirections, spliceFallbacks := proxy.SpliceStats()
	metrics["splice_directions_total"] = spliceDirections
	metrics["splice_fallbacks_total"] = spliceFallbacks

	return metrics
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
		"bind_errors":     a.getBindErrors(),
	}

	data, _ := json.Marshal(payload)
	return data
}

func (a *APIHandler) getBindErrors() map[string]string {
	if a.bindErrorsGetter != nil {
		return a.bindErrorsGetter()
	}
	return nil
}

// reloadMode1 applies the current in-memory device list to the live listeners.
func (a *APIHandler) reloadMode1() error {
	if a.mode1Reloader == nil {
		return nil
	}
	return a.mode1Reloader(a.cfg.GetDevices())
}

// rollbackDevices restores a pre-mutation device snapshot (memory + disk) and
// reapplies it to the live listeners. Used when activation fails after a
// successful persist, so the API never reports success for a device that is
// not actually listening.
func (a *APIHandler) rollbackDevices(before []config.DeviceConfig) error {
	snapshot := append([]config.DeviceConfig(nil), before...)
	err := a.cfg.ApplyDeviceTransaction(func([]config.DeviceConfig) ([]config.DeviceConfig, error) {
		return snapshot, nil
	})
	if err != nil {
		return err
	}
	if a.pinger != nil {
		a.pinger.TriggerSweep()
	}
	return a.reloadMode1()
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

var (
	errDeviceExists   = errors.New("device ID already exists")
	errDeviceNotFound = errors.New("device not found")
)

func writeDeviceTransactionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, config.ErrPersistence):
		writeError(w, http.StatusInternalServerError, "Failed to persist target configuration")
	case errors.Is(err, errDeviceExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errDeviceNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case strings.Contains(err.Error(), "collision"), strings.Contains(err.Error(), "conflict"):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
