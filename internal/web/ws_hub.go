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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	wsMagicGUID          = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	defaultMaxWSClients  = 8
	maxWSFrameBytes      = 4 * 1024
	wsWriteTimeout       = 2 * time.Second
	wsCloseTryAgainLater = 1013
)

type wsClient struct {
	conn      net.Conn
	bufr      *bufio.Reader
	bufw      *bufio.Writer
	mu        sync.Mutex
	createdAt time.Time
}

// wsFramePool recycles bounded inbound frame buffers. Telemetry clients send
// almost nothing, so this keeps the 1 Hz steady state allocation-free.
var wsFramePool = sync.Pool{
	New: func() any {
		buf := make([]byte, maxWSFrameBytes)
		return &buf
	},
}

// WSStats is a point-in-time snapshot of the telemetry WebSocket hub.
type WSStats struct {
	Clients         int    `json:"clients"`
	MaxClients      int    `json:"max_clients"`
	Evicted         uint64 `json:"evicted"`
	OriginRejected  uint64 `json:"origin_rejected"`
	VersionRejected uint64 `json:"version_rejected"`
}

func (c *wsClient) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return net.ErrClosed
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	defer func() {
		_ = c.conn.SetWriteDeadline(time.Time{})
	}()

	var headerBuf [10]byte
	header := headerBuf[:0]
	header = append(header, 0x80|opcode)

	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		binary.BigEndian.PutUint64(header[2:10], uint64(length))
		header = header[:10]
	}

	if _, err := c.bufw.Write(header); err != nil {
		return err
	}
	if length > 0 {
		if _, err := c.bufw.Write(payload); err != nil {
			return err
		}
	}
	return c.bufw.Flush()
}

func (c *wsClient) writeTextFrame(payload []byte) error {
	return c.writeFrame(0x1, payload)
}

func (c *wsClient) writeCloseFrame(code uint16, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	var closeBuf [125]byte
	payload := closeBuf[:2+len(reason)]
	binary.BigEndian.PutUint16(payload[:2], code)
	copy(payload[2:], reason)
	return c.writeFrame(0x8, payload)
}

func (c *wsClient) writePongFrame(payload []byte) error {
	return c.writeFrame(0xA, payload)
}

// WSHub coordinates pure Go RFC 6455 WebSocket clients for real-time telemetry
// streaming. The client count is strictly bounded; when full, the oldest
// client is evicted with a 1013 close frame.
type WSHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	closed  bool

	maxClients int

	evicted         atomic.Uint64
	originRejected  atomic.Uint64
	versionRejected atomic.Uint64
}

// NewWSHub initializes the WebSocket broadcast hub.
func NewWSHub() *WSHub {
	return &WSHub{
		clients:    make(map[*wsClient]struct{}),
		maxClients: defaultMaxWSClients,
	}
}

// SetMaxClients changes the hard client cap. Values <= 0 restore the default.
func (h *WSHub) SetMaxClients(n int) {
	if n <= 0 {
		n = defaultMaxWSClients
	}
	h.mu.Lock()
	h.maxClients = n
	h.mu.Unlock()
}

// Stats returns a snapshot of client count and rejection/eviction counters.
func (h *WSHub) Stats() WSStats {
	h.mu.RLock()
	clients := len(h.clients)
	maxClients := h.maxClients
	h.mu.RUnlock()

	return WSStats{
		Clients:         clients,
		MaxClients:      maxClients,
		Evicted:         h.evicted.Load(),
		OriginRejected:  h.originRejected.Load(),
		VersionRejected: h.versionRejected.Load(),
	}
}

// HandleUpgrade upgrades an incoming HTTP request to an RFC 6455 WebSocket
// connection. Cross-origin requests are rejected before hijacking.
func (h *WSHub) HandleUpgrade(w http.ResponseWriter, r *http.Request) error {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return errors.New("missing or invalid Upgrade header")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		h.versionRejected.Add(1)
		http.Error(w, "unsupported websocket version", http.StatusUpgradeRequired)
		return errors.New("missing or unsupported Sec-WebSocket-Version")
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			h.originRejected.Add(1)
			http.Error(w, "cross-origin websocket rejected", http.StatusForbidden)
			return errors.New("cross-origin websocket rejected")
		}
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
		conn:      conn,
		bufr:      rw.Reader,
		bufw:      rw.Writer,
		createdAt: time.Now(),
	}

	var evicted *wsClient
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return errors.New("hub is closed")
	}
	if h.maxClients > 0 && len(h.clients) >= h.maxClients {
		for existing := range h.clients {
			if evicted == nil || existing.createdAt.Before(evicted.createdAt) {
				evicted = existing
			}
		}
		if evicted != nil {
			delete(h.clients, evicted)
			h.evicted.Add(1)
		}
	}
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	if evicted != nil {
		_ = evicted.writeCloseFrame(wsCloseTryAgainLater, "try again later")
		_ = evicted.conn.Close()
	}

	go h.readPump(client)
	return nil
}

func (h *WSHub) readPump(c *wsClient) {
	defer func() {
		h.removeClient(c)
		_ = c.conn.Close()
	}()

	for {
		var header [2]byte
		if _, err := io.ReadFull(c.bufr, header[:]); err != nil {
			return
		}

		opcode := header[0] & 0x0f
		masked := header[1]&0x80 != 0
		length := int64(header[1] & 0x7f)

		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.bufr, ext[:]); err != nil {
				return
			}
			length = int64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.bufr, ext[:]); err != nil {
				return
			}
			raw := binary.BigEndian.Uint64(ext[:])
			if raw > maxWSFrameBytes {
				_ = c.writeCloseFrame(1009, "frame too large")
				return
			}
			length = int64(raw)
		}

		if length < 0 || length > maxWSFrameBytes {
			_ = c.writeCloseFrame(1009, "frame too large")
			return
		}

		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.bufr, mask[:]); err != nil {
				return
			}
		}

		payloadBuf := wsFramePool.Get().(*[]byte)
		payload := (*payloadBuf)[:int(length)]
		if length > 0 {
			if _, err := io.ReadFull(c.bufr, payload); err != nil {
				wsFramePool.Put(payloadBuf)
				return
			}
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}

		switch opcode {
		case 0x8: // close
			_ = c.writeCloseFrame(1000, "")
			wsFramePool.Put(payloadBuf)
			return
		case 0x9: // ping
			_ = c.writePongFrame(payload)
		case 0xA: // pong: ignore
		case 0x1, 0x2: // telemetry clients send no data; discard
		default:
			// Ignore unknown opcodes.
		}
		wsFramePool.Put(payloadBuf)
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
		_ = c.writeCloseFrame(1001, "server shutting down")
		_ = c.conn.Close()
	}
}
