package saga

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/horchestra/pack/pkg/graph"
)

// stand: zk is a savepoint, then nn, then dn (a savepoint too), and the stand itself.
func stand() []graph.Spec {
	return []graph.Spec{
		{Name: "zk", Savepoint: true}, {Name: "nn", Deps: []string{"zk"}}, {Name: "dn", Deps: []string{"nn", "zk"}, Savepoint: true},
		{Name: "stand", Deps: []string{"zk", "nn", "dn"}},
	}
}

type exec struct {
	mu   sync.Mutex
	log  []string
	fail map[string]error // apply of the node fails
}

func (e *exec) Apply(_ context.Context, n *graph.Node) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, "apply "+n.Name)
	return e.fail[n.Name]
}

func (e *exec) Undo(_ context.Context, n *graph.Node) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, "undo "+n.Name)
	return nil
}

type store struct {
	rec     *Record
	deleted bool
}

func (s *store) Load() (*Record, error) {
	if s.rec == nil {
		return nil, nil
	}
	c := *s.rec
	if s.rec.Saga != nil {
		sg := *s.rec.Saga
		sg.Done, sg.Running = slices.Clone(sg.Done), slices.Clone(sg.Running)
		c.Saga = &sg
	}
	return &c, nil
}
func (s *store) Save(r *Record) error {
	c := *r
	if r.Saga != nil {
		sg := *r.Saga
		sg.Done, sg.Running = slices.Clone(sg.Done), slices.Clone(sg.Running)
		c.Saga = &sg
	}
	s.rec = &c
	return nil
}
func (s *store) Delete() error { s.rec, s.deleted = nil, true; return nil }

func run(t *testing.T, e *exec, s Store) *Run {
	t.Helper()
	g, err := graph.New(stand())
	if err != nil {
		t.Fatal(err)
	}
	return &Run{Graph: g, Exec: e, Store: s, Jobs: 1, To: Installation{Pack: "p", Version: "1", Values: []byte(`{"a":1}`)}}
}

func TestInstall(t *testing.T) {
	e, s := &exec{}, &store{}
	if err := run(t, e, s).Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"apply zk", "apply nn", "apply dn", "apply stand"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
	if s.rec.State != Installed || s.rec.Saga != nil || s.rec.Version != "1" {
		t.Fatalf("record = %+v", s.rec)
	}
}

