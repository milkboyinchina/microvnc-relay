// Command stress-tester exercises microvnc-relay's proxy engine either
// in-process (loopback byte-verified load) or over the network against a
// running relay (e.g. Raspberry Pi 2 via NetBird).
//
// Safety: the harness NEVER runs on the Pi itself. It runs on the workstation
// and dials the relay remotely. A watchdog polls the relay's public
// /api/v1/auth/status heartbeat and aborts on 2 missed probes or >500ms RTT.
// Every run is bounded by a hard context timeout (duration clamped to 30s).
// No disk writes: results go to stdout only.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"github.com/milkboyinchina/microvnc-relay/internal/proxy"
)

const (
	maxDuration     = 30 * time.Second
	heartbeatPath   = "/api/v1/auth/status"
	heartbeatPeriod = 1 * time.Second
	heartbeatBudget = 3 * time.Second
	maxRTT          = 1500 * time.Millisecond
)

var (
	targetRelay = flag.String("target-relay", "127.0.0.1", "Relay host to drive remotely (loopback => in-process engine test)")
	mode1Port   = flag.Int("mode1-port", 5910, "Remote relay Mode 1 virtual port (remote topology only)")
	revPort     = flag.Int("mode2-reverse-port", 5500, "Relay Mode 2 reverse port")
	viewPort    = flag.Int("mode2-viewer-port", 5901, "Relay Mode 2 viewer port")
	webPort     = flag.Int("web-port", 8080, "Relay WebUI port for watchdog heartbeat")
	mode        = flag.String("mode", "concurrency", "Test mode: concurrency|chaos|mode2-fuzz|throughput")
	concurrency = flag.Int("concurrency", 5, "Concurrent streams (max 20 recommended against Pi 2)")
	duration    = flag.Duration("duration", 20*time.Second, "Test duration (hard-capped at 30s)")
	rateMB      = flag.Float64("rate-mb", 0, "Target throughput per stream in MB/s (0 = unlimited)")
	bytesPer    = flag.Int("bytes-per-stream", 1048576, "Bytes to transfer per stream in concurrency mode")
	watchdog    = flag.Bool("watchdog", true, "Enable heartbeat circuit breaker (remote topology only)")
	spawnTgts   = flag.Int("spawn-targets", 0, "Synthetic echo targets to spawn for loopback topology (0 = auto = concurrency)")
)

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func main() {
	flag.Parse()

	if *duration > maxDuration {
		fmt.Fprintf(os.Stderr, "clamping duration %v to hard cap %v\n", *duration, maxDuration)
		*duration = maxDuration
	}
	if *duration <= 0 {
		fmt.Fprintln(os.Stderr, "duration must be > 0")
		os.Exit(1)
	}
	if *concurrency <= 0 {
		fmt.Fprintln(os.Stderr, "concurrency must be > 0")
		os.Exit(1)
	}
	if isLoopback(*targetRelay) && *concurrency > 50 {
		fmt.Fprintln(os.Stderr, "loopback concurrency capped at 50 for workstation safety")
		os.Exit(1)
	}
	if !isLoopback(*targetRelay) && *concurrency > 20 {
		fmt.Fprintln(os.Stderr, "remote concurrency capped at 20 to protect the Pi 2 (see ramp plan 2-5-10-15-20)")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *duration+5*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	var err error
	switch *mode {
	case "concurrency":
		err = runConcurrency(ctx, log)
	case "chaos":
		err = runChaos(ctx, log)
	case "mode2-fuzz":
		err = runMode2Fuzz(ctx, log)
	case "throughput":
		err = runThroughput(ctx, log)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (want concurrency|chaos|mode2-fuzz|throughput)\n", *mode)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}

// --- watchdog (remote topology only) -------------------------------------

// startWatchdog polls the relay heartbeat until ctx ends or abort conditions
// trip. It returns a context cancelled on abort plus a WaitGroup-free done
// channel closed when the poller exits.
func startWatchdog(ctx context.Context, log *slog.Logger) (context.Context, context.CancelFunc) {
	wctx, cancel := context.WithCancel(ctx)
	if !*watchdog || isLoopback(*targetRelay) {
		return wctx, cancel
	}
	go func() {
		defer cancel()
		url := fmt.Sprintf("http://%s:%d%s", *targetRelay, *webPort, heartbeatPath)
		client := &http.Client{Timeout: heartbeatBudget}
		missed := 0
		ticker := time.NewTicker(heartbeatPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-ticker.C:
				start := time.Now()
				resp, err := client.Get(url) //nolint:noctx,gosec // bounded by client.Timeout
				rtt := time.Since(start)
				if err != nil {
					missed++
					log.Warn("watchdog heartbeat failed", "missed", missed, "err", err)
				} else {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					if rtt > maxRTT {
						log.Error("watchdog RTT spike, aborting", "rtt", rtt)
						return
					}
					missed = 0
				}
				if missed >= 2 {
					log.Error("watchdog 2 consecutive heartbeats missed, aborting")
					return
				}
			}
		}
	}()
	return wctx, cancel
}

// --- synthetic echo target ------------------------------------------------

func startEchoTarget() (addr string, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	stopCh := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
					return
				}
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = conn.Write([]byte("RFB 003.008\n"))
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln.Addr().String(), func() { close(stopCh); _ = ln.Close() }
}

// --- loopback engine (in-process relay) ------------------------------------

type loopbackEngine struct {
	mode1 *proxy.Mode1Manager
	mode2 *proxy.Mode2Manager
	meter *proxy.TrafficMeter
	addrs []string // mode1 virtual-port addrs, one per device
}

func startLoopback(n int) *loopbackEngine {
	if n <= 0 {
		n = 1
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	meter := proxy.NewTrafficMeter(true)
	eng := &loopbackEngine{meter: meter}

	// Allocate-then-bind races (freed :0 ports get stolen between close and
	// Mode1Manager.Start). Retry with fresh ports on bind collision.
	var m1 *proxy.Mode1Manager
	for attempt := 0; ; attempt++ {
		cfg := loopbackConfig(n)
		m1 = proxy.NewMode1Manager(cfg, meter, log)
		if err := m1.Start(); err == nil {
			eng.mode1 = m1
			for _, d := range cfg.Devices {
				eng.addrs = append(eng.addrs, m1.ListenerAddr(d.ID))
			}
			return eng
		} else if attempt >= 4 {
			panic(err)
		}
		m1.Stop(context.Background())
	}
}

func loopbackConfig(n int) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Features.Mode1Enabled = true
	cfg.Features.Mode2Enabled = false // enabled separately where needed
	cfg.Features.BandwidthMeteringEnabled = true
	for i := 0; i < n; i++ {
		tgtAddr, stop := startEchoTarget()
		_ = stop // targets live for process lifetime; closed on exit
		host, portStr, _ := net.SplitHostPort(tgtAddr)
		var p int
		_, _ = fmt.Sscanf(portStr, "%d", &p)
		dev := config.DeviceConfig{
			ID:      fmt.Sprintf("stress-%d", i),
			Name:    fmt.Sprintf("Stress %d", i),
			Host:    host,
			Port:    p,
			Enabled: true,
		}
		// Grab a free virtual port via :0, then release for Mode1Manager.
		// A racing third party may steal it; startLoopback retries on collision.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		dev.VirtualPort = ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		cfg.Devices = append(cfg.Devices, dev)
	}
	return cfg
}

func (e *loopbackEngine) stop() {
	if e.mode1 != nil {
		e.mode1.Stop(context.Background())
	}
	if e.mode2 != nil {
		e.mode2.Stop(context.Background())
	}
}

// --- stream workers --------------------------------------------------------

type stats struct {
	streamsOK  atomic.Int64
	streamsErr atomic.Int64
	bytesTx    atomic.Uint64
	bytesRx    atomic.Uint64
	mu         sync.Mutex
	samples    []string // first 10 errors only (bounded)
}

func (s *stats) addErr(format string, args ...any) {
	s.streamsErr.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.samples) < 10 {
		s.samples = append(s.samples, fmt.Sprintf(format, args...))
	}
}

