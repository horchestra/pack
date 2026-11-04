// Package db opens the operator's own database — PostgreSQL (pgx) or SQLite (modernc, no cgo) — and migrates it.
// The migrations are embedded: the binary is all that is needed. This is the operator's database (the policy,
// the Bus, the change requests), not the pack state store (К2), which pack owns and the operator only reads.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB is an open, migrated database.
type DB struct {
	*sql.DB
	driver string // pgx | sqlite
}

// Driver is the database's driver (pgx | sqlite): SQLite wants one writer and the nearest types.
func (d *DB) Driver() string { return d.driver }

// SQLite says whether the database is SQLite.
func (d *DB) SQLite() bool { return d.driver == "sqlite" }

// Open connects to dsn (postgres://… or sqlite://<path>) and applies the migrations not yet applied.
func Open(ctx context.Context, dsn string) (*DB, error) {
	driver, name, err := parse(dsn)
	if err != nil {
		return nil, err
	}
	sdb, err := sql.Open(driver, name)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		sdb.SetMaxOpenConns(1) // one writer, no "database is locked"
	}
	d := &DB{DB: sdb, driver: driver}
	if err := d.migrate(ctx); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return d, nil
}

// parse reads the scheme of a DSN: postgres:// (pgx) or sqlite://<path>.
func parse(dsn string) (driver, name string, err error) {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return "", "", fmt.Errorf("db %q: postgres://… or sqlite://<path>", dsn)
	}
	switch scheme {
	case "postgres", "postgresql":
		return "pgx", dsn, nil
	case "sqlite":
		if len(rest) == 0 {
			return "", "", fmt.Errorf("db %q: sqlite://<path>", dsn)
		}
		return "sqlite", rest, nil
	}
	return "", "", fmt.Errorf("db %q: postgres://… or sqlite://<path>", dsn)
}

// migrate applies the migrations after the newest applied, each in a transaction, in name order.
func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `create table if not exists operator_migrations (
		version integer primary key,
		applied_at timestamptz not null default current_timestamp)`); err != nil {
		return fmt.Errorf("the migrations table: %w", err)
	}
	var applied int
	if err := d.QueryRowContext(ctx, `select coalesce(max(version), 0) from operator_migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("the migrations applied: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var v int
		if _, err := fmt.Sscanf(name, "migrations/%d_", &v); err != nil {
			return fmt.Errorf("a migration's name is NNNN_what.sql: %s", name)
		}
		if v <= applied {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `insert into operator_migrations (version) values ($1)`, v); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
