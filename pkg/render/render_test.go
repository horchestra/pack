package render_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target/memory"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

func loadOpenbao(t *testing.T) *bundle.Bundle {
	t.Helper()
	dir := t.TempDir()
	if err := sdk.Generate(dir, &openbao.OpenBao{}); err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Load(filepath.Join(dir, "openbao"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderIt(b *bundle.Bundle, values map[string]any) (*render.Release, error) {
	return render.Render(b, render.Values{Values: values}, render.Context{
		Installation: "test",
		Target:       memory.New(),
		// tembo's minimum: memory ≥ 1Gi
		Keys: map[string]render.Placement{
			"postgres": {Resources: &render.Resources{Requests: map[string]string{"memory": "1Gi"}}},
		},
	})
}

// postgres chosen, storage not written: the preMutate rule holds and the
// formula fills the storage — the rule does not trip on what it filled.
func TestPostgresFillsTheStorage(t *testing.T) {
	rel, err := renderIt(loadOpenbao(t), map[string]any{
		"postgres": map[string]any{},
		"openbao":  map[string]any{"api_addr": "https://bao:8200"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var v any
	var ok bool
	for _, c := range rel.Report.Computed {
		if c.Key == "openbao" && c.Pointer == "/openbao/storage" {
			v, ok = c.Value, true
		}
	}
	if !ok {
		t.Fatalf("the formula did not fill the storage: %v", rel.Report.Computed)
	}
	blocks, _ := v.([]any)
	if len(blocks) != 1 {
		t.Fatalf("storage = %v", v)
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "postgresql" {
		t.Fatalf("storage = %v", block)
	}
	conf, _ := block["config"].(map[string]any)
	if url, _ := conf["connection_url"].(string); url != "postgres://postgres:5432/openbao" {
		t.Fatalf("connection_url = %q", url)
	}
}

// postgres chosen AND storage written by hand: the preMutate rule refuses it,
// with the author's message and the operator's pointer.
func TestPostgresForbidsHandWrittenStorage(t *testing.T) {
	_, err := renderIt(loadOpenbao(t), map[string]any{
		"postgres": map[string]any{},
		"openbao": map[string]any{
			"api_addr": "https://bao:8200",
			"storage":  []any{map[string]any{"type": "raft"}},
		},
	})
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
	rel, err := renderIt(loadOpenbao(t), map[string]any{
		"openbao": map[string]any{
			"api_addr": "https://bao:8200",
			"storage":  []any{map[string]any{"type": "raft"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Report.Computed) != 0 {
		t.Fatalf("no postgres, nothing to compute: %v", rel.Report.Computed)
	}
}

// Db is a pack with a formula; a pack that holds two of them has two computed values at one pointer.
type Db struct {
	URL string `json:"url,omitempty"`
}

func (*Db) SetDefaults() {}
func (*Db) Meta() sdk.Meta {
	return sdk.Meta{Name: "db", Version: "1", Artifact: "oci://packs/db", Dir: "/nonexistent"}
}
func (*Db) PackCompute() map[string]string { return map[string]string{"/url": "'db://' + me.service"} }
func (*Db) PackRoot() packschema.Root      { return packschema.Root{} }

type Pair struct {
	A *Db `json:"a,omitempty"`
	B *Db `json:"b,omitempty"`
}

func (*Pair) SetDefaults() {}
func (*Pair) Meta() sdk.Meta {
	return sdk.Meta{Name: "pair", Version: "1", Artifact: "oci://packs/pair", Dir: "/nonexistent"}
}
func (*Pair) PackRoot() packschema.Root { return packschema.Root{} }

// The same pack twice: the computed values are told apart by the key of the instance, not by a string the author
// had to make up.
func TestComputedOfTwoInstancesOfOnePack(t *testing.T) {
	dir := t.TempDir()
	if err := sdk.Generate(dir, &Pair{}); err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Load(filepath.Join(dir, "pair"))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := render.Render(b, render.Values{Values: map[string]any{"a": map[string]any{}, "b": map[string]any{}}},
		render.Context{Installation: "t", Target: memory.New()})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range rel.Report.Computed {
		if c.Pointer != "/url" {
			t.Fatalf("pointer = %q: it is the one the author wrote", c.Pointer)
		}
		got[c.Key], _ = c.Value.(string)
	}
	if len(rel.Report.Computed) != 2 || got["a"] != "db://a" || got["b"] != "db://b" {
		t.Fatalf("computed = %+v", rel.Report.Computed)
	}
}

// ---- rules and the keys formulas name

func loadPack(t *testing.T, root sdk.Pack) *bundle.Bundle {
	t.Helper()
	dir := t.TempDir()
	if err := sdk.Generate(dir, root); err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Load(filepath.Join(dir, root.Meta().Name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderPack(b *bundle.Bundle, values map[string]any) (*render.Release, error) {
	return render.Render(b, render.Values{Values: values}, render.Context{Installation: "t", Target: memory.New()})
}

// Calc has a value, one computed from it, and a rule about each phase.
type Calc struct {
	Port int    `json:"port,omitempty"`
	URL  string `json:"url,omitempty"`
}

func (*Calc) SetDefaults()              {}
func (*Calc) PackRoot() packschema.Root { return packschema.Root{} }
func (*Calc) Meta() sdk.Meta {
	return sdk.Meta{Name: "calc", Version: "1", Artifact: "oci://packs/calc", Dir: "/nonexistent"}
}
func (*Calc) PackCompute() map[string]string {
	return map[string]string{"/url": "'host:' + string(values.port)"}
}
func (*Calc) PackRules() map[string][]packschema.Rule {
	return map[string][]packschema.Rule{
		// the document as written: the port must be there
		"/port": {{Rule: "values.port > 0", Message: "the port is the operator's to give"}},
		// the effective document: what the formula wrote is in it
		"/url": {{Rule: "values.url.endsWith(':8080')", Phases: []packschema.Phase{packschema.PostMutate}, Message: "the url must point at 8080"}},
	}
}

func TestRulesOfBothPhases(t *testing.T) {
	b := loadPack(t, &Calc{})
	if _, err := renderPack(b, map[string]any{"port": 8080}); err != nil {
		t.Fatalf("both rules hold: %v", err)
	}
	_, err := renderPack(b, map[string]any{"port": 0})
	if err == nil || !strings.Contains(err.Error(), "(preMutate)") || !strings.Contains(err.Error(), "the port is the operator's to give") {
		t.Fatalf("a preMutate rule: %v", err)
	}
	// a port that is there but not 8080: only the rule about the formula's answer can see it
	_, err = renderPack(b, map[string]any{"port": 9090})
	if err == nil || !strings.Contains(err.Error(), "(postMutate)") || !strings.Contains(err.Error(), "/url: the url must point at 8080") {
		t.Fatalf("a postMutate rule: %v", err)
	}
}

// Odd has a rule that yields no boolean and one that cannot be evaluated.
type Odd struct {
	Port int    `json:"port,omitempty"`
	Name string `json:"name,omitempty"`
}

func (*Odd) SetDefaults()              {}
func (*Odd) PackRoot() packschema.Root { return packschema.Root{} }
func (*Odd) Meta() sdk.Meta {
	return sdk.Meta{Name: "odd", Version: "1", Artifact: "oci://packs/odd", Dir: "/nonexistent"}
}
func (*Odd) PackRules() map[string][]packschema.Rule {
	return map[string][]packschema.Rule{
		"/port": {{Rule: "values.port"}},
		"/name": {{Rule: "values.nothing.at.all == 'x'"}},
	}
}

func TestARuleThatIsNotABooleanOrCannotBeEvaluatedIsARefusal(t *testing.T) {
	_, err := renderPack(loadPack(t, &Odd{}), map[string]any{"port": 7, "name": "n"})
	if err == nil {
		t.Fatal("both rules must be refused")
	}
	for _, want := range []string{"/port: the rule yielded 7, not a boolean", "/name: "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in:\n%v", want, err)
		}
	}
}

// Ghost reads a key its pack does not have.
type Ghost struct {
	URL string `json:"url,omitempty"`
}

func (*Ghost) SetDefaults()              {}
func (*Ghost) PackRoot() packschema.Root { return packschema.Root{} }
func (*Ghost) Meta() sdk.Meta {
	return sdk.Meta{Name: "ghost", Version: "1", Artifact: "oci://packs/ghost", Dir: "/nonexistent"}
}
func (*Ghost) PackCompute() map[string]string {
	return map[string]string{"/url": "has(pack.values.postgres) ? pack.keys.postgres.service : null"}
}

func TestAFormulaThatNamesNoKeyOfThePackIsReported(t *testing.T) {
	ps, err := render.CheckBundle(loadPack(t, &Ghost{}))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range ps {
		found = found || (p.Path == "/url" && strings.Contains(p.Message, "pack.keys.postgres") && strings.Contains(p.Message, "not a key of the pack"))
	}
	if !found {
		t.Fatalf("problems = %v", ps)
	}
	// ... and the pack whose formulas name its own held pack has none
	ps, err = render.CheckBundle(loadOpenbao(t))
	if err != nil || len(ps) != 0 {
		t.Fatalf("openbao: %v, %v", ps, err)
	}
}
