package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

// The storage of state: db (release.yaml) is a database pack owns the schema of: the record of each installation
// (pack_records), the sagas' journals (pack_journal), the facts the targets told (pack_facts), and — on SQLite,
// where no advisory locks are — the saga locks (pack_locks). The DSN is postgres://… or sqlite://<path>; it is
// read from the environment variable release.yaml names (state_dsn_env, PACK_STATE_DSN by default), never from
// the file. pack applies the missing migrations; a schema newer than pack's is refused.
const schemaVersion = 1

// DB is a state database: PostgreSQL or SQLite.
type DB struct {
	sql    *sql.DB
	driver string // postgres | sqlite
}

// OpenDB opens the state database of a DSN — postgres://… (pgx) or sqlite://<path> (no cgo) — and applies the
// migrations the schema lacks. A schema newer than pack's own is refused: an older pack must not guess.
func OpenDB(ctx context.Context, dsn string) (*DB, error) {
	driver, source, ok := strings.Cut(dsn, "://")
	if !ok {
		return nil, fmt.Errorf("state DSN %q: postgres://… or sqlite://<path>", dsn)
	}
	switch driver {
	case "postgres", "postgresql":
		driver = "postgres"
		source = dsn // pgx takes the URL whole
	case "sqlite":
	default:
		return nil, fmt.Errorf("state DSN %q: postgres://… or sqlite://<path>", dsn)
	}
	sq, err := sql.Open(driverName(driver), source)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		sq.SetMaxOpenConns(1) // one writer: the lock, the record and the journal serialize on it
		for _, pragma := range []string{
			"PRAGMA journal_mode = WAL", // readers never block the one writer, across pack's connections
			"PRAGMA busy_timeout = 10000",
			"PRAGMA synchronous = NORMAL",
		} {
			if _, err := sq.ExecContext(ctx, pragma); err != nil {
				_ = sq.Close()
				return nil, fmt.Errorf("sqlite: %w", err)
			}
		}
	}
	db := &DB{sql: sq, driver: driver}
	if err := db.migrate(ctx); err != nil {
		_ = sq.Close()
		return nil, err
	}
	return db, nil
}

func driverName(d string) string {
	if d == "postgres" {
		return "pgx"
	}
	return "sqlite"
}

// Close releases the database.
func (db *DB) Close() error { return db.sql.Close() }

