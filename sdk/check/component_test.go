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
  "x-pack": {"ports": {"main": 80}, "readiness": {"tcp": "main"}, "hooks": HOOKS,
    "content": [{"path": "/etc/c/c.properties", "values": "conf", "format": "properties"}]},
  "properties": {
    "conf": {"type": "object",
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
	  "keytab":    {"type": "string", "format": "password"},
	  "notsecret": {"type": "string"},
	  "parsed":    {"type": "string", "format": "password"},
	  "conf": {`, 1)
	s = strings.Replace(s, `"content": [`, `"content": [
	  {"path": "/etc/security/keytabs/hdfs.keytab", "values": "keytab", "format": "secret", "mode": "0400"},
	  {"path": "/etc/x/plain", "values": "notsecret", "format": "secret"},
	  {"path": "/etc/x/t", "values": "parsed", "format": "secret", "parseAs": "text"},
	  `, 1)
	ps := run(t, s)
	if has(ps, "/keytab", "") {
		t.Errorf("a well-formed secret file is refused: %v", ps)
	}
	if !has(ps, "/notsecret", "must be format: password") {
		t.Errorf("secret file on a plain leaf must be refused: %v", ps)
	}
	if !has(ps, "/parsed", "no parseAs") {
		t.Errorf("secret file with parseAs must be refused: %v", ps)
	}

	s = strings.Replace(with("[]", `{"m": {"type": "integer", "default": 1}}`), `"x-pack": {`, `"x-pack": {"restartPolicy": "Sometimes", `, 1)
	if ps = run(t, s); !has(ps, "/x-pack/restartPolicy", "must be Always") {
		t.Errorf("an unknown policy must be refused: %v", ps)
	}
}

// The ports and volumes are the pack's own words (x-pack), and what refers to them must exist.
// A file of Content comes from one place, and that place exists.
func TestContent(t *testing.T) {
	for name, tc := range map[string]struct{ file, ptr, want string }{
		"from nowhere":    {`{"path": "/etc/x"}`, "/x-pack/content/1", "comes from nowhere"},
		"no such field":   {`{"path": "/etc/x.json", "values": "nope"}`, "/x-pack/content/1", "no such field"},
		"two sources":     {`{"path": "/etc/x", "source": "a", "template": "b"}`, "/x-pack/content/1", "no template, values, format or parseAs"},
		"template format": {`{"path": "/etc/x", "template": "b", "format": "json"}`, "/x-pack/content/1", "a template has no format"},
		"unknown parse":   {`{"path": "/etc/x", "template": "b", "parseAs": "nope"}`, "/x-pack/content/1", "not a parseable format"},
		"same path":       {`{"path": "/etc/c/c.properties", "source": "a"}`, "/x-pack/content/1", "also declared"},
	} {
		s := strings.Replace(with("[]", `{"m": {"type": "integer", "default": 1}}`), `"content": [{"path": "/etc/c/c.properties", "values": "conf", "format": "properties"}]`,
			`"content": [{"path": "/etc/c/c.properties", "values": "conf", "format": "properties"}, `+tc.file+`]`, 1)
		if ps := run(t, s); !has(ps, tc.ptr, tc.want) {
			t.Errorf("%s: %v", name, ps)
		}
	}
}

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
	  "s": {"type": "array", "x-pack-rules": [{"rule": "!has(values.conf.s) || size(values.conf.s) == 0"}]},
	  "both": {"type": "integer", "x-pack-rules": [{"rule": "values.conf.both > 0", "phases": ["preMutate", "postMutate"]}]},
	  "dyn": {"type": "integer", "x-pack-rules": [{"rule": "values.conf.dyn"}]}
	}`)
	if ps := run(t, ok); len(ps) != 0 {
		// dyn: a rule whose type the checker cannot fix is the render's to judge, not the author's
		t.Errorf("a well-formed rule is refused: %v", ps)
	}

	bad := with("[]", `{
	  "cel":   {"type": "integer", "x-pack-rules": [{"rule": "values.conf.cel +"}]},
	  "type":  {"type": "integer", "x-pack-rules": [{"rule": "values.conf.type + 1"}]},
	  "mismatch": {"type": "integer", "x-pack-rules": [{"rule": "1 + 'a'"}]},
	  "phase": {"type": "integer", "x-pack-rules": [{"rule": "true", "phases": ["preInstall"]}]},
	  "replica":  {"type": "integer", "x-pack-rules": [{"rule": "pack.index > 0"}]},
	  "replica2": {"type": "integer", "x-pack-rules": [{"rule": "pack.instance != ''"}]},
	  "replica3": {"type": "integer", "x-pack-rules": [{"rule": "pack.ordinal == 1"}]},
	  "twice": {"type": "integer", "x-pack-rules": [{"rule": "true"}, {"rule": "values.conf.twice + 1"}]}
	}`)
	ps := run(t, bad)
	for _, want := range []struct{ path, msg string }{
		{"/conf/cel/x-pack-rules/0", "does not compile"},
		{"/conf/type/x-pack-rules/0", "a rule must be boolean"},
		{"/conf/mismatch/x-pack-rules/0", "does not type-check"},
		{"/conf/phase/x-pack-rules/0", `phase "preInstall" is not one a rule runs at`},
		{"/conf/replica/x-pack-rules/0", "not per-replica"},
		{"/conf/replica2/x-pack-rules/0", "not per-replica"},
		{"/conf/replica3/x-pack-rules/0", "not per-replica"},
		{"/conf/twice/x-pack-rules/1", "a rule must be boolean"}, // the second rule of the node
	} {
		if !has(ps, want.path, want.msg) {
			t.Errorf("missing %s: %s — got %v", want.path, want.msg, ps)
		}
	}
	if has(ps, "/conf/twice/x-pack-rules/0", "") {
		t.Errorf("the first rule of the node is well-formed: %v", ps)
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

// A dynamic index off values makes the set of a formula's reads unknowable —
// it is the formula's ordering that needs them static; a rule answers about
// one document and may index as it likes. A literal index is a path like any
// other.
func TestDynamicIndexIsRefused(t *testing.T) {
	ok := with("[]", `{
	  "src":  {"type": "array"},
	  "head": {"type": "string", "x-pack-compute": "values.conf.src[0]"},
	  "b":    {"type": "object", "x-pack-rules": [{"rule": "size(values.conf.b[values.conf.i]) > 0"}]},
	  "i":    {"type": "integer"}
	}`)
	if ps := run(t, ok); len(ps) != 0 {
		t.Errorf("a literal index, and a rule's dynamic one, are fine: %v", ps)
	}

	bad := with("[]", `{
	  "a": {"type": "integer", "x-pack-compute": "size(values.conf.b[values.conf.i])"},
	  "b": {"type": "object"},
	  "i": {"type": "integer"}
	}`)
	if ps := run(t, bad); !has(ps, "/conf/a", "dynamic index in values.conf.b[]") {
		t.Errorf("a dynamic index in a formula must be refused: %v", ps)
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

// A hand-written diagnosis whose fix has a patch and no expr is a problem, as generation would refuse it.
func TestDiagnosisFixNeedsItsExpr(t *testing.T) {
	ps := run(t, `{"type":"object","properties":{"port":{"type":"integer"}},
		"x-pack-diagnose":[{"id":"d","match":"x","cause":"c","fix":[{"patch":"/port"}]}]}`)
	found := false
	for _, p := range ps {
		if strings.Contains(p.Message, "a patch with its expr, or a command") {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %v", ps)
	}
}
