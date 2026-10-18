package web

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type wsTestConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func wsHandshake(t *testing.T, addr, origin, version string) (int, *wsTestConn) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial ws server: %v", err)
	}

	req := fmt.Sprintf(
		"GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n",
		addr,
	)
	if version != "" {
		req += "Sec-WebSocket-Version: " + version + "\r\n"
	}
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	req += "\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		_ = conn.Close()
		t.Fatalf("write ws handshake: %v", err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		t.Fatalf("read ws status: %v", err)
	}

	var status int
	_, _ = fmt.Sscanf(statusLine, "HTTP/1.1 %d", &status)

	if status != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return status, nil
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			t.Fatalf("read ws headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	return status, &wsTestConn{conn: conn, br: br}
}

func TestWSHubRejectsCrossOrigin(t *testing.T) {
	hub := NewWSHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = hub.HandleUpgrade(w, r)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	status, _ := wsHandshake(t, addr, "http://evil.example", "13")
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin websocket, got %d", status)
	}
	if hub.Stats().OriginRejected != 1 {
		t.Fatalf("expected one origin rejection, got %d", hub.Stats().OriginRejected)
	}
}

func TestWSHubRejectsMissingVersion(t *testing.T) {
	hub := NewWSHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = hub.HandleUpgrade(w, r)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	status, _ := wsHandshake(t, addr, "", "")
	if status != http.StatusUpgradeRequired {
		t.Fatalf("expected 426 for missing websocket version, got %d", status)
	}
	if hub.Stats().VersionRejected != 1 {
		t.Fatalf("expected one version rejection, got %d", hub.Stats().VersionRejected)
	}
}

func TestWSHubEvictsOldestClientAtCap(t *testing.T) {
	hub := NewWSHub()
	hub.SetMaxClients(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = hub.HandleUpgrade(w, r)
	}))
	defer srv.Close()
	defer hub.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	origin := "http://" + addr

	_, c1 := wsHandshake(t, addr, origin, "13")
	if c1 == nil {
		t.Fatal("first websocket was not upgraded")
	}
	defer c1.conn.Close()

	_, c2 := wsHandshake(t, addr, origin, "13")
	if c2 == nil {
		t.Fatal("second websocket was not upgraded")
	}
	defer c2.conn.Close()

	_, c3 := wsHandshake(t, addr, origin, "13")
	if c3 == nil {
		t.Fatal("third websocket was not upgraded")
	}
	defer c3.conn.Close()

	// The oldest client must receive a 1013 close frame.
	_ = c1.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var header [2]byte
	if _, err := io.ReadFull(c1.br, header[:]); err != nil {
		t.Fatalf("read eviction close frame header: %v", err)
	}
	if header[0]&0x0f != 0x8 {
		t.Fatalf("expected close opcode 0x8, got 0x%x", header[0]&0x0f)
	}
	length := int(header[1] & 0x7f)
	payload := make([]byte, length)
	if _, err := io.ReadFull(c1.br, payload); err != nil {
		t.Fatalf("read eviction close frame payload: %v", err)
	}
	if len(payload) < 2 || binary.BigEndian.Uint16(payload[:2]) != wsCloseTryAgainLater {
		t.Fatalf("expected close code %d, got %v", wsCloseTryAgainLater, payload)
	}

	stats := hub.Stats()
	if stats.Clients != 2 {
		t.Fatalf("expected 2 remaining clients, got %d", stats.Clients)
	}
	if stats.Evicted != 1 {
		t.Fatalf("expected 1 eviction, got %d", stats.Evicted)
	}
}
