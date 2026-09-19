package db

import (
	"path/filepath"
	"testing"
)

func TestMigrateUpDownUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// Up from empty.
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tables := []string{"subscribers", "comments", "newsletters", "downloads", "pageviews", "media", "send_log"}
	for _, tbl := range tables {
		var name string
		err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after up: %v", tbl, err)
		}
	}

	// WAL mode must be active.
	var journal string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}

	// Down to zero.
	if err := MigrateTo(d, 0); err != nil {
		t.Fatalf("MigrateTo(0): %v", err)
	}
	for _, tbl := range tables {
		var name string
		err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name)
		if err == nil {
			t.Errorf("table %q still present after down", tbl)
		}
	}

	// Up again — lossless re-apply.
	if err := Migrate(d); err != nil {
		t.Fatalf("Migrate (second up): %v", err)
	}
	var name string
	if err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='subscribers'`).Scan(&name); err != nil {
		t.Errorf("subscribers missing after re-up: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenMigratesAutomatically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = d.Close() }()
	var v int64
	if err := d.QueryRow(`SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&v); err != nil {
		t.Fatalf("goose_db_version: %v", err)
	}
	if v < 1 {
		t.Errorf("goose version = %d, want >= 1", v)
	}
}
