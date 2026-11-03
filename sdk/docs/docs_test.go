package docs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
	"github.com/invopop/jsonschema"
)

func TestParseTheSubset(t *testing.T) {
	src := `# a comment
base: true
/a/b:
  title: Plain text # a comment
  description: |
    Two lines,

    and a blank one between.

  examples:
    - "quoted: with a colon"
    - 'it''s'
    - 5432
"/q~1k":
  title: "\u00e9\t\"x\""
groups:
  g: G
`
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"base", "/a/b", "/q~1k", "groups"}; !reflect.DeepEqual(m.Keys, want) {
		t.Fatalf("keys %v, want %v", m.Keys, want)
	}
	ab := m.Values["/a/b"].(*Map)
	if got := ab.Values["title"]; got != "Plain text" {
		t.Errorf("title %q", got)
	}
	if got := ab.Values["description"]; got != "Two lines,\n\nand a blank one between." {
		t.Errorf("description %q", got)
	}
	if got := ab.Values["examples"]; !reflect.DeepEqual(got, []string{"quoted: with a colon", "it's", "5432"}) {
		t.Errorf("examples %q", got)
	}
	if got := m.Values["/q~1k"].(*Map).Values["title"]; got != "é\t\"x\"" {
		t.Errorf("quoted %q", got)
	}
}

func TestParseRefusesWhatIsNotInTheSubset(t *testing.T) {
	for name, src := range map[string]string{
		"flow map":         "a: {b: c}\n",
		"flow list":        "a: [b]\n",
		"anchor":           "a: &x b\n",
		"folded":           "a: >\n  b\n",
		"colon in plain":   "a: b: c\n",
		"duplicate":        "a: b\na: c\n",
		"tab":              "a:\n\tb: c\n",
		"no value":         "a:\n",
		"deeper sibling":   "a: b\n  c: d\n",
		"list of maps":     "a:\n  - b: c\n",
		"unclosed quote":   "a: \"b\n",
		"text after quote": "a: \"b\" c\n",
		"bad escape":       "a: \"\\q\"\n",
		"list at top":      "- a\n",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: %q parsed", name, src)
		}
	}
}

type conf struct {
	Port  int    `json:"port,omitempty"`
	Level string `json:"level,omitempty"`
	Debug bool   `json:"debug,omitempty"`
}

type app struct {
	Name string `json:"name"`
	Conf conf   `json:"conf"`
}

func (a *app) PackRules() map[string][]packschema.Rule {
	return map[string][]packschema.Rule{
		packschema.Pointer(a, &a.Name): {{ID: "name-short", Rule: "size(values.name) < 10", Message: "short"}},
	}
}

func reflectApp(t *testing.T) *jsonschema.Schema {
	t.Helper()
	s, err := packschema.Reflect(&app{}, packschema.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func set(t *testing.T, files map[string]string) *Set {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, Dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const en = `/:
  title: App
  description: An application.
/name:
  title: Name
  description: Its name.
  examples:
    - web
/conf/port:
  title: Port
  description: Where it listens.
  examples:
    - 8080
  group: net
/conf/debug:
  description: Says more.
  examples:
    - true
groups:
  net: Network
rules:
  name-short: the name is under ten letters
`

const ru = `/name:
  title: Имя
  description: Его имя.
/conf/port:
  title: Порт
  description: ""
groups:
  net: Сеть
rules:
  name-short: имя короче десяти букв
`

func TestApplyWritesTheBaseAndTheTranslations(t *testing.T) {
	s := reflectApp(t)
	if err := set(t, map[string]string{"en.yaml": en, "ru.yaml": ru}).Apply(s); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc["properties"].(map[string]any)
	name := props["name"].(map[string]any)
	port := props["conf"].(map[string]any)["properties"].(map[string]any)["port"].(map[string]any)
	debug := props["conf"].(map[string]any)["properties"].(map[string]any)["debug"].(map[string]any)
	for _, c := range []struct {
		what      string
		got, want any
	}{
		{"root title", doc["title"], "App"},
		{"langs", doc[KeyLangs], []any{"en", "ru"}},
		{"groups", doc[KeyGroups], []any{map[string]any{"id": "net", "title": "Network", KeyI18n: map[string]any{"ru": map[string]any{"title": "Сеть"}}}}},
		{"name", name["description"], "Its name."},
		{"name i18n", name[KeyI18n], map[string]any{"ru": map[string]any{"title": "Имя", "description": "Его имя."}}},
		{"name examples", name["examples"], []any{"web"}},
		{"port examples are integers", port["examples"], []any{float64(8080)}},
		{"port group", port[KeyGroup], "net"},
		{"an empty text is no translation", port[KeyI18n], map[string]any{"ru": map[string]any{"title": "Порт"}}},
		{"debug examples are booleans", debug["examples"], []any{true}},
		{"rule", name["x-pack-rules"], []any{map[string]any{"id": "name-short", "rule": "size(values.name) < 10", "message": "the name is under ten letters",
			KeyI18n: map[string]any{"ru": map[string]any{"message": "имя короче десяти букв"}}}}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: %#v, want %#v", c.what, c.got, c.want)
		}
	}
}

func TestApplyRefusesWhatTheSchemaHasNot(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no field":          {"en.yaml": "/nope:\n  title: x\n"},
		"no field, in ru":   {"en.yaml": "/name:\n  title: x\n", "ru.yaml": "/conf/nope:\n  title: x\n"},
		"no rule":           {"en.yaml": "rules:\n  nope: x\n"},
		"no action":         {"en.yaml": "actions:\n  nope: x\n"},
		"group no title":    {"en.yaml": "/name:\n  group: g\n"},
		"group no field":    {"en.yaml": "groups:\n  g: G\n"},
		"examples of a map": {"en.yaml": "/conf:\n  examples:\n    - x\n"},
		"not an integer":    {"en.yaml": "/conf/port:\n  examples:\n    - eighty\n"},
		"a fraction of int": {"en.yaml": "/conf/port:\n  examples:\n    - \"80.5\"\n"},
		"over int64":        {"en.yaml": "/conf/port:\n  examples:\n    - 99999999999999999999\n"},
	} {
		if err := set(t, files).Apply(reflectApp(t)); err == nil {
			t.Errorf("%s: applied", name)
		}
	}
}

func TestLoadRefuses(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no base":              {"ru.yaml": "/name:\n  title: x\n"},
		"two bases":            {"en.yaml": "base: true\n", "ru.yaml": "base: true\n"},
		"examples in a transl": {"en.yaml": "/name:\n  title: x\n", "ru.yaml": "/name:\n  examples:\n    - x\n"},
		"a group unknown":      {"en.yaml": "/name:\n  title: x\n", "ru.yaml": "groups:\n  g: x\n"},
		"a name not a lang":    {"english.yaml": "/name:\n  title: x\n"},
		"yml":                  {"en.yml": "/name:\n  title: x\n"},
		"unknown key":          {"en.yaml": "/name:\n  label: x\n"},
	} {
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, Dir), 0o755)
		for n, src := range files {
			_ = os.WriteFile(filepath.Join(dir, Dir, n), []byte(src), 0o644)
		}
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if s, err := Load(t.TempDir()); s != nil || err != nil {
		t.Errorf("no docs/: %v, %v", s, err)
	}
}

