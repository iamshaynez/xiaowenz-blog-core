package backup

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
)

func TestBackerPicksUpConsoleConfigChanges(t *testing.T) {
	fake := s3store.NewFake("https://backup.test")
	var client s3store.Client // settings-backed stand-in the test can flip
	b := New(newBackupDB(t), slog.New(slog.DiscardHandler))
	b.Client = func() s3store.Client { return client }

	// Unconfigured: RunOnce skips, no error.
	client = nil
	if key, err := b.RunOnce(context.Background()); err != nil || key != "" {
		t.Fatalf("unconfigured RunOnce = %q, %v; want skip", key, err)
	}

	// Console config saved → the very next run backs up, same process.
	client = fake
	key, err := b.RunOnce(context.Background())
	if err != nil || !strings.HasPrefix(key, "backups/core-") {
		t.Fatalf("configured RunOnce = %q, %v; want snapshot key", key, err)
	}

	if !b.Configured() {
		t.Error("Configured() = false after config present")
	}
	client = nil
	if b.Configured() {
		t.Error("Configured() = true after config removed")
	}
}
