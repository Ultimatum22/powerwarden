package auth

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestRateLimiterAllowsWithinBurst(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Minute), 3, time.Second, time.Minute)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !rl.Allow("ip:1.2.3.4", now) {
			t.Fatalf("attempt %d should be allowed within burst", i)
		}
	}
	if rl.Allow("ip:1.2.3.4", now) {
		t.Fatal("4th immediate attempt should exceed the burst")
	}
}

func TestRateLimiterLocksOutAfterFailures(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, 10*time.Second)
	now := time.Now()
	key := "user:dave"

	if !rl.Allow(key, now) {
		t.Fatal("first attempt should be allowed")
	}
	rl.Failure(key, now)

	if rl.Allow(key, now.Add(500*time.Millisecond)) {
		t.Fatal("expected lockout to still be active after 500ms (base 1s)")
	}
	if !rl.Allow(key, now.Add(2*time.Second)) {
		t.Fatal("expected lockout to have cleared after 2s")
	}
}

func TestRateLimiterBackoffGrowsExponentially(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, time.Hour)
	now := time.Now()
	key := "user:dave"

	rl.Failure(key, now) // lockout ~1s
	rl.Failure(key, now) // lockout ~2s
	rl.Failure(key, now) // lockout ~4s

	if rl.Allow(key, now.Add(3*time.Second)) {
		t.Fatal("expected the lockout to have grown past 3s after 3 consecutive failures")
	}
	if !rl.Allow(key, now.Add(5*time.Second)) {
		t.Fatal("expected the lockout to have cleared by 5s")
	}
}

func TestRateLimiterBackoffCapsAtMax(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, 5*time.Second)
	now := time.Now()
	key := "user:dave"
	for i := 0; i < 20; i++ {
		rl.Failure(key, now)
	}
	if rl.Allow(key, now.Add(4*time.Second)) {
		t.Fatal("expected lockout still active just under the 5s cap")
	}
	if !rl.Allow(key, now.Add(6*time.Second)) {
		t.Fatal("expected lockout to clear just past the capped 5s wait, not grow unbounded")
	}
}

func TestRateLimiterSuccessClearsFailures(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, time.Hour)
	now := time.Now()
	key := "user:dave"
	rl.Failure(key, now)
	rl.Success(key)
	if !rl.Allow(key, now) {
		t.Fatal("expected Success to clear the lockout immediately")
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, time.Hour)
	now := time.Now()
	rl.Failure("ip:1.2.3.4", now)
	if !rl.Allow("ip:5.6.7.8", now) {
		t.Fatal("a lockout on one key must not affect a different key")
	}
}

func TestRateLimiterPrune(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Millisecond), 100, time.Second, time.Hour)
	now := time.Now()
	rl.Allow("ip:1.2.3.4", now)
	rl.Prune(now.Add(time.Hour), 10*time.Minute)

	rl.mu.Lock()
	_, stillPresent := rl.entries["ip:1.2.3.4"]
	rl.mu.Unlock()
	if stillPresent {
		t.Fatal("expected the idle entry to have been pruned")
	}
}
