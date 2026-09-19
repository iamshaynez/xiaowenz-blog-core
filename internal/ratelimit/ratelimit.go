// Package ratelimit provides in-memory per-key token buckets with periodic
// cleanup. Buckets live in-process; limits are per SPEC §3.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	remaining int
	reset     time.Time
}

// Limiter allows `rate` requests per `window` per key.
type Limiter struct {
	mu       sync.Mutex
	rate     int
	window   time.Duration
	buckets  map[string]*bucket
	now      func() time.Time
	stop     chan struct{}
	stopOnce sync.Once
}

// New creates a limiter and starts its periodic cleanup goroutine.
func New(rate int, window time.Duration) *Limiter {
	l := &Limiter{
		rate:    rate,
		window:  window,
		buckets: map[string]*bucket{},
		now:     time.Now,
		stop:    make(chan struct{}),
	}
	go func() {
		tick := time.NewTicker(window)
		defer tick.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-tick.C:
				l.cleanup()
			}
		}
	}()
	return l
}

// Allow consumes one token for key; false when the bucket is exhausted.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = &bucket{remaining: l.rate - 1, reset: now.Add(l.window)}
		return true
	}
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}

// Close stops the cleanup goroutine.
func (l *Limiter) Close() {
	l.stopOnce.Do(func() { close(l.stop) })
}

// cleanup drops buckets whose window has long expired.
func (l *Limiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-2 * l.window)
	for k, b := range l.buckets {
		if b.reset.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}
