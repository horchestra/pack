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

// A spec written by hand becomes a source that author check says ok of — its scenario of the required settings
// included — in Python here (Go and Java: internal/author's round trip).
func TestAuthorScaffold(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	t.Setenv("PYTHONPATH", filepath.Join(examples, "..", "sdk", "python"))
	t.Cleanup(func() {
		scaffoldSpec, scaffoldFrom, scaffoldLang, scaffoldOut, scaffoldGoImport, scaffoldSchema = "", "", "go", ".", "", false
		authorLang, authorTests = "", ""
	})
	d := t.TempDir()
	spec := `name: kv
version: "1.0.0"
image: images/kv:1.0.0
title: KV
doc: A key-value store.
langs:
  - en
  - ru
i18n:
  ru:
    title: KV
    description: Хранилище ключей и значений.
fields:
  - name: database
    type: string
    required: true
    env: KV_DB
    title: Database
    description: The database to open.
    i18n:
      ru:
        title: База
        description: База, которую открыть.
  - name: password
    type: secret
    required: true
    env: KV_PASSWORD
    title: Password
    description: The password of the admin.
  - name: port
    type: integer
    default: 6380
    title: Port
    description: Where it listens.
root:
  ports:
    kv: 6380
  readiness:
    tcp: kv
  minimum:
    memory: 256Mi
rules:
  /port:
    - id: port-high
      rule: values.port > 1024
      message: the port is above 1024
actions:
  - name: compact
    command:
      - kv
      - compact
    description: Compacts the store.
`
	if err := os.WriteFile(filepath.Join(d, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(d, "src")
	out := pack(t, d, "author", "scaffold", "--spec", filepath.Join(d, "spec.yaml"), "--lang", "python", "-o", src)
	if !strings.Contains(out, "kv.py") || !strings.Contains(out, "docs/ru.yaml") || !strings.Contains(out, "tests/basic.yaml") {
		t.Fatalf("scaffold:\n%s", out)
	}
	scaffoldSpec = ""
	if out := pack(t, d, "author", "check", src); !strings.HasSuffix(strings.TrimSpace(out), "ok") {
		t.Errorf("author check of what scaffold wrote:\n%s", out)
	}
	ctx := context.Background()
	gen, root, _, res, err := generateFrom(ctx, src)
	defer os.RemoveAll(gen)
	if err != nil || len(res.Generate) > 0 {
		t.Fatal(err, res.Generate)
	}
	raw, _ := os.ReadFile(filepath.Join(gen, root, "schema.json"))
	for _, want := range []string{`"x-pack-env":"KV_DB"`, `"format":"password"`, `"default":6380`, `"port-high"`, `"База"`, `"name":"compact"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the schema has no %s:\n%s", want, raw)
		}
	}
}
