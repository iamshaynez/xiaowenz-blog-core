// Package backup snapshots the SQLite database (plus a non-secret config
// digest) to a private R2 bucket on an operator-configurable cron schedule:
// wal_checkpoint(TRUNCATE) → VACUUM INTO temp → upload db + meta.json →
// prune all but the most recent KeepDefault pairs. The schedule and the R2
// target (console-managed in internal/sysconfig) live in the settings table;
// the client factory is called per operation, so console edits apply without
// a restart. RunOnce backs manual triggers and is serialized by a busy lock.
package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
)

const (
	// prefix is the object key prefix all backups live under.
	prefix = "backups/"
	// KeepDefault is how many recent snapshot pairs are retained.
	KeepDefault = 7
	// DefaultCron is the schedule used until the operator sets one
	// (04:00 UTC — low-traffic window for a UTC+8 audience).
	DefaultCron = "0 4 * * *"
	// SettingKey is the settings-table key holding the cron expression.
	SettingKey = "backup_cron"

	// keyLayout is the timestamp layout inside backup object keys.
	keyLayout = "20060102-150405"
)

// ErrBusy is returned when a backup is already in flight; the trigger
// (scheduler or manual) should simply try again later.
var ErrBusy = errors.New("backup already running")

// Backer performs scheduled and manual database backups.
type Backer struct {
	db  *sql.DB
	log *slog.Logger
	now func() time.Time

	// Client builds the backup target client; nil (or returning nil) =
	// backup disabled. Called per operation, so console config changes
	// apply immediately. Wire with sysconfig.BackupClientFactory.
	Client func() s3store.Client

	// Keep is how many recent snapshot pairs to retain (default KeepDefault).
	Keep int
	// GetCron returns the operator-set 5-field cron expression; empty or
	// invalid values fall back to DefaultCron.
	GetCron func() string
	// Meta returns a non-secret service-config digest recorded beside each
	// snapshot. Secrets must never appear here — booleans/names only.
	Meta func() map[string]any

	// checkInterval is how often the scheduler re-checks the clock; coarse
	// on purpose (cron granularity is a minute), tiny in tests.
	checkInterval time.Duration

	mu   sync.Mutex // TryLock: only one RunOnce at a time
	kick chan struct{}
}

// New builds a Backer; without a Client factory every operation reports
// "not configured" until the console provides one.
func New(d *sql.DB, log *slog.Logger) *Backer {
	if log == nil {
		log = slog.Default()
	}
	return &Backer{
		db:            d,
		log:           log,
		now:           time.Now,
		Keep:          KeepDefault,
		checkInterval: 30 * time.Second,
		kick:          make(chan struct{}, 1),
	}
}

// client resolves the current backup target (nil = disabled).
func (b *Backer) client() s3store.Client {
	if b.Client == nil {
		return nil
	}
	return b.Client()
}

// Configured reports whether backups have an R2 target at all.
func (b *Backer) Configured() bool { return b.client() != nil }

// ValidateCron reports whether expr is a valid 5-field cron expression
// (or a descriptor like @daily).
func ValidateCron(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return errors.New("empty cron expression")
	}
	if _, err := cron.ParseStandard(strings.TrimSpace(expr)); err != nil {
		return fmt.Errorf("invalid cron %q: %w", expr, err)
	}
	return nil
}

// Cron returns the effective cron expression: the operator value when valid,
// otherwise DefaultCron.
func (b *Backer) Cron() string {
	expr := ""
	if b.GetCron != nil {
		expr = strings.TrimSpace(b.GetCron())
	}
	if expr == "" {
		return DefaultCron
	}
	if _, err := cron.ParseStandard(expr); err != nil {
		b.log.Warn("backup cron invalid; using default", "expr", expr, "err", err)
		return DefaultCron
	}
	return expr
}

// NextRun reports when the next scheduled backup fires.
func (b *Backer) NextRun() (time.Time, error) {
	sched, err := cron.ParseStandard(b.Cron())
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron: %w", err)
	}
	return sched.Next(b.now()), nil
}