// q is the query with the driver's placeholders: ? for SQLite, $1, $2, … for PostgreSQL.
func (db *DB) q(query string) string {
	if db.driver != "postgres" {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// migrate applies the migrations the schema lacks, and refuses one newer than pack's own.
func (db *DB) migrate(ctx context.Context) error {
	jsonType, timeType := "jsonb", "timestamptz"
	if db.driver == "sqlite" {
		jsonType, timeType = "text", "timestamp" // a declared timestamp reads back as time.Time
	}
	has := func(table string) bool {
		var name string
		err := db.sql.QueryRowContext(ctx, db.q(`SELECT table_name FROM information_schema.tables WHERE table_name = ?`), table).Scan(&name)
		if db.driver == "sqlite" {
			err = db.sql.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		return err == nil
	}
	if !has("pack_schema") {
		stmts := []string{
			`CREATE TABLE pack_schema (version int NOT NULL)`,
			`INSERT INTO pack_schema (version) VALUES (0)`,
			`CREATE TABLE pack_records (installation uuid primary key, record ` + jsonType + ` NOT NULL,
				revision text, version bigint NOT NULL, updated_at ` + timeType + ` NOT NULL)`,
			`CREATE TABLE pack_journal (installation uuid, run text, seq bigint, time ` + timeType + `,
				kind text, node text, step text, message text, error text,
				primary key (installation, run, seq))`,
			`CREATE TABLE pack_facts (installation uuid, target text, facts ` + jsonType + `, collected_at ` + timeType + `,
				primary key (installation, target))`,
			`CREATE TABLE pack_locks (installation uuid primary key, owner text, expires_at ` + timeType + `)`,
		}
		if db.driver == "postgres" {
			stmts = stmts[:len(stmts)-1] // pack_locks is SQLite's alone: PostgreSQL locks advisedly
		}
		for _, q := range stmts {
			if _, err := db.sql.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("migrate the state schema: %w", err)
			}
		}
	}
	var v int
	if err := db.sql.QueryRowContext(ctx, `SELECT version FROM pack_schema`).Scan(&v); err != nil {
		return fmt.Errorf("the state schema's version: %w", err)
	}
	if v > schemaVersion {
		return fmt.Errorf("the state schema is version %d, this pack knows %d: a newer pack made it — run a pack of its time, or migrate it back", v, schemaVersion)
	}
	for v < schemaVersion {
		v++ // version 1 is the tables above: nothing to do between 0 and 1
		if _, err := db.sql.ExecContext(ctx, db.q(`UPDATE pack_schema SET version = ?`), v); err != nil {
			return fmt.Errorf("migrate the state schema to %d: %w", v, err)
		}
	}
	return nil
}

// uuidOK refuses an id that is no UUID before the database does, with the row's name in it.
func uuidOK(what, id string) error {
	if len(id) != 36 {
		return fmt.Errorf("%s %q: a UUID of an installation", what, id)
	}
	return nil
}

// Store is the record of installation in the database, with the saga's lock (saga.Locker): an advisory lock of
// PostgreSQL's on its own connection, a lease of pack_locks on SQLite, renewed while the saga runs.
type Store struct {
	db           *DB
	installation string
	version      int64 // the row's version as Load read it: optimistic locking
	loaded       bool

	lockConn *sql.Conn
	renew    chan struct{}
}

// DBStore is the record of the installation in db.
func (db *DB) Store(installation string) (*Store, error) {
	if err := uuidOK("installation", installation); err != nil {
		return nil, err
	}
	return &Store{db: db, installation: installation}, nil
}

func (s *Store) Load() (*saga.Record, error) {
	var raw []byte
	err := s.db.sql.QueryRow(s.db.q(`SELECT record, version FROM pack_records WHERE installation = ?`), s.installation).Scan(&raw, &s.version)
	if errors.Is(err, sql.ErrNoRows) {
		s.loaded, s.version = false, 0
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec saga.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("the record of %s: %w", s.installation, err)
	}
	s.loaded = true
	return &rec, nil
}

func (s *Store) Save(r *saga.Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !s.loaded {
		_, err = s.db.sql.Exec(s.db.q(`INSERT INTO pack_records (installation, record, revision, version, updated_at) VALUES (?, ?, ?, 1, ?)`),
			s.installation, raw, nullStr(r.Rev), now)
		if err != nil {
			return fmt.Errorf("the record of %s is there already: another pack wrote it first — load it again", s.installation)
		}
		s.loaded, s.version = true, 1
		return nil
	}
	res, err := s.db.sql.Exec(s.db.q(`UPDATE pack_records SET record = ?, revision = ?, version = version + 1, updated_at = ?
		WHERE installation = ? AND version = ?`), raw, nullStr(r.Rev), now, s.installation, s.version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("the record of %s changed underneath: another pack wrote it — load it again", s.installation)
	}
	s.version++
	return nil
}

func (s *Store) Delete() error {
	_, err := s.db.sql.Exec(s.db.q(`DELETE FROM pack_records WHERE installation = ?`), s.installation)
	s.loaded, s.version = false, 0
	return err
}

func nullStr(s string) any {
	if len(s) == 0 {
		return nil
	}
	return s
}

// lease is how long a SQLite lock lives without renewal, and how often the saga renews it.
const (
	lease = 30 * time.Second
	renew = 10 * time.Second
)

// Lock takes the installation's saga lock. PostgreSQL: pg_advisory_lock of the installation's hash, on a
// connection of its own — it goes with the connection. SQLite: a row of pack_locks with a lease the saga renews;
// an expired one is the next pack's to take.
func (s *Store) Lock() (unlock func(), err error) {
	if s.db.driver == "postgres" {
		conn, err := s.db.sql.Conn(context.Background())
		if err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock(hashtext($1))`, s.installation); err != nil {
			_ = conn.Close()
			return nil, err
		}
		s.lockConn = conn
		return func() {
			_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, s.installation)
			_ = conn.Close()
		}, nil
	}
	owner := strconv.Itoa(os.Getpid())
	now := time.Now().UTC()
	if _, err := s.db.sql.Exec(s.db.q(`DELETE FROM pack_locks WHERE installation = ? AND expires_at < ?`), s.installation, now); err != nil {
		return nil, err
	}
	if _, err := s.db.sql.Exec(s.db.q(`INSERT INTO pack_locks (installation, owner, expires_at) VALUES (?, ?, ?)`),
		s.installation, owner, now.Add(lease)); err != nil {
		who, expires := "", time.Time{}
		_ = s.db.sql.QueryRow(s.db.q(`SELECT owner, expires_at FROM pack_locks WHERE installation = ?`), s.installation).Scan(&who, &expires)
		return nil, fmt.Errorf("%s is locked by %s until %s: another pack is working on it", s.installation, who, expires.UTC().Format("15:04:05Z"))
	}
	stop := make(chan struct{})
	s.renew = stop
	go func() {
		t := time.NewTicker(renew)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case at := <-t.C:
				_, _ = s.db.sql.Exec(s.db.q(`UPDATE pack_locks SET expires_at = ? WHERE installation = ? AND owner = ?`),
					at.Add(lease), s.installation, owner)
			}
		}
	}()
	return func() {
		close(stop)
		_, _ = s.db.sql.Exec(s.db.q(`DELETE FROM pack_locks WHERE installation = ? AND owner = ?`), s.installation, owner)
	}, nil
}

// Journal is the journal of an installation in the database (pack_journal), appended and read as the file one is.
type Journal struct {
	db           *DB
	installation string
}

// JournalOf is the journal of the installation in db.
func (db *DB) JournalOf(installation string) (*Journal, error) {
	if err := uuidOK("installation", installation); err != nil {
		return nil, err
	}
	return &Journal{db: db, installation: installation}, nil
}

func (j *Journal) Append(e saga.Entry) error {
	var seq int64
	row := j.db.sql.QueryRow(j.db.q(`SELECT COALESCE(MAX(seq), 0) + 1 FROM pack_journal WHERE installation = ? AND run = ?`), j.installation, e.Run)
	if err := row.Scan(&seq); err != nil {
		return err
	}
	_, err := j.db.sql.Exec(j.db.q(`INSERT INTO pack_journal (installation, run, seq, time, kind, node, step, message, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		j.installation, e.Run, seq, e.Time.UTC(), string(e.Kind), e.Node, e.Step, e.Message, e.Error)
	return err
}

// Runs lists the sagas the journal holds, oldest first (an id sorts by its time).
func (j *Journal) Runs() ([]string, error) {
	rows, err := j.db.sql.Query(j.db.q(`SELECT DISTINCT run FROM pack_journal WHERE installation = ? ORDER BY run`), j.installation)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Read returns the entries of one saga.
func (j *Journal) Read(run string) ([]saga.Entry, error) {
	rows, err := j.db.sql.Query(j.db.q(`SELECT time, run, kind, node, step, message, error FROM pack_journal
		WHERE installation = ? AND run = ? ORDER BY seq`), j.installation, run)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []saga.Entry
	for rows.Next() {
		var e saga.Entry
		var kind string
		if err := rows.Scan(&e.Time, &e.Run, &kind, &e.Node, &e.Step, &e.Message, &e.Error); err != nil {
			return nil, err
		}
		e.Kind = saga.Kind(kind)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune removes the oldest sagas, so that keep newest stay; keep below 0 keeps all. It returns the ones removed.
func (j *Journal) Prune(keep int) ([]string, error) {
	if keep < 0 {
		return nil, nil
	}
	runs, err := j.Runs()
	if err != nil || len(runs) <= keep {
		return nil, err
	}
	old := runs[:len(runs)-keep]
	for _, r := range old {
		if _, err := j.db.sql.Exec(j.db.q(`DELETE FROM pack_journal WHERE installation = ? AND run = ?`), j.installation, r); err != nil {
			return old, err
		}
	}
	return old, nil
}

// Facts are the facts a target of the installation told (pack_facts).
type Facts struct {
	db           *DB
	installation string
}

// FactsOf are the facts of the installation in db.
func (db *DB) FactsOf(installation string) (*Facts, error) {
	if err := uuidOK("installation", installation); err != nil {
		return nil, err
	}
	return &Facts{db: db, installation: installation}, nil
}

// Save writes the facts a target told.
func (f *Facts) Save(name string, facts *target.Facts) error {
	raw, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	if _, err := f.db.sql.Exec(f.db.q(`DELETE FROM pack_facts WHERE installation = ? AND target = ?`), f.installation, name); err != nil {
		return err
	}
	_, err = f.db.sql.Exec(f.db.q(`INSERT INTO pack_facts (installation, target, facts, collected_at) VALUES (?, ?, ?, ?)`),
		f.installation, name, raw, time.Now().UTC())
	return err
}

// Load reads the facts the targets told, by the target's name.
func (f *Facts) Load() (map[string]*target.Facts, error) {
	rows, err := f.db.sql.Query(f.db.q(`SELECT target, facts FROM pack_facts WHERE installation = ?`), f.installation)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]*target.Facts{}
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		var facts target.Facts
		if err := json.Unmarshal([]byte(raw), &facts); err != nil {
			return nil, fmt.Errorf("the facts of %s: %w", name, err)
		}
		out[name] = &facts
	}
	return out, rows.Err()
}

// Forget removes the facts of a target that tells none now.
func (f *Facts) Forget(name string) error {
	_, err := f.db.sql.Exec(f.db.q(`DELETE FROM pack_facts WHERE installation = ? AND target = ?`), f.installation, name)
	return err
}
