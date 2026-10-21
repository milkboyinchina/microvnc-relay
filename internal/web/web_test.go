package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"github.com/milkboyinchina/microvnc-relay/internal/logger"
	"github.com/milkboyinchina/microvnc-relay/internal/proxy"
	"github.com/milkboyinchina/microvnc-relay/internal/telemetry"
)

func setupTestWebServer(t *testing.T, authEnabled bool) (*Server, *config.Config, *proxy.TrafficMeter, *logger.Logger) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Server.WebBind = "127.0.0.1:0"
	cfg.Features.WebUIEnabled = true
	cfg.Security.AuthEnabled = authEnabled
	cfg.Security.Username = "admin"
	// Precomputed bcrypt hash for "secret123" at cost 4. Using a low cost keeps
	// the HTTP integration suite fast; plaintext auto-upgrade is covered by
	// TestAuthPlaintextConfigHashIsUpgraded.
	cfg.Security.PasswordHash = "$2a$04$EbJ/8vBHPJSq.0Um0Ptyu.bo7EtTlnr0KnOtAE.NurZ0MzFbgnUsi"
	cfg.Devices = []config.DeviceConfig{
		{
			ID:          "lab-rpi",
			Name:        "Lab Pi",
			Host:        "127.0.0.1",
			Port:        5900,
			VirtualPort: 5910,
			Enabled:     true,
		},
	}

	meter := proxy.NewTrafficMeter(true)
	appLogger, err := logger.New(cfg.Logging, io.Discard)
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}

	pinger := telemetry.NewPinger(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	reloaderCalled := false
	server, err := NewServer(
		cfg,
		"",
		meter,
		pinger,
		appLogger,
		func(devs []config.DeviceConfig) error {
			reloaderCalled = true
			return nil
		},
		"0.1.0-test",
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}

	_ = reloaderCalled
	return server, cfg, meter, appLogger
}

func TestStaticAssetsServing(t *testing.T) {
	server, _, _, _ := setupTestWebServer(t, false)
	defer server.Stop(context.Background())

	addr := "http://" + server.Addr()

	// 1. GET / (index.html)
	resp, err := http.Get(addr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "μVNC Relay") {
		t.Error("index.html does not contain 'μVNC Relay'")
	}

	// 2. GET /style.css
	respCSS, err := http.Get(addr + "/style.css")
	if err != nil {
		t.Fatalf("GET /style.css: %v", err)
	}
	defer respCSS.Body.Close()
	if respCSS.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for style.css, got %d", respCSS.StatusCode)
	}

	// 3. GET /app.js
	respJS, err := http.Get(addr + "/app.js")
	if err != nil {
		t.Fatalf("GET /app.js: %v", err)
	}
	defer respJS.Body.Close()
	if respJS.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for app.js, got %d", respJS.StatusCode)
	}
}

func TestAuthFlowAndProtection(t *testing.T) {
	server, _, _, _ := setupTestWebServer(t, true)
	defer server.Stop(context.Background())

	addr := "http://" + server.Addr()

	// 1. Access protected /api/v1/devices without cookie -> 401
	resp, err := http.Get(addr + "/api/v1/devices")
	if err != nil {
		t.Fatalf("GET /api/v1/devices: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}

	// 2. Invalid login -> 401
	badLogin, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrongpassword"})
	respBad, err := http.Post(addr+"/api/v1/login", "application/json", bytes.NewReader(badLogin))
	if err != nil {
		t.Fatalf("POST /api/v1/login: %v", err)
	}
	_ = respBad.Body.Close()
	if respBad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", respBad.StatusCode)
	}

	// 3. Valid login -> 200 and session cookie
	goodLogin, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret123"})
	respGood, err := http.Post(addr+"/api/v1/login", "application/json", bytes.NewReader(goodLogin))
	if err != nil {
		t.Fatalf("POST /api/v1/login: %v", err)
	}
	defer respGood.Body.Close()
	if respGood.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for valid login, got %d", respGood.StatusCode)
	}

	cookies := respGood.Cookies()
	var sessCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessCookie = c
			break
		}
	}
	if sessCookie == nil || sessCookie.Value == "" {
		t.Fatal("expected non-empty uvnc_session cookie after successful login")
	}

	// 4. Access protected endpoint with session cookie -> 200 OK
	req, _ := http.NewRequest(http.MethodGet, addr+"/api/v1/devices", nil)
	req.AddCookie(sessCookie)
	client := &http.Client{}
	respAuthed, err := client.Do(req)
	if err != nil {
		t.Fatalf("authed GET /api/v1/devices: %v", err)
	}
	defer respAuthed.Body.Close()
	if respAuthed.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with session cookie, got %d", respAuthed.StatusCode)
	}
}

