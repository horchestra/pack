package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// pack values schema --json of the example packs is the golden schema: the composition, x-pack-* kept,
// --lang ru with the Russian titles. UPDATE_GOLDEN=1 rewrites the golden files.
func TestValuesSchemaGolden(t *testing.T) {
	_, file, _, _ := runtime.Caller(0) // absolute: a run of a command chdirs the process away
	here := filepath.Dir(file)
	packs := map[string]string{
		"postgres": filepath.Join(here, "../examples/postgres/golden/postgres"),
		"openbao":  filepath.Join(here, "../examples/openbao/golden/openbao"), // holds postgres
		"docker":   filepath.Join(here, "../examples/docker/golden/docker"),
	}
	goldenDir := filepath.Join(here, "testdata", "schema")
	for _, lang := range []string{"", "ru"} {
		for name, ref := range packs {
			d := t.TempDir()
			write(t, d, "release.yaml", map[string]any{"pack": ref})
			write(t, d, "values.yaml", map[string]any{})
			write(t, d, "env.yaml", map[string]any{"target": "memory"})
			args := []string{"values", "schema"}
			if len(lang) > 0 {
				args = append(args, "--lang", lang)
			}
			out := pack(t, d, args...)
			schemaLang = "" // a flag keeps its value between runs in one process
			golden := filepath.Join(goldenDir, name+lang+".json")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%s (%s): no golden file %s (UPDATE_GOLDEN=1 writes it): %v", name, lang, golden, err)
			}
			if out != string(want) {
				t.Errorf("%s --lang %s: not the golden %s (UPDATE_GOLDEN=1 rewrites it)", name, lang, golden)
			}
		}
	}
}
