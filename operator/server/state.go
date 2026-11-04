package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/saga"
)

// stateStore is the pack state store, read only (К2): pack owns the schema (pack_records, pack_journal,
// pack_facts), the operator only ever selects.
type stateStore struct {
	sql  *sql.DB
	pg   bool
	open bool // the store answered once (a missing schema is an empty store, not an error)
}

// openState opens the state store's DSN read-only; a store that never answers is closed silently — the
// endpoints of state then answer empty.
func openState(ctx context.Context, dsn string) (*stateStore, error) {
	if len(dsn) == 0 {
		return &stateStore{}, nil
	}
	scheme, name, ok := strings.Cut(dsn, "://")
	if !ok {
		return nil, fmt.Errorf("state_dsn %q: postgres://… or sqlite://<path>", dsn)
	}
	var driver string
	switch scheme {
	case "postgres", "postgresql":
		driver, name = "pgx", dsn
	case "sqlite":
		driver = "sqlite"
	default:
		return nil, fmt.Errorf("state_dsn %q: postgres://… or sqlite://<path>", dsn)
	}
	sdb, err := sql.Open(driver, name)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		sdb.SetMaxOpenConns(1)
		if _, err := sdb.ExecContext(ctx, "PRAGMA busy_timeout = 10000"); err != nil {
			_ = sdb.Close()
			return nil, err
		}
	}
	st := &stateStore{sql: sdb, pg: driver == "pgx"}
	var version int
	if err := sdb.QueryRowContext(ctx, `select version from pack_schema`).Scan(&version); err != nil {
		if isNoTable(err) { // a store pack never wrote yet
			return &stateStore{sql: sdb, pg: st.pg}, nil
		}
		_ = sdb.Close()
		return nil, fmt.Errorf("the state store: %w", err)
	}
	st.open = true
	return st, nil
}

// ensureOpen is the lazy open: pack writes the schema when it first runs, maybe after the operator started;
// every read looks again until the schema answers.
func (s *stateStore) ensureOpen(ctx context.Context) bool {
	if s.open || s.sql == nil {
		return s.open
	}
	var version int
	if err := s.sql.QueryRowContext(ctx, `select version from pack_schema`).Scan(&version); err != nil {
		return false
	}
	s.open = true
	return true
}

// journalTime parses the journal's time: pack writes it as Go's String ("2006-01-02 15:04:05.999999 -0700 MST"),
// a hand-built store may hold RFC3339.
func journalTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999 -0700 MST", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// isNoTable says the error is a missing table (pack never wrote the store).
func isNoTable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "does not exist")
}

// Close shuts the store.
func (s *stateStore) Close() {
	if s != nil && s.sql != nil {
		_ = s.sql.Close()
	}
}

// q is the placeholder style of the driver.
func (s *stateStore) q(query string) string {
	if !s.pg {
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

// record is a release's state record: the saga's record as pack saved it, with the revision it is of.
type record struct {
	Revision  string          `json:"revision"`
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
	Body      json.RawMessage `json:"body"`
}

// recordOf is the record of an installation (a release's id in its release.yaml); none — nil.
func (s *stateStore) recordOf(ctx context.Context, installation string) (*record, error) {
	if !s.ensureOpen(ctx) {
		return nil, nil
	}
	var rec record
	err := s.sql.QueryRowContext(ctx, s.q(`select record, revision, version, updated_at from pack_records
		where installation = ?`), installation).Scan(&rec.Body, &rec.Revision, &rec.Version, &rec.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		if isNoTable(err) {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

// sagaRun is one saga of the journal: its id, when, and how it ended (the last entry's word).
type sagaRun struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
}

// sagasOf lists the installation's sagas, the newest first.
func (s *stateStore) sagasOf(ctx context.Context, installation string) ([]sagaRun, error) {
	if !s.ensureOpen(ctx) {
		return nil, nil
	}
	rows, err := s.sql.QueryContext(ctx, s.q(`select run, min(time), max(seq) from pack_journal
		where installation = ? group by run order by min(time) desc`), installation)
	if err != nil {
		if isNoTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []sagaRun
	for rows.Next() {
		var run sagaRun
		var at string
		var maxSeq int64
		if err := rows.Scan(&run.ID, &at, &maxSeq); err != nil {
			return nil, err
		}
		run.At = journalTime(at)
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // sqlite is one connection: no second query with the first still open
	for i := range out {
		var kind string
		_ = s.sql.QueryRowContext(ctx, s.q(`select kind from pack_journal
			where installation = ? and run = ? order by seq desc limit 1`), installation, out[i].ID).Scan(&kind)
		switch kind {
		case string(saga.KindEnd), string(saga.KindDone), string(saga.KindUndone):
			out[i].Outcome = "done"
		case string(saga.KindFail):
			out[i].Outcome = "failed"
		default:
			out[i].Outcome = "interrupted"
		}
	}
	return out, nil
}

// sagaEntries reads one saga's journal from afterSeq on.
func (s *stateStore) sagaEntries(ctx context.Context, installation, run string, afterSeq int64) ([]saga.Entry, error) {
	if !s.ensureOpen(ctx) {
		return nil, nil
	}
	rows, err := s.sql.QueryContext(ctx, s.q(`select seq, time, kind, node, step, message, error from pack_journal
		where installation = ? and run = ? and seq > ? order by seq`), installation, run, afterSeq)
	if err != nil {
		if isNoTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []saga.Entry
	for rows.Next() {
		var e saga.Entry
		var seq int64
		var at string
		var node, step, message, eerr *string
		if err := rows.Scan(&seq, &at, &e.Kind, &node, &step, &message, &eerr); err != nil {
			return nil, err
		}
		e.Time = journalTime(at)
		e.Run = run
		if node != nil {
			e.Node = *node
		}
		if step != nil {
			e.Step = *step
		}
		if message != nil {
			e.Message = *message
		}
		if eerr != nil {
			e.Error = *eerr
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// factsOf is the installation's facts (pack_facts): by the target that collected them.
func (s *stateStore) factsOf(ctx context.Context, installation string) (map[string]json.RawMessage, error) {
	if !s.ensureOpen(ctx) {
		return nil, nil
	}
	rows, err := s.sql.QueryContext(ctx, s.q(`select target, facts from pack_facts where installation = ?`), installation)
	if err != nil {
		if isNoTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var target string
		var facts json.RawMessage
		if err := rows.Scan(&target, &facts); err != nil {
			return nil, err
		}
		out[target] = facts
	}
	return out, rows.Err()
}

// eventsAsJSON is saga entries as api.Events (К4: the saga events endpoint's data).
func eventsAsJSON(entries []saga.Entry) []api.Event {
	out := make([]api.Event, 0, len(entries))
	for _, e := range entries {
		out = append(out, api.FromEntry(e))
	}
	return out
}