// A failure rolls back to the last savepoint: zk is committed and stays, nn goes.
func TestRollbackStopsAtSavepoint(t *testing.T) {
	e, s := &exec{fail: map[string]error{"dn": errors.New("boom")}}, &store{}
	err := run(t, e, s).Execute(context.Background())
	if err == nil {
		t.Fatal("must fail")
	}
	if want := []string{"apply zk", "apply nn", "apply dn", "undo dn", "undo nn"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
	// zk is a savepoint, but nn is not committed by it: dn's own savepoint never got done
	sg := s.rec.Saga
	if s.rec.State != Failed || !slices.Equal(sg.Done, []string{"zk"}) || len(sg.Running) != 0 {
		t.Fatalf("record = %+v / %+v", s.rec, sg)
	}
}

// The same command continues the saga from where the savepoint left it.
func TestContinueAfterFailure(t *testing.T) {
	e, s := &exec{fail: map[string]error{"dn": errors.New("boom")}}, &store{}
	_ = run(t, e, s).Execute(context.Background())
	e.log, e.fail = nil, nil
	if err := run(t, e, s).Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"apply nn", "apply dn", "apply stand"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
	if s.rec.State != Installed {
		t.Fatalf("record = %+v", s.rec)
	}
}

// A saga cut short in the middle of a node undoes that node before running it again.
func TestResumeUndoesCutShort(t *testing.T) {
	s := &store{rec: &Record{State: InProgress, Saga: &Progress{
		Pack: "p", Version: "1", Values: []byte(`{ "a": 1 }`),
		Done: []string{"zk"}, Running: []string{"nn"},
	}}}
	e := &exec{}
	if err := run(t, e, s).Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"undo nn", "apply nn", "apply dn", "apply stand"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
}

func TestOtherSagaIsRefused(t *testing.T) {
	s := &store{rec: &Record{State: Failed, Saga: &Progress{Pack: "p", Version: "0"}}}
	if err := run(t, &exec{}, s).Execute(context.Background()); err == nil {
		t.Fatal("an unfinished saga towards another version must be refused")
	}
}

func TestUpgradeKeepsPrevious(t *testing.T) {
	s := &store{rec: &Record{State: Installed, Pack: "p", Version: "0"}}
	if err := run(t, &exec{}, s).Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.rec.Previous == nil || s.rec.Previous.Version != "0" || s.rec.Version != "1" {
		t.Fatalf("record = %+v", s.rec)
	}
}

func TestRemove(t *testing.T) {
	e, s := &exec{}, &store{rec: &Record{State: Installed}}
	if err := run(t, e, s).Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"undo stand", "undo dn", "undo nn", "undo zk"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
	if !s.deleted {
		t.Fatal("the record goes with the installation")
	}
}

// logExec logs from inside a step, the way an executor keeps what the system answered.
type logExec struct{ exec }

func (e *logExec) Apply(ctx context.Context, n *graph.Node) error {
	Logf(ctx, "applying %s", n.Name)
	return e.exec.Apply(ctx, n)
}

func kinds(m *Memory, node string) []Kind {
	var out []Kind
	for _, e := range m.Entries {
		if e.Node == node {
			out = append(out, e.Kind)
		}
	}
	return out
}

func TestJournalOfASuccessfulSaga(t *testing.T) {
	s, j := &store{}, &Memory{}
	r := run(t, &exec{}, s)
	r.Exec, r.Journal = &logExec{}, j
	if err := r.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j, "zk"); !slices.Equal(got, []Kind{KindStart, KindLog, KindDone}) {
		t.Fatalf("zk = %v", got)
	}
	first, last := j.Entries[0], j.Entries[len(j.Entries)-1]
	if first.Kind != KindSaga || last.Kind != KindEnd {
		t.Fatalf("first = %v, last = %v", first, last)
	}
	for _, e := range j.Entries {
		if len(e.Run) == 0 || e.Run != first.Run || e.Time.IsZero() {
			t.Fatalf("entry %+v: every line carries the saga's id and a time", e)
		}
	}
	var logged bool
	for _, e := range j.Entries {
		logged = logged || (e.Kind == KindLog && e.Node == "nn" && e.Message == "applying nn")
	}
	if !logged {
		t.Fatal("the executor's log line is kept with its node")
	}
}

func TestJournalOfAFailureAndItsRollback(t *testing.T) {
	s, j := &store{}, &Memory{}
	r := run(t, &exec{fail: map[string]error{"dn": errors.New("boom")}}, s)
	r.Journal = j
	_ = r.Execute(context.Background())
	if got := kinds(j, "dn"); !slices.Equal(got, []Kind{KindStart, KindFail, KindUndo, KindUndone}) {
		t.Fatalf("dn = %v", got)
	}
	if got := kinds(j, "nn"); !slices.Equal(got, []Kind{KindStart, KindDone, KindUndo, KindUndone}) {
		t.Fatalf("nn = %v", got)
	}
	if got := kinds(j, "zk"); !slices.Equal(got, []Kind{KindStart, KindDone}) {
		t.Fatalf("zk stays: %v", got)
	}
	var failed bool
	for _, e := range j.Entries {
		failed = failed || (e.Kind == KindFail && e.Node == "dn" && e.Error == "boom")
	}
	if !failed {
		t.Fatal("the failure is kept with its error")
	}
}

// A continued saga appends to the same journal under the same id.
func TestJournalSurvivesAContinuation(t *testing.T) {
	s, j := &store{}, &Memory{}
	r := run(t, &exec{fail: map[string]error{"dn": errors.New("boom")}}, s)
	r.Journal = j
	_ = r.Execute(context.Background())
	id := j.Entries[0].Run
	r = run(t, &exec{}, s)
	r.Journal = j
	if err := r.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, e := range j.Entries {
		if e.Run != id {
			t.Fatalf("entry %+v is of another saga", e)
		}
	}
}

// Memory is a Journal in memory: the test's double.
type Memory struct{ Entries []Entry }

func (m *Memory) Append(e Entry) error { m.Entries = append(m.Entries, e); return nil }

