package web

import (
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
