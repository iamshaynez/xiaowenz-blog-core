package store

import (
	"sync"
	"testing"
)

// The dedupe check must be atomic with the insert: N concurrent same-day
// records for one (slug, ip_hash) must count exactly once.
func TestRecordDownloadDedupIsAtomic(t *testing.T) {
	s := newTestStore(t)

	const goroutines = 16
	var wg sync.WaitGroup
	results := make([]bool, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			counted, err := s.RecordDownloadDedup("ep-race", "same-ip-hash", "ua", "ref")
			if err != nil {
				t.Errorf("RecordDownloadDedup: %v", err)
				return
			}
			results[idx] = counted
		}(i)
	}
	wg.Wait()

	counted := 0
	for _, c := range results {
		if c {
			counted++
		}
	}
	if counted != 1 {
		t.Errorf("counted = %d, want exactly 1 (atomic insert)", counted)
	}
	stats, err := s.DownloadStats(0)
	if err != nil {
		t.Fatalf("DownloadStats: %v", err)
	}
	if stats["ep-race"] != 1 {
		t.Errorf("rows for ep-race = %d, want 1", stats["ep-race"])
	}
}
