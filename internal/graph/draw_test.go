package graph

import (
	"strings"
	"testing"
)

func TestDrawing(t *testing.T) {
	g, err := New([]Spec{{Name: "db"}, {Name: "app", Deps: []string{"db", "db"}}, {Name: "savepoint:up", Savepoint: true, Deps: []string{"app"}}})
	if err != nil {
		t.Fatal(err)
	}
	dot := g.Dot()
	for _, want := range []string{`"db" -> "app";`, `"app" -> "savepoint:up";`, `"savepoint:up" [shape=diamond];`} {
		if !strings.Contains(dot, want) {
			t.Errorf("dot lacks %s:\n%s", want, dot)
		}
	}
	if strings.Count(dot, `"db" -> "app"`) != 1 {
		t.Errorf("a repeated edge is drawn twice:\n%s", dot)
	}
	m := g.Mermaid()
	for _, want := range []string{"flowchart LR", `n0["db"]`, `n2{"savepoint:up"}`, "n0 --> n1", "n1 --> n2"} {
		if !strings.Contains(m, want) {
			t.Errorf("mermaid lacks %s:\n%s", want, m)
		}
	}
}
