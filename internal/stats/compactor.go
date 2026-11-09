// Package stats keeps the pageviews table bounded: a nightly compactor rolls
// complete days into the permanent daily_stats layer (store.CompactPageviews)
// and, whenever rows were pruned, checkpoints the WAL and VACUUMs so the
// deleted pages are actually returned to the filesystem — a DELETE alone
// leaves the db file just as large.
package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

// DefaultCron fires at 03:30 — before the 04:00 backup so snapshots are
// already compacted (and small) when they are taken.
const DefaultCron = "30 3 * * *"

// ErrBusy is returned when a compaction is already in flight.
var ErrBusy = errors.New("stats compaction already running")

// Compactor schedules and runs pageview compaction.
type Compactor struct {
	db    *sql.DB
	store *store.Store
	log   *slog.Logger
	now   func() time.Time

	// Cron is the 5-field schedule; empty or invalid falls back to DefaultCron.
	Cron string
	// Retention bounds the raw pageviews window (default store.DefaultPVRetention).
	Retention time.Duration

	// checkInterval is how often the scheduler re-checks the clock; coarse on
	// purpose (cron granularity is a minute), tiny in tests.
	checkInterval time.Duration
	// testHook runs mid-RunOnce for concurrency tests only.
	testHook func()

	mu sync.Mutex // TryLock: only one RunOnce at a time
}

// NewCompactor builds a Compactor over the shared database and store.
func NewCompactor(d *sql.DB, st *store.Store, log *slog.Logger) *Compactor {
	if log == nil {
		log = slog.Default()
	}
	return &Compactor{
		db:            d,
		store:         st,
		log:           log,
		now:           time.Now,
		Retention:     store.DefaultPVRetention,
		checkInterval: 30 * time.Second,
	}
}

// cron returns the effective schedule: the configured value when valid,
// otherwise DefaultCron.
func (c *Compactor) cron() string {
	expr := strings.TrimSpace(c.Cron)
	if expr == "" {
		return DefaultCron
	}
	if _, err := cron.ParseStandard(expr); err != nil {
		c.log.Warn("stats cron invalid; using default", "expr", expr, "err", err)
		return DefaultCron
	}
	return expr
}

// RunOnce performs one compaction cycle: rollup + prune (one transaction in
// the store), then VACUUM only when rows were actually pruned. Returns days
// rolled, rows pruned, or ErrBusy when a run is already in flight.
func (c *Compactor) RunOnce(ctx context.Context) (rolled, pruned int64, err error) {
	if !c.mu.TryLock() {
		return 0, 0, ErrBusy
	}
	defer c.mu.Unlock()

	rolled, pruned, err = c.store.CompactPageviews(c.now(), c.Retention)
	if err != nil {
		return 0, 0, fmt.Errorf("compact pageviews: %w", err)
	}
	if c.testHook != nil {
		c.testHook()
	}
	if pruned > 0 {
		// DELETE 只挂 freelist；checkpoint + VACUUM 才把空间还给文件系统。
		if _, err := c.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return rolled, pruned, fmt.Errorf("wal_checkpoint: %w", err)
		}
		if _, err := c.db.ExecContext(ctx, `VACUUM`); err != nil {
			return rolled, pruned, fmt.Errorf("vacuum: %w", err)
		}
	}
	c.log.Info("pageview compaction done", "rolled_days", rolled, "pruned_rows", pruned,
		"vacuumed", pruned > 0)
	return rolled, pruned, nil
}

// Start runs the scheduler until ctx is cancelled: one immediate run first
// (a deploy bounds the table right away, and catches nights missed during
// downtime), then fires whenever the clock passes the next cron slot.
// Precision is bounded by checkInterval.
func (c *Compactor) Start(ctx context.Context) {
	if _, _, err := c.RunOnce(ctx); err != nil && !errors.Is(err, ErrBusy) {
		c.log.Error("pageview compaction failed", "err", err)
	}
	sched, err := cron.ParseStandard(c.cron())
	if err != nil {
		return // unreachable: cron() falls back to a valid default
	}
	next := sched.Next(c.now())
	tick := time.NewTicker(c.checkInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if c.now().Before(next) {
			continue
		}
		if _, _, err := c.RunOnce(ctx); err != nil && !errors.Is(err, ErrBusy) {
			c.log.Error("pageview compaction failed", "err", err)
		}
		next = sched.Next(c.now())
	}
}
