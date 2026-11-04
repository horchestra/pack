package bus

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/horchestra/pack-operator/db"
)

// SQLBus is the Bus on the operator's own database (migration 0002): PostgreSQL — the claims raced with the
// group row locked FOR UPDATE SKIP LOCKED; SQLite — one connection, one writer, the same transactions.
type SQLBus struct {
	db *db.DB
	// Lease is how long a claim holds without its subscriber; Poll is how often Next looks again.
	Lease, Poll time.Duration
	owner       string
}

var _ Bus = (*SQLBus)(nil)

// NewSQL is the Bus on the operator's database.
func NewSQL(d *db.DB) *SQLBus {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return &SQLBus{db: d, Lease: 10 * time.Second, Poll: 50 * time.Millisecond, owner: "bus-" + hex.EncodeToString(b[:])}
}

// bind numbers the ? of a query for PostgreSQL ($1, $2 …); SQLite takes them as they are.
func (b *SQLBus) bind(query string) string {
	if b.db.SQLite() {
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

func (b *SQLBus) Publish(ctx context.Context, kind string, body any) (int64, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	if b.db.SQLite() {
		res, err := b.db.ExecContext(ctx, b.bind(`insert into bus_events (kind, body, at) values (?, ?, ?)`), kind, raw, time.Now().UTC())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	var seq int64
	err = b.db.QueryRowContext(ctx, b.bind(`insert into bus_events (kind, body) values (?, ?) returning seq`), kind, raw).Scan(&seq)
	return seq, err
}

func (b *SQLBus) Submit(ctx context.Context, req Request) (Request, bool, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return Request{}, false, err
	}
	res, err := b.db.ExecContext(ctx, b.bind(`insert into bus_requests (key, id, body) values (?, ?, ?) on conflict (key) do nothing`),
		req.Key, req.ID, raw)
	if err != nil {
		return Request{}, false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := b.Publish(ctx, KindRequest, req); err != nil {
			return Request{}, false, err
		}
		return req, true, nil
	}
	// the same request asked again: the one that stands answers
	var body []byte
	if err := b.db.QueryRowContext(ctx, b.bind(`select body from bus_requests where key = ?`), req.Key).Scan(&body); err != nil {
		return Request{}, false, err
	}
	var standing Request
	if err := json.Unmarshal(body, &standing); err != nil {
		return Request{}, false, err
	}
	return standing, false, nil
}

// UpdateRequest records a state of the request that stands (the policy's transitions) and publishes it.
func (b *SQLBus) UpdateRequest(ctx context.Context, req Request) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := b.db.ExecContext(ctx, b.bind(`update bus_requests set body = ? where id = ?`), raw, req.ID); err != nil {
		return err
	}
	_, err = b.Publish(ctx, KindRequest, req)
	return err
}

// GetRequest reads the request that stands.
func (b *SQLBus) GetRequest(ctx context.Context, id string) (Request, error) {
	var body []byte
	if err := b.db.QueryRowContext(ctx, b.bind(`select body from bus_requests where id = ?`), id).Scan(&body); err != nil {
		return Request{}, err
	}
	var req Request
	return req, json.Unmarshal(body, &req)
}

// PublishJob records a job — a new one, or a state of one standing — and publishes it (migration 0005).
func (b *SQLBus) PublishJob(ctx context.Context, job Job) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if _, err := b.db.ExecContext(ctx, b.bind(`insert into bus_jobs (id, body, updated_at) values (?, ?, current_timestamp)
		on conflict (id) do update set body = excluded.body, updated_at = excluded.updated_at`), job.ID, raw); err != nil {
		return err
	}
	_, err = b.Publish(ctx, KindJob, job)
	return err
}

// GetJob reads the job that stands.
func (b *SQLBus) GetJob(ctx context.Context, id string) (Job, error) {
	var body []byte
	if err := b.db.QueryRowContext(ctx, b.bind(`select body from bus_jobs where id = ?`), id).Scan(&body); err != nil {
		return Job{}, err
	}
	var job Job
	return job, json.Unmarshal(body, &job)
}

