//go:build dev

package main

// IsDevBuild indicates that this binary was compiled with development debug
// hooks enabled. The pprof bridge itself lives in internal/web/debug_dev.go and
// is mounted by the WebUI server.
const IsDevBuild = true
