package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
)

// ── cron 表达式 ──────────────────────────────────────────────

func TestValidateCron(t *testing.T) {
	for _, ok := range []string{
		"0 4 * * *", "*/15 * * * *", "30 3 * * 1-5", "0 0 29 2 *", "5,35 1 * * *", "@daily",
	} {
		if err := ValidateCron(ok); err != nil {
			t.Errorf("ValidateCron(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "   ", "61 * * * *", "0 25 * * *", "* * *", "abc", "0 4 * * 8", "0 4 * * * *",
	} {
		if err := ValidateCron(bad); err == nil {
			t.Errorf("ValidateCron(%q) = nil, want error", bad)
		}
	}
}

func TestNextRunFromCron(t *testing.T) {
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.GetCron = func() string { return "30 4 * * *" }

	b.now = func() time.Time { return time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC) }
	got, err := b.NextRun()
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if want := time.Date(2026, 10, 29, 4, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("NextRun = %v, want %v", got, want)
	}

	// After today's slot passed, next is tomorrow's.
	b.now = func() time.Time { return time.Date(2026, 10, 29, 5, 0, 0, 0, time.UTC) }
	got, err = b.NextRun()
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if want := time.Date(2026, 10, 30, 4, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("NextRun = %v, want %v", got, want)
	}
}

func TestCronFallsBackToDefault(t *testing.T) {
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	if got := b.Cron(); got != DefaultCron {
		t.Errorf("Cron() = %q, want default %q", got, DefaultCron)
	}
	b.GetCron = func() string { return "*/5 * * * *" }
	if got := b.Cron(); got != "*/5 * * * *" {
		t.Errorf("Cron() = %q, want operator value", got)
	}
	// Invalid operator value must fall back, not break the scheduler.
	b.GetCron = func() string { return "nonsense" }
	if got := b.Cron(); got != DefaultCron {
		t.Errorf("Cron() = %q, want default fallback", got)
	}
}

// ── 调度循环 ────────────────────────────────────────────────

// manualClock lets tests step time while the scheduler reads it.
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

// cronSource is a swappable cron source the test can flip mid-run.
type cronSource struct {
	mu   sync.Mutex
	expr string
}

func (c *cronSource) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.expr
}

func (c *cronSource) set(expr string) {
	c.mu.Lock()
	c.expr = expr
	c.mu.Unlock()
}

func countDBKeys(keys []string) int {
	n := 0
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, ".db") {
			n++
		}
	}
	return n
}

func waitDBCount(t *testing.T, fake *s3store.Fake, min int, failMsg string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		keys, _ := fake.ListKeys(context.Background(), prefix)
		if countDBKeys(keys) >= min {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s (keys=%v)", failMsg, keys)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// settle gives the scheduler goroutine time to finish the immediate run and
// anchor its next-fire time before the test advances the clock (advancing
// first would make Next() skip the slot we intend to hit).
func settle() { time.Sleep(100 * time.Millisecond) }

func TestSchedulerFiresWhenCronDue(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	clk := &manualClock{t: time.Date(2026, 10, 29, 3, 59, 0, 0, time.UTC)}
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
	b.now = clk.now
	b.checkInterval = 5 * time.Millisecond
	b.GetCron = func() string { return "0 4 * * *" }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.Start(ctx); close(done) }()

	// Start runs once immediately so a deploy-time snapshot always exists.
	waitDBCount(t, fake, 1, "immediate run never happened")
	settle()

	// Clock crosses the 04:00 slot → the scheduled fire happens.
	clk.set(time.Date(2026, 10, 29, 4, 0, 30, 0, time.UTC))
	waitDBCount(t, fake, 2, "scheduler missed the 04:00 fire")

	// Frozen clock: the same minute must not fire twice.
	time.Sleep(50 * time.Millisecond)
	keys, _ := fake.ListKeys(context.Background(), prefix)
	if n := countDBKeys(keys); n != 2 {
		t.Errorf("db backups = %d, want 2 (no double fire)", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop after cancel")
	}
}

func TestSchedulerPicksUpCronChangeAfterKick(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	clk := &manualClock{t: time.Date(2026, 10, 29, 4, 0, 30, 0, time.UTC)}
	src := &cronSource{expr: "0 5 * * *"} // next fire 05:00
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
	b.now = clk.now
	b.checkInterval = 5 * time.Millisecond
	b.GetCron = src.get

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.Start(ctx); close(done) }()
	waitDBCount(t, fake, 1, "immediate run never happened")
	settle()

	// Operator switches to every-minute and kicks the scheduler; let the
	// re-anchor settle before the clock crosses the next slot.
	src.set("*/1 * * * *")
	b.Kick()
	settle()
	clk.set(time.Date(2026, 10, 29, 4, 1, 30, 0, time.UTC))
	waitDBCount(t, fake, 2, "scheduler did not pick up the new cron after Kick")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop after cancel")
	}
}

