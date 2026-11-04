package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/storage"
	"github.com/horchestra/pack/internal/workdir"
)

// state migrate moves the record and the journal from the file to the database and back, the history whole.
func TestStateMigrateFileDBFile(t *testing.T) {
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "state.db")
	t.Setenv("PACK_STATE_DSN", dsn)
	d := releaseOf(t, generated(t), nil)
	pack(t, d, "apply")

	rel, err := release.Load(d, "")
	if err != nil {
		t.Fatal(err)
	}
	wd := workdir.New(rel, "")
	rec0, err := wd.FileState().Load()
	if err != nil || rec0 == nil {
		t.Fatalf("the file record: %v %v", rec0, err)
	}
	j0 := storage.FileJournal{Dir: wd.JournalPath(), Installation: rec0.ID}
	runs0, err := j0.Runs()
	if err != nil || len(runs0) == 0 {
		t.Fatalf("the file journal: %v %v", runs0, err)
	}

	// to the database
	if err := migrateState(wd, workdir.StateFile, workdir.StateDB); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenDB(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	st, err := db.Store(rec0.ID)
	if err != nil {
		t.Fatal(err)
	}
	recDB, err := st.Load()
	if err != nil || recDB == nil || recDB.Pack != rec0.Pack {
		t.Fatalf("the record in the database: %+v, %v", recDB, err)
	}
	jDB, err := db.JournalOf(rec0.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs, _ := jDB.Runs(); len(runs) != len(runs0) {
		t.Fatalf("the journal in the database: %v, the file's was %v", runs, runs0)
	}
	// again: what is there is not written over
	if err := migrateState(wd, workdir.StateFile, workdir.StateDB); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("the database holds a record: %v", err)
	}

	// back to the file, the file copies removed as migrate's output says
	_ = os.Remove(filepath.Join(d, ".pack", "state.json"))
	_ = os.RemoveAll(filepath.Join(d, ".pack", "journal"))
	write(t, d, "release.yaml", map[string]any{"pack": rel.Pack, "state": "db", "id": rec0.ID})
	rel2, err := release.Load(d, "")
	if err != nil {
		t.Fatal(err)
	}
	wd2 := workdir.New(rel2, "")
	if from, to, err := migrateEnds(wd2, ""); err != nil || from != workdir.StateDB || to != workdir.StateFile {
		t.Fatalf("the default way: %s → %s, %v", from, to, err)
	}
	if err := migrateState(wd2, workdir.StateDB, workdir.StateFile); err != nil {
		t.Fatal(err)
	}
	back, err := wd2.FileState().Load()
	if err != nil || back == nil || back.Pack != rec0.Pack || back.ID != rec0.ID {
		t.Fatalf("the record back in the file: %+v, %v", back, err)
	}
	j1 := storage.FileJournal{Dir: wd2.JournalPath(), Installation: rec0.ID}
	runs1, _ := j1.Runs()
	if len(runs1) != len(runs0) {
		t.Fatalf("the journal back in the file: %v, was %v", runs1, runs0)
	}
	entriesDB, _ := jDB.Read(runs0[len(runs0)-1])
	entries1, _ := j1.Read(runs1[len(runs1)-1])
	if len(entries1) != len(entriesDB) || len(entries1) == 0 {
		t.Fatalf("the last saga: back in the file %d, in the database %d", len(entries1), len(entriesDB))
	}
}

// Stateless: two directories of the same release, one state database — the second apply sees what the first
// installed and does nothing.
func TestStatelessTwoDirectories(t *testing.T) {
	t.Setenv("PACK_STATE_DSN", "sqlite://"+filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("PACK_CACHE", t.TempDir())
	t.Setenv("PACK_REVISION", "test-rev")
	id := "33333333-3333-4333-8333-333333333333"
	pk := generated(t)
	mk := func() string {
		d := t.TempDir()
		write(t, d, "release.yaml", map[string]any{"pack": pk, "state": "db", "id": id})
		write(t, d, "values.yaml", map[string]any{
			"openbao":  map[string]any{"api_addr": "https://bao:8200"},
			"postgres": map[string]any{"id": "n1"},
		})
		write(t, d, "env.yaml", map[string]any{
			"target": "memory",
			"keys":   map[string]any{"postgres": map[string]any{"resources": map[string]any{"requests": map[string]any{"memory": "2Gi"}}}},
		})
		return d
	}
	d1, d2 := mk(), mk()
	out := pack(t, d1, "apply")
	if !strings.Contains(out, "installed openbao") {
		t.Fatalf("the first applies:\n%s", out)
	}
	out = pack(t, d2, "apply")
	if !strings.Contains(out, "nothing to do") {
		t.Fatalf("the second, of another directory, sees the installed:\n%s", out)
	}
	// idempotent: again, the same nothing to do
	if again := pack(t, d1, "apply"); again != out {
		t.Fatalf("again:\n%s\nwas:\n%s", again, out)
	}
}

// --approve names a plan of the release's revision: the release moved on, the digest is another's — refused.
func TestApproveIsOfTheRevision(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	t.Setenv("PACK_REVISION", "rev-a")
	digest := digestOf(t, pack(t, d, "plan"))
	t.Setenv("PACK_REVISION", "rev-b")
	inRelease(t, d)
	s, err := open(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Prepare(false)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if c.Plan.Revision != "rev-b" {
		t.Fatalf("the plan's revision: %q", c.Plan.Revision)
	}
	if c.Plan.Digest == digest {
		t.Fatal("the revision is the digest's: another revision, another digest")
	}
	if err := approved(s, c.Plan, digest, howApply); err == nil || !strings.Contains(err.Error(), "moved on") {
		t.Fatalf("a digest of another revision: %v", err)
	}
}
