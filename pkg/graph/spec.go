package graph

import (
	"fmt"
	"sort"
	"strings"
)

// SavepointPrefix names the nodes that stand for a savepoint of the plan: `savepoint:<name>`.
const SavepointPrefix = "savepoint:"

// IsSavepoint reports whether a node name is a savepoint's.
func IsSavepoint(name string) bool { return strings.HasPrefix(name, SavepointPrefix) }

// Spec is a node as the tool knows it from a rendered pack: a name, what it
// waits for, and whether it is a savepoint. There is no Go value behind it.
type Spec struct {
	Name      string
	Deps      []string
	Savepoint bool
}

// New builds the graph from specs, for a tool that has the pack's rendered
// form and not its Go types. A dependency naming no spec is an error; a cycle
// too.
func New(specs []Spec) (*Graph, error) {
	g := &Graph{byName: map[string]*Node{}}
	for _, s := range specs {
		if len(s.Name) == 0 {
			return nil, fmt.Errorf("a node without a name")
		}
		if _, dup := g.byName[s.Name]; dup {
			return nil, fmt.Errorf("two nodes are named %q", s.Name)
		}
		n := &Node{Name: s.Name, Savepoint: s.Savepoint}
		g.byName[s.Name] = n
		g.nodes = append(g.nodes, n)
	}
	for _, s := range specs {
		to := g.byName[s.Name]
		deps := append([]string(nil), s.Deps...)
		sort.Strings(deps)
		for _, d := range deps {
			from, ok := g.byName[d]
			if !ok {
				return nil, fmt.Errorf("%s waits for %q, which is not in the graph", s.Name, d)
			}
			if from == to {
				return nil, fmt.Errorf("%s waits for itself", s.Name)
			}
			g.edges = append(g.edges, Edge{From: from, To: to})
		}
	}
	g.index()
	if cyc := g.cycle(); cyc != nil {
		return nil, fmt.Errorf("dependency cycle: %s", strings.Join(cyc, " → "))
	}
	return g, nil
}