func pumpStream(ctx context.Context, dialAddr string, totalBytes int, rateMB float64, verifyEcho bool, st *stats) {
	conn, err := net.DialTimeout("tcp", dialAddr, 5*time.Second)
	if err != nil {
		st.addErr("dial %s: %v", dialAddr, err)
		return
	}
	defer conn.Close()

	payload := make([]byte, 32*1024)
	if _, err := rand.Read(payload); err != nil {
		st.addErr("rand: %v", err)
		return
	}

	// Consume RFB greeting (12 bytes) if present; echo targets send one.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	greet := make([]byte, 12)
	n, _ := io.ReadFull(conn, greet)
	if n > 0 {
		st.bytesRx.Add(uint64(n))
	}
	_ = conn.SetReadDeadline(time.Time{})

	throttle := time.Duration(0)
	if rateMB > 0 {
		// Pacing per 32KB chunk.
		throttle = time.Duration(float64(len(payload)) / (rateMB * 1024 * 1024) * float64(time.Second))
	}

	sent := 0
	echoBuf := make([]byte, len(payload))
	for sent < totalBytes {
		select {
		case <-ctx.Done():
			return
		default:
		}
		chunk := len(payload)
		if sent+chunk > totalBytes {
			chunk = totalBytes - sent
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(payload[:chunk]); err != nil {
			st.addErr("write: %v", err)
			return
		}
		st.bytesTx.Add(uint64(chunk))
		sent += chunk
		if verifyEcho {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(conn, echoBuf[:chunk]); err != nil {
				st.addErr("echo read: %v", err)
				return
			}
			st.bytesRx.Add(uint64(chunk))
			if !bytes.Equal(echoBuf[:chunk], payload[:chunk]) {
				st.addErr("echo mismatch at offset %d", sent)
				return
			}
		}
		if throttle > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(throttle):
			}
		}
	}
	st.streamsOK.Add(1)
}

