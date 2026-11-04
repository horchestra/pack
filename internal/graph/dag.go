// Package graph is the dependency graph of a rendered pack: nodes, what each waits for, and the savepoints that
// say how far a rollback goes. It is acyclic or New fails with the cycle spelled out. The order comes from the
// pack's structure — a pack is installed after the packs it holds — which the render writes down as the specs
// New is given.
package graph

import (
	"slices"
	"strings"
)

// Node is one thing to install or take away: a key of the pack, or a savepoint.
type Node struct {
	Name string
	// Savepoint: once the node is done, it and everything it waits for stay installed when a later step fails — a
	// rollback stops here. PackRoot names the savepoints of a pack.
	Savepoint bool
}

// Edge From must be ready before To starts.
type Edge struct {
	From, To *Node
}

// Graph is the acyclic graph of the components a value holds.
type Graph struct {
	nodes  []*Node // in discovery order, root first
	byName map[string]*Node
	edges  []Edge
	deps   map[*Node][]*Node // To → its From's
	next   map[*Node][]*Node // From → its To's
}

// Nodes lists the vertices in discovery order (the root first).
func (g *Graph) Nodes() []*Node { return slices.Clone(g.nodes) }

// Node is the vertex of that name, nil when the value holds none.
func (g *Graph) Node(name string) *Node { return g.byName[name] }

// Edges lists the edges in the order they were found.
func (g *Graph) Edges() []Edge { return slices.Clone(g.edges) }

// Deps lists what n waits for, by name.
func (g *Graph) Deps(n *Node) []*Node { return sortNodes(g.deps[n]) }

// Dependents lists what waits for n, by name.
func (g *Graph) Dependents(n *Node) []*Node { return sortNodes(g.next[n]) }

func (g *Graph) index() {
	g.deps = map[*Node][]*Node{}
	g.next = map[*Node][]*Node{}
	seen := map[[2]*Node]bool{}
	for _, e := range g.edges {
		k := [2]*Node{e.From, e.To}
		if seen[k] {
			continue
		}
		seen[k] = true
		g.deps[e.To] = append(g.deps[e.To], e.From)
		g.next[e.From] = append(g.next[e.From], e.To)
	}
}

func sortNodes(ns []*Node) []*Node {
	ns = slices.Clone(ns)
	slices.SortFunc(ns, func(a, b *Node) int { return strings.Compare(a.Name, b.Name) })
	return ns
}
