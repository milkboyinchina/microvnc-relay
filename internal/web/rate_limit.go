package web

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	maxTrackedIPs       = 256
	defaultMaxAttempts  = 5
	defaultWindow       = 5 * time.Minute
	defaultLockout      = 15 * time.Minute
	progressiveDelayDur = 1500 * time.Millisecond
)

type ipAttemptState struct {
	count       int
	firstFailed time.Time
	lastFailed  time.Time
	lockedUntil time.Time
}

// LoginRateLimiter enforces in-memory brute force protection with progressive
// delay and temporary IP lockout, preventing bcrypt CPU starvation on low-power
// devices. The tracked map is strictly bounded; when full, the least-offending
// unlocked entry is evicted. If every entry is actively locked, new attempts
// are rejected (fail closed) rather than growing the map.
type LoginRateLimiter struct {
	mu          sync.Mutex
	attempts    map[string]*ipAttemptState
	maxAttempts int
	window      time.Duration
	lockout     time.Duration
}

// NewLoginRateLimiter creates a new LoginRateLimiter with bounded memory envelope.
func NewLoginRateLimiter() *LoginRateLimiter {
	return &LoginRateLimiter{
		attempts:    make(map[string]*ipAttemptState),
		maxAttempts: defaultMaxAttempts,
		window:      defaultWindow,
		lockout:     defaultLockout,
	}
}

// ExtractClientIP extracts the remote IP address, stripping any port number.
// X-Forwarded-For is deliberately ignored: a reverse proxy deployment must be
// configured to pass the real source address (for example, Docker host
// networking or a trusted proxy that rewrites RemoteAddr).
func ExtractClientIP(r *http.Request) string {
	remote := r.RemoteAddr
	host, _, err := net.SplitHostPort(remote)
	if err == nil {
		return host
	}
	return strings.TrimSpace(remote)
}

// normalizeLimiterKey groups IPv4-mapped IPv6 with IPv4 and groups all IPv6
// addresses in the same /64. An attacker with a routed /64 would otherwise get
// 2^64 independent buckets.
func normalizeLimiterKey(ip string) string {
	host := strings.TrimSpace(ip)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if idx := strings.LastIndexByte(host, '%'); idx >= 0 {
		host = host[:idx]
	}

	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is6() {
		if prefix, err := addr.Prefix(64); err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}

// Check evaluates whether an IP is currently locked out, and whether
// progressive delay applies.
func (l *LoginRateLimiter) Check(ip string) (locked bool, remainingLockout time.Duration, delay time.Duration) {
	key := normalizeLimiterKey(ip)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	state, exists := l.attempts[key]
	if !exists {
		return false, 0, 0
	}

	if now.Before(state.lockedUntil) {
		return true, state.lockedUntil.Sub(now), 0
	}

	if !state.lockedUntil.IsZero() && now.After(state.lockedUntil) {
		delete(l.attempts, key)
		return false, 0, 0
	}

	if now.Sub(state.firstFailed) > l.window {
		delete(l.attempts, key)
		return false, 0, 0
	}

	// Progressive delay applies for the 3rd and 4th failed attempts. The
	// handler rejects immediately with 429 + Retry-After; it never sleeps.
	if state.count >= 3 {
		delay = progressiveDelayDur
	}

	return false, 0, delay
}

// RecordFailure records a failed login attempt for the given IP and applies
// lockout if the threshold is reached.
func (l *LoginRateLimiter) RecordFailure(ip string) (locked bool, count int, lockoutDuration time.Duration) {
	key := normalizeLimiterKey(ip)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.attempts) >= maxTrackedIPs {
		l.pruneExpiredLocked(now)
		if len(l.attempts) >= maxTrackedIPs {
			if !l.evictLeastOffendingLocked(now) {
				// Every tracked entry is actively locked. Fail closed instead
				// of growing the map without bound.
				slog.Error("SECURITY ALERT: login limiter saturated with locked entries; rejecting new attempts",
					"tracked_ips", len(l.attempts),
				)
				return true, 0, l.lockout
			}
		}
	}

	state, exists := l.attempts[key]
	if !exists || now.Sub(state.firstFailed) > l.window {
		state = &ipAttemptState{
			count:       1,
			firstFailed: now,
			lastFailed:  now,
		}
		l.attempts[key] = state
	} else {
		state.count++
		state.lastFailed = now
	}

	if state.count >= l.maxAttempts {
		state.lockedUntil = now.Add(l.lockout)
		slog.Error("SECURITY ALERT: IP locked out for brute-force defense",
			"ip", key,
			"failed_attempts", state.count,
			"lockout_minutes", int(l.lockout.Minutes()),
		)
		return true, state.count, l.lockout
	}

	slog.Warn("Failed WebUI login attempt",
		"ip", key,
		"attempt", state.count,
		"max_allowed", l.maxAttempts,
	)

	return false, state.count, 0
}

// RecordSuccess clears any failure state for the given IP upon successful
// authentication.
func (l *LoginRateLimiter) RecordSuccess(ip string) {
	key := normalizeLimiterKey(ip)

	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

func (l *LoginRateLimiter) pruneExpiredLocked(now time.Time) {
	for key, state := range l.attempts {
		lockExpired := !state.lockedUntil.IsZero() && now.After(state.lockedUntil)
		windowLapsed := now.Sub(state.firstFailed) > l.window
		if lockExpired || windowLapsed {
			delete(l.attempts, key)
		}
	}
}

func (l *LoginRateLimiter) evictLeastOffendingLocked(now time.Time) bool {
	var candidateKey string
	var candidate *ipAttemptState

	for key, state := range l.attempts {
		if now.Before(state.lockedUntil) {
			continue
		}
		if candidate == nil ||
			state.count < candidate.count ||
			(state.count == candidate.count && state.lastFailed.Before(candidate.lastFailed)) {
			candidateKey = key
			candidate = state
		}
	}

	if candidateKey == "" {
		return false
	}
	delete(l.attempts, candidateKey)
	return true
}