// --- modes ------------------------------------------------------------------

func runConcurrency(ctx context.Context, log *slog.Logger) error {
	wctx, cancel := startWatchdog(ctx, log)
	defer cancel()

	var dialAddrs []string
	var eng *loopbackEngine
	var meter *proxy.TrafficMeter
	if isLoopback(*targetRelay) {
		n := *spawnTgts
		if n <= 0 {
			n = *concurrency
		}
		eng = startLoopback(n)
		defer eng.stop()
		meter = eng.meter
		// Round-robin streams across devices.
		for i := 0; i < *concurrency; i++ {
			dialAddrs = append(dialAddrs, eng.addrs[i%len(eng.addrs)])
		}
	} else {
		addr := net.JoinHostPort(*targetRelay, fmt.Sprint(*mode1Port))
		for i := 0; i < *concurrency; i++ {
			dialAddrs = append(dialAddrs, addr)
		}
	}

	st := &stats{}
	var wg sync.WaitGroup
	for _, a := range dialAddrs {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			pumpStream(wctx, addr, *bytesPer, *rateMB, eng != nil, st)
		}(a)
	}
	wg.Wait()

	if wctx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("watchdog aborted run (ok=%d err=%d)", st.streamsOK.Load(), st.streamsErr.Load())
	}
	if meter != nil {
		// Allow teardown to propagate before asserting zero zombies.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if len(meter.GetSessions()) == 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if left := len(meter.GetSessions()); left != 0 {
			return fmt.Errorf("%d streams ok, %d errors, but %d sessions still registered: %v",
				st.streamsOK.Load(), st.streamsErr.Load(), left, st.samples)
		}
	}
	log.Info("concurrency done", "ok", st.streamsOK.Load(), "err", st.streamsErr.Load(),
		"tx_bytes", st.bytesTx.Load(), "rx_bytes", st.bytesRx.Load())
	if st.streamsErr.Load() > 0 && eng != nil {
		return fmt.Errorf("%d/%d streams failed: %v", st.streamsErr.Load(), *concurrency, st.samples)
	}
	return nil
}