func TestSchedulerFallsBackWhenOperatorCronInvalid(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	clk := &manualClock{t: time.Date(2026, 10, 29, 3, 59, 0, 0, time.UTC)}
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
	b.now = clk.now
	b.checkInterval = 5 * time.Millisecond
	b.GetCron = func() string { return "not a cron" } // DefaultCron = 0 4 * * *

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Start(ctx)
	waitDBCount(t, fake, 1, "immediate run never happened")
	settle()

	clk.set(time.Date(2026, 10, 29, 4, 0, 30, 0, time.UTC))
	waitDBCount(t, fake, 2, "default schedule did not fire")
}

// ── 手动运行的互斥 ──────────────────────────────────────────

// blockingS3 holds the first Put until released, keeping RunOnce mid-flight.
type blockingS3 struct {
	s3store.Client
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingS3) Put(ctx context.Context, key string, body io.Reader, size int64, ct string) (string, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.Client.Put(ctx, key, body, size, ct)
}

func TestRunOnceBusyWhileRunning(t *testing.T) {
	bs := &blockingS3{
		Client:  s3store.NewFake("https://backup.test"),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return bs }

	go func() { _, _ = b.RunOnce(context.Background()) }()
	<-bs.entered

	_, err := b.RunOnce(context.Background())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent RunOnce err = %v, want ErrBusy", err)
	}
	close(bs.release)
}

// ── 保留最近 N 份（db 与 meta 配对） ──────────────────────────

func TestPruneKeepsMostRecentPairs(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	ctx := context.Background()

	// Seed 9 older snapshot pairs on Sep 1..9.
	for i := 1; i <= 9; i++ {
		ts := time.Date(2026, 9, i, 1, 0, 0, 0, time.UTC).Format("20060102-150405")
		for _, k := range []string{prefix + "core-" + ts + ".db", prefix + "core-" + ts + ".meta.json"} {
			if _, err := fake.Put(ctx, k, strings.NewReader("x"), 1, "application/octet-stream"); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}

	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
	b.now = func() time.Time { return time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC) }

	if _, err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	keys, err := fake.ListKeys(ctx, prefix)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if n := countDBKeys(keys); n != KeepDefault {
		t.Errorf("db backups = %d, want %d", n, KeepDefault)
	}
	// Oldest three pairs (Sep 1-3) pruned, db and meta together.
	for _, k := range keys {
		if strings.Contains(k, "core-20260901") || strings.Contains(k, "core-20260902") || strings.Contains(k, "core-20260903") {
			t.Errorf("old backup %q not pruned", k)
		}
	}
	// Every remaining snapshot keeps its db+meta pair.
	dbs := map[string]bool{}
	metas := map[string]bool{}
	for _, k := range keys {
		switch {
		case strings.HasSuffix(k, ".meta.json"):
			metas[strings.TrimSuffix(strings.TrimPrefix(k, prefix+"core-"), ".meta.json")] = true
		case strings.HasSuffix(k, ".db"):
			dbs[strings.TrimSuffix(strings.TrimPrefix(k, prefix+"core-"), ".db")] = true
		}
	}
	for ts := range dbs {
		if !metas[ts] {
			t.Errorf("snapshot %s lost its meta file", ts)
		}
	}
}

