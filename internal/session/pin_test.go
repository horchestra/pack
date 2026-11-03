package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/saga"
)

const registryPack = "oci://registry.example.org/packs/openbao:2.1.0"

// the stand, with a pack in a registry (nothing is asked of it: only the lock is read)
func registryStand(t *testing.T) *Session {
	t.Helper()
	d := stand(t, both)
	if err := os.WriteFile(filepath.Join(root(d), "release.yaml"), []byte("pack: "+registryPack+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := release.Load(root(d), "")
	if err != nil {
		t.Fatal(err)
	}
	wd := workdir.New(rel, "")
	wd.Path = d.Path
	s, err := Open(context.Background(), wd, true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A pack in a registry is read by the digest of the lock; without the lock, or with the lock of another pack,
// the way out is said: `pack refresh`.
func TestPinnedByTheLock(t *testing.T) {
	s := registryStand(t)
	dir := s.Dir.Release.Dir

	if _, err := s.Pinned(registryPack); !errors.Is(err, oci.ErrNoLock) || !strings.Contains(err.Error(), "pack refresh") {
		t.Fatalf("no lock: %v", err)
	}
	const dgst = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := release.WriteLock(dir, &release.Lock{Pack: registryPack, Digest: dgst}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Pinned(registryPack)
	if err != nil || !strings.HasSuffix(got, "@"+dgst) {
		t.Fatalf("pinned = %q, %v", got, err)
	}
	if err := release.WriteLock(dir, &release.Lock{Pack: "oci://registry.example.org/packs/other:1", Digest: dgst}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pinned(registryPack); !errors.Is(err, oci.ErrNoLock) {
		t.Fatalf("the lock of another pack: %v", err)
	}
	// a reference that has its digest needs no lock
	named := "oci://registry.example.org/packs/openbao@" + dgst
	if got, err := s.Pinned(named); err != nil || got != named {
		t.Fatalf("by digest: %q, %v", got, err)
	}
	// and a directory is itself
	if got, err := s.Pinned("/some/dir"); err != nil || got != "/some/dir" {
		t.Fatalf("directory: %q, %v", got, err)
	}
}

// An installation that failed has its own state to continue from: what the target holds is not compared with the
// record until it is installed again.
func TestNoDriftOfAFailedInstallation(t *testing.T) {
	d := stand(t, both)
	s, _ := failApply(t, d, "openbao")
	c, _ := s.Prepare(false)
	_ = s.Apply(context.Background(), c)

	s2, _ := open(t, d)
	rec, _, rel, err := s2.Installed()
	if err != nil || rec.State != saga.Failed {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if drifts, err := s2.Drifts(context.Background(), rec, rel); err != nil || len(drifts) != 0 {
		t.Fatalf("drifts = %v, %v", drifts, err)
	}
}
