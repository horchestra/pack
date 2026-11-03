package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"

	"github.com/horchestra/pack/pkg/adapter"
	"github.com/horchestra/pack/pkg/graph"
)

func init() {
	graphCmd.Flags().StringVarP(&graphFormat, "format", "f", "dot", "dot (Graphviz) or mermaid")
	rootCmd.AddCommand(graphCmd)
}

var graphFormat string

var graphCmd = &cobra.Command{
	Use:   "graph",
	Short: "the dependency graph of the release: the keys, what each waits for, the savepoints",
	Args:  cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		if jsonOut && graphFormat == "mermaid" {
			return errors.New("--json and -f mermaid: the graph is one or the other")
		}
		if graphFormat != "dot" && graphFormat != "mermaid" {
			return fmt.Errorf("--format: %q is not dot or mermaid", graphFormat)
		}
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		c, err := s.Prepare(false)
		if err != nil {
			return err
		}
		g, err := graph.New(adapter.Specs(c.ToRel, c.Plan))
		if err != nil {
			return err
		}
		if jsonOut {
			type node struct {
				Name      string   `json:"name"`
				Savepoint bool     `json:"savepoint,omitempty"`
				Waits     []string `json:"waits,omitempty"` // what must be ready before it starts
			}
			var nodes []node
			for _, n := range g.Order() {
				nd := node{Name: n.Name, Savepoint: n.Savepoint}
				for _, d := range g.Deps(n) {
					nd.Waits = append(nd.Waits, d.Name)
				}
				nodes = append(nodes, nd)
			}
			return result(struct {
				Nodes []node `json:"nodes"`
			}{nodes})
		}
		if graphFormat == "mermaid" {
			fmt.Print(g.Mermaid())
		} else {
			fmt.Print(g.Dot())
		}
		return nil
	}),
}
