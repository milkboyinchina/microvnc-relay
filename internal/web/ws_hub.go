package web

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsMagicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsClient struct {
	conn net.Conn
	bufr *bufio.Reader
	bufw *bufio.Writer
	mu   sync.Mutex
}

func (c *wsClient) writeTextFrame(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	defer func() {
		_ = c.conn.SetWriteDeadline(time.Time{})
	}()

	length := len(payload)
	var header [10]byte
	header[0] = 0x81 // FIN = 1, Opcode = 1 (Text)
	headerLen := 2

	if length <= 125 {
		header[1] = byte(length)
	} else if length <= 65535 {
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(length))
		headerLen = 4
	} else {
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(length))
		headerLen = 10
	}

	if _, err := c.bufw.Write(header[:headerLen]); err != nil {
		return err
	}
	if _, err := c.bufw.Write(payload); err != nil {
		return err
	}
	return c.bufw.Flush()
}

// WSHub coordinates pure Go RFC 6455 WebSocket clients for real-time telemetry streaming.
type WSHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	closed  bool
}

// NewWSHub initializes the WebSocket broadcast hub.
func NewWSHub() *WSHub {
	return &WSHub{
		clients: make(map[*wsClient]struct{}),
	}
}

// HandleUpgrade upgrades an incoming HTTP request to an RFC 6455 WebSocket connection.
func (h *WSHub) HandleUpgrade(w http.ResponseWriter, r *http.Request) error {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return errors.New("missing or invalid Upgrade header")
	}

	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return errors.New("missing Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return errors.New("server does not support hijacking")
	}

	conn, rw, err := hj.Hijack()
	if err != nil {
		return fmt.Errorf("hijack error: %w", err)
	}

	// Compute Sec-WebSocket-Accept = Base64(SHA-1(Key + GUID))
	hash := sha1.New()
	hash.Write([]byte(key + wsMagicGUID))
	acceptKey := base64.StdEncoding.EncodeToString(hash.Sum(nil))

	response := fmt.Sprintf(
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n",
		acceptKey,
	)

	if _, err := rw.WriteString(response); err != nil {
		_ = conn.Close()
		return err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return err
	}

	client := &wsClient{
		conn: conn,
		bufr: rw.Reader,
		bufw: rw.Writer,
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return errors.New("hub is closed")
	}
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	// Drain incoming client frames (ping/pong/close) in background
	go h.readPump(client)

	return nil
}

func (h *WSHub) readPump(c *wsClient) {
	defer func() {
		h.removeClient(c)
		_ = c.conn.Close()
	}()

	// Discard any incoming frames or detect close
	buf := make([]byte, 512)
	for {
		_, err := c.bufr.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}
	}
}

func (h *WSHub) removeClient(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// Broadcast sends a JSON payload frame to all active WebSocket clients.
// Any client with a broken pipe or write error is disconnected immediately.
func (h *WSHub) Broadcast(payload []byte) {
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if err := c.writeTextFrame(payload); err != nil {
			h.removeClient(c)
			_ = c.conn.Close()
		}
	}
}

// ClientCount returns the number of currently connected WebSocket telemetry subscribers.
func (h *WSHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Close disconnects all active clients and shuts down the hub.
func (h *WSHub) Close() {
	h.mu.Lock()
	h.closed = true
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = make(map[*wsClient]struct{})
	h.mu.Unlock()

	for _, c := range clients {
		_ = c.conn.Close()
	}
}
