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

// Bind is the placeholder style of the driver: ? for SQLite, $n for PostgreSQL.
func (d *DB) Bind(query string) string {
	if d.SQLite() {
		return query
	}
	var out strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

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
		for _, pragma := range []string{
			"PRAGMA journal_mode = WAL", // readers never block the one writer
			"PRAGMA busy_timeout = 10000",
			"PRAGMA synchronous = NORMAL",
		} {
			if _, err := sdb.ExecContext(ctx, pragma); err != nil {
				_ = sdb.Close()
				return nil, fmt.Errorf("sqlite: %w", err)
			}
		}
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

// migrate applies the migrations after the newest applied, each in a transaction, in version order. A migration
// is NNNN_what.sql, or NNNN_what.pg.sql / NNNN_what.sqlite.sql where the dialects differ — the driver's own file
// wins; the version is recorded once either way.
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
	dialect := "pg"
	if d.SQLite() {
		dialect = "sqlite"
	}
	byVersion := map[int]map[string]string{} // version → variant ("" | pg | sqlite) → file
	for _, name := range names {
		var v int
		rest := ""
		if _, err := fmt.Sscanf(name, "migrations/%d_%s", &v, &rest); err != nil {
			return fmt.Errorf("a migration's name is NNNN_what.sql: %s", name)
		}
		variant := ""
		base := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		if i := strings.LastIndex(base, "."); i >= 0 {
			variant = base[i+1:]
		}
		if byVersion[v] == nil {
			byVersion[v] = map[string]string{}
		}
		byVersion[v][variant] = name
	}
	versions := make([]int, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	for _, v := range versions {
		if v <= applied {
			continue
		}
		name := byVersion[v][dialect]
		if len(name) == 0 {
			name = byVersion[v][""]
		}
		if len(name) == 0 {
			return fmt.Errorf("migration %d has no file for %s", v, dialect)
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
