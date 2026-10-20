//go:build dev

package web

import (
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/milkboyinchina/microvnc-relay/internal/proxy"
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

	// Dev-only fault injection for the Docker daisy-chain harness.
	mux.HandleFunc("/debug/fault/rst", func(w http.ResponseWriter, r *http.Request) {
		proxy.SetFaultRST(r.URL.Query().Get("on") == "1")
		_, _ = w.Write([]byte("rst=" + r.URL.Query().Get("on") + "\n"))
	})
	mux.HandleFunc("/debug/fault/drop", func(w http.ResponseWriter, r *http.Request) {
		proxy.SetFaultDrop(r.URL.Query().Get("on") == "1")
		_, _ = w.Write([]byte("drop=" + r.URL.Query().Get("on") + "\n"))
	})
}
