package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

// fakeBin builds the fake target once per test binary: pack-target-fake, in a directory PACK_TARGET_PATH names.
func fakeBin(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, Prefix+"fake"), "./testdata/fake").CombinedOutput()
	if err != nil {
		t.Fatalf("build the fake target: %v\n%s", err, out)
	}
	t.Setenv(EnvPath, dir)
	return dir
}

func open(t *testing.T, work string, settings map[string]string) target.Installation {
	t.Helper()
	t.Setenv("PACK_FAKE_DIR", work)
	in, err := target.Open("fake", "shop", settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.(interface{ Close() error }).Close() })
	return in
}

func TestTargetAsAProgramOfItsOwn(t *testing.T) {
	fakeBin(t)
	work := t.TempDir()
	in := open(t, work, map[string]string{"target": "fake"})

	if in.Namespace() != "fake-ns" {
		t.Fatalf("namespace = %q", in.Namespace())
	}

	// the placement goes over and comes back: replicas, the reach, and the raw part with an integer that stays one
	two := 2
	res, err := in.Resolve("kafka", render.Placement{Replicas: &two, Raw: map[string]any{"zone": 7},
		Reach: &render.Reach{Instances: []string{"h1", "h2"}, Ports: map[string]int{"b": 9092}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Service != "shop-kafka" || res.Address != "shop-kafka.zone7" || len(res.Instances) != 2 || res.Instances[1].Name != "shop-kafka-1" {
		t.Fatalf("resolved = %+v", res)
	}
	if res.Reach == nil || res.Reach.Ports["b"] != 9092 || len(res.Reach.Instances) != 2 {
		t.Fatalf("reach = %+v", res.Reach)
	}
	if one, _ := in.Resolve("zk", render.Placement{}); len(one.Instances) != 0 {
		t.Fatalf("a key with no replicas has no instances: %+v", one)
	}

	// a step, its events, the unit that went along
	var events []string
	err = in.Execute(context.Background(), target.Step{N: 1, Key: "kafka", Kind: target.Apply, Unit: &render.Unit{Image: "images/kafka:3"}},
		func(s string) { events = append(events, s) })
	if err != nil || len(events) != 1 || events[0] != "fake: apply kafka images/kafka:3" {
		t.Fatalf("execute: %v %v", err, events)
	}
	log, _ := os.ReadFile(filepath.Join(work, "log"))
	if string(log) != "apply kafka images/kafka:3\n" {
		t.Fatalf("log = %q", log)
	}

	// a failing step says why; its compensation does not fail
	t.Setenv("PACK_FAKE_FAIL", "apply kafka")
	in2 := open(t, work, nil)
	err = in2.Execute(context.Background(), target.Step{Key: "kafka", Kind: target.Apply}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "fake failure") {
		t.Fatalf("the failure is the target's own: %v", err)
	}
	if err := in2.Execute(context.Background(), target.Step{Key: "kafka", Kind: target.Apply, Rollback: true}, func(string) {}); err != nil {
		t.Fatalf("a rollback step: %v", err)
	}
}

func TestRecordAndObserve(t *testing.T) {
	fakeBin(t)
	in := open(t, t.TempDir(), nil)
	rec := in.(target.Recorder)
	if _, exists, err := rec.ReadInstalled(); err != nil || exists {
		t.Fatalf("nothing is installed: %v %v", exists, err)
	}
	if err := rec.WriteInstalled([]byte(`{"state":"installed"}`)); err != nil {
		t.Fatal(err)
	}
	if b, exists, err := rec.ReadInstalled(); err != nil || !exists || string(b) != `{"state":"installed"}` {
		t.Fatalf("record = %q %v %v", b, exists, err)
	}
	if err := rec.DeleteInstalled(); err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := rec.ReadInstalled(); exists {
		t.Fatal("deleted")
	}
	held, err := in.(target.Observer).Observe(context.Background())
	if err != nil || held["kafka"].Replicas != 3 || held["kafka"].Image != "images/kafka:3" {
		t.Fatalf("observed = %v %v", held, err)
	}
}

func TestNoSuchTarget(t *testing.T) {
	t.Setenv(EnvPath, t.TempDir())
	_, err := target.Open("nothing", "shop", nil)
	if err == nil || !strings.Contains(err.Error(), "pack-target-nothing") {
		t.Fatalf("the error says what was looked for: %v", err)
	}
}

func TestTokenIsRequired(t *testing.T) {
	err := authorized(context.Background(), "secret")
	if err == nil {
		t.Fatal("a call with no token is refused")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// With PACK_TARGET_PATH unset the programs are in .pack/bin of the directory pack runs in.
func TestDefaultDirIsDotPackBin(t *testing.T) {
	src := fakeBin(t)
	rel := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rel, ".pack", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(src, Prefix+"fake"), filepath.Join(rel, ".pack", "bin", Prefix+"fake")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPath, "")
	t.Chdir(rel)
	bin, err := Find("fake")
	if err != nil || filepath.Base(filepath.Dir(bin)) != "bin" || !strings.HasSuffix(filepath.Dir(bin), filepath.Join(".pack", "bin")) {
		t.Fatalf("Find = %q, %v", bin, err)
	}
	if _, err := Find("nothing"); err == nil || !strings.Contains(err.Error(), "pack refresh") {
		t.Errorf("the error says how to get it: %v", err)
	}
}

func TestDiscover(t *testing.T) {
	fakeBin(t)
	in := open(t, t.TempDir(), map[string]string{"target": "fake"})
	f, err := in.(target.Discoverer).Discover(context.Background(), []string{"h1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Hosts) != 1 || f.Hosts["h1"].MemoryMB != 2048 {
		t.Errorf("facts = %+v", f)
	}
}
