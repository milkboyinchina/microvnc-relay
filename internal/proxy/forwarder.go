package proxy

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

const (
	// pooledBufferSize is the userspace buffer size borrowed from sync.Pool when metering, QoS, or idle deadlines are active.
	pooledBufferSize = 32 * 1024
)

var copyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, pooledBufferSize)
		return &buf
	},
}

// ConfigureTCPConn applies low-latency and zombie-prevention socket options to a TCP connection.
func ConfigureTCPConn(conn net.Conn, keepAliveSec int) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetNoDelay(true)
	_ = tcpConn.SetKeepAlive(true)
	if keepAliveSec > 0 {
		_ = tcpConn.SetKeepAlivePeriod(time.Duration(keepAliveSec) * time.Second)
	}
}

// ExtractRemoteIP parses the IP address from a net.Addr, stripping the port.
func ExtractRemoteIP(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.IP
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return net.ParseIP(addr.String())
	}
	return net.ParseIP(host)
}

// ForwardStream bidirectionally relays bytes between clientConn and targetConn.
// As soon as either direction reaches EOF or encounters an error, both connections
// are immediately closed to unblock the opposite direction and prevent goroutine leaks.
func ForwardStream(
	clientConn net.Conn,
	targetConn net.Conn,
	sess *Session,
	limits config.ConnectionLimitsConfig,
	meteringEnabled bool,
) {
	ConfigureTCPConn(clientConn, limits.TCPKeepAliveSec)
	ConfigureTCPConn(targetConn, limits.TCPKeepAliveSec)

	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = clientConn.Close()
			_ = targetConn.Close()
		})
	}
	defer closeBoth()

	var addRx, addTx func(uint64)
	if sess != nil {
		sess.SetClientConn(clientConn)
		defer sess.SetClientConn(nil)
		addRx = sess.AddRx
		addTx = sess.AddTx
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer closeBoth()
		copyDirection(targetConn, clientConn, addRx, limits, meteringEnabled)
	}()

	copyDirection(clientConn, targetConn, addTx, limits, meteringEnabled)
	closeBoth()
	<-done
}

func copyDirection(
	dst net.Conn,
	src net.Conn,
	addBytes func(uint64),
	limits config.ConnectionLimitsConfig,
	meteringEnabled bool,
) {
	// Zero-copy fast path: when byte metering, idle deadlines, and QoS caps are disabled
	// (e.g. Headless Pure Relay profile), io.Copy invokes Linux kernel splice(2) directly.
	if (!meteringEnabled || addBytes == nil) && limits.MaxSessionKBps <= 0 && limits.IdleTimeoutSec <= 0 {
		_, _ = io.Copy(dst, src)
		return
	}

	// Real-time metered / QoS path using zero-allocation 32KB buffers from sync.Pool
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer copyBufPool.Put(bufPtr)

	idleTimeout := time.Duration(limits.IdleTimeoutSec) * time.Second
	maxBytesPerSec := float64(limits.MaxSessionKBps) * 1024.0

	for {
		if idleTimeout > 0 {
			_ = src.SetReadDeadline(time.Now().Add(idleTimeout))
		}

		readStart := time.Now()
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 && meteringEnabled && addBytes != nil {
				addBytes(uint64(nw))
			}
			if ew != nil || nw != nr {
				return
			}

			if maxBytesPerSec > 0 {
				expectedDur := time.Duration(float64(nw) / maxBytesPerSec * float64(time.Second))
				elapsed := time.Since(readStart)
				if expectedDur > elapsed {
					time.Sleep(expectedDur - elapsed)
				}
			}
		}
		if er != nil {
			return
		}
	}
}
