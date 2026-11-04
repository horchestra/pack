package cmd

import (
	"context"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/secretstore"
)

// run.approve: auto-safe applies a plan without risks, refuses one with them unless --approve names its digest;
// plan asks for every plan; auto for none.
func TestApprove(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	t.Cleanup(func() { applyApprove = "" })
	values := func(name string) {
		write(t, d, "values.yaml", map[string]any{
			"openbao":  map[string]any{"api_addr": "https://bao:8200"},
			"postgres": map[string]any{"id": "n1", "pg_password": map[string]any{"secretRef": map[string]any{"name": name, "key": "pw"}}},
		})
	}
	values("a")
	pack(t, d, "apply") // a first installation: no risk
	values("b")
	ctx := context.Background()
	inRelease(t, d)
	s, err := open(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Prepare(false)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := approved(s, c.Plan, "", howApply); err == nil || classify(err).Code != api.CodeNeedsApproval || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("a credential changed, auto-safe: %v", err)
	}
	if err := approved(s, c.Plan, "sha256:0000", howApply); err == nil || !strings.Contains(err.Error(), "is "+c.Plan.Digest+" now") {
		t.Errorf("another digest: %v", err)
	}
	if n := whatNext(ctx); n.State != StatePlanned || len(n.Blocking) != 1 || n.Next[1] != "pack apply --approve "+c.Plan.Digest {
		t.Errorf("next: %+v", n)
	}
	out := pack(t, d, "apply", "--approve", c.Plan.Digest)
	if !strings.Contains(out, "installed openbao") {
		t.Errorf("approved: %s", out)
	}
	applyApprove = ""

	s.Env.Run.Approve = "auto"
	if err := approved(s, c.Plan, "", howApply); err != nil {
		t.Errorf("auto: %v", err)
	}
	s.Env.Run.Approve = "plan"
	c.Plan.Risks = nil
	if err := approved(s, c.Plan, "", howApply); err == nil {
		t.Error("plan: a plan with no risk still asks")
	}
	err = approved(s, c.Plan, "", howRollback(3))
	if !strings.Contains(err.Error(), "pack rollback --to 3 --approve "+c.Plan.Digest) ||
		!slices.Equal(nextOf(err, api.CodeNeedsApproval), []string{"pack rollback --to 3 --dry-run", "pack rollback --to 3 --approve " + c.Plan.Digest}) {
		t.Errorf("a rollback's refusal names the rollback: %v %v", err, nextOf(err, api.CodeNeedsApproval))
	}
	undo := &plan.Plan{Pack: "openbao", Direction: plan.DirUndo, Order: []string{"postgres"}}
	plan.Seal(undo, nil, nil)
	if err := approved(s, undo, "", howRollback(0)); err == nil || !strings.Contains(err.Error(), "pack rollback --approve "+undo.Digest) {
		t.Errorf("plan: undoing a saga asks too: %v", err)
	}
	s.Env.Run.Approve = "auto-safe"
	if err := approved(s, undo, "", howRollback(0)); err != nil {
		t.Errorf("auto-safe: undoing a saga is recovery, no risk: %v", err)
	}
	s.Env.Run.Approve = "never"
	if err := approved(s, c.Plan, "", howApply); err == nil || !strings.Contains(err.Error(), "run.approve") {
		t.Errorf("a policy nobody knows: %v", err)
	}
}

// digestOf is the digest a command printed: "plan sha256:…".
func digestOf(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^plan (sha256:[0-9a-f]+)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no digest in:\n%s", out)
	}
	return m[1]
}

// Under run.approve: plan every command asks, and runs by the digest its look-only form printed: the plan of pack plan
// is the one apply makes — passwords generated (secrets.provider: generate) and not kept yet included —, and rollback
// and delete print their own.
func TestApproveCommands(t *testing.T) {
	kept := &memStore{m: map[string]map[string]string{}}
	secretstore.Register("approve-test", func(secretstore.Config) (secretstore.Store, error) { return kept, nil })
	t.Cleanup(func() {
		applyApprove, rollbackApprove, deleteApprove, rollbackDry, deleteDry = "", "", "", false, false
	})
	// the example openbao: its postgres's superuser password is required, and the store generates it
	d := t.TempDir()
	write(t, d, "release.yaml", map[string]any{"pack": filepath.Join(examples, "openbao", "golden", "openbao")})
	write(t, d, "env.yaml", map[string]any{
		"target": "memory",
		"run":    map[string]any{"approve": "plan"},
		"store":  map[string]any{"provider": "approve-test"},
		"keys":   map[string]any{"postgres": map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "500m", "memory": "512Mi"}}}},
	})
	values := func(db string) {
		write(t, d, "values.yaml", map[string]any{"openbao": map[string]any{}, "postgres": map[string]any{"database": db}})
	}
	values("bao")
	out := pack(t, d, "apply", "--approve", digestOf(t, pack(t, d, "plan")))
	applyApprove = ""
	if !strings.Contains(out, "installed openbao") || len(kept.m) != 1 {
		t.Fatalf("apply --approve, %d password(s) kept: %s", len(kept.m), out)
	}
	values("bao2")
	pack(t, d, "apply", "--approve", digestOf(t, pack(t, d, "plan")))
	applyApprove = ""

	rollbackDry = true
	dry := pack(t, d, "rollback", "--dry-run")
	rollbackDry = false
	if out := pack(t, d, "rollback", "--approve", digestOf(t, dry)); !strings.Contains(out, "back to the previous version") {
		t.Errorf("rollback --approve: %s", out)
	}
	rollbackApprove = ""

	deleteDry = true
	dry = pack(t, d, "delete", "--dry-run")
	deleteDry = false
	if out := pack(t, d, "delete", "--approve", digestOf(t, dry)); !strings.Contains(out, "not installed any more") {
		t.Errorf("delete --approve: %s", out)
	}
}

// memStore is a secret store in memory.
type memStore struct {
	mu sync.Mutex
	m  map[string]map[string]string
}

func (s *memStore) Get(_ context.Context, path string) (map[string]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.m[path]
	return maps.Clone(f), ok, nil
}

func (s *memStore) Put(_ context.Context, path string, fields map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[path] = maps.Clone(fields)
	return nil
}

// examples is the directory of the example packs, taken before a test moves into a release.
var examples = must(filepath.Abs(filepath.Join("..", "examples")))

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
