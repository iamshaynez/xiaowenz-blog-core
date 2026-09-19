package backup

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xiaowenz/xiaowen-core/internal/db"
	"github.com/xiaowenz/xiaowen-core/internal/s3store"
	"github.com/xiaowenz/xiaowen-core/internal/store"
)

func newBackupDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestRunOnceUploadsValidSnapshot(t *testing.T) {
	d := newBackupDB(t)
	st := store.New(d)
	if _, err := st.UpsertPending("snap@x.com", "tok", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fake := s3store.NewFake("https://media.test")
	b := New(d, fake, slog.New(slog.DiscardHandler))
	b.now = func() time.Time { return time.Date(2026, 9, 19, 14, 30, 0, 0, time.UTC) }

	key, err := b.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	want := "backups/core-20260919-143000.db"
	if key != want {
		t.Errorf("key = %q, want %q", key, want)
	}

	// The uploaded bytes must be a valid SQLite db containing our data.
	rd, _, err := fake.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, _ := io.ReadAll(rd)
	_ = rd.Close()
	tmp := filepath.Join(t.TempDir(), "restore.db")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatalf("write restore: %v", err)
	}
	restored, err := sql.Open("sqlite", "file:"+tmp)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer func() { _ = restored.Close() }()
	var email string
	if err := restored.QueryRow(`SELECT email FROM subscribers WHERE email='snap@x.com'`).Scan(&email); err != nil {
		t.Errorf("restored db missing seeded row: %v", err)
	}
}

func TestRunOncePrunesOldBackups(t *testing.T) {
	d := newBackupDB(t)
	fake := s3store.NewFake("https://media.test")
	ctx := context.Background()

	// Seed remote backups: two older than 30 days, one recent.
	for _, k := range []string{"backups/core-20260701-000000.db", "backups/core-20260801-000000.db", "backups/core-20260910-000000.db"} {
		if _, err := fake.Put(ctx, k, strings.NewReader("x"), 1, "application/octet-stream"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	b := New(d, fake, slog.New(slog.DiscardHandler))
	b.now = func() time.Time { return time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) }

	if _, err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	keys, err := fake.ListKeys(ctx, "backups/")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	for _, k := range keys {
		if strings.Contains(k, "20260701") || strings.Contains(k, "20260801") {
			t.Errorf("old backup %q not pruned", k)
		}
	}
	foundRecent := false
	for _, k := range keys {
		if strings.Contains(k, "20260910") {
			foundRecent = true
		}
	}
	if !foundRecent {
		t.Error("recent backup wrongly pruned")
	}
	// Plus the new backup itself.
	if len(keys) != 2 {
		t.Errorf("keys = %v, want recent + new", keys)
	}
}

func TestRunOnceSkippedWithoutR2(t *testing.T) {
	d := newBackupDB(t)
	b := New(d, nil, slog.New(slog.DiscardHandler))
	key, err := b.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce without S3 must not error, got %v", err)
	}
	if key != "" {
		t.Errorf("key = %q, want empty when skipped", key)
	}
}

func TestTickerRunsAndStops(t *testing.T) {
	d := newBackupDB(t)
	fake := s3store.NewFake("https://media.test")
	b := New(d, fake, slog.New(slog.DiscardHandler))
	b.Interval = 30 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Start(ctx); close(done) }()

	deadline := time.After(3 * time.Second)
	for {
		keys, _ := fake.ListKeys(context.Background(), "backups/")
		if len(keys) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("ticker never ran a backup")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop after cancel")
	}
}
