package storage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/horchestra/pack/pkg/saga"
)

func TestFileStore(t *testing.T) {
	s := FileStore{Dir: t.TempDir(), Name: "adh"}
	if r, err := s.Load(); r != nil || err != nil {
		t.Fatalf("empty: %v %v", r, err)
	}
	want := &saga.Record{State: saga.InProgress, Pack: "p", Version: "1", Values: []byte(`{"a":1}`),
		Saga: &saga.Progress{Done: []string{"zk"}, Running: []string{"nn"}}}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || got.State != saga.InProgress || !slices.Equal(got.Saga.Done, []string{"zk"}) || got.Pack != "p" {
		t.Fatalf("record = %+v, %v", got, err)
	}
	if st, _ := os.Stat(s.path()); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v: the record may name credentials", st.Mode())
	}
	if m, _ := filepath.Glob(filepath.Join(s.Dir, ".state-*")); len(m) != 0 {
		t.Fatalf("temporary files left: %v", m)
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
	if r, _ := s.Load(); r != nil {
		t.Fatal("deleted")
	}
}

func TestFileStoreLock(t *testing.T) {
	s := FileStore{Dir: t.TempDir(), Name: "adh"}
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock(); err == nil {
		t.Fatal("a second lock must be refused")
	}
	if _, err := (FileStore{Dir: s.Dir, Name: "other"}).Lock(); err != nil {
		t.Fatalf("another installation is free: %v", err)
	}
	unlock()
	unlock2, err := s.Lock()
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	unlock2()
	var _ saga.Locker = s
}

func TestFileJournal(t *testing.T) {
	f := FileJournal{Dir: t.TempDir(), Installation: "adh"}
	if runs, err := f.Runs(); err != nil || len(runs) != 0 {
		t.Fatalf("empty: %v %v", runs, err)
	}
	now := time.Now().UTC()
	for _, e := range []saga.Entry{
		{Time: now, Run: "20260101T000000.000Z", Kind: saga.KindSaga},
		{Time: now, Run: "20260101T000000.000Z", Kind: saga.KindLog, Node: "nn", Message: "applying"},
		{Time: now, Run: "20260102T000000.000Z", Kind: saga.KindSaga},
	} {
		if err := f.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := f.Runs()
	if err != nil || !slices.Equal(runs, []string{"20260101T000000.000Z", "20260102T000000.000Z"}) {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	got, err := f.Read(runs[0])
	if err != nil || len(got) != 2 || got[1].Node != "nn" || got[1].Message != "applying" {
		t.Fatalf("entries = %+v, %v", got, err)
	}
	var _ saga.Journal = f
	var _ saga.Store = FileStore{}
}

// The state names the installation and the journal what its targets said: both are the user's alone.
func TestFilesAreThePrivateOnes(t *testing.T) {
	dir := t.TempDir()
	j := FileJournal{Dir: filepath.Join(dir, "journal"), Installation: "adh"}
	if err := j.Append(saga.Entry{Run: "r1", Kind: saga.KindSaga}); err != nil {
		t.Fatal(err)
	}
	s := FileStore{Dir: filepath.Join(dir, "state"), Name: "state"}
	if err := s.Save(&saga.Record{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(dir, "journal"):                    0o700,
		filepath.Join(dir, "journal", "adh"):             0o700,
		filepath.Join(dir, "journal", "adh", "r1.jsonl"): 0o600,
		filepath.Join(dir, "state"):                      0o700,
		filepath.Join(dir, "state", "state.json"):        0o600,
	} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s: %o, want %o", path, got, want)
		}
	}
}

func TestJournalPrune(t *testing.T) {
	f := FileJournal{Dir: t.TempDir(), Installation: "i"}
	for _, r := range []string{"r1", "r2", "r3", "r4"} {
		if err := f.Append(saga.Entry{Run: r}); err != nil {
			t.Fatal(err)
		}
	}
	if gone, err := f.Prune(-1); err != nil || len(gone) != 0 {
		t.Fatalf("keep all: %v %v", gone, err)
	}
	gone, err := f.Prune(2)
	if err != nil || !slices.Equal(gone, []string{"r1", "r2"}) {
		t.Fatalf("pruned %v, %v", gone, err)
	}
	if runs, _ := f.Runs(); !slices.Equal(runs, []string{"r3", "r4"}) {
		t.Fatalf("left %v", runs)
	}
	if gone, _ := f.Prune(5); len(gone) != 0 {
		t.Fatalf("fewer than keep: %v", gone)
	}
}
