package graph

import (
	"strings"
	"testing"
)

func names(ns []*Node) string {
	var s []string
	for _, n := range ns {
		s = append(s, n.Name)
	}
	return strings.Join(s, ",")
}

func TestLayersAndClosure(t *testing.T) {
	g, err := New([]Spec{{Name: "deep", Deps: []string{"nn"}}, {Name: "nn", Deps: []string{"zk"}}, {Name: "zk"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(g.Order()); got != "zk,nn,deep" {
		t.Fatalf("order = %s", got)
	}
	var layers []string
	for _, l := range g.Layers() {
		layers = append(layers, names(l))
	}
	if got := strings.Join(layers, "|"); got != "zk|nn|deep" {
		t.Fatalf("layers = %s", got)
	}
	if got := names(g.Closure(g.Node("nn"))); got != "zk,nn" {
		t.Fatalf("closure = %s", got)
	}
}

func TestTwoNodesOfOneNameAreRefused(t *testing.T) {
	if _, err := New([]Spec{{Name: "zk"}, {Name: "zk"}}); err == nil {
		t.Fatal("a name is one node")
	}
	if _, err := New([]Spec{{Name: ""}}); err == nil {
		t.Fatal("a node has a name")
	}
}

func TestNew(t *testing.T) {
	g, err := New([]Spec{{Name: "dn", Deps: []string{"nn", "zk"}}, {Name: "nn", Deps: []string{"zk"}}, {Name: "zk"},
		{Name: "savepoint:zk", Deps: []string{"zk"}, Savepoint: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(g.Order()); got != "zk,nn,savepoint:zk,dn" {
		t.Fatalf("order = %s", got)
	}
	if _, err := New([]Spec{{Name: "a", Deps: []string{"b"}}, {Name: "b", Deps: []string{"a"}}}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a cycle must fail: %v", err)
	}
	if _, err := New([]Spec{{Name: "a", Deps: []string{"nope"}}}); err == nil {
		t.Fatal("an unknown dependency must fail")
	}
}

// What is behind a savepoint stays when a later step fails.
func TestCommitted(t *testing.T) {
	g, err := New([]Spec{{Name: "zk"}, {Name: "nn", Deps: []string{"zk"}}, {Name: "sp", Deps: []string{"zk"}, Savepoint: true}})
	if err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{"zk": true, "nn": true}
	if c := g.Committed(func(n *Node) bool { return done[n.Name] }); len(c) != 0 {
		t.Fatalf("the savepoint is not done: nothing is committed, got %d", len(c))
	}
	done["sp"] = true
	c := g.Committed(func(n *Node) bool { return done[n.Name] })
	if len(c) != 2 || !c[g.Node("zk")] || !c[g.Node("sp")] || c[g.Node("nn")] {
		t.Fatalf("committed = %v", c)
	}
}

func TestReverse(t *testing.T) {
	g, _ := New([]Spec{{Name: "zk"}, {Name: "nn", Deps: []string{"zk"}}})
	if got := names(g.Reverse()); got != "nn,zk" {
		t.Fatalf("reverse = %s", got)
	}
}