// Kick wakes the scheduler so a schedule change applies immediately instead
// of at the next check tick.
func (b *Backer) Kick() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Start runs the scheduler until ctx is cancelled: one immediate run first
// (a crash shortly after deploy still leaves a fresh snapshot), then fires
// whenever the clock passes the next cron slot. The loop re-reads the cron
// on every wake and after each Kick — a changed expression re-anchors the
// next fire — so schedule changes apply without a restart; precision is
// bounded by checkInterval.
func (b *Backer) Start(ctx context.Context) {
	if _, err := b.RunOnce(ctx); err != nil && !errors.Is(err, ErrBusy) {
		b.log.Error("backup failed", "err", err)
	}
	expr := b.Cron()
	next := b.nextFire(expr)
	tick := time.NewTicker(b.checkInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-b.kick:
			tick.Reset(b.checkInterval)
		}
		if cur := b.Cron(); cur != expr {
			expr = cur
			next = b.nextFire(expr)
			b.log.Info("backup schedule updated", "cron", expr, "next", next.Format(time.RFC3339))
		}
		if b.now().Before(next) {
			continue
		}
		if _, err := b.RunOnce(ctx); err != nil && !errors.Is(err, ErrBusy) {
			b.log.Error("backup failed", "err", err)
		}
		next = b.nextFire(expr)
	}
}

// nextFire computes the next due time for expr from the current clock.
func (b *Backer) nextFire(expr string) time.Time {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return b.now().Add(time.Hour) // unreachable: Cron() falls back
	}
	return sched.Next(b.now())
}