func TestDeviceCRUDAndMetricsEndpoints(t *testing.T) {
	server, cfg, meter, _ := setupTestWebServer(t, false)
	defer server.Stop(context.Background())

	addr := "http://" + server.Addr()

	// 1. GET /api/v1/devices
	resp, err := http.Get(addr + "/api/v1/devices")
	if err != nil {
		t.Fatalf("GET /api/v1/devices: %v", err)
	}
	defer resp.Body.Close()
	var devResp struct {
		Devices []config.DeviceConfig `json:"devices"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&devResp)
	if len(devResp.Devices) != 1 || devResp.Devices[0].ID != "lab-rpi" {
		t.Fatalf("unexpected devices: %+v", devResp.Devices)
	}

	// 2. POST /api/v1/devices
	newDev := config.DeviceConfig{
		ID:          "kiosk-02",
		Name:        "Kiosk 2",
		Host:        "192.168.1.55",
		Port:        5900,
		VirtualPort: 5912,
		Enabled:     true,
	}
	body, _ := json.Marshal(newDev)
	respPost, err := http.Post(addr+"/api/v1/devices", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/devices: %v", err)
	}
	defer respPost.Body.Close()
	if respPost.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", respPost.StatusCode)
	}

	if len(cfg.GetDevices()) != 2 {
		t.Fatalf("expected 2 devices in cfg, got %d", len(cfg.GetDevices()))
	}

	// 2b. POST /api/v1/devices with colliding virtual_port -> 409 Conflict
	conflictDev := config.DeviceConfig{
		ID:          "kiosk-conflict",
		Name:        "Kiosk Conflict",
		Host:        "192.168.1.56",
		Port:        5900,
		VirtualPort: 5912, // collides with kiosk-02
		Enabled:     true,
	}
	conflictBody, _ := json.Marshal(conflictDev)
	respConflict, err := http.Post(addr+"/api/v1/devices", "application/json", bytes.NewReader(conflictBody))
	if err != nil {
		t.Fatalf("POST conflict device: %v", err)
	}
	_ = respConflict.Body.Close()
	if respConflict.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on port collision, got %d", respConflict.StatusCode)
	}
	if len(cfg.GetDevices()) != 2 {
		t.Fatalf("expected devices count to remain 2 after conflict, got %d", len(cfg.GetDevices()))
	}

	// 3. DELETE /api/v1/devices/kiosk-02
	reqDel, _ := http.NewRequest(http.MethodDelete, addr+"/api/v1/devices/kiosk-02", nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE device: %v", err)
	}
	_ = respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on delete, got %d", respDel.StatusCode)
	}
	if len(cfg.GetDevices()) != 1 {
		t.Fatalf("expected 1 device after delete, got %d", len(cfg.GetDevices()))
	}

	// 4. Session management and Disconnect
	sess := meter.RegisterSession("lab-rpi", "Lab Pi", "mode1", "127.0.0.1:54321", "127.0.0.1:5900", func() {})
	sess.AddRx(1024)
	sess.AddTx(2048)

	respSess, err := http.Get(addr + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET /api/v1/sessions: %v", err)
	}
	defer respSess.Body.Close()
	var sList struct {
		Sessions []proxy.SessionSnapshot `json:"sessions"`
	}
	_ = json.NewDecoder(respSess.Body).Decode(&sList)
	if len(sList.Sessions) != 1 || sList.Sessions[0].SessionID != sess.ID {
		t.Fatalf("unexpected sessions list: %+v", sList.Sessions)
	}

	reqDisc, _ := http.NewRequest(http.MethodDelete, addr+"/api/v1/sessions/"+sess.ID, nil)
	respDisc, err := http.DefaultClient.Do(reqDisc)
	if err != nil {
		t.Fatalf("DELETE session: %v", err)
	}
	_ = respDisc.Body.Close()
	if respDisc.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on session disconnect, got %d", respDisc.StatusCode)
	}

	// 5. GET /api/v1/metrics
	respMet, err := http.Get(addr + "/api/v1/metrics")
	if err != nil {
		t.Fatalf("GET /api/v1/metrics: %v", err)
	}
	defer respMet.Body.Close()
	var metrics map[string]any
	_ = json.NewDecoder(respMet.Body).Decode(&metrics)
	if _, ok := metrics["memory_rss_bytes"]; !ok {
		t.Error("expected memory_rss_bytes in metrics response")
	}

	// 6. POST /api/v1/system/reset-counters
	respReset, err := http.Post(addr+"/api/v1/system/reset-counters", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/v1/system/reset-counters: %v", err)
	}
	_ = respReset.Body.Close()
	if respReset.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on reset, got %d", respReset.StatusCode)
	}
}

func TestWebSocketTelemetryUpgrade(t *testing.T) {
	server, _, _, _ := setupTestWebServer(t, false)
	defer server.Stop(context.Background())

	// Connect TCP socket directly to perform RFC 6455 handshake
	conn, err := net.DialTimeout("tcp", server.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	defer conn.Close()

	handshake := "GET /api/v1/ws/telemetry HTTP/1.1\r\n" +
		"Host: " + server.Addr() + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"

	if _, err := conn.Write([]byte(handshake)); err != nil {
		t.Fatalf("write ws handshake: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read ws handshake response: %v", err)
	}

	respStr := string(buf[:n])
	if !strings.Contains(respStr, "101 Switching Protocols") {
		t.Fatalf("expected 101 Switching Protocols, got: %s", respStr)
	}
	if !strings.Contains(respStr, "Sec-WebSocket-Accept:") {
		t.Fatalf("expected Sec-WebSocket-Accept header in: %s", respStr)
	}
}

func TestFirstRunAuthSetupAndReset(t *testing.T) {
	useFastBcrypt(t)
	tmpDir := t.TempDir()
	configPath := tmpDir + "/config.yaml"
	authPath := tmpDir + "/auth.json"

	cfg := config.DefaultConfig()
	cfg.Server.WebBind = "127.0.0.1:0"
	cfg.Features.WebUIEnabled = true
	cfg.Security.AuthEnabled = true
	cfg.Security.PasswordHash = "" // Trigger setup_required!

	meter := proxy.NewTrafficMeter(false)
	appLogger, _ := logger.New(cfg.Logging, io.Discard)
	pinger := telemetry.NewPinger(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	server, err := NewServer(cfg, configPath, meter, pinger, appLogger, nil, "0.1.0-test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	defer server.Stop(context.Background())

	addr := "http://" + server.Addr()

	// 1. GET /api/v1/auth/status -> setup_required: true
	resp, err := http.Get(addr + "/api/v1/auth/status")
	if err != nil {
		t.Fatalf("GET /auth/status: %v", err)
	}
	var st struct {
		AuthEnabled   bool `json:"auth_enabled"`
		Authenticated bool `json:"authenticated"`
		SetupRequired bool `json:"setup_required"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	_ = resp.Body.Close()

	if !st.SetupRequired {
		t.Fatal("expected setup_required: true on first run with empty password")
	}

	// 2. Access protected endpoint before setup -> 403 Forbidden
	respDev, err := http.Get(addr + "/api/v1/devices")
	if err != nil {
		t.Fatalf("GET /devices: %v", err)
	}
	_ = respDev.Body.Close()
	if respDev.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden before setup, got %d", respDev.StatusCode)
	}

	// The one-time setup token is printed to stdout/journal and kept in memory.
	server.auth.mu.RLock()
	setupToken := server.auth.setupToken
	server.auth.mu.RUnlock()
	if setupToken == "" {
		t.Fatal("expected a non-empty first-run setup token")
	}

	// 3. POST without the token -> 403 (token-only setup, no localhost bypass)
	noTokenPayload, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "mypassword123",
	})
	respNoToken, err := http.Post(addr+"/api/v1/auth/setup", "application/json", bytes.NewReader(noTokenPayload))
	if err != nil {
		t.Fatalf("POST /auth/setup without token: %v", err)
	}
	_ = respNoToken.Body.Close()
	if respNoToken.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without setup token, got %d", respNoToken.StatusCode)
	}

	// 4. POST with the correct token -> creates auth.json and logs in
	setupPayload, _ := json.Marshal(map[string]string{
		"username":    "admin",
		"password":    "mypassword123",
		"setup_token": setupToken,
	})
	respSetup, err := http.Post(addr+"/api/v1/auth/setup", "application/json", bytes.NewReader(setupPayload))
	if err != nil {
		t.Fatalf("POST /auth/setup: %v", err)
	}
	defer respSetup.Body.Close()
	if respSetup.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from setup, got %d", respSetup.StatusCode)
	}

	// Verify auth.json was created on disk
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("expected auth.json to exist at %s: %v", authPath, err)
	}

	// 5. A second setup attempt must be refused even with the old token.
	respAgain, err := http.Post(addr+"/api/v1/auth/setup", "application/json", bytes.NewReader(setupPayload))
	if err != nil {
		t.Fatalf("POST /auth/setup again: %v", err)
	}
	_ = respAgain.Body.Close()
	if respAgain.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on second setup, got %d", respAgain.StatusCode)
	}

	// Verify session cookie was returned
	cookies := respSetup.Cookies()
	var sessCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessCookie = c
			break
		}
	}
	if sessCookie == nil {
		t.Fatal("expected session cookie after setup")
	}

	// 4. Subsequent GET /api/v1/auth/status with cookie -> setup_required: false, authenticated: true
	req, _ := http.NewRequest(http.MethodGet, addr+"/api/v1/auth/status", nil)
	req.AddCookie(sessCookie)
	respAuthed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed status: %v", err)
	}
	var stAuthed struct {
		SetupRequired bool `json:"setup_required"`
		Authenticated bool `json:"authenticated"`
	}
	_ = json.NewDecoder(respAuthed.Body).Decode(&stAuthed)
	_ = respAuthed.Body.Close()

	if stAuthed.SetupRequired || !stAuthed.Authenticated {
		t.Fatalf("unexpected authed state: %+v", stAuthed)
	}
}

