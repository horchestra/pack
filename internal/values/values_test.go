package values

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A set of values is split in files: a directory of them, keys without the `values:` wrapper, merged deeply.
func TestLoadValuesFromFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "common.yaml", "common:\n  realm: EXAMPLE\n")
	write(t, dir, "kafka.yaml", "kafka:\n  conf:\n    min.insync.replicas: 2\n")
	write(t, dir, "kafka-secrets.yaml", "kafka:\n  jaas:\n    password:\n      secretRef:\n        name: k\n        key: password\n")
	write(t, dir, "rollout.yaml", "rollout:\n  maxUnavailable: 1\n")
	write(t, dir, "notes.txt", "not values")
	write(t, dir, ".hidden.yaml", "x: 1")

	v, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	kafka := v.Values["kafka"].(map[string]any)
	if kafka["conf"] == nil || kafka["jaas"] == nil || v.Values["common"] == nil || len(v.Values) != 2 {
		t.Errorf("merged: %v", v.Values)
	}
	if v.Rollout == nil || v.Rollout.MaxUnavailable != 1 {
		t.Errorf("a whole document in the directory: %+v", v.Rollout)
	}

	// a whole document, and a file beside the directory
	doc := write(t, t.TempDir(), "values.yaml", "values:\n  zookeeper: {}\nskip:\n  - kafka\n")
	v, err = Load([]string{dir, doc})
	if err != nil || v.Values["zookeeper"] == nil || len(v.Skip) != 1 {
		t.Errorf("%v %v", err, v)
	}

	if v, err := Load(nil); err != nil || v.Values != nil {
		t.Errorf("no values: %v %v", v, err)
	}
}

func TestLoadValuesRefusals(t *testing.T) {
	a := t.TempDir()
	write(t, a, "a.yaml", "kafka:\n  conf:\n    x: 1\n")
	write(t, a, "b.yaml", "kafka:\n  conf:\n    x: 2\n")
	if _, err := Load([]string{a}); err == nil || !strings.Contains(err.Error(), "/kafka/conf/x") || !strings.Contains(err.Error(), "a.yaml") {
		t.Errorf("a value two files give differently: %v", err)
	}
	// an object in one file, a scalar over it in another: the message names both files
	obj := t.TempDir()
	write(t, obj, "a.yaml", "kafka:\n  conf:\n    x: 1\n")
	write(t, obj, "b.yaml", "kafka:\n  conf: none\n")
	if _, err := Load([]string{obj}); err == nil ||
		!strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") {
		t.Errorf("a scalar over an object: %v", err)
	}
	same := t.TempDir()
	write(t, same, "a.yaml", "kafka:\n  conf:\n    x: 1\n")
	write(t, same, "b.yaml", "kafka:\n  conf:\n    x: 1\n")
	if _, err := Load([]string{same}); err != nil {
		t.Errorf("the same twice is not a conflict: %v", err)
	}
	if _, err := Load([]string{t.TempDir()}); err == nil {
		t.Error("an empty directory")
	}
	if _, err := Load([]string{filepath.Join(a, "missing.yaml")}); err == nil {
		t.Error("a missing file")
	}
	bad := write(t, t.TempDir(), "bad.yaml", "kafka: [unclosed")
	if _, err := Load([]string{bad}); err == nil {
		t.Error("a broken file")
	}
}