func written(t *testing.T, files map[string]string) *schema.Schema {
	t.Helper()
	s := reflectApp(t)
	if files != nil {
		if err := set(t, files).Apply(s); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out, err := schema.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGaps(t *testing.T) {
	if g := Gaps(written(t, nil)); len(g) != 1 || g[0].Path != "/" {
		t.Errorf("undocumented: %v", g)
	}
	var got []string
	for _, g := range Gaps(written(t, map[string]string{"en.yaml": en, "ru.yaml": ru})) {
		got = append(got, g.String())
	}
	want := []string{
		"/: no title in ru",
		"/: no description in ru",
		"/conf: no description in en",
		"/conf/port: no description in ru",
		"/conf/level: no description in en",
		"/conf/debug: no description in ru",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gaps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// What the skeleton writes reads back as the documentation it was made from.
func TestSkeletonRoundTrips(t *testing.T) {
	s := written(t, map[string]string{"en.yaml": en, "ru.yaml": ru})
	for _, lang := range []string{"en", "ru"} {
		sk := Skeleton(s, lang)
		if _, err := ParseFile(lang, sk); err != nil {
			t.Fatalf("%s: %v\n%s", lang, err, sk)
		}
	}
	again := written(t, map[string]string{"en.yaml": string(Skeleton(s, "en")), "ru.yaml": string(Skeleton(s, "ru"))})
	if !reflect.DeepEqual(s.Raw, again.Raw) {
		a, _ := json.Marshal(s.Raw)
		b, _ := json.Marshal(again.Raw)
		t.Errorf("round trip:\n%s\n%s", a, b)
	}
	ru := string(Skeleton(s, "ru"))
	for _, want := range []string{"  # en: Where it listens.\n  description: \"\"\n", "  # en: the name is under ten letters\n  name-short: имя короче десяти букв\n"} {
		if !strings.Contains(ru, want) {
			t.Errorf("ru skeleton lacks %q:\n%s", want, ru)
		}
	}
}

type tuning struct {
	Ratio float64 `json:"ratio,omitempty"`
}

// A number of examples is written back as the file says it, or refused: the same text from every SDK.
func TestNumberExamples(t *testing.T) {
	apply := func(x string) ([]any, error) {
		s, err := packschema.Reflect(&tuning{}, packschema.Options{})
		if err != nil {
			t.Fatal(err)
		}
		node, _ := Lookup(s, "/ratio")
		return examples(node, []string{x})
	}
	for _, ok := range []string{"0", "-3", "1.5", "0.25", "-0.0001", "123456789012.345", "9223372036854775807"} {
		got, err := apply(ok)
		if err != nil {
			t.Errorf("%s: %v", ok, err)
			continue
		}
		b, _ := json.Marshal(got[0])
		if string(b) != ok {
			t.Errorf("%s written as %s", ok, b)
		}
	}
	for _, bad := range []string{"nan", "inf", "-inf", "1e3", "1e999", "1.50", "1.0", ".5", "01.5", "0.00001", "1234567890123.456", "9223372036854775808", "+1"} {
		if _, err := apply(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}
