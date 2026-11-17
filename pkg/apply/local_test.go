package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

const localTestInstallation = "5d50a133-42e2-4b7f-93c2-f0c0a6c48d17"

type localTestPack struct{}

func (*localTestPack) SetDefaults() {}
func (*localTestPack) Meta() sdk.Meta {
	return sdk.Meta{Name: "local-test", Version: "1.0.0"}
}
func (*localTestPack) PackRoot() packschema.Root {
	return packschema.Root{Packages: map[string]packschema.OSInstall{
		"debian": {Names: []string{"ca-certificates"}},
	}}
}

type localTestTarget struct {
	mu            sync.Mutex
	held          map[string]*render.Unit
	steps         []target.Step
	block         bool
	beforeExecute func()
}

func newLocalTestTarget() *localTestTarget {
	return &localTestTarget{held: map[string]*render.Unit{}}
}

func (*localTestTarget) Namespace() string { return "local-test" }
func (*localTestTarget) Resolve(key string, _ render.Placement) (*render.Resolved, error) {
	return &render.Resolved{Service: key, Workload: render.WorkloadPackages}, nil
}

func (t *localTestTarget) Execute(ctx context.Context, step target.Step, _ func(string)) error {
	if t.beforeExecute != nil {
		t.beforeExecute()
	}
	if t.block && !step.Rollback {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
	if step.Unit != nil {
		unit := *step.Unit
		t.held[step.Key] = &unit
	}
	return nil
}

func (t *localTestTarget) Inspect(_ context.Context, unit render.Unit, _ []render.Credential) (target.Hold, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	held := t.held[unit.Key]
	if held == nil {
		return target.HoldAbsent, nil
	}
	if reflect.DeepEqual(*held, unit) {
		return target.HoldMatches, nil
	}
	return target.HoldDiffers, nil
}

func (t *localTestTarget) stepCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.steps)
}

type nonInspectingTarget struct{ base *localTestTarget }

func (t nonInspectingTarget) Namespace() string { return t.base.Namespace() }
func (t nonInspectingTarget) Resolve(key string, placement render.Placement) (*render.Resolved, error) {
	return t.base.Resolve(key, placement)
}
func (t nonInspectingTarget) Execute(ctx context.Context, step target.Step, events func(string)) error {
	return t.base.Execute(ctx, step, events)
}

