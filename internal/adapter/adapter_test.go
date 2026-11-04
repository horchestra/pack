package adapter

import (
	"context"
	"slices"
	"testing"

	"github.com/horchestra/pack/internal/graph"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

type fake struct {
	rec   []byte
	steps []target.Step
}

func (f *fake) ReadInstalled() ([]byte, bool, error) { return f.rec, f.rec != nil, nil }
func (f *fake) WriteInstalled(r []byte) error        { f.rec = r; return nil }
func (f *fake) DeleteInstalled() error               { f.rec = nil; return nil }
func (f *fake) Execute(_ context.Context, s target.Step, _ func(string)) error {
	f.steps = append(f.steps, s)
	return nil
}

func release(version string, keys ...string) *render.Release {
	rel := &render.Release{Report: render.Report{Deps: map[string][]string{"kafka": {"zookeeper"}}}}
	for _, k := range keys {
		rel.Units = append(rel.Units, render.Unit{Key: k, Image: k + ":" + version})
	}
	return rel
}

func TestSpecs(t *testing.T) {
	rel := release("1", "zookeeper", "kafka")
	p := &plan.Plan{
		Steps: []plan.Step{{N: 1, Key: "zookeeper"}, {N: 2, Key: "zookeeper"}, {N: 3, Key: "kafka"}},
		Units: []plan.Unit{{From: 1, To: 2, Savepoint: "zookeeper", Keys: []string{"zookeeper"}}, {From: 3, To: 3, Keys: []string{"kafka"}}},
	}
	g, err := graph.New(Specs(rel, p))
	if err != nil {
		t.Fatal(err)
	}
	sp := g.Node(graph.SavepointPrefix + "zookeeper")
	if sp == nil || !sp.Savepoint || g.Node("kafka").Savepoint || g.Node("zookeeper").Savepoint {
		t.Fatal("a unit that names a savepoint adds a node of it; keys are not savepoints")
	}
	if deps := g.Deps(sp); len(deps) != 1 || deps[0].Name != "zookeeper" {
		t.Fatalf("the savepoint waits for the keys of its unit: %v", deps)
	}
	if g.Node(graph.SavepointPrefix+"") != nil || len(g.Nodes()) != 3 {
		t.Fatalf("a unit without a savepoint adds nothing: %d nodes", len(g.Nodes()))
	}
	if deps := g.Deps(g.Node("kafka")); len(deps) != 1 || deps[0].Name != "zookeeper" {
		t.Fatalf("deps = %v", deps)
	}
}

func TestApplyRunsTheKeysSteps(t *testing.T) {
	f := &fake{}
	p := &plan.Plan{Steps: []plan.Step{
		{N: 1, Key: "zookeeper", Kind: plan.Apply}, {N: 2, Key: "zookeeper", Kind: plan.Wait},
		{N: 3, Key: "kafka", Kind: plan.Apply},
	}}
	e := &Exec{Plan: p, To: release("2", "zookeeper", "kafka"), X: f}
	if err := e.Apply(context.Background(), &graph.Node{Name: "zookeeper"}); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, s := range f.steps {
		kinds = append(kinds, s.Key+" "+s.Kind)
		if s.Unit == nil || s.Unit.Image != "zookeeper:2" {
			t.Errorf("unit = %+v", s.Unit)
		}
	}
	if !slices.Equal(kinds, []string{"zookeeper apply", "zookeeper wait"}) {
		t.Fatalf("steps = %v", kinds)
	}
}

func TestUndo(t *testing.T) {
	n := &graph.Node{Name: "kafka"}
	for _, tc := range []struct {
		name     string
		from, to *render.Release
		kind     string
		unit     string // image of Unit; "" — none
	}{
		{"first install: the key goes", nil, release("2", "kafka"), "remove", ""},
		{"upgrade: back to the installed unit", release("1", "kafka"), release("2", "kafka"), "apply", "kafka:1"},
		{"upgrade of an added key: it goes", release("1", "zookeeper"), release("2", "kafka"), "remove", ""},
		{"delete: the key goes", release("1", "kafka"), nil, "remove", ""},
	} {
		f := &fake{}
		if err := (&Exec{From: tc.from, To: tc.to, X: f}).Undo(context.Background(), n); err != nil {
			t.Fatal(tc.name, err)
		}
		if len(f.steps) != 1 || f.steps[0].Kind != tc.kind || !f.steps[0].Rollback {
			t.Fatalf("%s: steps = %+v", tc.name, f.steps)
		}
		got := ""
		if f.steps[0].Unit != nil {
			got = f.steps[0].Unit.Image
		}
		if got != tc.unit {
			t.Errorf("%s: unit = %q, want %q", tc.name, got, tc.unit)
		}
		if tc.kind == "remove" && f.steps[0].Previous == nil {
			t.Errorf("%s: a remove must say what it removes", tc.name)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	f := &fake{}
	s := Store{X: f}
	if r, err := s.Load(); r != nil || err != nil {
		t.Fatalf("empty: %v %v", r, err)
	}
}

func kindsOf(steps []target.Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Kind+" "+s.Name)
	}
	return out
}

// A key the upgrade did not change is not touched by the undo.
func TestUndoLeavesAnUnchangedKeyAlone(t *testing.T) {
	f := &fake{}
	same := release("1", "kafka")
	e := &Exec{From: same, To: release("1", "kafka"), X: f}
	if err := e.Undo(context.Background(), &graph.Node{Name: "kafka"}); err != nil || len(f.steps) != 0 {
		t.Fatalf("steps = %+v, %v", f.steps, err)
	}
	p := &plan.Plan{Steps: []plan.Step{{N: 1, Key: "zookeeper", Kind: plan.Apply}}}
	e = &Exec{Plan: p, From: release("1", "zookeeper", "kafka"), To: release("2", "zookeeper", "kafka"), X: f}
	if err := e.Undo(context.Background(), &graph.Node{Name: "kafka"}); err != nil || len(f.steps) != 0 {
		t.Fatalf("the plan has no step for kafka: steps = %+v, %v", f.steps, err)
	}
}

// What a hook did is taken back by its compensation, last first, and the key goes away if it was added.
func TestUndoCompensatesHooksAndRemovesAnAddedKey(t *testing.T) {
	p := &plan.Plan{Steps: []plan.Step{
		{N: 1, Key: "pg", Kind: plan.Hook, Name: "init", CompensateCommand: []string{"drop"}, CompensateImage: "pg:tool"},
		{N: 2, Key: "pg", Kind: plan.Apply},
		{N: 3, Key: "pg", Kind: plan.Hook, Name: "seed", CompensateCommand: []string{"unseed"}},
		{N: 4, Key: "pg", Kind: plan.Hook, Name: "irreversible"},
	}}
	f := &fake{}
	e := &Exec{Plan: p, To: release("1", "pg"), X: f}
	if err := e.Undo(context.Background(), &graph.Node{Name: "pg"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"hook seed", "remove ", "hook init"}
	if got := kindsOf(f.steps); !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if f.steps[2].Image != "pg:tool" || !slices.Equal(f.steps[2].Command, []string{"drop"}) {
		t.Fatalf("the compensation runs its own image and command: %+v", f.steps[2])
	}
}

// An upgrade is undone by the plan's downgrade hooks around applying the previous unit.
func TestUndoDowngradeAroundThePreviousUnit(t *testing.T) {
	p := &plan.Plan{
		Steps: []plan.Step{{N: 1, Key: "pg", Kind: plan.Apply}},
		Rollback: map[string]plan.KeyRollback{"pg": {
			Stop: true,
			Pre:  []plan.Step{{Name: "pre-down"}},
			Post: []plan.Step{{Name: "post-down"}},
		}},
	}
	f := &fake{}
	e := &Exec{Plan: p, From: release("1", "pg"), To: release("2", "pg"), X: f}
	if err := e.Undo(context.Background(), &graph.Node{Name: "pg"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop ", "hook pre-down", "apply ", "hook post-down"}
	if got := kindsOf(f.steps); !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if f.steps[2].Unit.Image != "pg:1" || f.steps[2].Previous.Image != "pg:2" {
		t.Fatalf("apply goes back to the previous unit: %+v", f.steps[2])
	}
}

// Within the process that ran it, only the steps begun are undone: a hook never reached is not compensated.
func TestUndoOnlyWhatWasBegun(t *testing.T) {
	p := &plan.Plan{Steps: []plan.Step{
		{N: 1, Key: "pg", Kind: plan.Hook, Name: "init", CompensateCommand: []string{"drop"}},
		{N: 2, Key: "pg", Kind: plan.Apply},
		{N: 3, Key: "pg", Kind: plan.Hook, Name: "seed", CompensateCommand: []string{"unseed"}},
	}}
	e := &Exec{Plan: p, To: release("1", "pg"), X: &failOn{n: 2}}
	if err := e.Apply(context.Background(), &graph.Node{Name: "pg"}); err == nil {
		t.Fatal("the apply fails")
	}
	f := &fake{}
	e.X = f
	if err := e.Undo(context.Background(), &graph.Node{Name: "pg"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"remove ", "hook init"} // step 3 was never begun
	if got := kindsOf(f.steps); !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

// failOn fails the step of that number.
type failOn struct{ n int }

func (failOn) ReadInstalled() ([]byte, bool, error) { return nil, false, nil }
func (failOn) WriteInstalled([]byte) error          { return nil }
func (failOn) DeleteInstalled() error               { return nil }
func (f *failOn) Execute(_ context.Context, s target.Step, _ func(string)) error {
	if s.N == f.n {
		return context.DeadlineExceeded
	}
	return nil
}

// A step runs on the target its plan step names.
func TestUndoRunsOnTheKeysTarget(t *testing.T) {
	p := &plan.Plan{Steps: []plan.Step{{N: 1, Key: "edge", Kind: plan.Apply, Target: "edge"}}}
	main, edge := &fake{}, &fake{}
	e := &Exec{Plan: p, To: release("1", "edge"), X: main, Execs: map[string]target.Executor{"edge": edge}}
	if err := e.Undo(context.Background(), &graph.Node{Name: "edge"}); err != nil {
		t.Fatal(err)
	}
	if len(main.steps) != 0 || len(edge.steps) != 1 {
		t.Fatalf("main %d, edge %d", len(main.steps), len(edge.steps))
	}
}
