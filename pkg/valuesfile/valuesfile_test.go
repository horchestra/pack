package valuesfile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/bundle"
)

// The test packs as the Go sdk writes them: openbao holds tembo, both documented in en and ru.
const packs = "../../sdk/internal/testdata/golden/openbao"

func load(t *testing.T) *bundle.Bundle {
	t.Helper()
	b, err := bundle.Load(packs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var given = map[string]any{
	"openbao":  map[string]any{"api_addr": "https://bao:8200", "ui": true},
	"postgres": map[string]any{"pg_port": float64(5440), "node": map[string]any{"nofailover": true}},
}

// UPDATE_GOLDEN=1 go test ./pkg/valuesfile rewrites testdata after a deliberate change of the file.
func TestGolden(t *testing.T) {
	b := load(t)
	for _, lang := range []string{"", "ru"} {
		got, err := Write(b, given, Sources{"/postgres/pg_port": {Value: float64(5440), From: FromSuggest, Why: "5432 is taken on db1"}}, lang)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Join("testdata", "openbao."+map[string]string{"": "en", "ru": "ru"}[lang]+".yaml")
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs (UPDATE_GOLDEN=1 rewrites it):\n%s", name, got)
		}
	}
}

// What the file says is what it was written from: the commented lines are no values.
func TestTheValuesReadBack(t *testing.T) {
	out, err := Write(load(t), given, nil, "ru")
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !reflect.DeepEqual(back, given) {
		t.Errorf("read back %v, want %v", back, given)
	}
}

// Uncommenting a line the pack wrote gives the file pack would write with that value set.
func TestUncommentingIsSetting(t *testing.T) {
	b := load(t)
	before, err := Write(b, given, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(before), "  #log_level: info\n", "  log_level: info\n", 1)
	if edited == string(before) {
		t.Fatalf("no #log_level line:\n%s", before)
	}
	v := map[string]any{}
	if err := yaml.Unmarshal([]byte(edited), &v); err != nil {
		t.Fatal(err)
	}
	after, err := Write(b, v, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if edited != string(after) {
		t.Errorf("uncommented:\n%s\nwritten:\n%s", edited, after)
	}
	if lost := Foreign([]byte(edited), after); len(lost) > 0 {
		t.Errorf("foreign in an edited file: %v", lost)
	}
}

func TestForeign(t *testing.T) {
	b := load(t)
	want, err := Write(b, given, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if lost := Foreign(want, want); len(lost) > 0 {
		t.Errorf("own file: %v", lost)
	}
	for name, edit := range map[string]func(string) string{
		"a note": func(s string) string { return strings.Replace(s, "  ui: true\n", "  # why on\n  ui: true\n", 1) },
		"a note at the end": func(s string) string {
			return strings.Replace(s, "  ui: true\n", "  ui: true # why on\n", 1)
		},
		"no stamp": func(s string) string { _, body, _ := strings.Cut(s, "\n"); return body },
	} {
		if lost := Foreign([]byte(edit(string(want))), want); len(lost) == 0 {
			t.Errorf("%s: not seen", name)
		}
	}
	if lost := Foreign([]byte("openbao:\n  ui: true\n"), want); len(lost) > 0 {
		t.Errorf("a file of values alone: %v", lost)
	}
}

func TestSourcesOf(t *testing.T) {
	s := Sources{"/a": {Value: float64(1), From: FromSuggest}}
	if got := s.Of("/a", float64(1)).From; got != FromSuggest {
		t.Errorf("the recorded value: %s", got)
	}
	if got := s.Of("/a", float64(2)).From; got != FromOperator {
		t.Errorf("a value changed since: %s", got)
	}
	dir := t.TempDir()
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	back, err := LoadSources(dir)
	if err != nil || !reflect.DeepEqual(back, s) {
		t.Errorf("saved and loaded: %v, %v", back, err)
	}
}

func TestLang(t *testing.T) {
	langs := []string{"en", "ru"}
	for lang, want := range map[string]string{"": "", "ru": "ru", "ru-RU": "ru", "de": "", "en": ""} {
		if got := pick(langs, lang); got != want {
			t.Errorf("pick(%q) = %q, want %q", lang, got, want)
		}
	}
	t.Setenv("LANG", "ru_RU.UTF-8")
	if got := Lang(""); got != "ru-RU" {
		t.Errorf("Lang from $LANG: %q", got)
	}
	if got := Lang("de"); got != "de" {
		t.Errorf("Lang of the environment: %q", got)
	}
	t.Setenv("LANG", "C")
	if got := Lang(""); len(got) > 0 {
		t.Errorf("Lang of C: %q", got)
	}
}
