package openbao

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/target/memory"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/compute"
	"github.com/horchestra/pack/sdk/packschema"
)

// The author checks pass on the pack as generated: the schema, the rules, the
// pack it holds.
func TestVerify(t *testing.T) {
	problems, err := sdk.Verify(&OpenBao{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("%s", p)
	}
}

func TestDefaults(t *testing.T) {
	o := &OpenBao{}
	o.SetDefaults()
	if o.Bao.LogLevel != "info" || !o.Bao.DisableMlock {
		t.Errorf("defaults = %+v", o.Bao)
	}
	if o.Postgres != nil {
		t.Error("the embedded postgres is the operator's to choose, never a default")
	}
}

func TestMeta(t *testing.T) {
	m := (&OpenBao{}).Meta()
	if m.Name != "openbao" || m.Version != "2.6.5" || m.Image != "images/openbao:2.6.5" {
		t.Errorf("meta = %+v", m)
	}
}

// PackRoot: the readiness probe names a declared port, and the rollback after
// the embedded postgres stops there.
func TestRoot(t *testing.T) {
	root := (&OpenBao{}).PackRoot()
	if root.Ports["api"] != 8200 || root.Ports["cluster"] != 8201 {
		t.Errorf("ports = %v", root.Ports)
	}
	if root.Readiness == nil {
		t.Fatal("no readiness")
	}
	port, _, _ := strings.Cut(root.Readiness.HTTP, "/")
	if _, ok := root.Ports[port]; !ok {
		t.Errorf("readiness %q names a port that is not declared", root.Readiness.HTTP)
	}
	if len(root.Savepoints) != 1 || root.Savepoints[0] != "postgres" {
		t.Errorf("the rollback stops after the embedded postgres: %v", root.Savepoints)
	}
}

// The formula is on the storage leaf; with the embedded postgres chosen it
// derives the connection_url from the postgres pack's own words — its
// service, its named port, the database the operator gave it.
func TestCompute(t *testing.T) {
	formulas := (&OpenBao{}).PackCompute()
	expr, ok := formulas["/openbao/storage"]
	if !ok || len(formulas) != 1 {
		t.Fatalf("formulas = %v", formulas)
	}
	env, err := compute.Env()
	if err != nil {
		t.Fatal(err)
	}
	a, err := compute.Compile(env, expr)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]any{"pack": map[string]any{
		"installation": "test",
		"values":       map[string]any{"postgres": map[string]any{"database": "openbao", "superuser": map[string]any{"name": "postgres"}}},
		"keys":         map[string]any{"postgres": map[string]any{"instances": []any{"postgres"}, "ports": map[string]any{"pg": 5432}}},
	}}
	v, err := compute.Eval(env, a, vars)
	if err != nil {
		t.Fatal(err)
	}
	storage, _ := v.(map[string]any)
	conf, _ := storage["postgresql"].(map[string]any)
	wantURL := "postgres://postgres:${secret:test-postgres-superuser-password/password}@postgres:5432/openbao"
	if len(storage) != 1 || conf["connection_url"] != wantURL {
		t.Errorf("storage = %v, want the postgresql block with %s", v, wantURL)
	}
	// the words are the operator's: another database, another superuser name,
	// another port
	vars["pack"].(map[string]any)["values"] = map[string]any{
		"postgres": map[string]any{"database": "vault", "superuser": map[string]any{"name": "bao"}},
	}
	vars["pack"].(map[string]any)["keys"] = map[string]any{
		"postgres": map[string]any{"instances": []any{"pg-0"}, "ports": map[string]any{"pg": 6432}},
	}
	v, err = compute.Eval(env, a, vars)
	if err != nil {
		t.Fatal(err)
	}
	storage, _ = v.(map[string]any)
	conf, _ = storage["postgresql"].(map[string]any)
	if wantURL = "postgres://bao:${secret:test-postgres-superuser-password/password}@pg-0:6432/vault"; conf["connection_url"] != wantURL {
		t.Errorf("the operator's words: storage = %v, want %s", v, wantURL)
	}
	// no postgres chosen — pack.values is the empty map the render gives, and
	// the formula fills nothing
	v, err = compute.Eval(env, a, map[string]any{"pack": map[string]any{"values": map[string]any{}}})
	if err != nil || v != nil {
		t.Errorf("no postgres chosen: %v (%v), the formula fills nothing", v, err)
	}
}

