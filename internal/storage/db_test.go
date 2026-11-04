package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

const idA = "11111111-1111-4111-8111-111111111111"
const idB = "22222222-2222-4222-8222-222222222222"

// sqliteDB is a state database on SQLite in a directory of the test's: the same code as PostgreSQL's, no server.
func sqliteDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The record: none, then written, then read back whole; a save from a stale read is refused (optimistic locking).
func TestDBStore(t *testing.T) {
	db := sqliteDB(t)
	s, err := db.Store(idA)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := s.Load(); err != nil || rec != nil {
		t.Fatalf("empty: %v %v", rec, err)
	}
	rec := &saga.Record{ID: idA, Pack: "openbao 2.1.0", Rev: "abc123"}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	again, err := db.Store(idA)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Load()
	if err != nil || got == nil || got.Pack != rec.Pack || got.Rev != "abc123" {
		t.Fatalf("read back: %+v, %v", got, err)
	}
	// both hold the row now: the stale one's save is refused
	if err := s.Save(&saga.Record{ID: idA, Pack: "openbao 2.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := again.Save(&saga.Record{ID: idA, Pack: "openbao 2.3.0"}); err == nil {
		t.Fatal("a save from a stale read wrote")
	}
	// another installation has a record of its own
	other, err := db.Store(idB)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := other.Load(); err != nil || rec != nil {
		t.Fatalf("another installation: %v %v", rec, err)
	}
}

// The saga lock: taken once, refused to the next pack, free when it is let go.
func TestDBLock(t *testing.T) {
	db := sqliteDB(t)
	s1, _ := db.Store(idA)
	s2, _ := db.Store(idA)
	unlock, err := s1.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Lock(); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("taken: %v", err)
	}
	unlock()
	unlock2, err := s2.Lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
	var _ saga.Locker = s1
}

// The journal: appended, listed oldest first, read back in order, pruned to the newest.
func TestDBJournal(t *testing.T) {
	db := sqliteDB(t)
	j, err := db.JournalOf(idA)
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := j.Runs(); err != nil || len(runs) != 0 {
		t.Fatalf("empty: %v %v", runs, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, r := range []string{"r1", "r2", "r3"} {
		if err := j.Append(saga.Entry{Run: r, Kind: saga.KindSaga, Time: now}); err != nil {
			t.Fatal(err)
		}
		if err := j.Append(saga.Entry{Run: r, Kind: saga.KindLog, Node: "postgres", Message: "line of " + r, Time: now}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := j.Runs()
	if err != nil || len(runs) != 3 || runs[0] != "r1" || runs[2] != "r3" {
		t.Fatalf("runs: %v %v", runs, err)
	}
	entries, err := j.Read("r2")
	if err != nil || len(entries) != 2 || entries[1].Message != "line of r2" || entries[1].Node != "postgres" {
		t.Fatalf("read: %v %v", entries, err)
	}
	gone, err := j.Prune(2)
	if err != nil || len(gone) != 1 || gone[0] != "r1" {
		t.Fatalf("prune: %v %v", gone, err)
	}
	if entries, _ := j.Read("r1"); len(entries) != 0 {
		t.Fatalf("pruned and read: %v", entries)
	}
}

// The facts: told, read back by the target's name, forgotten.
func TestDBFacts(t *testing.T) {
	db := sqliteDB(t)
	f, err := db.FactsOf(idA)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Save("docker", &target.Facts{Hosts: map[string]target.HostFacts{"h1": {MemoryMB: 2048}}}); err != nil {
		t.Fatal(err)
	}
	facts, err := f.Load()
	if err != nil || facts["docker"].Hosts["h1"].MemoryMB != 2048 {
		t.Fatalf("load: %v %v", facts, err)
	}
	if err := f.Forget("docker"); err != nil {
		t.Fatal(err)
	}
	if facts, _ := f.Load(); len(facts) != 0 {
		t.Fatalf("forgotten: %v", facts)
	}
}
