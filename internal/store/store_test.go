package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaowenz/xiaowen-core/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return New(d)
}

func futureExpiry() time.Time { return time.Now().Add(24 * time.Hour) }

// insertDownloadAt inserts a download row with an explicit timestamp so tests
// can place rows outside the current-day dedupe window.
func (s *Store) insertDownloadAt(slug, ipHash string, t time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO downloads (slug, ip_hash, ua, referer, created_at) VALUES (?, ?, '', '', ?)`,
		slug, ipHash, t.Unix())
	return err
}
