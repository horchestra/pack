package session

import (
	"context"
	"errors"

	"github.com/horchestra/pack/pkg/adapter"
	"github.com/horchestra/pack/pkg/graph"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"

	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/internal/testpacks/tembo"
	"github.com/horchestra/pack/internal/workdir"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/memory"
)

// apply is what `pack apply` does: open, prepare, run.
func apply(t *testing.T, d *workdir.Dir) (*Change, *memory.Target) {
	t.Helper()
	s, m := open(t, d)
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c, m
}

func kinds(p *plan.Plan) map[string]string {
	out := map[string]string{}
	for _, c := range p.Changes {
		out[c.Key] = c.Kind
	}
	return out
}

func installed(t *testing.T, d *workdir.Dir) *saga.Record {
	t.Helper()
	rec, err := d.FileState().Load()
	if err != nil || rec == nil {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	return rec
}

// root is the directory of the release.
func root(d *workdir.Dir) string { return filepath.Dir(d.Path) }

func setValues(t *testing.T, d *workdir.Dir, v map[string]any) {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root(d), "values.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func marshal(v any) ([]byte, error) { return yamlMarshal(v) }

// A second apply of what is installed changes nothing: no step reaches the target.
func TestRepeatedApplyIsANoOp(t *testing.T) {
	d := stand(t, both)
	apply(t, d)
	c, m := apply(t, d)
	for key, kind := range kinds(c.Plan) {
		if kind != "none" {
			t.Errorf("%s: %s", key, kind)
		}
	}
	if steps := m.Steps(); len(steps) != 0 {
		t.Fatalf("steps = %+v", steps)
	}
	if rec := installed(t, d); rec.State != saga.Installed {
		t.Fatalf("record = %+v", rec)
	}
}

// A value of the config changes: that key alone is applied again, without the hooks of a version change.
func TestConfigUpgrade(t *testing.T) {
	d := stand(t, both)
	apply(t, d)
	setValues(t, d, map[string]any{"openbao": map[string]any{"api_addr": "https://bao2:8200"}, "postgres": map[string]any{"id": "n1"}})
	c, m := apply(t, d)
	if k := kinds(c.Plan); k["openbao"] != "config" || k["postgres"] != "none" {
		t.Fatalf("kinds = %v", k)
	}
	if got := applied(m); !slices.Equal(got, []string{"openbao"}) {
		t.Fatalf("applied = %v", got)
	}
	rec := installed(t, d)
	if rec.Previous == nil {
		t.Fatalf("the replaced values are kept: %+v", rec)
	}
}

// A key the target lost is seen as drift and put back, and the rest is left alone.
func TestDriftIsPutBack(t *testing.T) {
	d := stand(t, both)
	apply(t, d)

	s, m := open(t, d)
	m.Lose("postgres")
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Drifts) != 1 || c.Drifts[0].Key != "postgres" {
		t.Fatalf("drifts = %v", c.Drifts)
	}
	if k := kinds(c.Plan); k["postgres"] != "drift" || k["openbao"] != "none" {
		t.Fatalf("kinds = %v", k)
	}
	if !slices.ContainsFunc(c.Plan.Warnings, func(w string) bool { return strings.HasPrefix(w, "drift: postgres") }) {
		t.Fatalf("warnings = %v", c.Plan.Warnings)
	}
	if err := s.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := applied(m); !slices.Equal(got, []string{"postgres"}) {
		t.Fatalf("applied = %v", got)
	}
	s2, _ := open(t, d)
	c2, err := s2.Prepare(false)
	if err != nil || len(c2.Drifts) != 0 {
		t.Fatalf("after the fix: %v, %v", c2.Drifts, err)
	}
}

// An image changed behind pack's back is drift too.
func TestDriftOfAnImage(t *testing.T) {
	d := stand(t, both)
	apply(t, d)
	s, m := open(t, d)
	m.Lose("openbao")
	// the system holds openbao, but with another image
	if err := m.Execute(context.Background(), target.Step{Key: "openbao", Kind: target.Apply, Unit: &renderUnit}, nil); err != nil {
		t.Fatal(err)
	}
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Drifts) != 1 || !strings.Contains(c.Drifts[0].Why, "images/other:1") {
		t.Fatalf("drifts = %v", c.Drifts)
	}
}