// JobsByState are the jobs whose state is one of states (a worker's recovery reads starting and running).
func (b *SQLBus) JobsByState(ctx context.Context, states ...string) ([]Job, error) {
	rows, err := b.db.QueryContext(ctx, `select body from bus_jobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, s := range states {
		want[s] = true
	}
	var jobs []Job
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var job Job
		if err := json.Unmarshal(body, &job); err != nil {
			return nil, err
		}
		if want[job.State] {
			jobs = append(jobs, job)
		}
	}
	return jobs, rows.Err()
}

// SetJobLastSeq moves the job's last_seq without an event: the worker's recovery reads it (К6).
func (b *SQLBus) SetJobLastSeq(ctx context.Context, id string, seq int64) error {
	var body []byte
	if err := b.db.QueryRowContext(ctx, b.bind(`select body from bus_jobs where id = ?`), id).Scan(&body); err != nil {
		return err
	}
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		return err
	}
	if job.LastSeq >= seq {
		return nil
	}
	job.LastSeq = seq
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx, b.bind(`update bus_jobs set body = ?, updated_at = current_timestamp where id = ?`), raw, id)
	return err
}

func (b *SQLBus) Subscribe(ctx context.Context, group string, from int64) (Subscription, error) {
	if _, err := b.db.ExecContext(ctx, b.bind(`insert into bus_positions (grp, seq) values (?, ?) on conflict (grp) do nothing`), group, from); err != nil {
		return nil, err
	}
	if _, err := b.db.ExecContext(ctx, b.bind(`update bus_positions set seq = ? where grp = ? and seq < ?`), from, group, from); err != nil {
		return nil, err
	}
	return &subscription{bus: b, group: group}, nil
}

// Leader: the named role is taken, or kept, until the ttl runs out; another owner takes it only expired.
func (b *SQLBus) Leader(ctx context.Context, name string, ttl time.Duration) (bool, error) {
	expires := time.Now().UTC().Add(ttl)
	if _, err := b.db.ExecContext(ctx, b.bind(`insert into bus_leaders (name, owner, expires_at) values (?, ?, ?)
		on conflict (name) do update set owner = excluded.owner, expires_at = excluded.expires_at
		where bus_leaders.expires_at < ? or bus_leaders.owner = excluded.owner`),
		name, b.owner, expires, time.Now().UTC()); err != nil {
		return false, err
	}
	var owner string
	if err := b.db.QueryRowContext(ctx, b.bind(`select owner from bus_leaders where name = ?`), name).Scan(&owner); err != nil {
		return false, err
	}
	return owner == b.owner, nil
}

type subscription struct {
	bus   *SQLBus
	group string
}

var errRaced = errors.New("the group moved meanwhile")

func (s *subscription) Next(ctx context.Context) (Message, error) {
	for {
		m, err := s.tryNext(ctx)
		if err == nil {
			return m, nil
		}
		if !errors.Is(err, errRaced) {
			return Message{}, err
		}
		select {
		case <-ctx.Done():
			return Message{}, ctx.Err()
		case <-time.After(s.bus.Poll):
		}
	}
}

// tryNext claims the group's next unclaimed event: the position row is read under a lock that skips a group
// another subscriber holds (PostgreSQL; SQLite's one writer serializes the same), the claim is a primary key —
// a race is a retry, never a duplicate.
func (s *subscription) tryNext(ctx context.Context) (Message, error) {
	tx, err := s.bus.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	rollback := func() { _ = tx.Rollback() }
	lock := ``
	if !s.bus.db.SQLite() {
		lock = ` for update skip locked`
	}
	var pos int64
	err = tx.QueryRowContext(ctx, s.bus.bind(`select seq from bus_positions where grp = ?`+lock), s.group).Scan(&pos)
	if errors.Is(err, sql.ErrNoRows) {
		rollback()
		return Message{}, errRaced // another subscriber holds the group right now
	}
	if err != nil {
		rollback()
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, s.bus.bind(`delete from bus_claims where grp = ? and expires_at < ?`), s.group, time.Now().UTC()); err != nil {
		rollback()
		return Message{}, err
	}
	var m Message
	err = tx.QueryRowContext(ctx, s.bus.bind(`select e.seq, e.kind, e.body, e.at from bus_events e
		where e.seq > ? and not exists (select 1 from bus_claims c where c.grp = ? and c.seq = e.seq)
		order by e.seq limit 1`), pos, s.group).Scan(&m.Seq, &m.Kind, &m.Body, &m.At)
	if errors.Is(err, sql.ErrNoRows) {
		rollback()
		return Message{}, errRaced // nothing published yet
	}
	if err != nil {
		rollback()
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, s.bus.bind(`insert into bus_claims (grp, seq, owner, expires_at) values (?, ?, ?, ?)`),
		s.group, m.Seq, s.bus.owner, time.Now().UTC().Add(s.bus.Lease)); err != nil {
		rollback()
		return Message{}, errRaced // claimed meanwhile: take the next look
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Ack confirms everything up to seq: the claims below fall, the position moves past them.
func (s *subscription) Ack(ctx context.Context, seq int64) error {
	tx, err := s.bus.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bus.bind(`delete from bus_claims where grp = ? and seq <= ?`), s.group, seq); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bus.bind(`update bus_positions set seq = ? where grp = ? and seq < ?`), seq, s.group, seq); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *subscription) Close() error { return nil }