type brokenJournal struct{ n int }

func (b *brokenJournal) Append(Entry) error { b.n++; return errors.New("disk full") }

func TestBrokenJournalDoesNotFailTheSaga(t *testing.T) {
	s, j := &store{}, &brokenJournal{}
	r := run(t, &exec{}, s)
	r.Journal = j
	if err := r.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j.n == 0 || s.rec.State != Installed {
		t.Fatalf("appends = %d, record = %+v", j.n, s.rec)
	}
}

func TestRemoveIsJournaled(t *testing.T) {
	j := &Memory{}
	r := run(t, &exec{}, &store{rec: &Record{State: Installed}})
	r.Journal = j
	if err := r.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := j.Entries[len(j.Entries)-1]; last.Kind != KindEnd || last.Message != "removed" {
		t.Fatalf("last = %+v", last)
	}
}

// An unfinished saga is taken back whole, savepoints or not.
func TestUndoTakesBackWholeSaga(t *testing.T) {
	e, s, j := &exec{fail: map[string]error{"dn": errors.New("boom")}}, &store{}, &Memory{}
	r := run(t, e, s)
	r.NoRollback = true // nn and zk stay in effect, dn failed under way
	r.Journal = j
	if err := r.Execute(context.Background()); err == nil {
		t.Fatal("must fail")
	}
	e.log = nil
	r = run(t, e, s)
	r.Journal = j
	if err := r.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"undo dn", "undo nn", "undo zk"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
	// a first installation taken back leaves nothing installed, and the revision that says so
	if s.rec == nil || len(s.rec.Pack) > 0 || s.rec.Saga != nil || len(s.rec.History) != 1 || s.rec.History[0].Outcome != RevUndone ||
		!strings.Contains(s.rec.History[0].Error, "boom") {
		t.Fatalf("record = %+v", s.rec)
	}
	if last := j.Entries[len(j.Entries)-1]; last.Kind != KindEnd {
		t.Fatalf("last = %+v", last)
	}
}

// An upgrade taken back returns the record to the installed version.
func TestUndoKeepsTheInstalledVersion(t *testing.T) {
	s := &store{rec: &Record{State: Installed, Pack: "p", Version: "0"}}
	e := &exec{fail: map[string]error{"nn": errors.New("boom")}}
	r := run(t, e, s)
	r.NoRollback = true
	_ = r.Execute(context.Background())
	if err := run(t, &exec{}, s).Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.rec == nil || s.rec.State != Installed || s.rec.Version != "0" || s.rec.Saga != nil {
		t.Fatalf("record = %+v", s.rec)
	}
}

func TestUndoNeedsAnUnfinishedSaga(t *testing.T) {
	if err := run(t, &exec{}, &store{rec: &Record{State: Installed}}).Undo(context.Background()); err == nil {
		t.Fatal("nothing to undo must be an error")
	}
	if err := run(t, &exec{}, &store{}).Undo(context.Background()); err == nil {
		t.Fatal("no record at all must be an error")
	}
}

// A node the record has in effect and the pack lacks is a record that does not match.
func TestUndoRefusesAMismatchedRecord(t *testing.T) {
	s := &store{rec: &Record{State: Failed, Saga: &Progress{Done: []string{"ghost"}}}}
	if err := run(t, &exec{}, s).Undo(context.Background()); err == nil {
		t.Fatal("must refuse")
	}
}

func TestRecordTakesTheInstallationID(t *testing.T) {
	s := &store{}
	r := run(t, &exec{}, s)
	r.ID = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	if err := r.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.rec.ID != r.ID {
		t.Fatalf("record = %+v", s.rec)
	}
	// the same installation continues, another one is refused
	if err := r.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	other := run(t, &exec{}, s)
	other.ID = "11111111-2222-4333-8444-555555555555"
	if err := other.Execute(context.Background()); err == nil {
		t.Fatal("a record of another installation must be refused")
	}
}