// ── meta 配置快照 ───────────────────────────────────────────

func TestRunOnceUploadsMetaSnapshot(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
	fixed := time.Date(2026, 9, 19, 14, 30, 0, 0, time.UTC)
	b.now = func() time.Time { return fixed }
	b.GetCron = func() string { return "15 3 * * *" }
	b.Meta = func() map[string]any {
		return map[string]any{"env": "prod", "r2_backup_bucket": "core-backups", "smtp_configured": true}
	}

	key, err := b.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	metaKey := strings.TrimSuffix(key, ".db") + ".meta.json"
	rd, _, err := fake.Get(context.Background(), metaKey)
	if err != nil {
		t.Fatalf("meta not uploaded: %v", err)
	}
	data, _ := io.ReadAll(rd)
	_ = rd.Close()

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("meta is not JSON: %v\n%s", err, data)
	}
	if m["db_key"] != key {
		t.Errorf("db_key = %v, want %v", m["db_key"], key)
	}
	if m["created_at"] != fixed.UTC().Format(time.RFC3339) {
		t.Errorf("created_at = %v", m["created_at"])
	}
	if v, _ := m["schema_version"].(float64); v < 1 {
		t.Errorf("schema_version = %v, want >= 1", m["schema_version"])
	}
	bk, _ := m["backup"].(map[string]any)
	if bk == nil || bk["cron"] != "15 3 * * *" {
		t.Errorf("backup.cron = %v, want operator cron", m["backup"])
	}
	if bk == nil || bk["keep"] != float64(KeepDefault) {
		t.Errorf("backup.keep = %v, want %d", m["backup"], KeepDefault)
	}
	svc, _ := m["service"].(map[string]any)
	if svc == nil || svc["env"] != "prod" || svc["smtp_configured"] != true {
		t.Errorf("service = %v, want injected non-secret config", m["service"])
	}
}

// ── R2 连通性测试 ───────────────────────────────────────────

func TestTestConnectionRoundTrip(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
	if err := b.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	// The probe object must be cleaned up afterwards.
	keys, _ := fake.ListKeys(context.Background(), prefix)
	for _, k := range keys {
		if strings.Contains(k, "probe") {
			t.Errorf("probe object %q left behind", k)
		}
	}
}

func TestTestConnectionUnconfigured(t *testing.T) {
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	if err := b.TestConnection(context.Background()); err == nil {
		t.Fatal("TestConnection without R2 must fail")
	}
}

// failingS3 makes Put error to simulate bad credentials / network.
type failingS3 struct{ s3store.Client }

func (s failingS3) Put(context.Context, string, io.Reader, int64, string) (string, error) {
	return "", errors.New("403 InvalidAccessKeyId")
}

func TestTestConnectionReportsFailures(t *testing.T) {
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return failingS3{Client: s3store.NewFake("https://backup.test")} }
	err := b.TestConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want wrapped put failure", err)
	}
}

// ── 备份历史列表 ────────────────────────────────────────────

func TestRecentListsNewestFirst(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		ts := time.Date(2026, 9, i, 1, 0, 0, 0, time.UTC).Format("20060102-150405")
		_, _ = fake.Put(ctx, prefix+"core-"+ts+".db", strings.NewReader("x"), 1, "application/octet-stream")
		_, _ = fake.Put(ctx, prefix+"core-"+ts+".meta.json", strings.NewReader("x"), 1, "application/json")
	}
	// Unparseable keys must be ignored, not crash the listing.
	_, _ = fake.Put(ctx, prefix+"README", strings.NewReader("x"), 1, "text/plain")

	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
	snaps, err := b.Recent(ctx, 2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	wantFirst := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	if !snaps[0].When.Equal(wantFirst) {
		t.Errorf("snaps[0].When = %v, want %v", snaps[0].When, wantFirst)
	}
	if !snaps[0].HasMeta || !snaps[1].HasMeta {
		t.Error("snaps missing meta flag")
	}
}
