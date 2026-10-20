package web

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginRateLimiterLockoutAndReset(t *testing.T) {
	limiter := NewLoginRateLimiter()
	limiter.window = 1 * time.Second
	limiter.lockout = 50 * time.Millisecond
	testIP := "192.168.1.50"

	// 1. Attempts 1 to 4 should not lock out
	for i := 1; i <= 4; i++ {
		locked, count, _ := limiter.RecordFailure(testIP)
		if locked {
			t.Fatalf("expected not locked on attempt %d", i)
		}
		if count != i {
			t.Fatalf("expected count %d, got %d", i, count)
		}
	}

	// 2. 5th attempt triggers lockout
	locked, count, lockoutDur := limiter.RecordFailure(testIP)
	if !locked || count != 5 || lockoutDur == 0 {
		t.Fatalf("expected locked out on 5th attempt, got locked=%v count=%d", locked, count)
	}

	// Check reports locked
	isLocked, remaining, _ := limiter.Check(testIP)
	if !isLocked || remaining <= 0 {
		t.Fatalf("expected Check to return locked, got %v", isLocked)
	}

	// 3. After lockout expires, IP is unlocked
	time.Sleep(60 * time.Millisecond)
	isLocked, _, _ = limiter.Check(testIP)
	if isLocked {
		t.Fatalf("expected lockout to expire, still locked")
	}

	// 4. RecordSuccess clears state
	limiter.RecordFailure(testIP)
	limiter.RecordSuccess(testIP)
	isLocked, _, _ = limiter.Check(testIP)
	if isLocked {
		t.Fatalf("expected clean state after RecordSuccess")
	}
}

func TestLoginRateLimiterGroupsIPv6BySlash64(t *testing.T) {
	limiter := NewLoginRateLimiter()
	for i := 0; i < limiter.maxAttempts; i++ {
		limiter.RecordFailure("2001:db8:1:2:3:4:5:6")
	}

	// Same /64, different host bits: must share the lockout.
	if locked, _, _ := limiter.Check("2001:db8:1:2:ffff::1"); !locked {
		t.Fatal("expected addresses in the same /64 to share a lockout")
	}

	// Different /64: must not be locked.
	if locked, _, _ := limiter.Check("2001:db8:1:3::1"); locked {
		t.Fatal("addresses in a different /64 must not share a lockout")
	}
}

func TestLoginRateLimiterNormalizesIPv4MappedIPv6(t *testing.T) {
	limiter := NewLoginRateLimiter()
	for i := 0; i < limiter.maxAttempts; i++ {
		limiter.RecordFailure("::ffff:192.168.1.5")
	}

	if locked, _, _ := limiter.Check("192.168.1.5"); !locked {
		t.Fatal("IPv4 and IPv4-mapped IPv6 must share a lockout bucket")
	}
}

func TestLoginRateLimiterEvictsLeastOffendingUnlockedEntry(t *testing.T) {
	limiter := NewLoginRateLimiter()
	now := time.Now()

	limiter.mu.Lock()
	for i := 0; i < maxTrackedIPs; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		limiter.attempts[ip] = &ipAttemptState{
			count:       1,
			firstFailed: now,
			lastFailed:  now.Add(time.Duration(i) * time.Millisecond),
		}
	}
	limiter.mu.Unlock()

	// The map is full. A new failure must evict the oldest least-offending
	// unlocked entry (10.0.0.0) rather than growing without bound.
	limiter.RecordFailure("192.0.2.1")

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.attempts) != maxTrackedIPs {
		t.Fatalf("expected bounded map of %d entries, got %d", maxTrackedIPs, len(limiter.attempts))
	}
	if _, ok := limiter.attempts["10.0.0.0"]; ok {
		t.Fatal("expected oldest least-offending unlocked entry to be evicted")
	}
	if _, ok := limiter.attempts["192.0.2.1"]; !ok {
		t.Fatal("expected the new failure to be tracked")
	}
}

func TestLoginRateLimiterFailsClosedWhenAllEntriesAreLocked(t *testing.T) {
	limiter := NewLoginRateLimiter()
	now := time.Now()

	limiter.mu.Lock()
	for i := 0; i < maxTrackedIPs; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		limiter.attempts[ip] = &ipAttemptState{
			count:       limiter.maxAttempts,
			firstFailed: now,
			lastFailed:  now,
			lockedUntil: now.Add(time.Hour),
		}
	}
	limiter.mu.Unlock()

	locked, _, _ := limiter.RecordFailure("192.0.2.1")
	if !locked {
		t.Fatal("expected a saturated all-locked limiter to fail closed")
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.attempts) != maxTrackedIPs {
		t.Fatalf("expected the map to remain bounded at %d entries, got %d", maxTrackedIPs, len(limiter.attempts))
	}
}
