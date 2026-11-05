// Package bustest is the test suite every Bus implementation passes (the SQL one here, NATS or Kafka when they
// come): order and position, claims without duplicates, acknowledgment, one leader, idempotent requests.
package bustest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/horchestra/pack/operator/bus"
)

// Test runs the whole suite against the buses newBuses gives: n buses on one fresh store (a leader has a
// contender on the same ground).
func Test(t *testing.T, newBuses func(t *testing.T, n int) []bus.Bus) {
	t.Helper()
	one := func(t *testing.T) bus.Bus { return newBuses(t, 1)[0] }
	t.Run("order", func(t *testing.T) { testOrder(t, one) })
	t.Run("position", func(t *testing.T) { testPosition(t, one) })
	t.Run("two subscribers, no duplicates", func(t *testing.T) { testNoDuplicates(t, one) })
	t.Run("one leader", func(t *testing.T) { testLeader(t, newBuses) })
	t.Run("a request once", func(t *testing.T) { testIdempotent(t, one) })
}

func testOrder(t *testing.T, newBus func(t *testing.T) bus.Bus) {
	t.Helper()
	b := newBus(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := b.Publish(ctx, bus.KindJob, map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := b.Subscribe(ctx, "g", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for i := 0; i < 3; i++ {
		m, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]int // jsonb on PostgreSQL writes the same JSON its own way: compared decoded
		if m.Kind != bus.KindJob || m.Decode(&body) != nil || body["n"] != i {
			t.Fatalf("event %d: %+v", i, m)
		}
		if err := sub.Ack(ctx, m.Seq); err != nil {
			t.Fatal(err)
		}
	}
	// acknowledged is behind: a new subscription of the group waits for what is new
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := sub.Next(short); err == nil {
		t.Error("an acknowledged event claimed again")
	}
}

func testPosition(t *testing.T, newBus func(t *testing.T) bus.Bus) {
	t.Helper()
	b := newBus(t)
	ctx := context.Background()
	var seqs []int64
	for i := 0; i < 3; i++ {
		s, err := b.Publish(ctx, "e", i)
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, s)
	}
	// a new group starts where it is told
	sub, err := b.Subscribe(ctx, "late", seqs[1])
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	m, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Seq != seqs[2] {
		t.Errorf("from %d: the first event is %d, want %d", seqs[1], m.Seq, seqs[2])
	}
}

// Two subscribers of one group, a stream of events: every event claimed exactly once, the union whole.
func testNoDuplicates(t *testing.T, newBus func(t *testing.T) bus.Bus) {
	t.Helper()
	b := newBus(t)
	ctx := context.Background()
	const n = 12
	for i := 0; i < n; i++ {
		if _, err := b.Publish(ctx, "e", i); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	run, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		sub, err := b.Subscribe(run, "shared", 0)
		if err != nil {
			t.Error(err)
			return
		}
		defer sub.Close()
		for {
			m, err := sub.Next(run)
			if err != nil {
				if run.Err() != nil {
					return // stopped: everything is seen
				}
				t.Error(err)
				return
			}
			mu.Lock()
			seen[m.Seq]++
			all := len(seen) == n
			mu.Unlock()
			if err := sub.Ack(run, m.Seq); err != nil {
				t.Error(err)
				return
			}
			if all {
				stop()
				return
			}
		}
	}
	wg.Add(2)
	go worker()
	go worker()
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("%d of %d events claimed", len(seen), n)
	}
	for seq, count := range seen {
		if count != 1 {
			t.Errorf("event %d claimed %d times", seq, count)
		}
	}
}

// One name, two contenders: one leader; fallen (its ttl run out), the other takes it.
func testLeader(t *testing.T, newBuses func(t *testing.T, n int) []bus.Bus) {
	t.Helper()
	pair := newBuses(t, 2)
	a, b := pair[0], pair[1]
	ctx := context.Background()
	okA, err := a.Leader(ctx, "sync", time.Minute)
	if err != nil || !okA {
		t.Fatalf("the first takes it: %v, %v", okA, err)
	}
	if okB, err := b.Leader(ctx, "sync", time.Minute); err != nil || okB {
		t.Fatalf("the second does not: %v, %v", okB, err)
	}
	// the holder keeps it; a short ttl run out gives it to the contender
	if ok, err := a.Leader(ctx, "sync", time.Minute); err != nil || !ok {
		t.Fatalf("kept: %v, %v", ok, err)
	}
	if ok, err := a.Leader(ctx, "brief", 80*time.Millisecond); err != nil || !ok {
		t.Fatalf("brief taken: %v, %v", ok, err)
	}
	time.Sleep(150 * time.Millisecond)
	if ok, err := b.Leader(ctx, "brief", time.Minute); err != nil || !ok {
		t.Fatalf("expired, taken by the other: %v, %v", ok, err)
	}
	if ok, err := a.Leader(ctx, "brief", time.Minute); err != nil || ok {
		t.Fatalf("the fallen holder is not leader again: %v, %v", ok, err)
	}
}

// The same request twice is one: the second submit returns the first, created nothing.
func testIdempotent(t *testing.T, newBus func(t *testing.T) bus.Bus) {
	t.Helper()
	b := newBus(t)
	ctx := context.Background()
	target := bus.Target{Release: "releases/prod/openbao"}
	params := bus.Params{Digest: "sha256:abc"}
	mk := func() bus.Request {
		r, err := bus.NewRequest("infra/packs", "0123456789abcdef0123456789abcdef01234567", target, bus.ActionApply, params, "alice")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, created, err := b.Submit(ctx, mk())
	if err != nil || !created {
		t.Fatalf("the first is created: %v, %v", created, err)
	}
	again, created, err := b.Submit(ctx, mk())
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != first.ID || again.Key != first.Key {
		t.Errorf("the second is the first: %+v vs %+v", again, first)
	}
	// and its key is the contract's: the same inputs, the same key
	if bus.RequestKey("infra/packs", "0123456789abcdef0123456789abcdef01234567", target, bus.ActionApply, params) != first.Key {
		t.Errorf("the key is not canonical: %s", first.Key)
	}
}
