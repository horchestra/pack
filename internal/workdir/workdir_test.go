package workdir

import (
	"os"
	"path/filepath"
	"testing"
	"uuid"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/pkg/saga"
)

func newDir(t *testing.T) *Dir {
	t.Helper()
	return &Dir{Release: &release.Release{Dir: "."}, Path: filepath.Join(t.TempDir(), ".pack")}
}

func TestLayout(t *testing.T) {
	d := newDir(t)
	d.Ensure()
	d.Ensure() // twice: nothing breaks
	if st, err := os.Stat(d.Path); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf(".pack is the user's alone: %v %v", st, err)
	}
	if b, err := os.ReadFile(filepath.Join(d.Path, ".gitignore")); err != nil || string(b) != "*\n" {
		t.Fatalf(".gitignore = %q, %v", b, err)
	}
	cache := t.TempDir()
	t.Setenv("PACK_CACHE", cache)
	if d.JournalPath() != filepath.Join(d.Path, "journal") || d.PacksPath() != filepath.Join(cache, "packs") ||
		d.BinPath() != filepath.Join(cache, "bin") {
		t.Fatalf("journal %s, packs %s, bin %s", d.JournalPath(), d.PacksPath(), d.BinPath())
	}
	d.Journal = "/elsewhere"
	if d.JournalPath() != "/elsewhere" {
		t.Fatal("PACK_JOURNAL moves the journals only")
	}
	d.Journal = "off"
	if d.JournalFor("id") != nil {
		t.Fatal("off keeps no journal")
	}
}

func TestStateIsAFileInThePackDir(t *testing.T) {
	d := newDir(t)
	if fs := d.FileState(); fs.Dir != d.Path || fs.Name != "state" {
		t.Fatalf("state = %#v", fs)
	}
	d.Env = "prod"
	if d.FileState().Name != "state.prod" {
		t.Fatal("--env prod has a state of its own")
	}
	d.Release.State = StateTarget
	if _, err := d.Store(nil); err == nil {
		t.Fatal("state target without a target that keeps records must fail")
	}
	d.Release.State = "nonsense"
	if _, err := d.Store(nil); err == nil {
		t.Fatal("an unknown state kind must fail")
	}
}

func TestInstallationIDIsAUUIDKeptInTheState(t *testing.T) {
	d := newDir(t)
	first, err := d.InstallationID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first); err != nil || first[14] != '4' {
		t.Fatalf("%q is not a UUID v4: %v", first, err)
	}
	// nothing is kept until an apply writes the record: a plan sees a fresh id each time
	if again, _ := d.InstallationID(); again == first {
		t.Fatal("no record yet: the id is not kept")
	}
	if err := d.FileState().Save(&saga.Record{ID: first}); err != nil {
		t.Fatal(err)
	}
	if again, _ := d.InstallationID(); again != first {
		t.Fatalf("the record's id is the installation's: %s != %s", again, first)
	}

	d.Release.State, d.Release.ID = StateTarget, ""
	if _, err := d.InstallationID(); err == nil {
		t.Fatal("state: target needs an id in release.yaml")
	}
	d.Release.ID = "not-a-uuid"
	if _, err := d.InstallationID(); err == nil {
		t.Fatal("the id must be a UUID")
	}
	d.Release.ID = first
	if id, err := d.InstallationID(); err != nil || id != first {
		t.Fatalf("id = %q, %v", id, err)
	}
}

// state: observe keeps the record in the run's memory alone and takes the installation's id from the release.
func TestObserveState(t *testing.T) {
	d := newDir(t)
	d.Release.State = StateObserve
	st, err := d.Store(nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := st.Load(); rec != nil {
		t.Fatal("a fresh observe store holds a record")
	}
	if err := st.Save(&saga.Record{ID: "id-1"}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := st.Load(); rec == nil || rec.ID != "id-1" {
		t.Fatal("the record did not live for the run")
	}
	if err := st.Delete(); err != nil {
		t.Fatal(err)
	}
	if rec, _ := st.Load(); rec != nil {
		t.Fatal("deleted, and still there")
	}
	if _, err := d.InstallationID(); err == nil {
		t.Fatal("observe without id: the run's memory has nothing to take the id from")
	}
	d.Release.ID = NewID()
	if id, err := d.InstallationID(); err != nil || id != d.Release.ID {
		t.Fatalf("id = %q, %v", id, err)
	}
}
