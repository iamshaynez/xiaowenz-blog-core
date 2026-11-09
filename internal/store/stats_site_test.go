package store

import (
	"testing"
	"time"
)

// ── 多站维度：写入带 site，读数按 site 隔离 ──────────────────────────

func TestPageviewStatsIsolatedBySite(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }

	// 博客（site=''）两条；ask 站三条（两访客）。
	_ = s.RecordPageview("", "/blog/a", "", "h1", "ua")
	_ = s.RecordPageview("", "/blog/a", "", "h2", "ua")
	for _, ip := range []string{"h1", "h1", "h3"} {
		_ = s.RecordPageview("ask.xiaowenz.com", "/q", "https://g.com", ip, "ua")
	}

	pv, uv, err := s.PageviewStats("", now.Add(-24*time.Hour).Unix())
	if err != nil {
		t.Fatalf("PageviewStats(blog): %v", err)
	}
	if pv != 2 || uv != 2 {
		t.Errorf("blog pv=%d uv=%d, want 2/2（不得混入他站）", pv, uv)
	}
	pv, uv, err = s.PageviewStats("ask.xiaowenz.com", now.Add(-24*time.Hour).Unix())
	if err != nil {
		t.Fatalf("PageviewStats(ask): %v", err)
	}
	if pv != 3 || uv != 2 {
		t.Errorf("ask pv=%d uv=%d, want 3/2", pv, uv)
	}
	// 未注册过的站读数为 0，不报错。
	if pv, uv, _ = s.PageviewStats("nope.example.com", 0); pv != 0 || uv != 0 {
		t.Errorf("unknown site = %d/%d, want 0/0", pv, uv)
	}
}

func TestPathStatsScopedBySite(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	_ = s.RecordPageview("", "/same", "", "h1", "ua")
	_ = s.RecordPageview("card.xiaowenz.com", "/same", "", "h2", "ua")
	_ = s.RecordPageview("card.xiaowenz.com", "/same", "", "h3", "ua")

	paths, err := s.PathStats("", 0, 10)
	if err != nil {
		t.Fatalf("PathStats(blog): %v", err)
	}
	if len(paths) != 1 || paths[0].PV != 1 {
		t.Errorf("blog paths = %+v, want 1 row pv=1", paths)
	}
	paths, err = s.PathStats("card.xiaowenz.com", 0, 10)
	if err != nil {
		t.Fatalf("PathStats(card): %v", err)
	}
	if len(paths) != 1 || paths[0].PV != 2 || paths[0].UV != 2 {
		t.Errorf("card paths = %+v, want pv=2 uv=2", paths)
	}
}

// rollup 对多站各自聚合；压缩后各站指标互不串扰。
func TestRollupAndCompactPerSite(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.Local)
	yesterday := now.AddDate(0, 0, -1)
	s.now = func() time.Time { return yesterday.Add(time.Hour) }
	_ = s.RecordPageview("", "/a", "", "h1", "ua")
	_ = s.RecordPageview("ask.xiaowenz.com", "/q", "", "h2", "ua")
	_ = s.RecordPageview("ask.xiaowenz.com", "/q", "", "h3", "ua")

	s.now = func() time.Time { return now }
	if _, _, err := s.CompactPageviews(s.now(), 365*24*time.Hour); err != nil {
		t.Fatalf("CompactPageviews: %v", err)
	}

	pv, uv, err := s.PageviewStats("ask.xiaowenz.com", 0)
	if err != nil {
		t.Fatalf("PageviewStats(ask): %v", err)
	}
	if pv != 2 || uv != 2 {
		t.Errorf("ask rollup pv=%d uv=%d, want 2/2", pv, uv)
	}
	if pv, uv, _ := s.PageviewStats("", 0); pv != 1 || uv != 1 {
		t.Errorf("blog rollup pv=%d uv=%d, want 1/1", pv, uv)
	}
	// 时间线同样按站隔离。
	ts, err := s.PageviewTimeseries("ask.xiaowenz.com", 0)
	if err != nil || len(ts) != 1 || ts[0].PV != 2 {
		t.Errorf("ask timeseries = %+v err=%v, want 1 day pv=2", ts, err)
	}
}

// ── 多站 DAU 总览（管理台统计页） ──────────────────────────────

func TestSiteStatsWindows(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	// ask：今天 2 PV/2 UV；3 天前 1 PV；20 天前 5 PV（只进 30 天窗）。
	s.now = func() time.Time { return now.Add(-time.Hour) }
	_ = s.RecordPageview("ask.xiaowenz.com", "/", "", "a1", "ua")
	_ = s.RecordPageview("ask.xiaowenz.com", "/", "", "a2", "ua")
	s.now = func() time.Time { return now.AddDate(0, 0, -3) }
	_ = s.RecordPageview("ask.xiaowenz.com", "/", "", "a1", "ua")
	s.now = func() time.Time { return now.AddDate(0, 0, -20) }
	for i := 0; i < 5; i++ {
		_ = s.RecordPageview("ask.xiaowenz.com", "/", "", "a1", "ua")
	}
	// card：只有 6 天前一条（近 7 天窗口 = [今天-6, 今天]，含边界）。
	s.now = func() time.Time { return now.AddDate(0, 0, -6) }
	_ = s.RecordPageview("card.xiaowenz.com", "/", "", "c1", "ua")

	s.now = func() time.Time { return now }
	if _, _, err := s.CompactPageviews(s.now(), 365*24*time.Hour); err != nil {
		t.Fatalf("CompactPageviews: %v", err)
	}

	rows, err := s.SiteStats(now.AddDate(0, 0, -29))
	if err != nil {
		t.Fatalf("SiteStats: %v", err)
	}
	bySite := map[string]SiteStatRow{}
	for _, r := range rows {
		bySite[r.Site] = r
	}
	ask := bySite["ask.xiaowenz.com"]
	if ask.TodayPV != 2 || ask.TodayUV != 2 {
		t.Errorf("ask today = %d/%d, want 2/2", ask.TodayPV, ask.TodayUV)
	}
	if ask.WeekPV != 3 { // 今天 2 + 3 天前 1
		t.Errorf("ask week pv = %d, want 3", ask.WeekPV)
	}
	if ask.MonthPV != 8 { // + 20 天前 5
		t.Errorf("ask month pv = %d, want 8", ask.MonthPV)
	}
	card := bySite["card.xiaowenz.com"]
	if card.TodayPV != 0 || card.WeekPV != 1 || card.MonthPV != 1 {
		t.Errorf("card = %+v, want 0/1/1", card)
	}
}
