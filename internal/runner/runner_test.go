package runner

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/horchestra/pack/internal/graph"
)

func standSpecs() []graph.Spec {
	return []graph.Spec{
		{Name: "zk"}, {Name: "nn", Deps: []string{"zk"}}, {Name: "dn", Deps: []string{"nn", "zk"}},
		{Name: "stand", Deps: []string{"zk", "nn", "dn"}},
	}
}

func stand(t *testing.T) *graph.Graph {
	t.Helper()
	g, err := graph.New(standSpecs())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// fan is a root over three independent nodes: a front as wide as it gets.
func fan(t *testing.T) *graph.Graph {
	t.Helper()
	g, err := graph.New([]graph.Spec{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "fan", Deps: []string{"a", "b", "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRunOrder(t *testing.T) {
	g := stand(t)
	var mu sync.Mutex
	var seen []string
	res, err := Run(context.Background(), g, func(_ context.Context, n *graph.Node) error {
		mu.Lock()
		seen = append(seen, n.Name)
		mu.Unlock()
		return nil
	}, Options{Jobs: 4})
	if err != nil || res.Err() != nil {
		t.Fatal(err, res.Err())
	}
	if !slices.Equal(seen, []string{"zk", "nn", "dn", "stand"}) {
		t.Fatalf("seen = %v", seen)
	}
}

func TestRunReverse(t *testing.T) {
	g := stand(t)
	var seen []string
	_, _ = Reverse(context.Background(), g, func(_ context.Context, n *graph.Node) error {
		seen = append(seen, n.Name)
		return nil
	}, Options{Jobs: 1})
	if !slices.Equal(seen, []string{"stand", "dn", "nn", "zk"}) {
		t.Fatalf("seen = %v", seen)
	}
}

func TestRunParallelAndJobs(t *testing.T) {
	g := fan(t)
	for _, jobs := range []int{1, 2, 8} {
		var cur, peak atomic.Int32
		_, err := Run(context.Background(), g, func(_ context.Context, n *graph.Node) error {
			if n.Name == "fan" {
				return nil
			}
			c := cur.Add(1)
			for p := peak.Load(); c > p && !peak.CompareAndSwap(p, c); p = peak.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			cur.Add(-1)
			return nil
		}, Options{Jobs: jobs})
		if err != nil {
			t.Fatal(err)
		}
		want := int32(min(jobs, 3))
		if peak.Load() != want {
			t.Errorf("jobs=%d: peak = %d, want %d", jobs, peak.Load(), want)
		}
	}
}

func TestRunFailureSkipsDependentsOnly(t *testing.T) {
	g := fan(t)
	boom := errors.New("boom")
	res, err := Run(context.Background(), g, func(_ context.Context, n *graph.Node) error {
		if n.Name == "a" {
			return boom
		}
		return nil
	}, Options{Jobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Status{"a": Failed, "b": Done, "c": Done, "fan": Skipped}
	for name, st := range want {
		if got := res.Status[g.Node(name)]; got != st {
			t.Errorf("%s = %s, want %s", name, got, st)
		}
	}
	if !errors.Is(res.Err(), boom) {
		t.Errorf("err = %v", res.Err())
	}
}

func TestRunFailFast(t *testing.T) {
	g := fan(t)
	res, _ := Run(context.Background(), g, func(_ context.Context, n *graph.Node) error {
		if n.Name == "a" {
			return errors.New("boom")
		}
		return nil
	}, Options{Jobs: 1, FailFast: true})
	if res.Status[g.Node("b")] != Canceled || res.Status[g.Node("c")] != Canceled || res.Status[g.Node("fan")] != Skipped {
		t.Fatalf("status = %v", res.Status)
	}
}

func TestRunContextCanceled(t *testing.T) {
	g := stand(t)
	ctx, cancel := context.WithCancel(context.Background())
	res, err := Run(ctx, g, func(_ context.Context, n *graph.Node) error {
		cancel()
		return nil
	}, Options{Jobs: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if res.Status[g.Node("zk")] != Done || res.Status[g.Node("nn")] != Canceled {
		t.Fatalf("status = %v", res.Status)
	}
}

func TestResultErrNilSafe(t *testing.T) {
	var r *Result
	if r.Err() != nil {
		t.Fatal("a nil Result has no errors")
	}
}

// Run hands back a Result even when ctx is already done.
func TestRunResultOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Run(ctx, stand(t), func(context.Context, *graph.Node) error { return nil }, Options{Jobs: 1})
	if res == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("res = %v, err = %v", res, err)
	}
}

// A savepoint costs nothing, so it goes before whatever else is waiting.
func TestRunSavepointFirst(t *testing.T) {
	g, err := graph.New([]graph.Spec{
		{Name: "a"}, {Name: "b", Deps: []string{"a"}}, {Name: "savepoint:a", Deps: []string{"a"}, Savepoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	_, _ = Run(context.Background(), g, func(_ context.Context, n *graph.Node) error {
		seen = append(seen, n.Name)
		return nil
	}, Options{Jobs: 1})
	if !slices.Equal(seen, []string{"a", "savepoint:a", "b"}) {
		t.Fatalf("seen = %v", seen)
	}
}
