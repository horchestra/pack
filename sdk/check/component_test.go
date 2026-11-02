package check

import (
	"strings"
	"testing"

	"github.com/horchestra/pack/sdk/schema"
)

func run(t *testing.T, raw string) []Problem {
	t.Helper()
	s, err := schema.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	ps, err := Run(Component{Name: "c", Schema: s})
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func has(ps []Problem, path, fragment string) bool {
	for _, p := range ps {
		if p.Path == path && strings.Contains(p.Message, fragment) {
			return true
		}
	}
	return false
}

const base = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object", "additionalProperties": false,
  "x-pack": {"ports": {"main": 80}, "readiness": {"tcp": "main"}, "hooks": HOOKS},
  "properties": {
    "conf": {"type": "object", "x-pack-file": {"path": "/etc/c/c.properties", "format": "properties"},
             "properties": PROPS}
  }
}`

func with(hooks, props string) string {
	return strings.NewReplacer("HOOKS", hooks, "PROPS", props).Replace(base)
}

// A formula whose static type contradicts the node's is refused; a dynamic
// one is not, and a null branch does not hide the type of the other.
func TestFormulaTypes(t *testing.T) {
	ps := run(t, with("[]", `{
	  "n":  {"type": "integer", "x-pack-compute": "string(values.m)"},
	  "s":  {"type": "string",  "x-pack-compute": "values.m + 1"},
	  "f":  {"type": "number",  "x-pack-compute": "limits.memory / 1000"},
	  "d":  {"type": "string",  "x-pack-compute": "values.m"},
	  "q":  {"type": "string",  "x-pack-compute": "has(values.m) ? string(values.m) : null"},
	  "b":  {"type": "boolean", "x-pack-compute": "has(values.m) ? null : 1"},
	  "e":  {"type": "string",  "x-pack-compute": "1 + 'a'"},
	  "m":  {"type": "integer", "default": 1}
	}`))
	if !has(ps, "/conf/n", "yields string, the node is integer") {
		t.Errorf("string into integer must be refused: %v", ps)
	}
	if !has(ps, "/conf/s", "yields integer, the node is string") {
		t.Errorf("integer into string must be refused: %v", ps)
	}
	if has(ps, "/conf/f", "yields") {
		t.Errorf("an integer fits a number node: %v", ps)
	}
	if has(ps, "/conf/d", "yields") || has(ps, "/conf/q", "yields") {
		t.Errorf("a dynamic or matching formula is fine: %v", ps)
	}
	if !has(ps, "/conf/b", "yields integer, the node is boolean") {
		t.Errorf("the non-null branch is the formula's type: %v", ps)
	}
	if !has(ps, "/conf/e", "does not type-check") {
		t.Errorf("a genuine type error must be refused: %v", ps)
	}
}

// Install hooks have nothing to stop, no version to come from and no old image.
func TestInstallHooks(t *testing.T) {
	ps := run(t, with(`[
	  {"name": "format", "phase": "preInstall", "command": ["hdfs", "namenode", "-format"], "idempotent": true},
	  {"name": "bad-exclusive", "phase": "preInstall", "command": ["x"], "exclusive": true},
	  {"name": "bad-when", "phase": "postInstall", "command": ["x"], "when": {"from": "<1"}},
	  {"name": "bad-image", "phase": "postInstall", "command": ["x"], "image": "old"},
	  {"name": "ok-to", "phase": "postInstall", "command": ["x"], "when": {"to": ">=2"}},
	  {"name": "up", "phase": "preUpgrade", "command": ["x"], "exclusive": true, "when": {"from": "<2"}}
	]`, `{"m": {"type": "integer", "default": 1}}`))
	for _, want := range []struct{ path, msg string }{
		{"/x-pack/hooks/1", "exclusive on an install hook"},
		{"/x-pack/hooks/2/when/from", "no version to come from"},
		{"/x-pack/hooks/3/image", "no old image"},
	} {
		if !has(ps, want.path, want.msg) {
			t.Errorf("missing %s: %s — got %v", want.path, want.msg, ps)
		}
	}
	for _, p := range ps {
		if strings.HasPrefix(p.Path, "/x-pack/hooks/0") || strings.HasPrefix(p.Path, "/x-pack/hooks/4") || strings.HasPrefix(p.Path, "/x-pack/hooks/5") {
			t.Errorf("a well-formed hook is refused: %s", p)
		}
	}
}

// A secret file mounts a referenced secret's key: only on a password leaf,
// never with a template.
func TestSecretFiles(t *testing.T) {
	// secret files are leaves of their own, beside the config subtree
	s := strings.Replace(with("[]", `{"m": {"type": "integer", "default": 1}}`), `"conf": {`, `
	  "keytab":    {"type": "string", "format": "password", "x-pack-file": {"path": "/etc/security/keytabs/hdfs.keytab", "format": "secret", "mode": "0400"}},
	  "notsecret": {"type": "string", "x-pack-file": {"path": "/etc/x/plain", "format": "secret"}},
	  "tmpl":      {"type": "string", "format": "password", "x-pack-file": {"path": "/etc/x/t", "format": "secret", "template": "t.tmpl", "parseAs": "text"}},
	  "conf": {`, 1)
	ps := run(t, s)
	if has(ps, "/keytab", "") {
		t.Errorf("a well-formed secret file is refused: %v", ps)
	}
	if !has(ps, "/notsecret", "must be format: password") {
		t.Errorf("secret file on a plain leaf must be refused: %v", ps)
	}
	if !has(ps, "/tmpl", "no template or parseAs") {
		t.Errorf("secret file with a template must be refused: %v", ps)
	}

	s = strings.Replace(with("[]", `{"m": {"type": "integer", "default": 1}}`), `"x-pack": {`, `"x-pack": {"restartPolicy": "Sometimes", `, 1)
	if ps = run(t, s); !has(ps, "/x-pack/restartPolicy", "must be Always") {
		t.Errorf("an unknown policy must be refused: %v", ps)
	}
}

// The ports and volumes are the pack's own words (x-pack), and what refers to them must exist.
func TestPortsAndVolumes(t *testing.T) {
	ok := with("[]", `{"m": {"type": "integer", "default": 1}}`)
	if ps := run(t, ok); len(ps) != 0 {
		t.Errorf("a well-formed pack is refused: %v", ps)
	}
	lost := strings.Replace(ok, `"readiness": {"tcp": "main"}`, `"readiness": {"tcp": "lost"}`, 1)
	if ps := run(t, lost); !has(ps, "/x-pack/readiness", `port "lost"`) {
		t.Errorf("readiness on a port that is not declared: %v", ps)
	}
	bad := strings.Replace(ok, `"ports": {"main": 80}`, `"ports": {"main": 70000}, "data": {"d": {"mountPath": "rel"}}, "minimum": {"memory": "lots"}`, 1)
	ps := run(t, bad)
	for path, want := range map[string]string{"/x-pack/ports/main": "not a port", "/x-pack/data/d": "not absolute", "/x-pack/minimum/memory": ""} {
		if !has(ps, path, want) {
			t.Errorf("%s: want %q: %v", path, want, ps)
		}
	}
}

func TestRestartPolicy(t *testing.T) {
	ok := with("[]", `{"m": {"type": "integer", "default": 1}}`)
	bad := strings.Replace(ok, `"readiness"`, `"restartPolicy": "Sometimes", "readiness"`, 1)
	if ps := run(t, bad); !has(ps, "/x-pack/restartPolicy", "must be Always") {
		t.Errorf("a restart policy that is none of the three: %v", ps)
	}
}

// Rules: they compile, yield a boolean, run at a phase a rule runs at, read
// no replica identity; on the field of a held pack they would be cut out of
// the flat view.
func TestRules(t *testing.T) {
	ok := with("[]", `{
	  "m": {"type": "integer", "default": 1},
	  "s": {"type": "array", "x-pack-rules": [{"rule": "!has(values.conf.s) || size(values.conf.s) == 0"}]}
	}`)
	if ps := run(t, ok); len(ps) != 0 {
		t.Errorf("a well-formed rule is refused: %v", ps)
	}

	bad := with("[]", `{
	  "cel":   {"type": "integer", "x-pack-rules": [{"rule": "values.conf.cel +"}]},
	  "type":   {"type": "integer", "x-pack-rules": [{"rule": "values.conf.type + 1"}]},
	  "phase":  {"type": "integer", "x-pack-rules": [{"rule": "true", "phases": ["preInstall"]}]},
	  "replica": {"type": "integer", "x-pack-rules": [{"rule": "pack.index > 0"}]}
	}`)
	ps := run(t, bad)
	for _, want := range []struct{ path, msg string }{
		{"/conf/cel/x-pack-rules/0", "does not compile"},
		{"/conf/type/x-pack-rules/0", "a rule must be boolean"},
		{"/conf/phase/x-pack-rules/0", `phase "preInstall" is not one a rule runs at`},
		{"/conf/replica/x-pack-rules/0", "not per-replica"},
	} {
		if !has(ps, want.path, want.msg) {
			t.Errorf("missing %s: %s — got %v", want.path, want.msg, ps)
		}
	}

	// on the field of a held pack: the field is a $ref, the flat view cuts it out
	s, err := schema.Parse([]byte(`{"type":"object","properties":{
		"dep": {"$ref": "pack://dep#", "x-pack-rules": [{"rule": "true"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	open, err := schema.Parse([]byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	open.Foreign = s.Foreign
	s.Attach("pack://dep", open)
	ps, err = Run(Component{Name: "c", Schema: s})
	if err != nil {
		t.Fatal(err)
	}
	if !has(ps, "/dep", "cut out of the flat view") {
		t.Errorf("rules on a held pack's field must be refused: %v", ps)
	}
}

// The mutate phases are a rule's, never a hook's.
func TestHookPhasesAreTheSagas(t *testing.T) {
	ps := run(t, with(`[
	  {"name": "mut", "phase": "preMutate", "command": ["x"]},
	  {"name": "ok", "phase": "preInstall", "command": ["x"], "idempotent": true}
	]`, `{"m": {"type": "integer", "default": 1}}`))
	if !has(ps, "/x-pack/hooks/0", `phase "preMutate" is not one of`) {
		t.Errorf("a hook at a mutate phase must be refused: %v", ps)
	}
	for _, p := range ps {
		if strings.HasPrefix(p.Path, "/x-pack/hooks/1") {
			t.Errorf("a well-formed hook is refused: %s", p)
		}
	}
}
