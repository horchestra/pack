package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

func TestResolve(t *testing.T) {
	m := New()
	one, err := m.Resolve("zk", render.Placement{})
	if err != nil || one.Address != "zk.memory" || len(one.Instances) != 0 {
		t.Fatalf("single = %+v, %v", one, err)
	}
	three := 3
	many, err := m.Resolve("dn", render.Placement{Replicas: &three})
	if err != nil || len(many.Instances) != 3 || many.Instances[2].Name != "dn-2" {
		t.Fatalf("replicas = %+v, %v", many, err)
	}
	zero := 0
	if _, err := m.Resolve("dn", render.Placement{Replicas: &zero}); err == nil {
		t.Fatal("no replicas is no key")
	}
}

func TestExecuteAndRecord(t *testing.T) {
	m := New()
	m.Fail = func(s target.Step) error {
		if s.Key == "bad" {
			return errors.New("boom")
		}
		return nil
	}
	if err := m.Execute(context.Background(), target.Step{Key: "ok", Kind: target.Apply}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Execute(context.Background(), target.Step{Key: "bad", Kind: target.Apply}, nil); err == nil {
		t.Fatal("must fail")
	}
	if got := m.Steps(); len(got) != 1 || got[0].Key != "ok" {
		t.Fatalf("steps = %+v", got)
	}
	if _, has, _ := m.ReadInstalled(); has {
		t.Fatal("no record yet")
	}
	_ = m.WriteInstalled([]byte("x"))
	if r, has, _ := m.ReadInstalled(); !has || string(r) != "x" {
		t.Fatal("the record is kept")
	}
	_ = m.DeleteInstalled()
	if _, has, _ := m.ReadInstalled(); has {
		t.Fatal("the record is deleted")
	}
	if _, err := target.Open("memory", "id", nil); err != nil {
		t.Fatalf("registered: %v", err)
	}
}