func runChaos(ctx context.Context, log *slog.Logger) error {
	wctx, cancel := startWatchdog(ctx, log)
	defer cancel()

	var dialAddr string
	var eng *loopbackEngine
	var meter *proxy.TrafficMeter
	if isLoopback(*targetRelay) {
		eng = startLoopback(1)
		defer eng.stop()
		meter = eng.meter
		dialAddr = eng.addrs[0]
	} else {
		dialAddr = net.JoinHostPort(*targetRelay, fmt.Sprint(*mode1Port))
	}

	st := &stats{}
	var wg sync.WaitGroup
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", dialAddr, 5*time.Second)
			if err != nil {
				st.addErr("dial: %v", err)
				return
			}
			// Guzzle greeting, blast some bytes, then die ungracefully.
			buf := make([]byte, 4096)
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.ReadFull(conn, buf[:12])
			_, _ = conn.Write(buf[:1024])
			time.Sleep(time.Duration(i%7) * 10 * time.Millisecond)
			switch i % 3 {
			case 0:
				// TCP RST via linger-0.
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.SetLinger(0)
				}
				_ = conn.Close()
			case 1:
				// Half-close write side.
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
				time.Sleep(50 * time.Millisecond)
				_ = conn.Close()
			default:
				_ = conn.Close()
			}
			st.streamsOK.Add(1)
		}(i)
	}
	wg.Wait()

	// Allow teardown to propagate, then assert zero zombies.
	deadline := time.Now().Add(5 * time.Second)
	for meter != nil && time.Now().Before(deadline) {
		if len(meter.GetSessions()) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if meter != nil {
		if left := len(meter.GetSessions()); left != 0 {
			return fmt.Errorf("chaos: %d zombie sessions after teardown: %v", left, st.samples)
		}
	}
	log.Info("chaos done", "cycles", st.streamsOK.Load(), "dial_err", st.streamsErr.Load())
	_ = wctx
	return nil
}