// The rule holds exactly when: no postgres, or no hand-written storage.
func TestRule(t *testing.T) {
	var expr string
	for ptr, rules := range (&OpenBao{}).PackRules() {
		if ptr != "/openbao/storage" || len(rules) != 1 {
			t.Fatalf("rules = %v", (&OpenBao{}).PackRules())
		}
		expr = rules[0].Rule
		if len(rules[0].Phases) > 0 {
			t.Errorf("an unphased rule is the operator's document's (preMutate): %v", rules[0].Phases)
		}
	}
	env, err := compute.Env()
	if err != nil {
		t.Fatal(err)
	}
	a, err := compute.Compile(env, expr)
	if err != nil {
		t.Fatal(err)
	}
	// pack.values is the values of every key of the installation, as the
	// render gives them; values is the pack's own subtree
	storage := map[string]any{"openbao": map[string]any{"storage": map[string]any{"raft": map[string]any{}}}}
	withPack := func(values map[string]any, keys ...string) map[string]any {
		all := map[string]any{}
		for _, k := range keys {
			all[k] = map[string]any{}
		}
		return map[string]any{"values": values, "pack": map[string]any{"values": all}}
	}
	cases := []struct {
		name string
		vars map[string]any
		want bool
	}{
		{"neither postgres nor storage", withPack(map[string]any{"openbao": map[string]any{}}, "openbao"), true},
		{"no postgres, own storage", withPack(storage, "openbao"), true},
		{"postgres, no storage", withPack(map[string]any{"openbao": map[string]any{}}, "openbao", "postgres"), true},
		{"postgres, empty storage", withPack(map[string]any{"openbao": map[string]any{"storage": map[string]any{}}}, "openbao", "postgres"), true},
		{"postgres AND own storage", withPack(storage, "openbao", "postgres"), false},
	}
	for _, c := range cases {
		v, err := compute.Eval(env, a, c.vars)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if v != c.want {
			t.Errorf("%s: the rule is %v, want %v", c.name, v, c.want)
		}
	}
}

// The held pack is a $ref in the schema; the storage leaf carries the formula
// and the rule.
func TestSchema(t *testing.T) {
	raw, err := sdk.Schema(&OpenBao{})
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	props := s["properties"].(map[string]any)
	if props["postgres"].(map[string]any)["$ref"] != packschema.PackScheme+"postgres#" {
		t.Errorf("postgres = %v", props["postgres"])
	}
	if req := s["required"].([]any); len(req) != 1 || req[0] != "openbao" {
		t.Errorf("required = %v: the application's own configuration, the pack it holds is optional", req)
	}
	bao := props["openbao"].(map[string]any)
	if file := s["x-pack"].(map[string]any)["content"].([]any)[0].(map[string]any); file["path"] != "/openbao/config/config.json" || file["values"] != "openbao" {
		t.Errorf("the config file = %v", file)
	}
	storage := bao["properties"].(map[string]any)["storage"].(map[string]any)
	if got := storage["x-pack-compute"]; got != (&OpenBao{}).PackCompute()["/openbao/storage"] {
		t.Errorf("x-pack-compute = %v, want the formula", got)
	}
	rules, _ := storage["x-pack-rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("x-pack-rules = %v", storage)
	}
	rule, want := rules[0].(map[string]any), (&OpenBao{}).PackRules()["/openbao/storage"][0]
	if rule["rule"] != want.Rule || rule["message"] != want.Message {
		t.Errorf("rule = %v, want %q", rule, want)
	}
	if _, ok := rule["phases"]; ok {
		t.Errorf("an unphased rule is the operator's document's (preMutate): %v", rule)
	}
}

// ---- the pack renders

