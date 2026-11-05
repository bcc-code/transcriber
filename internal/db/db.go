// Package db opens the SQLite database and applies the embedded goose
// migrations. Queries live in internal/db/queries and are compiled by sqlc
// into package dbq.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go driver; the Docker build sets CGO_ENABLED=0
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens (creating if needed) the SQLite file at path and migrates it to
// the latest schema. Use ":memory:" for a throwaway database.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY between
	// our own goroutines and keeps ":memory:" databases from splitting.
	conn.SetMaxOpenConns(1)
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := Migrate(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// Migrate applies all pending embedded migrations.
func Migrate(ctx context.Context, conn *sql.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, sub)
	if err != nil {
		return fmt.Errorf("goose: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
