package stats

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/db"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

// newFixture 打开真实临时库：Compactor 与 Store 共享同一连接池（与线上同构）。
func newFixture(t *testing.T) (*Compactor, *store.Store, *sql.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := store.New(d)
	c := NewCompactor(d, st, slog.New(slog.DiscardHandler))
	return c, st, d
}

// seedRaw 直接写 raw pageview 行（绕过 Store.now，精确控制 created_at）。
func seedRaw(t *testing.T, d *sql.DB, at time.Time, ipHash string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := d.Exec(
			`INSERT INTO pageviews (path, referrer, ip_hash, ua, created_at) VALUES ('/x', '', ?, 'd', ?)`,
			ipHash, at.Unix()); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func freelist(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRow(`PRAGMA freelist_count`).Scan(&n); err != nil {
		t.Fatalf("freelist_count: %v", err)
	}
	return n
}

// RunOnce 的完整契约：rollup → prune → 有删除时 VACUUM 回收文件体积。
func TestRunOnceCompactsAndVacuums(t *testing.T) {
	c, st, d := newFixture(t)
	now := time.Date(2026, 11, 7, 3, 30, 0, 0, time.Local)
	c.now = func() time.Time { return now }
	c.Retention = 30 * 24 * time.Hour

	seedRaw(t, d, now.Add(-40*24*time.Hour), "old1", 3) // 保留期外：聚合后被清
	seedRaw(t, d, now.Add(-3*24*time.Hour), "r1", 2)    // 保留期内：聚合后留明细
	seedRaw(t, d, now.Add(-time.Hour), "t1", 1)         // 今天：不聚合不清

	rolled, pruned, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if rolled != 2 || pruned != 3 {
		t.Fatalf("rolled=%d pruned=%d, want 2/3", rolled, pruned)
	}
	// DELETE 只把页挂进 freelist；VACUUM 必须把它真正还给文件系统。
	if fl := freelist(t, d); fl != 0 {
		t.Errorf("freelist_count = %d after RunOnce, want 0（VACUUM 未回收）", fl)
	}
	// 全历史读数经永久层存活。
	pv, uv, err := st.PageviewStats("", 0)
	if err != nil {
		t.Fatalf("PageviewStats: %v", err)
	}
	if pv != 6 || uv != 3 {
		t.Errorf("pv=%d uv=%d, want 6/3（明细清了，指标还在）", pv, uv)
	}
}

// 没有可清理的行时不必重写整个 db 文件。
func TestRunOnceWithoutPruneSkipsVacuum(t *testing.T) {
	c, _, d := newFixture(t)
	now := time.Date(2026, 11, 7, 3, 30, 0, 0, time.Local)
	c.now = func() time.Time { return now }
	seedRaw(t, d, now.Add(-time.Hour), "t1", 1)

	rolled, pruned, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if rolled != 0 || pruned != 0 {
		t.Fatalf("rolled=%d pruned=%d, want 0/0", rolled, pruned)
	}
}

// 手动触发与调度循环并发：同一时刻只允许一次压缩。
func TestRunOnceBusy(t *testing.T) {
	c, _, _ := newFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.testHook = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	go func() { _, _, _ = c.RunOnce(context.Background()) }()
	<-entered

	if _, _, err := c.RunOnce(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent RunOnce err = %v, want ErrBusy", err)
	}
	close(release)
}

// ── 调度 ─────────────────────────────────────────────────────

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func TestStartFiresOnSchedule(t *testing.T) {
	c, _, _ := newFixture(t)
	clk := &manualClock{t: time.Date(2026, 11, 7, 3, 59, 0, 0, time.Local)}
	c.now = clk.now
	c.Cron = "0 4 * * *"
	c.checkInterval = 5 * time.Millisecond
	c.Retention = 3650 * 24 * time.Hour // 不触发清理，专注调度

	var runs atomic.Int64
	c.testHook = func() { runs.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Start(ctx); close(done) }()

	// Start 先立即跑一次（部署即完成首次压缩）。
	waitRuns(t, &runs, 1, "immediate run never happened")
	// 等调度器锚定下一次触发点后再推进时钟。
	time.Sleep(100 * time.Millisecond)
	clk.set(time.Date(2026, 11, 7, 4, 0, 30, 0, time.Local))
	waitRuns(t, &runs, 2, "scheduler missed the 04:00 fire")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop after cancel")
	}
}

func waitRuns(t *testing.T, runs *atomic.Int64, min int64, msg string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if runs.Load() >= min {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s (runs=%d)", msg, runs.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCronFallsBackToDefault(t *testing.T) {
	c, _, _ := newFixture(t)
	if got := c.cron(); got != DefaultCron {
		t.Errorf("cron() = %q, want default %q", got, DefaultCron)
	}
	c.Cron = "*/5 * * * *"
	if got := c.cron(); got != "*/5 * * * *" {
		t.Errorf("cron() = %q, want operator value", got)
	}
	c.Cron = "nonsense"
	if got := c.cron(); got != DefaultCron {
		t.Errorf("cron() = %q, want default fallback", got)
	}
}
