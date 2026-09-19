package ratelimit

import (
	"testing"
	"time"
)

func TestAllowBurstThenReject(t *testing.T) {
	l := New(3, time.Minute)
	defer l.Close()
	ip := "1.2.3.4"
	for i := 0; i < 3; i++ {
		if !l.Allow(ip) {
			t.Fatalf("request %d within burst must be allowed", i+1)
		}
	}
	if l.Allow(ip) {
		t.Error("4th request within window must be rejected")
	}
}

func TestWindowRefill(t *testing.T) {
	now := time.Now()
	l := New(2, time.Minute)
	l.now = func() time.Time { return now }
	defer l.Close()

	if !l.Allow("ip") || !l.Allow("ip") {
		t.Fatal("burst must be allowed")
	}
	if l.Allow("ip") {
		t.Fatal("over burst must be rejected")
	}
	// Advance past the window: bucket resets.
	l.now = func() time.Time { return now.Add(2 * time.Minute) }
	if !l.Allow("ip") {
		t.Error("after window reset, request must be allowed")
	}
}

func TestPerIPIsolation(t *testing.T) {
	l := New(1, time.Minute)
	defer l.Close()
	if !l.Allow("a") {
		t.Fatal("a first request must pass")
	}
	if l.Allow("a") {
		t.Fatal("a second request must fail")
	}
	if !l.Allow("b") {
		t.Fatal("b must have its own bucket")
	}
}

func TestCleanupRemovesStaleBuckets(t *testing.T) {
	now := time.Now()
	l := New(1, time.Minute)
	l.now = func() time.Time { return now }
	defer l.Close()

	l.Allow("stale-ip")
	l.Allow("fresh-ip")

	// Move time forward: stale bucket expires, then cleanup runs.
	l.now = func() time.Time { return now.Add(10 * time.Minute) }
	l.Allow("fresh-ip") // touch so fresh bucket stays recent-ish
	l.cleanup()

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.buckets["stale-ip"]; ok {
		t.Error("stale bucket not cleaned")
	}
}
