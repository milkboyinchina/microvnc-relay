//go:build !dev

package web

import "net/http"

// mountDebugBridge is a no-op in production builds: zero routes, zero overhead.
func mountDebugBridge(mux *http.ServeMux) {}