// A kept node is left as it is by the rollback of a failure; the explicit Undo takes it back all the same.
func TestKeepLeavesANodeOnRollback(t *testing.T) {
	e, s := &exec{fail: map[string]error{"dn": errors.New("boom")}}, &store{}
	r := run(t, e, s)
	r.Keep = []string{"nn"}
	_ = r.Execute(context.Background())
	if want := []string{"apply zk", "apply nn", "apply dn", "undo dn"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
	if !slices.Equal(s.rec.Saga.Done, []string{"nn", "zk"}) && !slices.Equal(s.rec.Saga.Done, []string{"zk", "nn"}) {
		t.Fatalf("done = %v", s.rec.Saga.Done)
	}
	e.log = nil
	if err := run(t, e, s).Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"undo nn", "undo zk"}; !slices.Equal(e.log, want) {
		t.Fatalf("Undo ignores Keep: %v", e.log)
	}
}

// A savepoint node left in the record by a saga is not in the graph of an undo built without the plan.
func TestUndoIgnoresSavepointNodes(t *testing.T) {
	s := &store{rec: &Record{State: Failed, Saga: &Progress{Done: []string{"zk", "savepoint:end"}}}}
	e := &exec{}
	if err := run(t, e, s).Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"undo zk"}; !slices.Equal(e.log, want) {
		t.Fatalf("log = %v", e.log)
	}
}

// A rollback over independent branches with several jobs: the record is read and
// written from as many goroutines — run with -race.
func TestRollbackInParallel(t *testing.T) {
	specs := []graph.Spec{{Name: "top", Deps: []string{"a", "b", "c", "d"}}}
	for _, n := range []string{"a", "b", "c", "d"} {
		specs = append(specs, graph.Spec{Name: n})
	}
	g, err := graph.New(specs)
	if err != nil {
		t.Fatal(err)
	}
	e, s := &exec{fail: map[string]error{"top": errors.New("boom")}}, &store{}
	r := &Run{Graph: g, Exec: e, Store: s, Jobs: 4, To: Installation{Pack: "p", Version: "1"}}
	if err := r.Execute(context.Background()); err == nil {
		t.Fatal("must fail")
	}
	if s.rec.State != Failed {
		t.Fatalf("record = %+v", s.rec)
	}
	if len(s.rec.Saga.Done) != 0 || len(s.rec.Saga.Running) != 0 {
		t.Fatalf("everything rolled back expected, record = %+v", s.rec.Saga)
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		if !slices.Contains(e.log, "undo "+n) {
			t.Fatalf("no undo of %s in %v", n, e.log)
		}
	}
}

func TestHistory(t *testing.T) {
	rec := &Record{}
	for i := range 5 {
		rec.revise(Revision{Pack: "p", Version: fmt.Sprint(i), Outcome: RevInstalled}, 3)
	}
	if len(rec.History) != 3 || rec.History[0].N != 3 || rec.History[2].N != 5 || rec.Revision(4).Version != "3" || rec.Revision(1) != nil {
		t.Fatalf("history = %+v", rec.History)
	}
	for range DefaultHistoryKeep + 5 {
		rec.revise(Revision{Outcome: RevInstalled}, 0)
	}
	if len(rec.History) != DefaultHistoryKeep {
		t.Fatalf("keep 0 is the default: %d", len(rec.History))
	}
	rec.History = rec.History[:3]
	rec.revise(Revision{Outcome: RevRemoved}, -1)
	if len(rec.History) != 4 || rec.History[3].At.IsZero() || rec.History[3].N != rec.History[2].N+1 {
		t.Fatalf("keep all: %+v", rec.History)
	}
}

func TestCurrent(t *testing.T) {
	a := Installation{Pack: "p", Version: "1", Values: []byte(`{"a":1}`)}
	b := Installation{Pack: "p", Version: "1", Values: []byte(`{"a":2}`)}
	rec := &Record{State: Installed, Pack: "p", Version: "1", Values: []byte("{\n  \"a\": 1\n}")}
	rec.revise(Revision{Installation: a, Outcome: RevInstalled}, 0)
	rec.revise(Revision{Installation: b, Outcome: RevInstalled}, 0)
	rec.revise(Revision{Installation: a, Outcome: RevUndone}, 0)
	if c := rec.Current(); c == nil || c.N != 1 {
		t.Fatalf("current = %+v: the newest installed revision of what is installed, however its values were written", c)
	}
	rec.State = InProgress
	if rec.Current() != nil {
		t.Error("a saga under way: none is current")
	}
}
