// Package pgstore implements the scratch.Store interface backed by PostgreSQL.
//
// Production guidance:
//   - Run exactly ONE host-managed migration (systemd oneshot unit) before
//     starting the application. Docker / docker-compose is the TEST RIG only.
//   - DSN via TILLER_STORE_DSN env var or --dsn flag.
//   - Schema is applied idempotently (CREATE TABLE IF NOT EXISTS + version row
//     ON CONFLICT DO NOTHING). Safe to re-run; no destructive changes.
package pgstore

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaDDL string

// SchemaVersion is the expected version number written by schema.sql.
const SchemaVersion = 5

// DB wraps a *sql.DB opened against a PostgreSQL DSN.
type DB struct {
	db *sql.DB
}

// Open opens a PostgreSQL connection using the pgx stdlib driver.
// The caller is responsible for calling Close when done.
func Open(dsn string) (*DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	return &DB{db: db}, nil
}

// Close closes the underlying database connection pool.
func (d *DB) Close() error { return d.db.Close() }

// Ping verifies the connection is alive.
func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// Migrate applies the embedded schema DDL idempotently and returns the
// schema version row written by the DDL.
//
// It is safe to call Migrate multiple times; all statements are guarded with
// IF NOT EXISTS and the version INSERT uses ON CONFLICT DO NOTHING.
func Migrate(ctx context.Context, dsn string) (version int, err error) {
	d, err := Open(dsn)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	return d.Migrate(ctx)
}

// Migrate applies the embedded schema and returns the current schema version.
func (d *DB) Migrate(ctx context.Context) (int, error) {
	if _, err := d.db.ExecContext(ctx, schemaDDL); err != nil {
		return 0, fmt.Errorf("pgstore: migrate: %w", err)
	}
	var v int
	row := d.db.QueryRowContext(ctx,
		`SELECT version FROM schema_version ORDER BY version DESC LIMIT 1`)
	if err := row.Scan(&v); err != nil {
		return 0, fmt.Errorf("pgstore: version query: %w", err)
	}
	return v, nil
}

// Status returns schema version and row counts per table, or "uninitialized"
// indication when the schema_version table does not exist.
type Status struct {
	Version   int
	RowCounts map[string]int64
}

// QueryStatus connects, reads the schema version and per-table row counts.
// If the schema has not been initialised it returns Status{Version: 0}.
func QueryStatus(ctx context.Context, dsn string) (Status, error) {
	d, err := Open(dsn)
	if err != nil {
		return Status{}, err
	}
	defer d.Close()
	return d.QueryStatus(ctx)
}

// QueryStatus reads the schema version and per-table row counts.
func (d *DB) QueryStatus(ctx context.Context) (Status, error) {
	// Check if schema_version table exists.
	var exists bool
	err := d.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name   = 'schema_version'
		)`).Scan(&exists)
	if err != nil {
		return Status{}, fmt.Errorf("pgstore: status check: %w", err)
	}
	if !exists {
		return Status{Version: 0}, nil
	}

	var v int
	if err := d.db.QueryRowContext(ctx,
		`SELECT version FROM schema_version ORDER BY version DESC LIMIT 1`).Scan(&v); err != nil {
		return Status{}, fmt.Errorf("pgstore: version query: %w", err)
	}

	tables := []string{"run", "dispatch", "dispatch_seq", "doc", "trace_event", "audit_event", "schema_version"}
	counts := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		// #nosec G201 — table name is from a fixed internal slice, not user input.
		if err := d.db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", t)).Scan(&n); err != nil {
			return Status{}, fmt.Errorf("pgstore: count %s: %w", t, err)
		}
		counts[t] = n
	}
	return Status{Version: v, RowCounts: counts}, nil
}
