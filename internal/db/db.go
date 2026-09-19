// Package db opens the SQLite database and applies embedded goose migrations.
package db

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func init() {
	goose.SetBaseFS(migrationsFS)
	_ = goose.SetDialect("sqlite3")
}

// Open opens (creating if needed) the SQLite database at path, enables WAL
// mode, and runs all pending migrations. Migration failure is fatal to the
// caller (fail fast per AGENTS §3.4).
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite is single-writer; one connection avoids SQLITE_BUSY under WAL.
	d.SetMaxOpenConns(1)
	if err := Migrate(d); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// Migrate applies all pending migrations (goose up).
func Migrate(d *sql.DB) error {
	if err := goose.Up(d, "migrations"); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// MigrateTo migrates to a specific version (used for down-to-zero tests).
func MigrateTo(d *sql.DB, version int64) error {
	if err := goose.DownTo(d, "migrations", version); err != nil {
		return fmt.Errorf("migrate to %d: %w", version, err)
	}
	return nil
}
