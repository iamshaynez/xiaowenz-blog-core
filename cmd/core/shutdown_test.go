package main

import (
	"testing"
	"time"
)

func TestWaitWithTimeoutCompletes(t *testing.T) {
	done := waitWithTimeout(func() {}, 5*time.Second)
	if !done {
		t.Error("fast wait must report completion")
	}
}

func TestWaitWithTimeoutGivesUp(t *testing.T) {
	start := time.Now()
	done := waitWithTimeout(func() { time.Sleep(10 * time.Second) }, 50*time.Millisecond)
	if done {
		t.Error("blocked wait must report timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waitWithTimeout blocked %v, want ~50ms", elapsed)
	}
}
