package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	packdocs "github.com/horchestra/pack/docs"
	packexamples "github.com/horchestra/pack/examples"
	"github.com/horchestra/pack/internal/author"
)

// ref is the part of the guide a topic is, with the code of the language asked for alone.
func TestAuthorRef(t *testing.T) {
	guide := "# G\n\n## The struct\n\ntext\n\n```go\ngo code\n```\n\n```python\npy code\n```\n\nA **suggestion** offers.\n\n```yaml\nx: 1\n```\n\nA **diagnosis** says.\n\n## Files\n\nfiles\n"
	got, err := refOf(guide, "struct", "python")
	if err != nil || !strings.Contains(got, "py code") || strings.Contains(got, "go code") || strings.Contains(got, "files") {
		t.Errorf("struct in python:\n%s %v", got, err)
	}
	got, err = refOf(guide, "suggest", "go")
	if err != nil || !strings.Contains(got, "x: 1") || strings.Contains(got, "diagnosis") || strings.Contains(got, "go code") {
		t.Errorf("suggest:\n%s %v", got, err)
	}
	if _, err := refOf(guide, "nope", "go"); err == nil || !strings.Contains(err.Error(), "one of") {
		t.Errorf("a topic nobody knows: %v", err)
	}
	for topic := range refTopics { // every topic is in the guide pack carries
		if text, err := refOf(packdocs.Authoring, topic, "go"); err != nil || len(text) < 40 {
			t.Errorf("%s: %q %v", topic, text, err)
		}
	}
}

// cel works an expression out as a formula of the key would.
func TestAuthorCel(t *testing.T) {
	packDir := generated(t)
	t.Cleanup(func() { authorPack, authorKey, authorValues, authorFacts, authorEnv = "", "", "", "", "" })
	d := t.TempDir()
	write(t, d, "values.yaml", map[string]any{"postgres": map[string]any{"id": "n7"}})
	out := pack(t, d, "author", "cel", "--pack", packDir, "--key", "postgres", "--values", filepath.Join(d, "values.yaml"),
		"values.pg_port + 1")
	if strings.TrimSpace(out) != "5434" {
		t.Errorf("values.pg_port + 1: %q", out)
	}
}

// check generates the pack from its source and says what to fix, where in the source: a formula that does not
// compile at the field's line, a source that does not run with what the generator said.
func TestAuthorCheck(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	t.Cleanup(func() { authorLang, authorTests = "", "" })
	src := t.TempDir()
	orig, err := os.ReadFile(filepath.Join(examples, "postgres", "python", "postgres.py"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(src, os.DirFS(filepath.Join(examples, "postgres", "python"))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYTHONPATH", filepath.Join(examples, "..", "sdk", "python"))
	broken := strings.Replace(string(orig), "string(limits.memory / 1048576 / 4) + 'MB'", "string(limits.memory / ", 1)
	if err := os.WriteFile(filepath.Join(src, "postgres.py"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	out, root, lang, res, err := generateFrom(ctx, src)
	defer os.RemoveAll(out)
	if err != nil || lang != "python" || len(res.Generate) > 0 || root != "postgres" {
		t.Fatalf("%v %s %s %q", err, lang, root, res.Generate)
	}
	ps, err := checkPack(ctx, filepath.Join(out, root), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ps {
		if strings.Contains(p.Path, "shared_buffers") && !p.Warning {
			found = true
			want := 1 + strings.Count(string(orig[:strings.Index(string(orig), "    shared_buffers:")]), "\n")
			if w, ok := author.Locate(src, lang, p.Path); !ok || filepath.Base(w.File) != "postgres.py" || w.Line != want {
				t.Errorf("where %s is written: %v %v", p.Path, w, ok)
			}
		}
	}
	if !found {
		t.Errorf("the formula that does not compile: %v", ps)
	}

	if err := os.WriteFile(filepath.Join(src, "postgres.py"), []byte(string(orig)+"\ndef (:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out2, _, _, res, err := generateFrom(ctx, src)
	defer os.RemoveAll(out2)
	if err != nil || !strings.Contains(res.Generate, "postgres.py") || !strings.Contains(res.Generate, "SyntaxError") {
		t.Errorf("a source that does not run: %v %q", err, res.Generate)
	}
}

// A --tests named that holds no scenarios is an error, not an ok of nothing run; tests/ found beside the source is
// taken quietly.
func TestAuthorTestsNamed(t *testing.T) {
	src := t.TempDir()
	if got := testsOf(src, ""); len(got) > 0 {
		t.Errorf("no tests/: %q", got)
	}
	if _, err := scenarioFiles([]string{testsOf(src, filepath.Join(src, "test"))}); err == nil {
		t.Error("a --tests that is not there")
	}
	if !packexamples.IsSource("postgres.py") || packexamples.IsSource("test_postgres.py") || packexamples.IsSource("PostgresTest.java") {
		t.Error("the sources of an example")
	}
}
