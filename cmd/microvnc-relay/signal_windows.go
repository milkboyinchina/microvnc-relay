//go:build windows

package main

import "errors"

func sendReloadSignal(pid int) error {
	return errors.New("live reload via SIGHUP signal is not supported on Windows")
}

func isProcessAlive(pid int) bool {
	return false
}
