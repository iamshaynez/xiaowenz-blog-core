// Package backup snapshots the SQLite database to R2 on a 24h ticker
// (SPEC §6): wal_checkpoint(TRUNCATE) → VACUUM INTO temp → S3 put → prune
// remote backups older than 30 days.
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaowenz/xiaowen-core/internal/s3store"
)

const (
	// Retention is how long remote backups are kept.
	Retention = 30 * 24 * time.Hour
	prefix    = "backups/"
)

// Backer performs periodic database backups.
type Backer struct {
	db  *sql.DB
	s3  s3store.Client
	log *slog.Logger
	now func() time.Time

	// Interval between runs; 24h per SPEC, shrinkable in tests.
	Interval time.Duration
}

// New builds a Backer; s3 may be nil (backup disabled, WARN on each tick).
func New(d *sql.DB, s3 s3store.Client, log *slog.Logger) *Backer {
	if log == nil {
		log = slog.Default()
	}
	return &Backer{db: d, s3: s3, log: log, now: time.Now, Interval: 24 * time.Hour}
}

// Start runs the ticker until ctx is cancelled; one immediate run first so a
// crash shortly after deploy still leaves a fresh snapshot.
func (b *Backer) Start(ctx context.Context) {
	if _, err := b.RunOnce(ctx); err != nil {
		b.log.Error("backup failed", "err", err)
	}
	tick := time.NewTicker(b.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := b.RunOnce(ctx); err != nil {
				b.log.Error("backup failed", "err", err)
			}
		}
	}
}

// RunOnce performs one full backup cycle. Without R2 it logs WARN and skips.
// Returns the uploaded key ("" when skipped).
func (b *Backer) RunOnce(ctx context.Context) (string, error) {
	if b.s3 == nil {
		b.log.Warn("backup skipped: R2 not configured")
		return "", nil
	}

	if _, err := b.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", fmt.Errorf("wal_checkpoint: %w", err)
	}

	tmp, err := os.CreateTemp("", "core-backup-*.db")
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	// VACUUM INTO rejects bound parameters; the path is generated above, so
	// quoting it is safe.
	if _, err := b.db.ExecContext(ctx, `VACUUM INTO '`+tmpPath+`'`); err != nil {
		return "", fmt.Errorf("vacuum into: %w", err)
	}

	key := prefix + "core-" + b.now().UTC().Format("20060102-150405") + ".db"
	f, err := os.Open(tmpPath)
	if err != nil {
		return "", fmt.Errorf("open snapshot: %w", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat snapshot: %w", err)
	}
	if _, err := b.s3.Put(ctx, key, f, fi.Size(), "application/octet-stream"); err != nil {
		return "", fmt.Errorf("upload backup: %w", err)
	}

	pruned, err := b.prune(ctx)
	if err != nil {
		// Prune failure must not fail the backup itself.
		b.log.Warn("backup prune failed", "err", err)
	}
	b.log.Info("backup completed", "key", key, "bytes", fi.Size(), "pruned", pruned)
	return key, nil
}

// prune deletes remote backups older than Retention, by key timestamp.
func (b *Backer) prune(ctx context.Context) (int, error) {
	keys, err := b.s3.ListKeys(ctx, prefix)
	if err != nil {
		return 0, err
	}
	cutoff := b.now().Add(-Retention)
	pruned := 0
	for _, k := range keys {
		ts, ok := parseKeyTime(k)
		if !ok {
			continue
		}
		if ts.Before(cutoff) {
			if err := b.s3.Delete(ctx, k); err != nil {
				return pruned, fmt.Errorf("delete %s: %w", k, err)
			}
			pruned++
		}
	}
	return pruned, nil
}

// parseKeyTime extracts the timestamp from backups/core-YYYYMMDD-HHMMSS.db.
func parseKeyTime(key string) (time.Time, bool) {
	name := strings.TrimPrefix(filepath.Base(key), "core-")
	name = strings.TrimSuffix(name, ".db")
	ts, err := time.Parse("20060102-150405", name)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}
