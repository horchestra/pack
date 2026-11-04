package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// store.provider: file — the passwords pack makes are kept as files of store.path, and the target gets them the
// way it gets them from OpenBao: the installation applies, the secret is a file, a read gives it back.
func TestSecretsInFiles(t *testing.T) {
	secrets := filepath.Join(t.TempDir(), "secrets")
	d := t.TempDir()
	write(t, d, "release.yaml", map[string]any{"pack": filepath.Join(examples, "openbao", "golden", "openbao")})
	write(t, d, "env.yaml", map[string]any{
		"target": "memory",
		"store":  map[string]any{"provider": "file", "path": secrets},
		"keys":   map[string]any{"postgres": map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "500m", "memory": "512Mi"}}}},
	})
	write(t, d, "values.yaml", map[string]any{"openbao": map[string]any{}, "postgres": map[string]any{"database": "bao"}})
	if out := pack(t, d, "apply"); !strings.Contains(out, "installed openbao") {
		t.Fatalf("apply:\n%s", out)
	}
	var found []string
	_ = filepath.Walk(secrets, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".json") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 1 {
		t.Fatalf("one secret kept, as a file: %v", found)
	}
	if fi, _ := os.Stat(found[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("the secret is the user's alone: %v", fi.Mode())
	}
	b, _ := os.ReadFile(found[0])
	fields := map[string]string{}
	if err := json.Unmarshal(b, &fields); err != nil || len(fields["password"]) == 0 {
		t.Fatalf("the password is kept: %s %v", b, err)
	}
	// again: the password is read from the file, not made anew — nothing changes
	if out := pack(t, d, "apply"); !strings.Contains(out, "nothing to do") {
		t.Errorf("again:\n%s", out)
	}
	if b2, _ := os.ReadFile(found[0]); string(b2) != string(b) {
		t.Errorf("the password was made anew: %s → %s", b, b2)
	}
}