func loadOpenbao(t *testing.T) *bundle.Bundle {
	t.Helper()
	dir := t.TempDir()
	if err := sdk.Generate(dir, &OpenBao{}); err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Load(filepath.Join(dir, "openbao"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderOpenbao(b *bundle.Bundle, values map[string]any) (*render.Release, error) {
	return render.Render(b, render.Values{Values: values}, render.Context{
		Installation: "test",
		Target:       memory.New(),
		// the postgres pack's own minimum
		Keys: map[string]render.Placement{
			"postgres": {Resources: &render.Resources{Requests: map[string]string{"cpu": "500m", "memory": "512Mi"}}},
		},
	})
}

func withPostgres(openbao map[string]any) map[string]any {
	return map[string]any{
		"openbao": openbao,
		"postgres": map[string]any{
			"database":  "openbao",
			"superuser": map[string]any{"password": map[string]any{"secretRef": map[string]any{"name": "pg", "key": "password"}}},
		},
	}
}

// Every key a formula or a rule of the pack reads is a key of the pack.
func TestCheckBundle(t *testing.T) {
	ps, err := render.CheckBundle(loadOpenbao(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		t.Errorf("%s", p)
	}
}

// postgres chosen, storage not written: the rule holds and the formula fills
// the storage — with the database the operator gave postgres.
func TestPostgresFillsTheStorage(t *testing.T) {
	rel, err := renderOpenbao(loadOpenbao(t), withPostgres(map[string]any{"api_addr": "https://bao:8200"}))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	for _, c := range rel.Report.Computed {
		if c.Key == "openbao" && c.Pointer == "/openbao/storage" {
			v = c.Value
		}
	}
	storage, _ := v.(map[string]any)
	conf, _ := storage["postgresql"].(map[string]any)
	wantURL := "postgres://postgres:${secret:test-postgres-superuser-password/password}@postgres.memory:5432/openbao"
	if url, _ := conf["connection_url"].(string); url != wantURL {
		t.Fatalf("connection_url = %q, want %q (computed = %v)", url, wantURL, rel.Report.Computed)
	}
	// and it lands in the rendered config.json
	var config []byte
	for _, u := range rel.Units {
		for _, f := range u.Files {
			if f.Path == "/openbao/config/config.json" {
				config = f.Content
			}
		}
	}
	if !strings.Contains(string(config), wantURL) {
		t.Errorf("config.json:\n%s", config)
	}
}

// postgres chosen AND storage written by hand: the preMutate rule refuses it,
// with the message and the operator's pointer.
func TestPostgresForbidsHandWrittenStorage(t *testing.T) {
	_, err := renderOpenbao(loadOpenbao(t), withPostgres(map[string]any{
		"api_addr": "https://bao:8200",
		"storage":  map[string]any{"raft": map[string]any{}},
	}))
	if err == nil {
		t.Fatal("hand-written storage beside the embedded postgres must be refused")
	}
	if !strings.Contains(err.Error(), "(preMutate)") {
		t.Errorf("the refusal is the operator's document's, before the formulas: %v", err)
	}
	if !strings.Contains(err.Error(), "/openbao/storage: storage is filled from the embedded postgres") {
		t.Errorf("the refusal names the pointer and the message: %v", err)
	}
}

// No postgres: the rule holds of hand-written storage, and the formula fills
// nothing.
func TestHandWrittenStorageWithoutPostgres(t *testing.T) {
	rel, err := renderOpenbao(loadOpenbao(t), map[string]any{
		"openbao": map[string]any{
			"api_addr": "https://bao:8200",
			"storage":  map[string]any{"raft": map[string]any{"path": "/openbao/file"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Report.Computed) != 0 {
		t.Fatalf("no postgres, nothing to compute: %v", rel.Report.Computed)
	}
}

// An empty storage map is no storage: the rule's third disjunct holds of it
// and the formula fills it, as if the operator had written nothing.
func TestPostgresFillsAnEmptyStorage(t *testing.T) {
	rel, err := renderOpenbao(loadOpenbao(t), withPostgres(map[string]any{
		"api_addr": "https://bao:8200",
		"storage":  map[string]any{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	for _, c := range rel.Report.Computed {
		if c.Key == "openbao" && c.Pointer == "/openbao/storage" {
			v = c.Value
		}
	}
	if storage, _ := v.(map[string]any); storage["postgresql"] == nil {
		t.Errorf("an empty storage is the formula's to fill: %v", rel.Report.Computed)
	}
}
