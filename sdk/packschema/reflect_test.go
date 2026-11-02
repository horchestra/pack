package packschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type listener struct {
	Address string `json:"address"`
	TLS     bool   `json:"tls,omitempty"`
}

// cfg is a service's own configuration, the way an author writes it.
type cfg struct {
	Name    string   `json:"name"`                               // no omitempty: required
	Port    int      `json:"port,omitempty" env:"PORT"`          // optional, an environment variable
	Level   string   `json:"level,omitempty"`                    // set by SetDefaults
	Listen  listener `json:"listen" file:"/etc/svc/listen.json"` // a subtree written as a file
	Script  string   `json:"script,omitempty" file:"/opt/run.sh" template:"run.ptmpl" mode:"0500"`
	Unit    string   `json:"unit,omitempty" file:"/opt/unit" template:"unit.sh" format:"raw"`
	Key     Secret   `json:"key" file:"/secrets/key" format:"secret" mode:"0400"`
	Dep     *cfgDep  `json:"dep,omitempty"`
	Skipped string   `json:"-"`
	Props   struct {
		A string `json:"a,omitempty"`
	} `json:"props" file:"/etc/svc.properties"`
}

type cfgDep struct {
	Host string `json:"host,omitempty"`
}

func (c *cfg) SetDefaults() { c.Level = "info" }
func (*cfg) PackRoot() Root { return Root{Ports: map[string]int{"api": 8200}} }

// ruled is a pack with validation rules: storage is derived from the optional
// dependency, never written by hand.
type ruled struct {
	Storage []string `json:"storage,omitempty"`
	Dep     *cfgDep  `json:"dep,omitempty"`
}

func (*ruled) PackRoot() Root { return Root{} }
func (*ruled) PackRules() map[string][]Rule {
	return map[string][]Rule{
		"/storage": {{
			Rule:    "!has(values.dep) || !has(values.storage) || size(values.storage) == 0",
			Message: "storage is filled from dep and must not be set",
			Phases:  []Phase{PreMutate, PostMutate},
		}},
	}
}

func reflectJSON(t *testing.T, v any, o Options) map[string]any {
	t.Helper()
	s, err := Reflect(v, o)
	if err != nil {
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
	return doc
}

func prop(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	p, _ := doc["properties"].(map[string]any)
	m, _ := p[name].(map[string]any)
	if m == nil {
		t.Fatalf("no property %s in %v", name, doc["properties"])
	}
	return m
}

func TestRequiredIsTheAbsenceOfOmitempty(t *testing.T) {
	doc := reflectJSON(t, &cfg{}, Options{})
	req := map[string]bool{}
	for _, r := range doc["required"].([]any) {
		req[r.(string)] = true
	}
	if !req["name"] || !req["listen"] || req["port"] || req["level"] || req["dep"] {
		t.Fatalf("required = %v", req)
	}
	if _, ok := doc["properties"].(map[string]any)["Skipped"]; ok {
		t.Fatal("json:\"-\" is not a property")
	}
}

func TestEnvAndDefaults(t *testing.T) {
	doc := reflectJSON(t, &cfg{}, Options{})
	if prop(t, doc, "port")["x-pack-env"] != "PORT" {
		t.Fatalf("port = %v", prop(t, doc, "port"))
	}
	if prop(t, doc, "level")["default"] != "info" {
		t.Fatal("what SetDefaults sets is the default")
	}
	if _, has := prop(t, doc, "port")["default"]; has {
		t.Fatal("a field left zero has no default")
	}
	if prop(t, doc, "name")["default"] != nil {
		t.Fatal("a required field with no value has no default")
	}
	if rt, _ := doc["x-pack"].(map[string]any); rt == nil || rt["ports"].(map[string]any)["api"].(float64) != 8200 {
		t.Fatalf("x-pack = %v", doc["x-pack"])
	}
}

func TestFileTags(t *testing.T) {
	doc := reflectJSON(t, &cfg{}, Options{})
	want := map[string]File{
		"listen": {Path: "/etc/svc/listen.json", Format: "json"},
		"script": {Path: "/opt/run.sh", Format: "template", Template: "run.ptmpl", Mode: "0500"},
		"unit":   {Path: "/opt/unit", Format: "raw", Template: "unit.sh"},
		"key":    {Path: "/secrets/key", Format: "secret", Mode: "0400"},
		"props":  {Path: "/etc/svc.properties", Format: "properties"},
	}
	for name, f := range want {
		got, _ := prop(t, doc, name)["x-pack-file"].(map[string]any)
		raw, _ := json.Marshal(f)
		var exp map[string]any
		_ = json.Unmarshal(raw, &exp)
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("%s: x-pack-file = %v, want %v", name, got, exp)
		}
	}
}

