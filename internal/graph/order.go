package graph

import (
	"slices"
)

// Order is a topological order: every node after everything it waits for.
// Ties go by name, so the order is the same on every run.
func (g *Graph) Order() []*Node {
	var out []*Node
	for _, layer := range g.Layers() {
		out = append(out, layer...)
	}
	return out
}

// Layers groups the nodes by depth: layer 0 waits for nothing, layer k waits
// only for layers below k. The nodes of one layer can start together; it is
// the widest parallel front a scheduler could use if every step took the same time.
func (g *Graph) Layers() [][]*Node {
	left := map[*Node]int{}
	var layer []*Node
	for _, n := range g.nodes {
		left[n] = len(g.deps[n])
		if left[n] == 0 {
			layer = append(layer, n)
		}
	}
	var out [][]*Node
	for len(layer) > 0 {
		layer = sortNodes(layer)
		out = append(out, layer)
		var nextLayer []*Node
		for _, n := range layer {
			for _, m := range g.next[n] {
				if left[m]--; left[m] == 0 {
					nextLayer = append(nextLayer, m)
				}
			}
		}
		layer = nextLayer
	}
	return out
}

// Reverse is Order read backwards: what a teardown follows.
func (g *Graph) Reverse() []*Node {
	o := g.Order()
	slices.Reverse(o)
	return o
}

// Closure is n and everything it waits for, transitively, in Order.
func (g *Graph) Closure(n *Node) []*Node {
	in := map[*Node]bool{}
	var visit func(*Node)
	visit = func(m *Node) {
		if in[m] {
			return
		}
		in[m] = true
		for _, d := range g.deps[m] {
			visit(d)
		}
	}
	visit(n)
	var out []*Node
	for _, m := range g.Order() {
		if in[m] {
			out = append(out, m)
		}
	}
	return out
}

// cycle returns the names along one cycle (first name repeated at the end), nil when acyclic.
func (g *Graph) cycle() []string {
	const (
		white = iota
		grey
		black
	)
	color := map[*Node]int{}
	var stack []*Node
	var found []string
	var visit func(*Node) bool
	visit = func(n *Node) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range sortNodes(g.next[n]) {
			switch color[m] {
			case grey:
				i := slices.Index(stack, m)
				for _, s := range stack[i:] {
					found = append(found, s.Name)
				}
				found = append(found, m.Name)
				return true
			case white:
				if visit(m) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, n := range sortNodes(g.nodes) {
		if color[n] == white && visit(n) {
			return found
		}
	}
	return nil
}

// Committed are the nodes a rollback leaves alone: every savepoint for which done says true, with everything it
// waits for, transitively.
func (g *Graph) Committed(done func(*Node) bool) map[*Node]bool {
	out := map[*Node]bool{}
	for _, n := range g.nodes {
		if n.Savepoint && done(n) {
			for _, m := range g.Closure(n) {
				out[m] = true
			}
		}
	}
	return out
}
