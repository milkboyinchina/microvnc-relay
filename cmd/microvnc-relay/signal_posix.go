//go:build !windows

package main

import "syscall"

func sendReloadSignal(pid int) error {
	return syscall.Kill(pid, syscall.SIGHUP)
}

func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