func TestFileTagMistakesAreRefused(t *testing.T) {
	type noFile struct {
		A string `json:"a" template:"x.ptmpl"`
	}
	type badName struct {
		A string `json:"a" file:"/etc/x" template:"x.sh"` // a template that is not *.ptmpl
	}
	type badRaw struct {
		A string `json:"a" file:"/etc/x" template:"x.ptmpl" format:"raw"`
	}
	type noFormat struct {
		A string `json:"a" file:"/etc/x"` // the name says nothing
	}
	type strayTemplate struct {
		A string `json:"a" file:"/etc/x.json" template:"x.ptmpl" format:"json"`
	}
	for name, v := range map[string]any{"noFile": &noFile{}, "badName": &badName{}, "badRaw": &badRaw{}, "noFormat": &noFormat{}, "stray": &strayTemplate{}} {
		if _, err := Reflect(v, Options{}); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestPackFieldBecomesAReference(t *testing.T) {
	c := &cfg{Dep: &cfgDep{Host: "db"}}
	doc := reflectJSON(t, c, Options{Components: func(t reflect.Type) (string, bool) {
		if t == reflect.TypeFor[*cfgDep]() {
			return "dep", true
		}
		return "", false
	}})
	dep := prop(t, doc, "dep")
	if dep["$ref"] != "pack://dep#" {
		t.Fatalf("dep = %v", dep)
	}
	if d, _ := dep["default"].(map[string]any); d["host"] != "db" {
		t.Fatalf("what the author set in the pack is its default: %v", dep)
	}
	if strings.Contains(string(mustJSON(t, doc)), "\"host\":{") {
		t.Fatal("the pack's own settings are its schema's, not the parent's")
	}
}

// PackRules lands on the node as x-pack-rules, with the phases as given; a
// pointer that names nothing is an error.
func TestRules(t *testing.T) {
	doc := reflectJSON(t, &ruled{}, Options{})
	rules, _ := prop(t, doc, "storage")["x-pack-rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("x-pack-rules = %v", prop(t, doc, "storage"))
	}
	rule := rules[0].(map[string]any)
	if rule["rule"] != "!has(values.dep) || !has(values.storage) || size(values.storage) == 0" {
		t.Fatalf("rule = %v", rule)
	}
	if rule["message"] != "storage is filled from dep and must not be set" {
		t.Fatalf("message = %v", rule)
	}
	phases, _ := rule["phases"].([]any)
	if len(phases) != 2 || phases[0] != "preMutate" || phases[1] != "postMutate" {
		t.Fatalf("phases = %v", rule)
	}
}

func TestRulesOnANodeThatIsNotThereAreRefused(t *testing.T) {
	if _, err := Reflect(&rulesElsewhere{}, Options{}); err == nil {
		t.Fatal("a rule on a property the schema does not have must be refused")
	}
}

type rulesElsewhere struct {
	A string `json:"a,omitempty"`
}

func (*rulesElsewhere) PackRoot() Root { return Root{} }
func (*rulesElsewhere) PackRules() map[string][]Rule {
	return map[string][]Rule{"/nowhere": {{Rule: "true"}}}
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type shelf struct {
	Dep *cfgDep `json:"dep,omitempty"`
}

type deep struct {
	Shelf shelf `json:"shelf"`
}

// A pack is a property of the root of the pack that holds it: one inside a struct of its own would be a $ref nothing
// can attach.
func TestPackInsideAStructIsRefused(t *testing.T) {
	_, err := Reflect(&deep{}, Options{Components: func(t reflect.Type) (string, bool) {
		if t == reflect.TypeFor[*cfgDep]() {
			return "dep", true
		}
		return "", false
	}})
	if err == nil || !strings.Contains(err.Error(), "held by a field of the pack itself") {
		t.Fatalf("err = %v", err)
	}
}
