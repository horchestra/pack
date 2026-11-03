package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/pkg/target"
)

// pack discover asks the target the facts, keeps them in .pack/facts, and pack next says how old they are.
func TestDiscover(t *testing.T) {
	stand := filepath.Join(t.TempDir(), "stand.json")
	if err := os.WriteFile(stand, []byte(`{"hosts": {"db1": {"os": "debian", "os_version": "13", "cpus": 4, "memory_mb": 8192,
		"disks": [{"mount": "/var/lib", "size_mb": 100000, "free_mb": 52000}], "ports": [{"port": 5432, "proto": "tcp", "process": "postgres"}]},
		"db2": {"os": "debian", "memory_mb": 4096}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := releaseOf(t, generated(t), map[string]any{"facts": stand, "discover": map[string]any{"hosts": []string{"db1"}}})
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	discoverFixture = fixture
	t.Cleanup(func() { discoverFixture = "" })
	out := pack(t, d, "discover")
	if !strings.Contains(out, "default: 1 host(s)") || !strings.Contains(out, "db1, debian 13, 4 cpu, 8192 MB, /var/lib 52000/100000 MB free, 1 port(s) in use") {
		t.Fatalf("discover:\n%s", out)
	}
	inRelease(t, d)
	facts, _, err := session.LoadFacts(dir)
	if err != nil || facts["default"] == nil || len(facts["default"].Hosts) != 1 || facts["default"].At.IsZero() {
		t.Fatalf("the facts kept: %+v %v", facts, err)
	}
	var fx map[string]json.RawMessage
	if b, err := os.ReadFile(fixture); err != nil || json.Unmarshal(b, &fx) != nil || fx["default"] == nil {
		t.Errorf("the fixture: %s %v", b, err)
	}
	if n := whatNext(context.Background()); n.Facts["default"].IsZero() {
		t.Errorf("next says nothing of the facts: %+v", n)
	}
}

// What a target told once and tells no more goes; a file of facts that does not read stops nothing; the time a
// target did not say is pack's.
func TestFactsKeptHonest(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	inRelease(t, d)
	if err := session.SaveFacts(dir, "default", &target.Facts{}); err != nil {
		t.Fatal(err)
	}
	facts, _, err := session.LoadFacts(dir)
	if err != nil || facts["default"].At.IsZero() {
		t.Fatalf("not stamped: %+v %v", facts, err)
	}
	// the memory target of this release has no facts: setting: it tells none, and the old file goes
	pack(t, d, "discover")
	inRelease(t, d)
	if facts, _, _ := session.LoadFacts(dir); facts["default"] != nil {
		t.Errorf("stale facts kept: %+v", facts["default"])
	}
	if err := os.MkdirAll(dir.FactsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.FactsFile(dir, "default"), []byte(`{"hosts": {`), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, warnings, err := session.LoadFacts(dir)
	if err != nil || len(facts) != 0 || len(warnings) != 1 {
		t.Errorf("a broken file: %v %v %v", facts, warnings, err)
	}
	pack(t, d, "plan") // a command goes on without them
}

func TestTargetNamedDefault(t *testing.T) {
	d := releaseOf(t, generated(t), map[string]any{"targets": map[string]any{"default": map[string]any{"target": "memory"}}})
	inRelease(t, d)
	if _, err := open(context.Background(), true); err == nil || !strings.Contains(err.Error(), "targets.default") {
		t.Errorf("an entry named default: %v", err)
	}
}
