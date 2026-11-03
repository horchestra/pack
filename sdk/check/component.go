// Package check is the author's side of the contract: what `pack check` (and
// the loader again, at acceptance) verifies about a component before anything
// is rendered. Every problem is a path and a sentence; the list is complete, not
// first-failure.
package check

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/horchestra/pack/sdk/compute"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
	"github.com/horchestra/pack/sdk/semver"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fileAt struct {
	ptr  string
	file *packschema.File
}

type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	// Warning is something to say that does not make the pack wrong — what its docs/ does not say.
	Warning bool `json:"warning,omitempty"`
}

func (p Problem) String() string {
	if p.Warning {
		return p.Path + ": warning: " + p.Message
	}
	return p.Path + ": " + p.Message
}

// Component is what the checks see of one component.
type Component struct {
	Name      string
	Schema    *schema.Schema
	Templates map[string][]byte
	Image     *ocispec.Image // nil for a pack that runs nothing, and for a check run on a directory
}

// nodeOwned are the directories a node owns; a config there would silently lose.
var nodeOwned = []string{"/etc/resolv.conf", "/etc/passwd", "/etc/group", "/etc/hosts", "/etc/ssl", "/etc/pki", "/proc", "/sys", "/dev", "/run/secrets"}

// Run performs the author checks of spec §2.6 on one component.
func Run(c Component) ([]Problem, error) {
	var ps []Problem
	add := func(ptr, format string, args ...any) {
		ps = append(ps, Problem{Path: ptr, Message: fmt.Sprintf(format, args...)})
	}

	root, err := c.Schema.Root.RootAnnotation()
	if err != nil {
		return nil, err
	}

	// 1. defaults pass the schema; const and default never share a node
	defaults, _, err := c.Schema.Defaults()
	if err != nil {
		add("", "defaults: %v", err)
	} else {
		compiled, err := c.Schema.Compile("pack://" + c.Name)
		if err != nil {
			return nil, fmt.Errorf("schema does not compile: %w", err)
		}
		if defaults == nil {
			defaults = map[string]any{}
		}
		for _, v := range compiled.Validate(defaults) {
			// required leaves without a default are the operator's to name — not an error here
			if v.Required {
				continue
			}
			add(v.Path, "defaults do not pass the schema: %s", v.Message)
		}
	}

	env, err := compute.Env()
	if err != nil {
		return nil, err
	}

	var files []fileAt
	var fileStack []fileAt
	var argLeaves, envLeaves, passwordLeaves []string
	ports := map[string]bool{}
	for name := range root.Ports {
		ports[name] = true
	}
	insideFile := map[string]string{} // instance ptr → the file subtree it is under
	computes := map[string]string{}
	computeTypes := map[string][]string{} // instance ptr → the node's JSON types
	rules := map[string][]packschema.Rule{}

	err = c.Schema.Visit(func(ptr string, s *schema.Schema, n *schema.Node) error {
		// track "is this node under a subtree that Content writes as a file"
		for len(fileStack) > 0 && !strings.HasPrefix(ptr, fileStack[len(fileStack)-1].ptr+"/") && ptr != fileStack[len(fileStack)-1].ptr {
			fileStack = fileStack[:len(fileStack)-1]
		}
		if len(fileStack) > 0 {
			insideFile[ptr] = fileStack[len(fileStack)-1].ptr
		}
		if n.HasConst && n.HasDefault {
			add(ptr, "const and default on one node — a fact or a setting, not both")
		}
		if n.File != nil {
			if len(fileStack) > 0 && fileStack[len(fileStack)-1].ptr != ptr {
				add(ptr, "a file inside the file %s", fileStack[len(fileStack)-1].file.Path)
			}
			files = append(files, fileAt{ptr, n.File})
			fileStack = append(fileStack, fileAt{ptr, n.File})
			insideFile[ptr] = ptr
			if n.File.Format == packschema.FormatSecret {
				if !n.IsPassword() {
					add(ptr, "a file of format secret mounts a secret's key as the file: the node must be format: password")
				}
				if len(n.File.ParseAs) > 0 {
					add(ptr, "format secret has no parseAs: the bytes are the secret's")
				}
			}
		}
		if n.Arg != nil {
			argLeaves = append(argLeaves, ptr)
			if n.IsPassword() {
				add(ptr, "x-pack-arg on a password: /proc/<pid>/cmdline is world-readable — use a flagfile or x-pack-env")
			}
		}
		if len(n.Env) > 0 {
			envLeaves = append(envLeaves, ptr)
		}
		if n.IsPassword() {
			passwordLeaves = append(passwordLeaves, ptr)
		}
		if len(n.Compute) > 0 {
			computes[ptr] = n.Compute
			computeTypes[ptr] = n.Types // a default beside a formula is allowed: the default is the constant, the formula the computation
		}
		if len(n.Rules) > 0 {
			if n.IsForeign() {
				add(ptr, "x-pack-rules on the field of a held pack: the field is cut out of the flat view, the rules with it — hang them on the pack's own nodes")
			}
			rules[ptr] = n.Rules
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 2. formulas compile; reads are known; no cycles among them
	reads := map[string][]string{}
	for ptr, expr := range computes {
		a, err := compute.Compile(env, expr)
		if err != nil {
			add(ptr, "x-pack-compute does not compile: %v", err)
			continue
		}
		reads[ptr] = compute.Reads(a)
		// the formula's static type against the node's, where the formula fixes one
		if rt, err := compute.ResultType(env, expr); err != nil {
			add(ptr, "x-pack-compute does not type-check: %v", err)
		} else if len(rt) > 0 && len(computeTypes[ptr]) > 0 && !typeAccepts(computeTypes[ptr], rt) {
			add(ptr, "x-pack-compute yields %s, the node is %s", rt, strings.Join(computeTypes[ptr], "|"))
		}
		for _, r := range reads[ptr] {
			if r == "pack.instance" || r == "pack.index" || r == "pack.ordinal" {
				if owner, ok := insideFile[ptr]; !ok || !perInstance(files, owner) {
					add(ptr, "x-pack-compute reads %s: replicas are byte-identical, so an instance's identity may only reach a perInstance file", r)
				}
			}
			if strings.HasSuffix(r, "[]") && (r == "values[]" || strings.HasPrefix(r, "values.")) {
				add(ptr, "dynamic index in %s: the set of reads must be static", r)
			}
			// 3. no formula reads a password
			if vp, ok := strings.CutPrefix(r, "values."); ok {
				target := "/" + strings.ReplaceAll(vp, ".", "/")
				for _, pw := range passwordLeaves {
					if target == pw || strings.HasPrefix(pw, target+"/") {
						add(ptr, "x-pack-compute reads the password %s: a derived value is stored", pw)
					}
				}
			}
		}
	}
	if cycle := findCycle(reads); len(cycle) > 0 {
		add(cycle, "x-pack-compute reads form a cycle")
	}

	// 2b. rules compile, yield a boolean, run at a phase a rule runs at, read no replica identity
	for _, ptr := range slices.Sorted(maps.Keys(rules)) {
		for i, rule := range rules[ptr] {
			rptr := fmt.Sprintf("%s/x-pack-rules/%d", ptr, i)
			for _, ph := range rule.Phases {
				if !slices.Contains(packschema.RulePhases, ph) {
					add(rptr, "phase %q is not one a rule runs at: %s", ph, join(packschema.RulePhases, ", "))
				}
			}
			a, err := compute.Compile(env, rule.Rule)
			if err != nil {
				add(rptr, "x-pack-rules does not compile: %v", err)
				continue
			}
			if rt, err := compute.ResultType(env, rule.Rule); err != nil {
				add(rptr, "x-pack-rules does not type-check: %v", err)
			} else if len(rt) > 0 && rt != "boolean" {
				add(rptr, "x-pack-rules yields %s, a rule must be boolean", rt)
			}
			for _, r := range compute.Reads(a) {
				if r == "pack.instance" || r == "pack.index" || r == "pack.ordinal" {
					add(rptr, "x-pack-rules reads %s: a rule is not per-replica", r)
				}
			}
		}
	}

	// 4. every password leaf lands in a file or an env
	envSet := map[string]bool{}
	for _, e := range envLeaves {
		envSet[e] = true
	}
	for _, pw := range passwordLeaves {
		if _, inFile := insideFile[pw]; !inFile && !envSet[pw] {
			add(pw, "a password with nowhere to go: put it under the values of a file of Content or on an x-pack-env leaf")
		}
	}

	// 5. -flagfile= in argv ↔ a flagfile file; a flag name never in both
	argv := c.argv(root)
	flagfiles := map[string]bool{}
	for _, f := range files {
		if f.file.Format == "flagfile" {
			flagfiles[f.file.Path] = true
		}
	}
	for _, a := range argv {
		if p, ok := strings.CutPrefix(a, "-flagfile="); ok {
			if !flagfiles[p] {
				add("", "argv names -flagfile=%s but no file of format flagfile has that path", p)
			}
		}
		if p, ok := strings.CutPrefix(a, "--flagfile="); ok {
			if !flagfiles[p] {
				add("", "argv names --flagfile=%s but no file of format flagfile has that path", p)
			}
		}
	}

	// 6. files: path form, node-owned dirs, format, template, parseAs, perInstance
	for i := range root.Content {
		fp := &root.Content[i]
		ptr := fmt.Sprintf("/x-pack/content/%d", i)
		if len(fp.Values) > 0 {
			if _, n, err := c.Schema.Lookup("/" + fp.Values); err != nil || n == nil {
				add(ptr, "values %q: no such field", fp.Values)
				continue
			}
		}
		if len(fp.Values) == 0 || len(fp.Template) > 0 {
			files = append(files, fileAt{ptr, fp}) // the others hang on their nodes
		}
	}
	for os, p := range root.Packages {
		if len(os) == 0 {
			add("/x-pack/packages", "an OS with no name")
		}
		if len(p.Names) == 0 {
			add("/x-pack/packages/"+os, "no names: which packages?")
		}
		seen := map[string]bool{}
		for list, fs := range map[string][]packschema.File{"repo": p.Repo, "content": p.Content} {
			for i := range fs {
				f := &fs[i]
				ptr := fmt.Sprintf("/x-pack/packages/%s/%s/%d", os, list, i)
				if !path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path {
					add(ptr, "file path %q must be absolute and clean", f.Path)
				}
				if seen[f.Path] {
					add(ptr, "file path %q is written twice", f.Path)
				}
				seen[f.Path] = true
				from := 0
				for _, v := range []string{f.Source, f.Template, f.URL} {
					if len(v) > 0 {
						from++
					}
				}
				if len(f.Values) > 0 && len(f.Template) == 0 {
					from++
					if _, n, err := c.Schema.Lookup("/" + f.Values); err != nil || n == nil {
						add(ptr, "values %q: no such field", f.Values)
					}
				}
				if from != 1 {
					add(ptr, "file %s comes from one place: source, template, url or values", f.Path)
				}
				for _, name := range []string{f.Source, f.Template} {
					if _, ok := c.Templates[name]; len(name) > 0 && !ok && c.Templates != nil {
						add(ptr, "%q is not among the component's files", name)
					}
				}
				if len(f.Mode) > 0 && !validMode(f.Mode) {
					add(ptr, "mode %q is not an octal file mode", f.Mode)
				}
			}
		}
	}
	seenPaths := map[string]string{}
	for _, f := range files {
		fp := f.file
		if !path.IsAbs(fp.Path) || path.Clean(fp.Path) != fp.Path || strings.Contains(fp.Path, "..") {
			add(f.ptr, "file path %q must be absolute and clean", fp.Path)
		}
		for _, owned := range nodeOwned {
			if fp.Path == owned || strings.HasPrefix(fp.Path, owned+"/") {
				add(f.ptr, "file path %q is owned by the node: the file would silently lose", fp.Path)
			}
		}
		if prev, dup := seenPaths[fp.Path]; dup {
			add(f.ptr, "file path %q also declared at %s", fp.Path, prev)
		}
		seenPaths[fp.Path] = f.ptr
		named := func(what, name string) {
			if _, ok := c.Templates[name]; !ok && c.Templates != nil {
				add(f.ptr, "%s %q is not among the component's files (%s)", what, name, strings.Join(slices.Sorted(maps.Keys(c.Templates)), ", "))
			}
		}
		switch {
		case len(fp.URL) > 0:
			add(f.ptr, "a file downloaded where it lands is of Packages: an image has its files in it")
		case len(fp.Source) > 0:
			named("source", fp.Source)
			if len(fp.Template) > 0 || len(fp.Values) > 0 || len(fp.Format) > 0 || len(fp.ParseAs) > 0 {
				add(f.ptr, "a source is shipped as it is: no template, values, format or parseAs")
			}
		case len(fp.Template) > 0:
			named("template", fp.Template)
			if len(fp.Format) > 0 {
				add(f.ptr, "a template has no format: parseAs says what its result is")
			}
			if len(fp.ParseAs) > 0 && !slices.Contains(packschema.Formats, fp.ParseAs) {
				add(f.ptr, "parseAs %q is not a parseable format", fp.ParseAs)
			}
		case len(fp.Values) > 0:
			if !slices.Contains(packschema.Formats, fp.Format) && fp.Format != packschema.FormatSecret {
				add(f.ptr, "format %q of %s is not an emitter (%s, %s)", fp.Format, fp.Path, strings.Join(packschema.Formats, ", "), packschema.FormatSecret)
			}
			if len(fp.ParseAs) > 0 {
				add(f.ptr, "parseAs only belongs to a template")
			}
		default:
			add(f.ptr, "file %s comes from nowhere: source, template or values", fp.Path)
		}
		if len(fp.Mode) > 0 && !validMode(fp.Mode) {
			add(f.ptr, "mode %q is not an octal file mode", fp.Mode)
		}
	}

	// 7. restartPolicy is one of three; readiness and volumes name what PackRoot declares
	switch root.RestartPolicy {
	case "", "Always", "OnFailure", "Never":
	default:
		add("/x-pack/restartPolicy", "restartPolicy must be Always, OnFailure or Never, got %q", root.RestartPolicy)
	}
	if root.Readiness != nil {
		if len(root.Readiness.TCP) > 0 && !ports[root.Readiness.TCP] {
			add("/x-pack/readiness", "tcp names port %q, which x-pack.ports does not declare", root.Readiness.TCP)
		}
		if len(root.Readiness.HTTP) > 0 {
			p, _, _ := strings.Cut(root.Readiness.HTTP, "/")
			if !ports[p] {
				add("/x-pack/readiness", "http names port %q, which x-pack.ports does not declare", p)
			}
		}
	}
	for name, v := range root.Data {
		if !strings.HasPrefix(v.MountPath, "/") {
			add("/x-pack/data/"+name, "mountPath %q is not absolute", v.MountPath)
		}
	}
	for name, p := range root.Ports {
		if p < 1 || p > 65535 {
			add("/x-pack/ports/"+name, "port %d is not a port", p)
		}
	}
	for name, qty := range root.Minimum {
		if _, err := compute.Quantity(qty); err != nil {
			add("/x-pack/minimum/"+name, "%v", err)
		}
	}

	// 9. hooks and upgrade
	if root.Upgrade != nil {
		if _, err := semver.ParseConstraint(root.Upgrade.From); err != nil {
			add("/x-pack/upgrade/from", "%v", err)
		}
	}
	for i, h := range root.Hooks {
		ptr := fmt.Sprintf("/x-pack/hooks/%d", i)
		if len(h.Name) == 0 {
			add(ptr, "hook without a name")
		}
		if !slices.Contains(packschema.Phases, h.Phase) {
			add(ptr, "phase %q is not one of %s", h.Phase, join(packschema.Phases, ", "))
		}
		if len(h.Image) > 0 && h.Image != "new" && h.Image != "old" {
			add(ptr, "image must be new or old, got %q", h.Image)
		}
		if len(h.Command) == 0 {
			add(ptr, "hook without a command")
		}
		if h.When != nil {
			for _, cons := range []string{h.When.From, h.When.To} {
				if _, err := semver.ParseConstraint(cons); err != nil {
					add(ptr+"/when", "%v", err)
				}
			}
		}
		if packschema.IsInstall(h.Phase) {
			if h.Exclusive {
				add(ptr, "exclusive on an install hook: the component is not running yet")
			}
			if h.When != nil && len(h.When.From) > 0 {
				add(ptr+"/when/from", "an install hook has no version to come from")
			}
			if h.Image == "old" {
				add(ptr+"/image", "an install hook has no old image")
			}
		}
		if h.Compensate != nil && len(h.Compensate.Command) == 0 {
			add(ptr+"/compensate", "compensate without a command")
		}
	}

	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Path < ps[j].Path })
	return ps, nil
}

func join(v []packschema.Phase, sep string) string {
	out := make([]string, len(v))
	for i, p := range v {
		out[i] = string(p)
	}
	return strings.Join(out, sep)
}

// argv is the static part of the command line: the image's entrypoint+cmd, or
// x-pack.command in its place. Leaves annotated x-pack-arg join at render time.
func (c Component) argv(root *packschema.Root) []string {
	if len(root.Command) > 0 {
		return root.Command
	}
	if c.Image != nil {
		return append(append([]string{}, c.Image.Config.Entrypoint...), c.Image.Config.Cmd...)
	}
	return nil
}

// findCycle returns a pointer on a cycle among formulas that read each other
// through values.*, or "".
func findCycle(reads map[string][]string) string {
	const white, grey, black = 0, 1, 2
	color := map[string]int{}
	var visit func(ptr string) string
	visit = func(ptr string) string {
		switch color[ptr] {
		case grey:
			return ptr
		case black:
			return ""
		}
		color[ptr] = grey
		for _, r := range reads[ptr] {
			vp, ok := strings.CutPrefix(r, "values.")
			if !ok {
				continue
			}
			target := "/" + strings.ReplaceAll(vp, ".", "/")
			for other := range reads {
				if other == target || strings.HasPrefix(target, other+"/") {
					if c := visit(other); len(c) > 0 {
						return c
					}
				}
			}
		}
		color[ptr] = black
		return ""
	}
	ptrs := make([]string, 0, len(reads))
	for p := range reads {
		ptrs = append(ptrs, p)
	}
	sort.Strings(ptrs)
	for _, p := range ptrs {
		if c := visit(p); len(c) > 0 {
			return c
		}
	}
	return ""
}

// perInstance reports whether the file at ptr is declared perInstance.
func perInstance(files []fileAt, ptr string) bool {
	for _, f := range files {
		if f.ptr == ptr {
			return f.file.PerInstance
		}
	}
	return false
}

func validMode(m string) bool {
	if len(m) != 4 && len(m) != 3 {
		return false
	}
	for _, r := range m {
		if r < '0' || r > '7' {
			return false
		}
	}
	return true
}

// typeAccepts says whether a node of the given JSON types takes a value of the
// formula's type: `number` takes an integer too, nothing else is coerced.
func typeAccepts(nodeTypes []string, got string) bool {
	for _, t := range nodeTypes {
		if t == got || (t == "number" && got == "integer") {
			return true
		}
	}
	return false
}