// RunOnce performs one full backup cycle. Without R2 it logs WARN and skips.
// Returns the uploaded db key ("" when skipped) or ErrBusy when a backup is
// already in flight.
func (b *Backer) RunOnce(ctx context.Context) (string, error) {
	if !b.mu.TryLock() {
		return "", ErrBusy
	}
	defer b.mu.Unlock()

	s3 := b.client()
	if s3 == nil {
		b.log.Warn("backup skipped: R2 not configured in console")
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

	ts := b.now().UTC()
	key := prefix + "core-" + ts.Format(keyLayout) + ".db"
	f, err := os.Open(tmpPath)
	if err != nil {
		return "", fmt.Errorf("open snapshot: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return "", fmt.Errorf("stat snapshot: %w", err)
	}
	_, err = s3.Put(ctx, key, f, fi.Size(), "application/octet-stream")
	_ = f.Close()
	if err != nil {
		return "", fmt.Errorf("upload backup: %w", err)
	}

	if err := b.uploadMeta(ctx, s3, key, ts); err != nil {
		// A missing meta file must not void the snapshot itself.
		b.log.Warn("backup meta upload failed", "err", err)
	}

	pruned, err := b.prune(ctx, s3)
	if err != nil {
		// Prune failure must not fail the backup itself.
		b.log.Warn("backup prune failed", "err", err)
	}
	b.log.Info("backup completed", "key", key, "bytes", fi.Size(), "pruned", pruned)
	return key, nil
}

// uploadMeta records a small JSON digest beside the snapshot: when it was
// taken, against which schema/cron, and the non-secret service config. This
// is what makes a bare .db restorable with its context.
func (b *Backer) uploadMeta(ctx context.Context, s3 s3store.Client, dbKey string, ts time.Time) error {
	meta := map[string]any{
		"version":        1,
		"created_at":     ts.Format(time.RFC3339),
		"db_key":         dbKey,
		"schema_version": b.schemaVersion(ctx),
		"backup":         map[string]any{"cron": b.Cron(), "keep": b.keep()},
	}
	if b.Meta != nil {
		meta["service"] = b.Meta()
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	metaKey := strings.TrimSuffix(dbKey, ".db") + ".meta.json"
	if _, err := s3.Put(ctx, metaKey, strings.NewReader(string(data)), int64(len(data)), "application/json"); err != nil {
		return fmt.Errorf("upload meta: %w", err)
	}
	return nil
}

func (b *Backer) schemaVersion(ctx context.Context) int64 {
	var v int64
	// goose's bookkeeping table exists in every migrated database; a zero
	// result simply means "unknown" and stays harmless in the digest.
	_ = b.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version`).Scan(&v)
	return v
}

func (b *Backer) keep() int {
	if b.Keep <= 0 {
		return KeepDefault
	}
	return b.Keep
}

// prune retains only the keep() most recent snapshot pairs, deleting older
// db and meta objects together.
func (b *Backer) prune(ctx context.Context, s3 s3store.Client) (int, error) {
	keys, err := s3.ListKeys(ctx, prefix)
	if err != nil {
		return 0, err
	}
	dbTS, metaTS := map[time.Time]bool{}, map[time.Time]bool{}
	for _, k := range keys {
		if ts, ok := parseKeyTime(k); ok {
			if strings.HasSuffix(k, ".meta.json") {
				metaTS[ts] = true
			} else {
				dbTS[ts] = true
			}
		}
	}
	all := make([]time.Time, 0, len(dbTS)+len(metaTS))
	for ts := range dbTS {
		all = append(all, ts)
	}
	for ts := range metaTS {
		if !dbTS[ts] {
			all = append(all, ts)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].After(all[j]) })
	if len(all) <= b.keep() {
		return 0, nil
	}

	pruned := 0
	for _, ts := range all[b.keep():] {
		if dbTS[ts] {
			if err := s3.Delete(ctx, prefix+"core-"+ts.Format(keyLayout)+".db"); err != nil {
				return pruned, err
			}
			pruned++
		}
		if metaTS[ts] {
			if err := s3.Delete(ctx, prefix+"core-"+ts.Format(keyLayout)+".meta.json"); err != nil {
				return pruned, err
			}
			pruned++
		}
	}
	return pruned, nil
}

// TestConnection verifies the R2 backup target end-to-end: write a probe
// object, see it listed, delete it. This exercises the same credentials and
// bucket the backups use, so a passing probe means passing backups.
func (b *Backer) TestConnection(ctx context.Context) error {
	s3 := b.client()
	if s3 == nil {
		return errors.New("备份 R2 未在后台配置（管理台「备份」页填写 endpoint / 密钥 / 桶）")
	}
	probe := prefix + "connection-probe.txt"
	if _, err := s3.Put(ctx, probe, strings.NewReader("probe "+b.now().UTC().Format(time.RFC3339)), 64, "text/plain"); err != nil {
		return fmt.Errorf("写入探针对象失败: %w", err)
	}
	keys, err := s3.ListKeys(ctx, prefix)
	if err != nil {
		return fmt.Errorf("列举备份桶失败: %w", err)
	}
	found := false
	for _, k := range keys {
		if k == probe {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("探针对象写入后未出现在列表中")
	}
	if err := s3.Delete(ctx, probe); err != nil {
		return fmt.Errorf("删除探针对象失败: %w", err)
	}
	return nil
}

// Snapshot is one backup entry in the history listing.
type Snapshot struct {
	When    time.Time
	DBKey   string
	MetaKey string
	HasMeta bool
}

// Recent lists the newest snapshot entries (db + optional meta), newest
// first, at most limit of them. Unparseable keys are ignored.
func (b *Backer) Recent(ctx context.Context, limit int) ([]Snapshot, error) {
	s3 := b.client()
	if s3 == nil {
		return nil, nil
	}
	keys, err := s3.ListKeys(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	byTS := map[time.Time]*Snapshot{}
	for _, k := range keys {
		ts, ok := parseKeyTime(k)
		if !ok {
			continue
		}
		s := byTS[ts]
		if s == nil {
			s = &Snapshot{When: ts}
			byTS[ts] = s
		}
		if strings.HasSuffix(k, ".meta.json") {
			s.MetaKey, s.HasMeta = k, true
		} else {
			s.DBKey = k
		}
	}
	all := make([]Snapshot, 0, len(byTS))
	for _, s := range byTS {
		all = append(all, *s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].When.After(all[j].When) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// parseKeyTime extracts the timestamp from core-YYYYMMDD-HHMMSS.db and
// core-YYYYMMDD-HHMMSS.meta.json keys.
func parseKeyTime(key string) (time.Time, bool) {
	name := strings.TrimPrefix(filepath.Base(key), "core-")
	name = strings.TrimSuffix(name, ".db")
	name = strings.TrimSuffix(name, ".meta.json")
	ts, err := time.Parse(keyLayout, name)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}
