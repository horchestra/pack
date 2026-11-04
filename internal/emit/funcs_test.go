package emit

import (
	"strings"
	"testing"
)

func render(t *testing.T, tmpl string, data any, parseAs string) string {
	t.Helper()
	out, err := Template("t", []byte(tmpl), data, parseAs)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A template has sprig and Helm's own functions.
func TestTemplateFunctions(t *testing.T) {
	data := map[string]any{"name": "nn", "port": 8020, "tags": []any{"a", "b"}, "opts": map[string]any{"x": "1", "z": []any{"p", "q"}}}
	for name, tc := range map[string]struct{ tmpl, want string }{
		"sprig default":   {`{{ default "dflt" .none | upper }}`, "DFLT"},
		"sprig quote":     {`{{ .name | quote }}`, `"nn"`},
		"sprig list":      {`{{ join "," .tags }}`, "a,b"},
		"toYaml indent":   {"opts:\n{{ toYaml .opts | indent 2 }}", "opts:\n  x: \"1\"\n  z:\n  - p\n  - q"},
		"toJson":          {`{{ toJson .opts }}`, `{"x":"1","z":["p","q"]}`},
		"fromYaml":        {`{{ (fromYaml "a: 1\nb: two").b }}`, "two"},
		"fromJson":        {`{{ (fromJson "{\"a\":3}").a }}`, "3"},
		"toToml":          {`{{ toToml (dict "a" 1 "b" "x") }}`, "a = 1\nb = 'x'"},
		"required given":  {`{{ required "need a name" .name }}`, "nn"},
		"include":         {`{{ define "h" }}host-{{ .name }}{{ end }}{{ include "h" . | upper }}`, "HOST-NN"},
		"tpl":             {`{{ tpl "port {{ .port }}" . }}`, "port 8020"},
		"lookup is empty": {`{{ len (lookup "v1" "Pod" "ns" "p") }}`, "0"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := render(t, tc.tmpl, data, "text"); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTemplateMissingKey(t *testing.T) {
	// a value the data has not is nil for default and required; if it reaches the result it is an error, with its line
	if _, err := Template("t", []byte("a: 1\nb: {{ .typo }}\n"), map[string]any{}, "yaml"); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("a missing key in the result: %v", err)
	}
	if _, err := Template("t", []byte(`{{ required "typo is required" .typo }}`), map[string]any{}, "text"); err == nil || !strings.Contains(err.Error(), "typo is required") {
		t.Errorf("required on a missing key: %v", err)
	}
	if got := render(t, `{{ .typo | default "d" }}`, map[string]any{}, "text"); got != "d" {
		t.Errorf("default on a missing key: %q", got)
	}
}

func TestTemplateRequiredAndEnv(t *testing.T) {
	if _, err := Template("t", []byte(`{{ required "name is required" .name }}`), map[string]any{"name": ""}, "text"); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("required on an empty string: %v", err)
	}
	// env and expandenv are not there, as in Helm: the environment of the machine must not leak into a file
	if _, err := Template("t", []byte(`{{ env "HOME" }}`), map[string]any{}, "text"); err == nil {
		t.Error("env is a function of a template")
	}
	// the result is still parsed back as the format it claims
	if _, err := Template("t", []byte(`{{ "a: [" }}`), map[string]any{}, "yaml"); err == nil {
		t.Error("a broken yaml passed")
	}
}

func TestTOML(t *testing.T) {
	v := map[string]any{
		"title": "stand",
		"port":  float64(8020), // the values tree holds numbers as float64
		"ratio": 0.5,
		"tags":  []any{"a", "b"},
		"db":    map[string]any{"host": "pg", "port": float64(5432)},
	}
	got, err := Emit("toml", v)
	if err != nil {
		t.Fatal(err)
	}
	want := "port = 8020\nratio = 0.5\ntags = ['a', 'b']\ntitle = 'stand'\n\n[db]\nhost = 'pg'\nport = 5432\n"
	if string(got) != want {
		t.Errorf("toml:\n%s\nwant:\n%s", got, want)
	}
	again, _ := Emit("toml", v)
	if string(again) != string(got) {
		t.Error("toml is not deterministic")
	}
	if err := Parse("toml", got); err != nil {
		t.Errorf("the output does not parse back: %v", err)
	}
	if _, err := Emit("toml", map[string]any{"x": nil}); err == nil || !strings.Contains(err.Error(), "no null") {
		t.Errorf("null: %v", err)
	}
	if _, err := Emit("toml", "not a table"); err == nil {
		t.Error("a string is not a TOML file")
	}
	if err := Parse("toml", []byte("a = ")); err == nil {
		t.Error("a broken toml passed")
	}
}