// Without `observe`, the memory target does not tell what it holds, and there is no drift to see.
func TestNoDriftWhereTheTargetDoesNotTell(t *testing.T) {
	d := stand(t, both)
	b, _ := os.ReadFile(filepath.Join(root(d), "env.yaml"))
	b = []byte(strings.ReplaceAll(string(b), "observe: \"true\"\n", ""))
	if err := os.WriteFile(filepath.Join(root(d), "env.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	apply(t, d)
	s, _ := open(t, d)
	c, err := s.Prepare(false)
	if err != nil || len(c.Drifts) != 0 {
		t.Fatalf("drifts = %v, %v", c.Drifts, err)
	}
}

// OpenBaoNext is the next version of the pack: another image, and a hook that migrates before it is applied.
type openbaoNext struct {
	Bao      openbao.Config `json:"openbao"`
	Postgres *tembo.Config  `json:"postgres,omitempty"`
}

func (o *openbaoNext) SetDefaults() { o.Bao.LogLevel = "info" }
func (*openbaoNext) PackCompute() map[string]string {
	return (&openbao.OpenBao{}).PackCompute()
}
func (*openbaoNext) PackRules() map[string][]packschema.Rule { return (&openbao.OpenBao{}).PackRules() }

func (*openbaoNext) Meta() sdk.Meta {
	m := (&openbao.OpenBao{}).Meta()
	m.Version, m.Image = "2.2.0", "images/openbao:2.2.0"
	return m
}

func (*openbaoNext) PackRoot() packschema.Root {
	r := (&openbao.OpenBao{}).PackRoot()
	r.Hooks = []packschema.Hook{{
		Name: "migrate", Phase: "preUpgrade", Command: []string{"bao", "migrate"},
		Compensate: &packschema.Step{Image: "old", Command: []string{"bao", "migrate", "--undo"}},
	}, {
		Name: "unmigrate", Phase: "preDowngrade", Command: []string{"bao", "migrate", "--undo"},
	}}
	return r
}

// point makes the release use the next version of the pack.
func point(t *testing.T, d *workdir.Dir, p sdk.Pack, name string) *workdir.Dir {
	t.Helper()
	packs := filepath.Join(root(d), name)
	if err := sdk.Generate(packs, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root(d), "release.yaml"), []byte("pack: "+filepath.Join(packs, "openbao")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := release.Load(root(d), "")
	if err != nil {
		t.Fatal(err)
	}
	wd := workdir.New(rel, "")
	wd.Path = d.Path
	return wd
}

func stepKinds(m *memory.Target, key string) []string {
	var out []string
	for _, s := range m.Steps() {
		if s.Key == key {
			name := s.Kind
			if len(s.Name) > 0 {
				name += ":" + s.Name
			}
			if s.Rollback {
				name = "undo-" + name
			}
			out = append(out, name)
		}
	}
	return out
}

// A new version of a pack: its hooks run around the apply of that key only, the record says what was replaced,
// and going back is a downgrade with its own hooks.
func TestVersionUpgradeAndDowngrade(t *testing.T) {
	d := stand(t, both)
	apply(t, d)

	up := point(t, d, &openbaoNext{}, "packs2")
	c, m := apply(t, up)
	if c.Plan.Direction != "upgrade" {
		t.Fatalf("direction = %s", c.Plan.Direction)
	}
	if k := kinds(c.Plan); k["openbao"] != "image" || k["postgres"] != "none" {
		t.Fatalf("kinds = %v", k)
	}
	if got := stepKinds(m, "openbao"); !slices.Equal(got, []string{"hook:migrate", "apply", "wait"}) {
		t.Fatalf("steps of openbao = %v", got)
	}
	if got := stepKinds(m, "postgres"); len(got) != 0 {
		t.Fatalf("postgres was not touched: %v", got)
	}
	rec := installed(t, up)
	if rec.Version != "2.2.0" || rec.Previous == nil || rec.Previous.Version != "2.1.0" || rec.State != saga.Installed {
		t.Fatalf("record = %+v", rec)
	}

	// back to the first version
	down := point(t, d, &openbao.OpenBao{}, "packs")
	c, m = apply(t, down)
	if c.Plan.Direction != "downgrade" {
		t.Fatalf("direction = %s", c.Plan.Direction)
	}
	// the hooks are the version being installed's: 2.1.0 has none
	if got := stepKinds(m, "openbao"); !slices.Equal(got, []string{"apply", "wait"}) {
		t.Fatalf("steps of openbao = %v", got)
	}
	if rec := installed(t, down); rec.Version != "2.1.0" || rec.Previous == nil || rec.Previous.Version != "2.2.0" {
		t.Fatalf("record = %+v", rec)
	}
}

// An upgrade that fails is taken back: the previous version is applied again and the hook's compensation runs;
// the key that did not change is left alone.
func TestFailedUpgradeIsTakenBack(t *testing.T) {
	d := stand(t, both)
	apply(t, d)

	up := point(t, d, &openbaoNext{}, "packs2")
	s, m := failApply(t, up, "openbao")
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), c); err == nil {
		t.Fatal("must fail")
	}
	got := stepKinds(m, "openbao")
	if !slices.Contains(got, "hook:migrate") || !slices.Contains(got, "undo-hook:migrate") || !slices.Contains(got, "undo-apply") {
		t.Fatalf("steps of openbao = %v", got)
	}
	for _, st := range m.Steps() {
		if st.Rollback && st.Kind == target.Apply && st.Key == "openbao" && st.Unit != nil && !strings.HasSuffix(st.Unit.Image, "2.1.0") {
			t.Fatalf("the previous image is applied, not %s", st.Unit.Image)
		}
	}
	if got := stepKinds(m, "postgres"); len(got) != 0 {
		t.Fatalf("postgres was not touched: %v", got)
	}
	if rec := installed(t, up); rec.State != saga.Failed || rec.Version != "2.1.0" {
		t.Fatalf("what stays installed is the first version: %+v", rec)
	}
}

func yamlMarshal(v any) ([]byte, error) { return yaml.Marshal(v) }

var renderUnit = render.Unit{Key: "openbao", Image: "images/other:1"}

// A key the new version dropped is taken away before the record says the new version is installed: if that
// fails, the record stays in progress and the same apply finishes it.
func TestDroppedKeyIsRetriedAfterAFailure(t *testing.T) {
	d := stand(t, both)
	apply(t, d)

	setValues(t, d, map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8200"}})
	s, m := open(t, d)
	m.Fail = func(st target.Step) error {
		if st.Key == "postgres" && st.Kind == target.Remove {
			return errors.New("cannot remove")
		}
		return nil
	}
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), c); err == nil {
		t.Fatal("removing the dropped key must fail")
	}
	if rec := installed(t, d); rec.State == saga.Installed {
		t.Fatalf("the record must not say installed with postgres still there: %+v", rec)
	}

	s2, m2 := open(t, d)
	c2, err := s2.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Apply(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(removedKeys(m2), "postgres") {
		t.Fatalf("the retry removes postgres: %v", removedKeys(m2))
	}
	if rec := installed(t, d); rec.State != saga.Installed || rec.Saga != nil {
		t.Fatalf("after the retry: %+v", rec)
	}
}

// The id of an installation outlives its removal: what stays (credentials, data) is found by the next install.
func TestInstallationKeepsItsIdAcrossDelete(t *testing.T) {
	d := stand(t, both)
	s, _ := open(t, d)
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	s2, _ := open(t, d)
	_, _, rel, _ := s2.Installed()
	g, err := graph.New(adapter.Specs(rel, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Remove(context.Background(), g, rel); err != nil {
		t.Fatal(err)
	}
	s3, _ := open(t, d)
	if s3.ID != s.ID {
		t.Fatalf("a new install after delete is %s, the installation was %s", s3.ID, s.ID)
	}
}
