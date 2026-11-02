package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/horchestra/pack/sdk"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/adapter"
	"github.com/horchestra/pack/pkg/graph"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/memory"
)

// A release of the packs the sdk generated: openbao, which holds tembo (installed as `postgres`) — the way an
// author writes them and an operator installs them.
func stand(t *testing.T, values map[string]any) *workdir.Dir {
	t.Helper()
	d := t.TempDir()
	packs := filepath.Join(d, "packs")
	if err := sdk.Generate(packs, &openbao.OpenBao{}); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		t.Helper()
		b, err := yaml.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("release.yaml", map[string]any{"pack": filepath.Join(packs, "openbao")})
	write("values.yaml", values)
	write("env.yaml", map[string]any{
		"target":  "memory",
		"observe": "true", // the memory target tells what it holds, so that drift can be seen
		"run":     map[string]any{"jobs": 1},
		"keys":    map[string]any{"postgres": map[string]any{"resources": map[string]any{"requests": map[string]any{"memory": "2Gi"}}}},
	})
	rel, err := release.Load(d, "")
	if err != nil {
		t.Fatal(err)
	}
	wd := workdir.New(rel, "")
	wd.Path = filepath.Join(d, ".pack")
	wd.Ensure()
	return wd
}

var both = map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8200"}, "postgres": map[string]any{"id": "n1"}}

