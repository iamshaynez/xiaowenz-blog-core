package store

import (
	"testing"
	"time"
)

// ── 行瘦身：ip_hash 截短 + UA 只存设备分类 ──────────────────────────

// 设备分类把整条 UA（100–300B）收敛为一个字符：明细层只需要"什么样的设备"。
func TestDeviceClass(t *testing.T) {
	for _, x := range []struct{ ua, want string }{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1", "m"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Mobile Safari/537.36", "m"},
		{"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) Safari/604.1", "m"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0 Safari/537.36", "d"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15", "d"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0", "d"},
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "b"},
		{"Mozilla/5.0 (compatible; bingbot/2.0)", "b"},
		{"curl/8.7.1", "o"},
		{"", "o"},
	} {
		if got := deviceClass(x.ua); got != x.want {
			t.Errorf("deviceClass(%q) = %q, want %q", x.ua, got, x.want)
		}
	}
}

func TestRecordPageviewStoresDeviceClassNotFullUA(t *testing.T) {
	s := newTestStore(t)
	full := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148 Safari/604.1"
	if err := s.RecordPageview("", "/a", "", "h1", full); err != nil {
		t.Fatalf("RecordPageview: %v", err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT ua FROM pageviews LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("query ua: %v", err)
	}
	if stored != "m" {
		t.Errorf("stored ua = %q, want device class %q（整条 UA 不落盘）", stored, "m")
	}
}

// ── rollup + prune：明细层有界，指标层永久 ─────────────────────────

// CompactPageviews 的完整契约：先聚合后清理（顺序保证未聚合的天不被删），
// 当天（未完结的天）不聚合也不清理。
func TestCompactPageviewsRollsUpThenPrunes(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.Local)
	// 40 天前：保留期外，聚合后必须被清掉。
	dayA := now.AddDate(0, 0, -40)
	s.now = func() time.Time { return dayA.Add(time.Hour) }
	_ = s.RecordPageview("", "/old", "", "ipA1", "ua")
	_ = s.RecordPageview("", "/old", "", "ipA1", "ua") // 同日同访客 → 1 UV
	// 3 天前：保留期内，聚合后明细继续留（路径/来源维度的数据地基）。
	dayB := now.AddDate(0, 0, -3)
	s.now = func() time.Time { return dayB.Add(time.Hour) }
	_ = s.RecordPageview("", "/a", "https://g.com", "ipB1", "ua")
	_ = s.RecordPageview("", "/a", "", "ipB1", "ua")
	s.now = func() time.Time { return dayB.Add(2 * time.Hour) }
	_ = s.RecordPageview("", "/b", "", "ipB2", "ua")
	// 今天：未完结，不聚合、不清理。
	s.now = func() time.Time { return now }
	_ = s.RecordPageview("", "/today", "", "ipT1", "ua")

	s.now = func() time.Time { return now }
	rolled, pruned, err := s.CompactPageviews(s.now(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CompactPageviews: %v", err)
	}
	if rolled != 2 {
		t.Errorf("rolled days = %d, want 2（40天前 + 3天前；今天未完结不聚合）", rolled)
	}
	if pruned != 2 {
		t.Errorf("pruned rows = %d, want 2（仅 40 天前的两行）", pruned)
	}

	// 聚合层：两个完结日，数值精确（同日去重）。
	byDay := map[string]DayStat{}
	// daily_stats 行直接读（同包测试可触达）
	rows, err := s.db.Query(`SELECT day, pv, uv FROM daily_stats ORDER BY day`)
	if err != nil {
		t.Fatalf("query daily_stats: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.PV, &d.UV); err != nil {
			t.Fatalf("scan: %v", err)
		}
		byDay[d.Day] = d
	}
	a := dayA.Format("2006-01-02")
	b := dayB.Format("2006-01-02")
	if byDay[a] != (DayStat{Day: a, PV: 2, UV: 1}) {
		t.Errorf("dayA rollup = %+v, want pv=2 uv=1", byDay[a])
	}
	if byDay[b] != (DayStat{Day: b, PV: 3, UV: 2}) {
		t.Errorf("dayB rollup = %+v, want pv=3 uv=2", byDay[b])
	}
	if _, ok := byDay[now.Format("2006-01-02")]; ok {
		t.Error("当天未完结，不得出现在 daily_stats")
	}

	// 明细层：只剩保留期内的 3 天前 + 今天。
	var rawLeft int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pageviews`).Scan(&rawLeft); err != nil {
		t.Fatal(err)
	}
	if rawLeft != 4 {
		t.Errorf("raw rows left = %d, want 4（3天前3行 + 今天1行）", rawLeft)
	}

	// 幂等：重跑数值稳定，已聚合的旧日不受影响。
	rolled2, pruned2, err := s.CompactPageviews(s.now(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CompactPageviews again: %v", err)
	}
	if rolled2 != 1 || pruned2 != 0 {
		t.Errorf("second run rolled=%d pruned=%d, want 1/0", rolled2, pruned2)
	}
	if err := s.db.QueryRow(`SELECT pv, uv FROM daily_stats WHERE day=?`, a).Scan(new(int), new(int)); err != nil {
		t.Errorf("已聚合的旧日（明细已清）在重跑后丢失: %v", err)
	}
}

// 统计读数跨过清理后依然完整：永久层（daily_stats）+ 未聚合的 raw 合并，
// 旧总量不因明细被清而缩水。
func TestPageviewStatsSurvivePrune(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.Local)
	old := now.AddDate(0, 0, -40)
	for i, ip := range []string{"ip1", "ip1", "ip2"} {
		s.now = func() time.Time { return old.Add(time.Duration(i) * time.Minute) }
		_ = s.RecordPageview("", "/old", "", ip, "ua")
	}
	s.now = func() time.Time { return now }
	_ = s.RecordPageview("", "/today", "", "ipT", "ua")

	s.now = func() time.Time { return now }
	if _, _, err := s.CompactPageviews(s.now(), 30*24*time.Hour); err != nil {
		t.Fatalf("CompactPageviews: %v", err)
	}

	// since=0：窗口超出保留期 → 走合并路径，全历史可见。
	pv, uv, err := s.PageviewStats("", 0)
	if err != nil {
		t.Fatalf("PageviewStats: %v", err)
	}
	if pv != 4 || uv != 3 {
		t.Errorf("pv=%d uv=%d, want 4/3（rollup 3/2 + 今日 raw 1/1）", pv, uv)
	}

	ts, err := s.PageviewTimeseries("", 0)
	if err != nil {
		t.Fatalf("PageviewTimeseries: %v", err)
	}
	if len(ts) != 2 {
		t.Fatalf("days = %d, want 2: %+v", len(ts), ts)
	}
	if ts[0].Day != old.Format("2006-01-02") || ts[0].PV != 3 || ts[0].UV != 2 {
		t.Errorf("old day = %+v, want pv=3 uv=2", ts[0])
	}
	if ts[1].Day != now.Format("2006-01-02") || ts[1].PV != 1 {
		t.Errorf("today = %+v, want pv=1", ts[1])
	}
}

// 同一天既有 rollup 行又残留 raw 行（清理前的过渡态）：读数不得双计。
func TestPageviewTimeseriesNoDoubleCount(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.Local)
	yesterday := now.AddDate(0, 0, -1)
	s.now = func() time.Time { return yesterday.Add(time.Hour) }
	_ = s.RecordPageview("", "/a", "", "ip1", "ua")
	_ = s.RecordPageview("", "/a", "", "ip2", "ua")

	s.now = func() time.Time { return now }
	// 只聚合不清理：昨天的 raw 行还在，rollup 行也在。
	if _, _, err := s.CompactPageviews(s.now(), 365*24*time.Hour); err != nil {
		t.Fatalf("CompactPageviews: %v", err)
	}

	pv, uv, err := s.PageviewStats("", 0)
	if err != nil {
		t.Fatalf("PageviewStats: %v", err)
	}
	if pv != 2 || uv != 2 {
		t.Errorf("pv=%d uv=%d, want 2/2（昨日只计 rollup 一份）", pv, uv)
	}
}

// 窗口整体落在保留期内：继续走 raw 精确路径（含 24 小时窗的半日边界），
// 不因日粒度合并而把"昨天一整天"算进"24 小时"。
func TestPageviewStatsShortWindowStaysExact(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local)
	for _, at := range []time.Time{
		now.Add(-35 * 24 * time.Hour), // 保留期外（本测试 PVRetention=30d 不影响 raw 路径）
		now.Add(-36 * time.Hour),      // 昨天白天：在 24h 窗外
		now.Add(-2 * time.Hour),       // 2 小时前：窗内
	} {
		s.now = func() time.Time { return at }
		_ = s.RecordPageview("", "/a", "", "ip1", "ua")
	}
	s.PVRetention = 30 * 24 * time.Hour
	s.now = func() time.Time { return now }

	pv, uv, err := s.PageviewStats("", now.Add(-24*time.Hour).Unix())
	if err != nil {
		t.Fatalf("PageviewStats: %v", err)
	}
	if pv != 1 || uv != 1 {
		t.Errorf("24h 窗 pv=%d uv=%d, want 1/1（精确到时间戳，非日粒度）", pv, uv)
	}
}
