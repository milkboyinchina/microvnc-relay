//go:build linux

package proxy

import (
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestConfigureTCPConnSetsKeepaliveAndUserTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	client, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	server := <-accepted
	defer server.Close()

	ConfigureTCPConn(client, 15, 30)

	rawConn, err := client.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}

	var (
		keepAlive int
		userTO    int
		sockErr   error
	)
	if err := rawConn.Control(func(fd uintptr) {
		keepAlive, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE)
		if sockErr != nil {
			return
		}
		userTO, sockErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
	}); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if sockErr != nil {
		t.Fatalf("getsockopt: %v", sockErr)
	}

	if keepAlive != 1 {
		t.Fatalf("SO_KEEPALIVE = %d, want 1", keepAlive)
	}
	if userTO != 30000 {
		t.Fatalf("TCP_USER_TIMEOUT = %d, want 30000", userTO)
	}
}