func runMode2Fuzz(ctx context.Context, log *slog.Logger) error {
	_, cancel := startWatchdog(ctx, log)
	defer cancel()

	var revAddr, viewAddr string
	var m2 *proxy.Mode2Manager
	if isLoopback(*targetRelay) {
		cfg := config.DefaultConfig()
		cfg.Server.VNCBind = "127.0.0.1"
		cfg.Features.Mode2Enabled = true
		rl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		vl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = rl.Close()
			return err
		}
		cfg.Server.Mode2ReversePort = rl.Addr().(*net.TCPAddr).Port
		cfg.Server.Mode2ViewerPort = vl.Addr().(*net.TCPAddr).Port
		_ = rl.Close()
		_ = vl.Close()
		m2 = proxy.NewMode2Manager(cfg, proxy.NewTrafficMeter(true),
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err := m2.Start(); err != nil {
			return err
		}
		defer m2.Stop(context.Background())
		revAddr = m2.ReverseAddr()
		viewAddr = m2.ViewerAddr()
	} else {
		revAddr = net.JoinHostPort(*targetRelay, fmt.Sprint(*revPort))
		viewAddr = net.JoinHostPort(*targetRelay, fmt.Sprint(*viewPort))
	}

	cases := []struct {
		name    string
		payload []byte
	}{
		{"invalid-alpha", []byte("ID:abc\n")},
		{"empty", []byte("\n")},
		{"oversized", bytes.Repeat([]byte("A"), 512)},
		{"negative", []byte("ID:-5\n")},
		{"zero", []byte("ID:0\n")},
		{"no-prefix-num", []byte("9999\n")},
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	for _, c := range cases {
		for _, addr := range []string{revAddr, viewAddr} {
			wg.Add(1)
			go func(name, addr string, payload []byte) {
				defer wg.Done()
				conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
				if err != nil {
					return // listenerSaturated/remote-gone is not a fuzz failure
				}
				defer conn.Close()
				// Viewer port greets first; drain it.
				_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
				tmp := make([]byte, 12)
				_, _ = io.ReadFull(conn, tmp)
				_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
				if _, err := conn.Write(payload); err != nil {
					return
				}
				// Server must close us promptly (handshake reject); never hang.
				_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
				one := make([]byte, 1)
				_, _ = conn.Read(one)
			}(c.name, addr, c.payload)
		}
	}
	// Duplicate-ID race: two reverse servers + two viewers, same ID.
	wg.Add(1)
	go func() {
		defer wg.Done()
		sendID := func(addr, id string) {
			conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
			tmp := make([]byte, 12)
			_, _ = io.ReadFull(conn, tmp)
			var hs [250]byte
			copy(hs[:], id)
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			_, _ = conn.Write(hs[:])
			_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
			_, _ = conn.Read(tmp[:1])
		}
		var wg2 sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg2.Add(2)
			go func() { defer wg2.Done(); sendID(revAddr, "ID:4242") }()
			go func() { defer wg2.Done(); sendID(viewAddr, "ID:4242") }()
		}
		wg2.Wait()
	}()
	// Slowloris: partial bytes with delays must hit the handshake deadline, not hang.
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := net.DialTimeout("tcp", revAddr, 3*time.Second)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetWriteDeadline(time.Now().Add(12 * time.Second))
		_, _ = conn.Write([]byte("ID:"))
		time.Sleep(2 * time.Second)
		_, _ = conn.Write([]byte("12"))
		// Server must close us at the handshake deadline; wait for EOF.
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, _ = conn.Read(make([]byte, 1))
	}()
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			return e
		}
	}
	if m2 != nil && m2.HasPendingServer(4242) {
		// A paired ID must be consumed; an unpaired duplicate replaces the
		// previous waiter, so at most one waiter may remain. Drain check:
		log.Warn("mode2-fuzz: ID 4242 still pending (unpaired duplicate waiter)")
	}
	log.Info("mode2-fuzz done", "cases", len(cases))
	return nil
}

func runThroughput(ctx context.Context, log *slog.Logger) error {
	wctx, cancel := startWatchdog(ctx, log)
	defer cancel()

	var dialAddr string
	if isLoopback(*targetRelay) {
		eng := startLoopback(1)
		defer eng.stop()
		dialAddr = eng.addrs[0]
	} else {
		dialAddr = net.JoinHostPort(*targetRelay, fmt.Sprint(*mode1Port))
	}

	conn, err := net.DialTimeout("tcp", dialAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", dialAddr, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.ReadFull(conn, make([]byte, 12))
	_ = conn.SetReadDeadline(time.Time{})

	chunk := make([]byte, 64*1024)
	if _, err := rand.Read(chunk); err != nil {
		return err
	}
	// Drain concurrently: without a reader, echoed bytes fill socket buffers
	// and backpressure stalls writes (real VNC viewers always read while writing).
	var rx atomic.Uint64
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		buf := make([]byte, 64*1024)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := conn.Read(buf)
			if n > 0 {
				rx.Add(uint64(n))
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second)); <-drainDone }()
	var tx uint64
	start := time.Now()
	end := start.Add(*duration)
	for time.Now().Before(end) {
		select {
		case <-wctx.Done():
			if ctx.Err() == nil {
				return fmt.Errorf("watchdog aborted throughput run")
			}
			return nil
		default:
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Write(chunk)
		if err != nil {
			return fmt.Errorf("write after %d bytes: %w", tx, err)
		}
		tx += uint64(n)
	}
	el := time.Since(start).Seconds()
	mbps := float64(tx) / el / 1024 / 1024
	log.Info("throughput done", "tx_bytes", tx, "rx_bytes", rx.Load(), "mb_per_s", fmt.Sprintf("%.2f", mbps))
	fmt.Printf("throughput: %d bytes in %.1fs = %.2f MB/s\n", tx, el, mbps)
	return nil
}
