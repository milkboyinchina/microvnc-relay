package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"github.com/milkboyinchina/microvnc-relay/internal/logger"
	"github.com/milkboyinchina/microvnc-relay/internal/proxy"
	"github.com/milkboyinchina/microvnc-relay/internal/telemetry"
	webassets "github.com/milkboyinchina/microvnc-relay/web"
)

// Server coordinates the embedded WebUI, REST API, and WebSocket streaming telemetry hub.
type Server struct {
	cfg        *config.Config
	configPath string
	httpServer *http.Server
	listener   net.Listener
	api        *APIHandler
	auth       *AuthManager
	wsHub      *WSHub
	stopCh     chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
	started    bool
	stopped    bool
}

// NewServer initializes the HTTP Server, AuthManager, WSHub, and REST API handlers.
func NewServer(
	cfg *config.Config,
	configPath string,
	meter *proxy.TrafficMeter,
	pinger *telemetry.Pinger,
	appLogger *logger.Logger,
	mode1Reloader func([]config.DeviceConfig) error,
	version string,
) (*Server, error) {
	auth := NewAuthManager(cfg, configPath)
	wsHub := NewWSHub()

	api := NewAPIHandler(
		cfg,
		configPath,
		meter,
		pinger,
		appLogger,
		auth,
		wsHub,
		mode1Reloader,
		version,
	)

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	// Mount embedded WebUI static assets
	distFS, err := fs.Sub(webassets.DistFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("mount embedded dist filesystem: %w", err)
	}
	fileServer := http.FileServer(http.FS(distFS))

	// Serve index.html and static files at root
	mux.Handle("/", fileServer)

	httpServer := &http.Server{
		Addr:              cfg.Server.WebBind,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return &Server{
		cfg:        cfg,
		configPath: configPath,
		httpServer: httpServer,
		api:        api,
		auth:       auth,
		wsHub:      wsHub,
		stopCh:     make(chan struct{}),
	}, nil
}

// Start opens the HTTP listener and starts the 1Hz WebSocket telemetry broadcaster.
// If features.webui_enabled is false, Start is a no-op (Headless Pure Relay mode).
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started || s.stopped {
		return nil
	}
	s.started = true

	if !s.cfg.Features.WebUIEnabled {
		slog.Info("WebUI server disabled by feature flag (running in Headless Pure Relay profile)")
		return nil
	}

	ln, err := net.Listen("tcp", s.cfg.Server.WebBind)
	if err != nil {
		return fmt.Errorf("listen WebUI %s: %w", s.cfg.Server.WebBind, err)
	}
	s.listener = ln

	slog.Info("started WebUI dashboard and REST/WebSocket server",
		"bind", ln.Addr().String(),
		"auth_enabled", s.cfg.Security.AuthEnabled,
	)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("WebUI server terminated unexpectedly", "err", err)
		}
	}()

	// 1Hz WebSocket telemetry broadcaster (only marshals JSON if clients are connected)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		interval := time.Duration(s.cfg.Telemetry.BroadcastIntervalSec) * time.Second
		if interval <= 0 {
			interval = 1 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if s.wsHub.ClientCount() > 0 {
					payload := s.api.BuildTelemetryPayload()
					s.wsHub.Broadcast(payload)
				}
			case <-s.stopCh:
				return
			}
		}
	}()

	return nil
}

// Addr returns the bound address of the WebUI server, or empty if not listening.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Stop gracefully shuts down the HTTP server, disconnects WebSocket clients, and waits for background workers.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	close(s.stopCh)
	s.wsHub.Close()
	s.mu.Unlock()

	var err error
	if s.httpServer != nil && s.listener != nil {
		err = s.httpServer.Shutdown(ctx)
	}

	s.wg.Wait()
	return err
}
