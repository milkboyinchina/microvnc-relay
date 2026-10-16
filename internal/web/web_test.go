package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	cfg.Security.PasswordHash = "secret123" // Test plaintext auto-upgrade
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
