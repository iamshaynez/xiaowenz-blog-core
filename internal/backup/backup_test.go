package backup

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/db"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
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
	b := New(d, slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return fake }
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

func TestRunOnceSkippedWithoutR2(t *testing.T) {
	d := newBackupDB(t)
	b := New(d, slog.New(slog.DiscardHandler))
	key, err := b.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce without S3 must not error, got %v", err)
	}
	if key != "" {
		t.Errorf("key = %q, want empty when skipped", key)
	}
}
