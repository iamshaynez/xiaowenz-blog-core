package store

import (
	"testing"
	"time"
)

func TestDownloadSameDayDedupe(t *testing.T) {
	s := newTestStore(t)

	// First hit of the day: recorded as a fresh download.
	first, err := s.RecordDownloadDedup("ep1", "iphash-a", "ua", "ref")
	if err != nil {
		t.Fatalf("RecordDownloadDedup: %v", err)
	}
	if !first {
		t.Error("first download of the day must count")
	}
	// Same slug + same ip_hash same day: deduped.
	again, err := s.RecordDownloadDedup("ep1", "iphash-a", "ua", "ref")
	if err != nil {
		t.Fatalf("RecordDownloadDedup again: %v", err)
	}
	if again {
		t.Error("same-day repeat from same ip_hash must not count")
	}
	// Different ip_hash counts separately.
	other, _ := s.RecordDownloadDedup("ep1", "iphash-b", "ua", "ref")
	if !other {
		t.Error("different ip_hash must count")
	}
	// Different slug counts separately.
	diff, _ := s.RecordDownloadDedup("ep2", "iphash-a", "ua", "ref")
	if !diff {
		t.Error("different slug must count")
	}
}

func TestDownloadStatsPerSlug(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.now = func() time.Time { return now }

	_, _ = s.RecordDownloadDedup("ep1", "h1", "", "")
	_, _ = s.RecordDownloadDedup("ep1", "h2", "", "")
	_, _ = s.RecordDownloadDedup("ep2", "h1", "", "")

	stats, err := s.DownloadStats(now.Add(-30 * 24 * time.Hour).Unix())
	if err != nil {
		t.Fatalf("DownloadStats: %v", err)
	}
	if stats["ep1"] != 2 || stats["ep2"] != 1 {
		t.Errorf("stats = %v, want ep1=2 ep2=1", stats)
	}

	// Old downloads outside the window excluded.
	old := now.Add(-40 * 24 * time.Hour)
	if err := s.insertDownloadAt("ep1", "h9", old); err != nil {
		t.Fatalf("insertDownloadAt: %v", err)
	}
	stats, _ = s.DownloadStats(now.Add(-30 * 24 * time.Hour).Unix())
	if stats["ep1"] != 2 {
		t.Errorf("stats after old insert = %v, ep1 should stay 2", stats)
	}
}

func TestPageviewRecordAndStats(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.now = func() time.Time { return now }

	for _, pv := range [][2]string{{"/a", "h1"}, {"/a", "h2"}, {"/b", "h1"}} {
		if err := s.RecordPageview(pv[0], "ref", pv[1], "ua"); err != nil {
			t.Fatalf("RecordPageview: %v", err)
		}
	}
	pv, uv, err := s.PageviewStats(now.Add(-30 * 24 * time.Hour).Unix())
	if err != nil {
		t.Fatalf("PageviewStats: %v", err)
	}
	if pv != 3 || uv != 2 {
		t.Errorf("pv=%d uv=%d, want 3/2", pv, uv)
	}
}

func TestMediaCRUD(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateMedia("audio/ep1.mp3", "https://media.example.com/audio/ep1.mp3", 12345, "audio/mpeg"); err != nil {
		t.Fatalf("CreateMedia: %v", err)
	}
	m, err := s.GetMediaByKey("audio/ep1.mp3")
	if err != nil {
		t.Fatalf("GetMediaByKey: %v", err)
	}
	if m.URL != "https://media.example.com/audio/ep1.mp3" || m.Bytes != 12345 {
		t.Errorf("media = %+v", m)
	}
	if _, err := s.GetMediaByKey("audio/none.mp3"); err == nil {
		t.Error("missing key must error")
	}
	// Duplicate key rejected.
	if err := s.CreateMedia("audio/ep1.mp3", "u", 1, "c"); err == nil {
		t.Error("duplicate key must fail")
	}
	list, err := s.ListMedia()
	if err != nil {
		t.Fatalf("ListMedia: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("media list = %d, want 1", len(list))
	}
}

func TestTokenGeneration(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	b, _ := NewToken()
	if len(a) != 64 {
		t.Errorf("token len = %d, want 64 hex chars", len(a))
	}
	if a == b {
		t.Error("tokens must be unique")
	}
}

func TestHashIPDailyRotation(t *testing.T) {
	day1 := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	h1 := HashIP("1.2.3.4", day1)
	h1b := HashIP("1.2.3.4", day1)
	h2 := HashIP("1.2.3.4", day2)
	if h1 != h1b {
		t.Error("same ip same day must hash equal")
	}
	if h1 == h2 {
		t.Error("hash must rotate daily")
	}
	if len(h1) != 64 {
		t.Errorf("hash len = %d, want 64", len(h1))
	}
}
