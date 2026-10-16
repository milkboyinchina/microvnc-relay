//go:build !dev

package main

import "net/http"

// IsDevBuild indicates that this binary was compiled in production mode.
const IsDevBuild = false

// setupDebugBridge is an empty stub optimized away by the Go compiler in production builds.
func setupDebugBridge(mux *http.ServeMux) {}
