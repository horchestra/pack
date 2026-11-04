package graph

import (
	"fmt"
	"strings"
)

// Dot is the graph in Graphviz language: an edge goes from what is ready first to what waits for it; a savepoint is
// a diamond.
func (g *Graph) Dot() string {
	var b strings.Builder
	b.WriteString("digraph pack {\n\trankdir=LR;\n")
	for _, n := range g.nodes {
		if n.Savepoint {
			_, _ = fmt.Fprintf(&b, "\t%q [shape=diamond];\n", n.Name)
		} else {
			_, _ = fmt.Fprintf(&b, "\t%q;\n", n.Name)
		}
	}
	for _, e := range g.uniqueEdges() {
		_, _ = fmt.Fprintf(&b, "\t%q -> %q;\n", e.From.Name, e.To.Name)
	}
	b.WriteString("}\n")
	return b.String()
}

// Mermaid is the graph as a Mermaid flowchart, same shapes as Dot.
func (g *Graph) Mermaid() string {
	id := map[*Node]string{}
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	for i, n := range g.nodes {
		id[n] = fmt.Sprintf("n%d", i)
		if n.Savepoint {
			_, _ = fmt.Fprintf(&b, "\t%s{%q}\n", id[n], n.Name)
		} else {
			_, _ = fmt.Fprintf(&b, "\t%s[%q]\n", id[n], n.Name)
		}
	}
	for _, e := range g.uniqueEdges() {
		_, _ = fmt.Fprintf(&b, "\t%s --> %s\n", id[e.From], id[e.To])
	}
	return b.String()
}

func (g *Graph) uniqueEdges() []Edge {
	seen := map[[2]*Node]bool{}
	var out []Edge
	for _, e := range g.edges {
		k := [2]*Node{e.From, e.To}
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}
