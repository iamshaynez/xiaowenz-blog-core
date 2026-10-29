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

// ── 富统计：时间线 / 路径 / 来源（admin 统计页的数据地基）────────────

func TestPageviewTimeseries(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local)
	// D1：/a 两个访客 + /b 一个访客
	s.now = func() time.Time { return base }
	_ = s.RecordPageview("/a", "https://g.com", "ip1", "ua")
	_ = s.RecordPageview("/a", "", "ip2", "ua")
	_ = s.RecordPageview("/b", "", "ip1", "ua")
	// D2：/a 一个回访访客
	s.now = func() time.Time { return base.AddDate(0, 0, 1) }
	_ = s.RecordPageview("/a", "https://t.co", "ip1", "ua")

	ts, err := s.PageviewTimeseries(0)
	if err != nil {
		t.Fatalf("PageviewTimeseries: %v", err)
	}
	if len(ts) != 2 {
		t.Fatalf("days = %d, want 2: %+v", len(ts), ts)
	}
	d1, d2 := ts[0], ts[1]
	if d1.Day != base.Format("2006-01-02") || d1.PV != 3 || d1.UV != 2 {
		t.Errorf("day1 = %+v, want {2026-10-27 3 2}", d1)
	}
	if d2.PV != 1 || d2.UV != 1 {
		t.Errorf("day2 = %+v, want pv=1 uv=1（同日 ip 去重、跨日独立计）", d2)
	}
}

func TestPathStatsOrdered(t *testing.T) {
	s := newTestStore(t)
	for _, x := range []struct{ path, ip string }{
		{"/blog/hot", "ip1"}, {"/blog/hot", "ip2"}, {"/blog/hot", "ip1"},
		{"/blog/cold", "ip1"},
	} {
		if err := s.RecordPageview(x.path, "", x.ip, "ua"); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := s.PathStats(0, 10)
	if err != nil {
		t.Fatalf("PathStats: %v", err)
	}
	if len(paths) != 2 || paths[0].Path != "/blog/hot" || paths[0].PV != 3 || paths[0].UV != 2 {
		t.Fatalf("paths = %+v, want hot(3/2) first", paths)
	}
	if paths[1].Path != "/blog/cold" || paths[1].PV != 1 {
		t.Errorf("paths[1] = %+v", paths[1])
	}
}

func TestReferrerStatsGrouped(t *testing.T) {
	s := newTestStore(t)
	for _, x := range []struct{ ref, ip string }{
		{"https://google.com", "ip1"}, {"https://google.com", "ip2"},
		{"", "ip1"}, {"", "ip3"}, {"", "ip1"},
	} {
		if err := s.RecordPageview("/p", x.ref, x.ip, "ua"); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := s.ReferrerStats(0, 10)
	if err != nil {
		t.Fatalf("ReferrerStats: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 2 groups", refs)
	}
	if refs[0].Referrer != "" || refs[0].PV != 3 || refs[0].UV != 2 {
		t.Errorf("直接访问应为首位: %+v", refs[0])
	}
	if refs[1].Referrer != "https://google.com" || refs[1].PV != 2 {
		t.Errorf("refs[1] = %+v", refs[1])
	}
}
