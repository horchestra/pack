// Package testpacks gives the core's tests the packs an author writes, generated: openbao, which holds tembo —
// the sdk's test packs (sdk/internal/testpacks), as the sdk writes them (sdk/internal/testdata/golden, which its
// golden test keeps honest). The core reads packs; it never needs their Go types.
package testpacks

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/horchestra/pack/sdk/packschema"
)

// golden is where the sdk wrote the test packs.
func golden() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "sdk", "internal", "testdata", "golden")
}

// Generated copies the generated test packs into dir — openbao/ and tembo/, as sdk.Generate(dir, &openbao.OpenBao{})
// writes them — and returns the directory of openbao, the pack that holds tembo.
func Generated(t testing.TB, dir string) string {
	t.Helper()
	src := golden()
	err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dir, rel)
		if e.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "openbao")
}

// Next copies the generated test packs into dir as the next version of openbao: version and image 2.2.0, and the
// hooks given in its root — what an upgrade is tested with. It returns the directory of openbao.
func Next(t testing.TB, dir string, hooks []packschema.Hook) string {
	t.Helper()
	bao := Generated(t, dir)
	pf := filepath.Join(bao, "pack.yaml")
	b, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "2.1.0") {
		t.Fatalf("%s says no 2.1.0:\n%s", pf, b)
	}
	if err := os.WriteFile(pf, []byte(strings.ReplaceAll(string(b), "2.1.0", "2.2.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	sf := filepath.Join(bao, "schema.json")
	raw, err := os.ReadFile(sf)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	root, _ := s["x-pack"].(map[string]any)
	hb, err := json.Marshal(hooks)
	if err != nil {
		t.Fatal(err)
	}
	var hs any
	_ = json.Unmarshal(hb, &hs)
	root["hooks"] = hs
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sf, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return bao
}