func TestIndexHTMLModalNesting(t *testing.T) {
	data, err := os.ReadFile("../../web/dist/index.html")
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	html := string(data)

	loginIdx := strings.Index(html, `id="login-modal"`)
	setupIdx := strings.Index(html, `id="setup-modal"`)
	if loginIdx == -1 || setupIdx == -1 {
		t.Fatalf("expected both login-modal and setup-modal in index.html (login: %d, setup: %d)", loginIdx, setupIdx)
	}
	if loginIdx >= setupIdx {
		t.Fatalf("expected login-modal to precede setup-modal")
	}

	// Between login-modal opening and setup-modal opening, all divs must be balanced
	between := html[loginIdx:setupIdx]
	openDivs := strings.Count(between, "<div")
	closeDivs := strings.Count(between, "</div>")

	if openDivs != closeDivs {
		t.Fatalf("login-modal is not properly closed before setup-modal: open <div count=%d, close </div> count=%d (diff=%d)",
			openDivs, closeDivs, openDivs-closeDivs,
		)
	}
}

func TestDeviceCRUDPersistFailureReturns500AndRollsBack(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	configYAML := fmt.Sprintf(`
server:
  web_bind: "127.0.0.1:0"
  vnc_bind: "127.0.0.1"
  targets_file: "blocker/targets.yaml"
features:
  webui_enabled: true
  mode1_enabled: false
  mode2_enabled: false
  latency_probes_enabled: false
  bandwidth_metering_enabled: false
security:
  auth_enabled: false
devices:
  - id: "keep"
    name: "Keep"
    host: "127.0.0.1"
    port: 5900
    virtual_port: 5910
    enabled: true
`)
	if err := os.WriteFile(configPath, []byte(configYAML), 0640); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	meter := proxy.NewTrafficMeter(false)
	appLogger, err := logger.New(cfg.Logging, io.Discard)
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	pinger := telemetry.NewPinger(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	server, err := NewServer(cfg, configPath, meter, pinger, appLogger, nil, "test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	defer server.Stop(context.Background())

	addr := "http://" + server.Addr()

	// POST must fail with 500 and leave the existing device list untouched.
	newDev := config.DeviceConfig{ID: "new", Host: "127.0.0.1", Port: 5901, VirtualPort: 5911, Enabled: true}
	body, _ := json.Marshal(newDev)
	respPost, err := http.Post(addr+"/api/v1/devices", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST device: %v", err)
	}
	_ = respPost.Body.Close()
	if respPost.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 on persist failure, got %d", respPost.StatusCode)
	}

	devices := cfg.GetDevices()
	if len(devices) != 1 || devices[0].ID != "keep" {
		t.Fatalf("POST failure mutated devices: %+v", devices)
	}

	// PUT must fail with 500 and preserve the old name.
	updated := config.DeviceConfig{ID: "keep", Name: "Changed", Host: "127.0.0.1", Port: 5900, VirtualPort: 5910, Enabled: true}
	body, _ = json.Marshal(updated)
	reqPut, _ := http.NewRequest(http.MethodPut, addr+"/api/v1/devices/keep", bytes.NewReader(body))
	reqPut.Header.Set("Content-Type", "application/json")
	respPut, err := http.DefaultClient.Do(reqPut)
	if err != nil {
		t.Fatalf("PUT device: %v", err)
	}
	_ = respPut.Body.Close()
	if respPut.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 on persist failure, got %d", respPut.StatusCode)
	}
	if dev, ok := cfg.FindDevice("keep"); !ok || dev.Name != "Keep" {
		t.Fatalf("PUT failure mutated device: %+v ok=%v", dev, ok)
	}

	// DELETE must fail with 500 and keep the device.
	reqDel, _ := http.NewRequest(http.MethodDelete, addr+"/api/v1/devices/keep", nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE device: %v", err)
	}
	_ = respDel.Body.Close()
	if respDel.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 on persist failure, got %d", respDel.StatusCode)
	}
	if _, ok := cfg.FindDevice("keep"); !ok {
		t.Fatal("DELETE failure removed the device from memory")
	}
}

func TestDeviceActivationFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := fmt.Sprintf(`
server:
  web_bind: "127.0.0.1:0"
  vnc_bind: "127.0.0.1"
features:
  webui_enabled: true
  mode1_enabled: true
  mode2_enabled: false
  latency_probes_enabled: false
  bandwidth_metering_enabled: false
security:
  auth_enabled: false
devices: []
`)
	if err := os.WriteFile(configPath, []byte(configYAML), 0640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	// Occupy a port so the Mode 1 activation must fail after persistence.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer blocker.Close()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port

	meter := proxy.NewTrafficMeter(false)
	appLogger, err := logger.New(cfg.Logging, io.Discard)
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	pinger := telemetry.NewPinger(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	mode1 := proxy.NewMode1Manager(cfg, meter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := mode1.Start(); err != nil {
		t.Fatalf("mode1.Start: %v", err)
	}
	defer mode1.Stop(context.Background())

	server, err := NewServer(cfg, configPath, meter, pinger, appLogger, mode1.Reload, "test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	defer server.Stop(context.Background())

	addr := "http://" + server.Addr()
	dev := config.DeviceConfig{ID: "blocked", Host: "127.0.0.1", Port: 5900, VirtualPort: blockedPort, Enabled: true}
	body, _ := json.Marshal(dev)
	resp, err := http.Post(addr+"/api/v1/devices", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST device: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on activation failure, got %d", resp.StatusCode)
	}
	if devices := cfg.GetDevices(); len(devices) != 0 {
		t.Fatalf("activation failure left device in memory: %+v", devices)
	}
	if _, ok := cfg.FindDevice("blocked"); ok {
		t.Fatal("activation failure left the blocked device in config")
	}

	// PUT must be transactional too: a valid device updated onto an occupied
	// port must remain at its previous virtual_port.
	freeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	freePort := freeLn.Addr().(*net.TCPAddr).Port
	_ = freeLn.Close()

	stable := config.DeviceConfig{ID: "stable", Host: "127.0.0.1", Port: 5900, VirtualPort: freePort, Enabled: true}
	stableBody, _ := json.Marshal(stable)
	respCreate, err := http.Post(addr+"/api/v1/devices", "application/json", bytes.NewReader(stableBody))
	if err != nil {
		t.Fatalf("POST stable device: %v", err)
	}
	_ = respCreate.Body.Close()
	if respCreate.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating stable device, got %d", respCreate.StatusCode)
	}

	updated := stable
	updated.VirtualPort = blockedPort
	updatedBody, _ := json.Marshal(updated)
	req, err := http.NewRequest(http.MethodPut, addr+"/api/v1/devices/stable", bytes.NewReader(updatedBody))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	respUpdate, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT device: %v", err)
	}
	_ = respUpdate.Body.Close()
	if respUpdate.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on update activation failure, got %d", respUpdate.StatusCode)
	}
	got, ok := cfg.FindDevice("stable")
	if !ok {
		t.Fatal("stable device disappeared after failed update")
	}
	if got.VirtualPort != freePort {
		t.Fatalf("failed update changed virtual_port: got %d want %d", got.VirtualPort, freePort)
	}
}

func TestCollectMetricsNilMeter(t *testing.T) {
	cfg := config.DefaultConfig()
	api := NewAPIHandler(cfg, "", nil, nil, nil, nil, nil, nil, "test")

	// Ensure CollectMetrics does not panic when meter is nil
	metrics := api.CollectMetrics()
	if metrics == nil {
		t.Fatal("expected non-nil metrics map")
	}
	if count, ok := metrics["active_sessions_count"].(int); !ok || count != 0 {
		t.Fatalf("expected active_sessions_count to be 0, got %v", metrics["active_sessions_count"])
	}
}
