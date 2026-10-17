package proxy

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

const (
	// pooledBufferSize is the userspace buffer size borrowed from sync.Pool when metering, QoS, or idle deadlines are active.
	pooledBufferSize = 32 * 1024
	// defaultTCPUserTimeout is the Linux TCP_USER_TIMEOUT fallback.
	defaultTCPUserTimeout = 30 * time.Second
	// defaultHalfCloseTimeout bounds the wait after one direction reaches EOF.
	defaultHalfCloseTimeout = 5 * time.Second
)

var copyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, pooledBufferSize)
		return &buf
	},
}

// ConfigureTCPConn applies low-latency and zombie-prevention socket options to
// a TCP connection. Keepalive detects idle dead peers; TCP_USER_TIMEOUT bounds
// how long unacknowledged data may remain before the kernel fails the link.
func ConfigureTCPConn(conn net.Conn, keepAliveSec, userTimeoutSec int) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}

	_ = tcpConn.SetNoDelay(true)
	_ = tcpConn.SetKeepAlive(true)

	period := time.Duration(keepAliveSec) * time.Second
	if period <= 0 {
		period = 15 * time.Second
	}
	_ = tcpConn.SetKeepAliveConfig(net.KeepAliveConfig{
		Enable:   true,
		Idle:     period,
		Interval: period,
		Count:    3,
	})

	userTimeout := time.Duration(userTimeoutSec) * time.Second
	if userTimeout <= 0 {
		userTimeout = defaultTCPUserTimeout
	}
	_ = setTCPUserTimeout(tcpConn, userTimeout)
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
//
// On a normal EOF in one direction it half-closes that direction's destination
// and waits up to half_close_timeout_sec for the opposite direction to finish.
// An error in either direction closes both sockets immediately.
func ForwardStream(
	clientConn net.Conn,
	targetConn net.Conn,
	sess *Session,
	limits config.ConnectionLimitsConfig,
	meteringEnabled bool,
) {
	ConfigureTCPConn(clientConn, limits.TCPKeepAliveSec, limits.TCPUserTimeoutSec)
	ConfigureTCPConn(targetConn, limits.TCPKeepAliveSec, limits.TCPUserTimeoutSec)

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

	halfCloseTimeout := time.Duration(limits.HalfCloseTimeoutSec) * time.Second
	if halfCloseTimeout <= 0 {
		halfCloseTimeout = defaultHalfCloseTimeout
	}

	done := make(chan struct{}, 2)
	go func() {
		_, err := copyDirection(targetConn, clientConn, addRx, limits, meteringEnabled)
		if isGracefulCopyError(err) {
			closeWrite(targetConn)
		} else {
			closeBoth()
		}
		done <- struct{}{}
	}()

	go func() {
		_, err := copyDirection(clientConn, targetConn, addTx, limits, meteringEnabled)
		if isGracefulCopyError(err) {
			closeWrite(clientConn)
		} else {
			closeBoth()
		}
		done <- struct{}{}
	}()

	// Wait for the first direction, then give the other direction a bounded
	// window to drain after the half-close.
	<-done
	select {
	case <-done:
		// Both directions finished normally.
	case <-time.After(halfCloseTimeout):
		// The opposite direction did not drain in time. Force both sockets
		// closed and give the wedged copy a final short window to return.
		closeBoth()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	closeBoth()
}

func isGracefulCopyError(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

func closeWrite(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}
}

func copyDirection(
	dst net.Conn,
	src net.Conn,
	addBytes func(uint64),
	limits config.ConnectionLimitsConfig,
	meteringEnabled bool,
) (int64, error) {
	// Zero-copy fast path: when byte metering, idle deadlines, and QoS caps are
	// disabled (e.g. Headless Pure Relay profile), io.Copy invokes Linux kernel
	// splice(2) directly.
	if (!meteringEnabled || addBytes == nil) && limits.MaxSessionKBps <= 0 && limits.IdleTimeoutSec <= 0 {
		return io.Copy(dst, src)
	}

	// Real-time metered / QoS path using zero-allocation 32KB buffers from sync.Pool.
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer copyBufPool.Put(bufPtr)

	idleTimeout := time.Duration(limits.IdleTimeoutSec) * time.Second
	maxBytesPerSec := float64(limits.MaxSessionKBps) * 1024.0

	var total int64
	for {
		if idleTimeout > 0 {
			_ = src.SetReadDeadline(time.Now().Add(idleTimeout))
		}

		readStart := time.Now()
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
				if meteringEnabled && addBytes != nil {
					addBytes(uint64(nw))
				}
			}
			if ew != nil {
				return total, ew
			}
			if nw != nr {
				return total, io.ErrShortWrite
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
			return total, er
		}
	}
}
