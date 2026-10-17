//go:build dev

package web

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
)

// mountDebugBridge exposes net/http/pprof routes on the WebUI mux in dev builds only.
// Production builds (!dev) compile this away to a no-op (see debug_prod.go).
func mountDebugBridge(mux *http.ServeMux) {
	slog.Warn("DEVELOPMENT DEBUG BRIDGE ACTIVE — pprof endpoints enabled")
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}
