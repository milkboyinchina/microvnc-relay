package web

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxTrackedIPs       = 128
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

// LoginRateLimiter enforces in-memory brute force protection with progressive delay
// and temporary IP lockout, preventing bcrypt CPU starvation on low-power devices.
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
func ExtractClientIP(r *http.Request) string {
	remote := r.RemoteAddr
	host, _, err := net.SplitHostPort(remote)
	if err == nil {
		return host
	}
	return strings.TrimSpace(remote)
}

// Check evaluates whether an IP is currently locked out, and whether progressive delay applies.
func (l *LoginRateLimiter) Check(ip string) (locked bool, remainingLockout time.Duration, delay time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	state, exists := l.attempts[ip]
	if !exists {
		return false, 0, 0
	}

	// Check if active lockout applies
	if now.Before(state.lockedUntil) {
		return true, state.lockedUntil.Sub(now), 0
	}

	// If lockout expired or window lapsed, reset
	if now.After(state.lockedUntil) && !state.lockedUntil.IsZero() {
		delete(l.attempts, ip)
		return false, 0, 0
	}

	if now.Sub(state.firstFailed) > l.window {
		delete(l.attempts, ip)
		return false, 0, 0
	}

	// Progressive delay applied for 3rd and 4th failed attempts
	if state.count >= 3 {
		delay = progressiveDelayDur
	}

	return false, 0, delay
}

// RecordFailure records a failed login attempt for the given IP and applies lockout if threshold reached.
func (l *LoginRateLimiter) RecordFailure(ip string) (locked bool, count int, lockoutDuration time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	// Prune expired entries if approaching max capacity
	if len(l.attempts) >= maxTrackedIPs {
		for k, v := range l.attempts {
			if now.After(v.lockedUntil) && !v.lockedUntil.IsZero() {
				delete(l.attempts, k)
			} else if now.Sub(v.firstFailed) > l.window {
				delete(l.attempts, k)
			}
		}
	}

	state, exists := l.attempts[ip]
	if !exists || now.Sub(state.firstFailed) > l.window {
		state = &ipAttemptState{
			count:       1,
			firstFailed: now,
			lastFailed:  now,
		}
		l.attempts[ip] = state
	} else {
		state.count++
		state.lastFailed = now
	}

	if state.count >= l.maxAttempts {
		state.lockedUntil = now.Add(l.lockout)
		slog.Error("SECURITY ALERT: IP locked out for brute-force defense",
			"ip", ip,
			"failed_attempts", state.count,
			"lockout_minutes", int(l.lockout.Minutes()),
		)
		return true, state.count, l.lockout
	}

	slog.Warn("Failed WebUI login attempt",
		"ip", ip,
		"attempt", state.count,
		"max_allowed", l.maxAttempts,
	)

	return false, state.count, 0
}

// RecordSuccess clears any failure state for the given IP upon successful authentication.
func (l *LoginRateLimiter) RecordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}
