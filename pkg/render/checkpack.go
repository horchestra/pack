package render

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/sdk/check"
	"github.com/horchestra/pack/sdk/compute"
	"github.com/horchestra/pack/sdk/schema"
)

type (
	schemaT    = schema.Schema
	schemaNode = schema.Node
)

// CheckBundle runs the author checks of every pack in a bundle and the ones that need no values: the order is an
// order, the savepoints name what a pack holds, a path is written by one file.
func CheckBundle(b *bundle.Bundle) ([]check.Problem, error) {
	var problems []check.Problem
	for _, name := range slices.Sorted(maps.Keys(b.Packs)) {
		c := b.Packs[name]
		ps, err := check.Run(check.Component{Name: c.Name, Schema: c.Schema, Templates: c.Templates, Image: c.Image})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
		for _, p := range ps {
			problems = append(problems, check.Problem{Path: c.Name + ":" + p.Path, Message: p.Message})
		}
	}
	add := func(path, format string, args ...any) {
		problems = append(problems, check.Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	// the order — the same derivation the renderer does, on an empty document
	r := &render{b: b, userSet: map[string]bool{}, report: Report{Secrets: map[string][]string{}}}
	if err := r.configure(Values{}); err != nil {
		add("", "%v", err)
	}
	for _, sp := range b.Root.Root.Savepoints {
		if _, ok := b.Instances[sp]; !ok {
			add("/x-pack/savepoints", "%q is not a pack this pack holds", sp)
		}
	}
	// files: one path, one owner within an instance
	for _, key := range b.Keys {
		owners := map[string]string{}
		for i, f := range b.Packs[b.KeyPack[key]].Root.Content {
			if len(f.Values) == 0 || len(f.Template) > 0 {
				ptr := fmt.Sprintf("/%s/x-pack/content/%d", key, i)
				if prev, dup := owners[f.Path]; dup {
					add(ptr, "file path %q is also written by %s", f.Path, prev)
				}
				owners[f.Path] = ptr
			}
		}
		err := b.Composition.Visit(func(ptr string, _ *schemaT, n *schemaNode) error {
			if !strings.HasPrefix(ptr, "/"+key+"/") || n.File == nil {
				return nil
			}
			if prev, dup := owners[n.File.Path]; dup {
				add(ptr, "file path %q is also written by %s", n.File.Path, prev)
			}
			owners[n.File.Path] = ptr
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// formulas and rules name the keys they read: pack.keys.<key> and pack.values.<key> must be a key of this pack
	// — a field renamed in the struct would otherwise leave a formula that quietly yields nothing
	env, err := compute.Env()
	if err != nil {
		return nil, err
	}
	err = b.Composition.Visit(func(ptr string, _ *schemaT, n *schemaNode) error {
		exprs := []string{}
		if len(n.Compute) > 0 {
			exprs = append(exprs, n.Compute)
		}
		for _, rule := range n.Rules {
			exprs = append(exprs, rule.Rule)
		}
		for _, expr := range exprs {
			a, err := compute.Compile(env, expr)
			if err != nil {
				continue // the pack's own check says so
			}
			for _, rd := range compute.Reads(a) {
				for _, prefix := range []string{"pack.keys.", "pack.values."} {
					tail, ok := strings.CutPrefix(rd, prefix)
					if !ok {
						continue
					}
					name, _, _ := strings.Cut(tail, ".")
					if _, known := b.Instances[name]; !known {
						add(b.External(ptr), "%s reads %s%s, which is not a key of the pack (%s)", formulaOrRule(n, expr), prefix, name, strings.Join(b.Keys, ", "))
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return problems, nil
}

func formulaOrRule(n *schemaNode, expr string) string {
	if n.Compute == expr {
		return "the formula"
	}
	return "a rule"
}