func open(t *testing.T, d *workdir.Dir) (*Session, *memory.Target) {
	t.Helper()
	s, err := Open(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Inst.(*memory.Target)
}

func applied(m *memory.Target) []string {
	var out []string
	for _, s := range m.Steps() {
		if s.Kind == target.Apply && !s.Rollback {
			out = append(out, s.Key)
		}
	}
	return out
}

// The held pack is a key of its own, installed before the pack that holds it.
func TestPlanOfAPackThatHoldsAnother(t *testing.T) {
	s, _ := open(t, stand(t, both))
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, u := range c.ToRel.Units {
		keys = append(keys, u.Key)
	}
	if !slices.Equal(keys, []string{"postgres", "openbao"}) {
		t.Fatalf("units = %v", keys)
	}
	u := c.ToRel.Units[0]
	if u.Component != "tembo" || u.Image != "images/tembo:0.2.0" || len(u.Ports) != 3 || u.Ports[0].Name != "api" {
		t.Fatalf("tembo = %+v", u)
	}
	if len(u.Data) != 1 || u.Data[0].Path != "/var/lib/postgresql" || u.Data[0].Size != "10Gi" {
		t.Fatalf("the volume is the pack's own word: %+v", u.Data)
	}
	// the files of content/ and of the fields
	paths := map[string]bool{}
	for _, f := range u.Files {
		paths[f.Path] = true
	}
	if !paths["/etc/tembo/tembo.env"] || len(paths) != 1 {
		t.Fatalf("files = %v", paths)
	}
	var env []string
	for _, e := range u.Env {
		env = append(env, e.Name+"="+e.Value)
	}
	if !slices.Contains(env, "TEMBO_ID=n1") || !slices.Contains(env, "TEMBO_PG_PORT=5433") {
		t.Fatalf("env = %v", env)
	}
	bao := c.ToRel.Units[1]
	if bao.Component != "openbao" || len(bao.Files) != 1 || bao.Files[0].Path != "/etc/openbao/config.json" {
		t.Fatalf("openbao = %+v", bao)
	}
	if deps := c.ToRel.Report.Deps["openbao"]; !slices.Equal(deps, []string{"postgres"}) {
		t.Fatalf("deps = %v", deps)
	}
}

// The held pack is optional: not named in the values, it is not installed.
func TestTheHeldPackIsOptional(t *testing.T) {
	s, _ := open(t, stand(t, map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8200"}}))
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ToRel.Units) != 1 || c.ToRel.Units[0].Key != "openbao" {
		t.Fatalf("units = %+v", c.ToRel.Units)
	}
}

func TestValuesAreValidatedInTheOperatorsWords(t *testing.T) {
	s, _ := open(t, stand(t, map[string]any{"openbao": map[string]any{"ui": "yes"}}))
	_, err := s.Prepare(false)
	if err == nil {
		t.Fatal("api_addr is required and ui is a boolean")
	}
	t.Log(err)
}

func TestApplyThenDelete(t *testing.T) {
	d := stand(t, both)
	s, m := open(t, d)
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := applied(m); !slices.Equal(got, []string{"postgres", "openbao"}) {
		t.Fatalf("applied = %v", got)
	}
	rec, err := d.FileState().Load()
	if err != nil || rec.State != saga.Installed || rec.ID != s.ID || rec.Saga != nil {
		t.Fatalf("record = %+v, %v", rec, err)
	}

	s2, m2 := open(t, d)
	if s2.ID != s.ID {
		t.Fatalf("id %s != %s", s2.ID, s.ID)
	}
	_, _, rel, err := s2.Installed()
	if err != nil || rel == nil {
		t.Fatalf("installed: %v %v", rel, err)
	}
	g, err := graph.New(adapter.Specs(rel, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Remove(context.Background(), g, rel); err != nil {
		t.Fatal(err)
	}
	// the installation is gone, its id is not: the credentials and the objects it made are known by it
	if rec, _ := d.FileState().Load(); rec == nil || len(rec.Pack) > 0 || rec.ID != s2.ID {
		t.Fatalf("delete leaves the id and nothing else: %+v (id %s)", rec, s2.ID)
	}
	var removed []string
	for _, st := range m2.Steps() {
		if st.Kind == target.Remove {
			removed = append(removed, st.Key)
		}
	}
	if !slices.Equal(removed, []string{"openbao", "postgres"}) {
		t.Fatalf("removed = %v: the pack that holds goes first", removed)
	}
}

func failApply(t *testing.T, d *workdir.Dir, key string) (*Session, *memory.Target) {
	t.Helper()
	s, m := open(t, d)
	m.Fail = func(st target.Step) error {
		if st.Key == key && st.Kind == target.Apply && !st.Rollback {
			return errors.New("boom")
		}
		return nil
	}
	return s, m
}

func removedKeys(m *memory.Target) []string {
	var out []string
	for _, st := range m.Steps() {
		if st.Rollback && st.Kind == target.Remove {
			out = append(out, st.Key)
		}
	}
	return out
}

// A failure of the pack that holds the other takes back what is not behind a savepoint — and `postgres` is one,
// by the PackRoot of openbao: it stays; apply again finishes without installing it twice.
func TestFailureAfterASavepointKeepsIt(t *testing.T) {
	d := stand(t, both)
	s, m := failApply(t, d, "openbao")
	c, err := s.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), c); err == nil {
		t.Fatal("must fail")
	}
	rec, _ := d.FileState().Load()
	if rec == nil || rec.State != saga.Failed || rec.Saga == nil {
		t.Fatalf("record = %+v", rec)
	}
	if !slices.Contains(rec.Saga.Done, "postgres") || slices.Contains(rec.Saga.Done, "openbao") {
		t.Fatalf("done = %v", rec.Saga.Done)
	}
	if got := removedKeys(m); !slices.Equal(got, []string{"openbao"}) {
		t.Fatalf("taken back: %v", got)
	}

	s2, m2 := open(t, d)
	c2, err := s2.Prepare(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Apply(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
	if got := applied(m2); !slices.Equal(got, []string{"openbao"}) {
		t.Fatalf("applied again: %v — postgres is done and stays", got)
	}
	if rec, _ := d.FileState().Load(); rec == nil || rec.State != saga.Installed {
		t.Fatalf("record = %+v", rec)
	}
}

// An unfinished saga is taken back whole, the savepoints notwithstanding.
func TestRollbackOfAnUnfinishedSaga(t *testing.T) {
	d := stand(t, both)
	s, _ := failApply(t, d, "openbao")
	c, _ := s.Prepare(false)
	_ = s.Apply(context.Background(), c)

	s2, m2 := open(t, d)
	rec, _, curRel, err := s2.Installed()
	if err != nil || !rec.Unfinished() {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	_, toRel, err := s2.Version(rec.Saga.Installation)
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.New(adapter.Specs(toRel, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Undo(context.Background(), g, curRel, toRel); err != nil {
		t.Fatal(err)
	}
	if rec, _ := d.FileState().Load(); rec == nil || len(rec.Pack) > 0 || rec.Saga != nil || rec.ID != s2.ID {
		t.Fatalf("a first installation taken back leaves the id and nothing else: %+v (id %s)", rec, s2.ID)
	}
	if got := removedKeys(m2); !slices.Contains(got, "postgres") {
		t.Fatalf("taken back: %v", got)
	}
}

// The id is kept as soon as an installing command has it; a command that only looks keeps nothing.
func TestIdIsKeptByTheCommandThatInstalls(t *testing.T) {
	d := stand(t, both)
	looked, err := Open(context.Background(), d, true)
	if err != nil {
		t.Fatal(err)
	}
	looked.Close()
	if rec, _ := d.FileState().Load(); rec != nil {
		t.Fatalf("plan keeps nothing: %+v", rec)
	}
	first, err := Open(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := d.FileState().Load()
	if rec == nil || rec.ID != first.ID || len(rec.Pack) > 0 {
		t.Fatalf("record = %+v", rec)
	}
	again, err := Open(context.Background(), d, false) // the first apply failed before its first step; the next is the same installation
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("id %s != %s", again.ID, first.ID)
	}
	// and what is in the record is no installation: delete and rollback say so
	if rec, _, _, err := again.Installed(); err != nil || rec == nil || len(rec.Pack) > 0 {
		t.Fatalf("installed = %+v, %v", rec, err)
	}
}
