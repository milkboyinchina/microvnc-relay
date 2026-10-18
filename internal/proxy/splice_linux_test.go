//go:build linux

package proxy

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

func TestSpliceMeteredLargeBidirectionalTransfer(t *testing.T) {
	// Echo target: everything written is read back.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	targetConn, err := net.DialTimeout("tcp", echoLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial echo: %v", err)
	}
	defer targetConn.Close()

	// Client side of the relay: a real TCP pair so both sides can use splice.
	clientLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen client side: %v", err)
	}
	defer clientLn.Close()

	relaySideCh := make(chan net.Conn, 1)
	go func() {
		conn, err := clientLn.Accept()
		if err == nil {
			relaySideCh <- conn
		}
	}()

	clientConn, err := net.DialTimeout("tcp", clientLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial client side: %v", err)
	}
	defer clientConn.Close()

	var relaySide net.Conn
	select {
	case relaySide = <-relaySideCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out accepting relay-side conn")
	}
	defer relaySide.Close()

	limits := config.DefaultConfig().ConnectionLimits
	limits.IdleTimeoutSec = 0
	limits.MaxSessionKBps = 0

	meter := NewTrafficMeter(true)
	sess := meter.RegisterSession("splice-dev", "Splice Dev", "mode1", "client", "target", func() {
		_ = relaySide.Close()
		_ = targetConn.Close()
	})
	if sess == nil {
		t.Fatal("RegisterSession returned nil")
	}
	defer meter.UnregisterSession(sess.ID)

	beforeDirections, _ := SpliceStats()

	forwardDone := make(chan struct{})
	go func() {
		ForwardStream(relaySide, targetConn, sess, limits, true)
		close(forwardDone)
	}()

	payload := bytes.Repeat([]byte("splice-frame-"), (4<<20)/13+1)
	payload = payload[:4<<20]

	writeDone := make(chan error, 1)
	go func() {
		_, err := clientConn.Write(payload)
		writeDone <- err
	}()

	got := make([]byte, len(payload))
	_ = clientConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(clientConn, got); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("client write: %v", err)
	}
	if !bytes.Equal(got, payload) {
		for i := range payload {
			if payload[i] != got[i] {
				t.Fatalf("payload mismatch at byte %d: got %q want %q", i, got[i], payload[i])
			}
		}
		t.Fatal("payload mismatch")
	}

	_ = clientConn.Close()
	_ = targetConn.Close()
	select {
	case <-forwardDone:
	case <-time.After(5 * time.Second):
		t.Fatal("ForwardStream did not terminate")
	}

	want := uint64(len(payload))
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := sess.Snapshot(time.Now())
		if snap.RxBytesTotal == want && snap.TxBytesTotal == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("byte counters did not settle: rx=%d tx=%d want=%d", snap.RxBytesTotal, snap.TxBytesTotal, want)
		}
		time.Sleep(10 * time.Millisecond)
	}

	afterDirections, _ := SpliceStats()
	if afterDirections < beforeDirections+2 {
		t.Fatalf("expected at least 2 splice directions, before=%d after=%d", beforeDirections, afterDirections)
	}
}
