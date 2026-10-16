//go:build dev

package main

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
)

// IsDevBuild indicates that this binary was compiled with development debug hooks enabled.
const IsDevBuild = true

// setupDebugBridge mounts runtime profiling and debug introspection routes.
func setupDebugBridge(mux *http.ServeMux) {
	slog.Warn("⚠️  DEVELOPMENT DEBUG BRIDGE ACTIVE — pprof and diagnostic endpoints enabled")

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}
