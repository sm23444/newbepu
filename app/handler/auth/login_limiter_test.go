package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginAttemptLimiterBlocksUntilWindowExpires(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	limiter := newLoginAttemptLimiter(3, time.Minute, 8, func() time.Time {
		return now
	})

	for attempt := 1; attempt <= 3; attempt++ {
		if !limiter.reserve("client\x00admin") {
			t.Fatalf("attempt %d was blocked before the limit", attempt)
		}
	}
	if limiter.reserve("client\x00admin") {
		t.Fatal("attempt above the limit was allowed")
	}

	now = now.Add(time.Minute - time.Nanosecond)
	if limiter.reserve("client\x00admin") {
		t.Fatal("attempt was allowed before the window expired")
	}

	now = now.Add(time.Nanosecond)
	if !limiter.reserve("client\x00admin") {
		t.Fatal("first attempt in a new window was blocked")
	}
}

func TestLoginAttemptLimiterClearResetsFailures(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	limiter := newLoginAttemptLimiter(2, time.Minute, 8, func() time.Time {
		return now
	})

	key := "client\x00admin"
	if !limiter.reserve(key) || !limiter.reserve(key) {
		t.Fatal("attempts up to the limit should be allowed")
	}
	if limiter.reserve(key) {
		t.Fatal("attempt above the limit was allowed")
	}

	limiter.clear(key)
	if !limiter.reserve(key) || !limiter.reserve(key) {
		t.Fatal("clear did not reset the attempt count")
	}
	if limiter.reserve(key) {
		t.Fatal("attempt limit was not enforced after reset")
	}
}

func TestLoginAttemptLimiterBoundsTrackedKeys(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	limiter := newLoginAttemptLimiter(1, time.Minute, 2, func() time.Time {
		return now
	})

	if !limiter.reserve("oldest") || !limiter.reserve("newer") || !limiter.reserve("newest") {
		t.Fatal("first attempt for a key should be allowed")
	}
	if len(limiter.entries) != 2 {
		t.Fatalf("tracked keys = %d, want 2", len(limiter.entries))
	}
	if _, exists := limiter.entries["oldest"]; exists {
		t.Fatal("oldest key was not evicted at capacity")
	}

	now = now.Add(time.Minute)
	if !limiter.reserve("after-expiry") {
		t.Fatal("first attempt after expiration was blocked")
	}
	if len(limiter.entries) != 1 {
		t.Fatalf("expired keys were not pruned; tracked keys = %d", len(limiter.entries))
	}
}

func TestLoginAttemptLimiterReservesConcurrentAttempts(t *testing.T) {
	const (
		maxAttempts = 5
		workers     = 100
	)

	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	limiter := newLoginAttemptLimiter(maxAttempts, time.Minute, 8, func() time.Time {
		return now
	})

	var allowed atomic.Int32
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			if limiter.reserve("client\x00admin") {
				allowed.Add(1)
			}
		}()
	}
	wait.Wait()

	if got := allowed.Load(); got != maxAttempts {
		t.Fatalf("allowed concurrent attempts = %d, want %d", got, maxAttempts)
	}
}

func TestLoginLimiterKeySeparatesClientAndAccount(t *testing.T) {
	first := loginLimiterKey("192.0.2.1", "admin")
	if first == loginLimiterKey("192.0.2.2", "admin") {
		t.Fatal("different client IPs produced the same limiter key")
	}
	if first == loginLimiterKey("192.0.2.1", "operator") {
		t.Fatal("different accounts produced the same limiter key")
	}
}
