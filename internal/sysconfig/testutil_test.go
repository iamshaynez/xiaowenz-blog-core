package sysconfig

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/db"
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