func localTestRelease(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw := []byte("state: observe\nid: " + localTestInstallation + "\n")
	if err := os.WriteFile(filepath.Join(dir, "release.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLocalRepeatedApplyIsNoOp(t *testing.T) {
	dir := localTestRelease(t)
	installation := newLocalTestTarget()
	var first *plan.Plan
	if err := Local(context.Background(), LocalOptions{
		Dir: dir, Pack: &localTestPack{}, Target: installation,
		Planned: func(p *plan.Plan) { first = p },
	}); err != nil {
		t.Fatal(err)
	}
	if first == nil || len(first.Steps) == 0 {
		t.Fatalf("the first apply planned no steps: %+v", first)
	}
	count := installation.stepCount()
	if count == 0 {
		t.Fatal("the first apply executed no steps")
	}

	var repeated *plan.Plan
	if err := Local(context.Background(), LocalOptions{
		Dir: dir, Pack: &localTestPack{}, Target: installation,
		Planned: func(p *plan.Plan) { repeated = p },
	}); err != nil {
		t.Fatal(err)
	}
	if repeated == nil || len(repeated.Steps) != 0 {
		t.Fatalf("the repeated apply is not a no-op: %+v", repeated)
	}
	if got := installation.stepCount(); got != count {
		t.Fatalf("the repeated apply executed steps: before %d, after %d", count, got)
	}
}

func TestLocalRequiresInspector(t *testing.T) {
	base := newLocalTestTarget()
	err := Local(context.Background(), LocalOptions{
		Dir: localTestRelease(t), Pack: &localTestPack{}, Target: nonInspectingTarget{base: base},
	})
	if err == nil || !strings.Contains(err.Error(), "state: observe") {
		t.Fatalf("a target without inspection was accepted: %v", err)
	}
	if got := base.stepCount(); got != 0 {
		t.Fatalf("a target without inspection executed %d steps", got)
	}
}

type failingTarget struct{ *localTestTarget }

func (t *failingTarget) Execute(ctx context.Context, step target.Step, events func(string)) error {
	if err := t.localTestTarget.Execute(ctx, step, events); err != nil {
		return err
	}
	if step.Kind == target.Apply && !step.Rollback {
		return errors.New("injected apply failure")
	}
	return nil
}

func TestLocalZeroHistoryDisablesRollback(t *testing.T) {
	dir := localTestRelease(t)
	if err := os.WriteFile(filepath.Join(dir, "env.yaml"), []byte("history_count: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installation := &failingTarget{localTestTarget: newLocalTestTarget()}
	var planned *plan.Plan
	err := Local(context.Background(), LocalOptions{
		Dir: dir, Pack: &localTestPack{}, Target: installation,
		Planned: func(p *plan.Plan) { planned = p },
	})
	if err == nil {
		t.Fatal("the injected failure was ignored")
	}
	if planned == nil || !planned.Policy.NoRollbackAll {
		t.Fatalf("zero history did not disable rollback in the plan: %+v", planned)
	}
	installation.mu.Lock()
	defer installation.mu.Unlock()
	for _, step := range installation.steps {
		if step.Rollback {
			t.Fatalf("zero history attempted a rollback step: %+v", step)
		}
	}
}

func TestLocalRejectsMismatchedTargetInstallation(t *testing.T) {
	installation := newLocalTestTarget()
	err := Local(context.Background(), LocalOptions{
		Dir: localTestRelease(t), Pack: &localTestPack{}, Target: installation,
		InstallationID: "018c2dca-2a5b-7d14-9db5-b83f37946dd1",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match release") {
		t.Fatalf("a target bound to another installation was accepted: %v", err)
	}
	if got := installation.stepCount(); got != 0 {
		t.Fatalf("the mismatched target executed %d steps", got)
	}
}

func TestLocalCancellation(t *testing.T) {
	installation := newLocalTestTarget()
	installation.block = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := Local(ctx, LocalOptions{Dir: localTestRelease(t), Pack: &localTestPack{}, Target: installation})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the apply did not return the context error: %v", err)
	}
}

type placementTarget struct {
	*localTestTarget
	placement render.Placement
}

func (t *placementTarget) Resolve(key string, placement render.Placement) (*render.Resolved, error) {
	t.placement = placement
	return t.localTestTarget.Resolve(key, placement)
}

func TestLocalUsesEnvironmentPlacements(t *testing.T) {
	dir := localTestRelease(t)
	env := []byte("keys:\n  local-test:\n    packages: []\n")
	if err := os.WriteFile(filepath.Join(dir, "env.yaml"), env, 0o600); err != nil {
		t.Fatal(err)
	}
	installation := &placementTarget{localTestTarget: newLocalTestTarget()}
	if err := Local(context.Background(), LocalOptions{
		Dir: dir, Pack: &localTestPack{}, Target: installation,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := installation.placement.Raw["packages"]; !ok {
		t.Fatalf("the environment placement was not rendered: %#v", installation.placement)
	}
}

func TestLocalReportsPlanBeforeApply(t *testing.T) {
	installation := newLocalTestTarget()
	planned := false
	installation.beforeExecute = func() {
		if !planned {
			t.Error("the target executed before the plan was reported")
		}
	}
	if err := Local(context.Background(), LocalOptions{
		Dir: localTestRelease(t), Pack: &localTestPack{}, Target: installation,
		Planned: func(p *plan.Plan) {
			if p == nil || len(p.Digest) == 0 {
				t.Errorf("the reported plan is not sealed: %+v", p)
			}
			planned = true
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !planned {
		t.Fatal("the plan was not reported")
	}
}
