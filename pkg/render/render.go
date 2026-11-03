package render

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"github.com/horchestra/pack/sdk/compute"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"

	"github.com/horchestra/pack/pkg/bundle"
)

// Context is what the bundle cannot know: under what name it is being installed, the environment's targets, and
// how the environment runs each key.
type Context struct {
	Installation string
	Target       Target
	// Targets are the other targets of the environment, by the name it gave them: a key whose placement names
	// `target: <name>` runs on that one, every other on Target. A neighbour on another target is reached at its
	// Reach, not at its Address.
	Targets map[string]Target
	// Keys is how the environment runs each key — replicas, resources, env, metadata, the target, how it is
	// reached. The values do not say it: they configure the application, the environment places and runs it.
	Keys map[string]Placement
	// Secrets, when set, gives the passwords the operator did not: see CredentialSource.
	Secrets CredentialSource
}

// Placement is how the environment runs one key. What the render reads of it — resources (cgroups, the same on
// every target, and what formulas see as `limits`), env, metadata — it uses itself; the rest is the target's.
type Placement struct {
	Target      string // the target it runs on; "" — the default one
	Replicas    *int   // nil — the target's default
	Resources   *Resources
	Env         []EnvVar // added to the component's own; a name the component declares is refused
	Labels      map[string]string
	Annotations map[string]string
	// Reach is how the key is reached from outside its target; nil — the target says.
	Reach *Reach
	// Raw is the rest of the key's entry in the environment: whatever its target has words for (hosts, publish,
	// node selectors …). The render passes it on and never reads it.
	Raw map[string]any
}

// Target is what the render asks of the system it renders for: names, addresses and instances. A key's address
// ends up inside a config file, so a pack is rendered for one target, never for "any".
type Target interface {
	// Namespace is the scope the target installs into; empty where it has none.
	Namespace() string
	// Resolve says what a selected key is on the target, given how the environment runs it. A key the environment
	// gives no replicas has no instances of its own and is addressed as one service. An error says what the
	// target cannot do with the placement.
	Resolve(key string, p Placement) (*Resolved, error)
}

// Resolved is a key as the target will run it.
type Resolved struct {
	// Service is the key's stable name; Address how a neighbour reaches it
	// as one service.
	Service string
	Address string
	// Instances are its replicas in order — empty for a key the environment
	// gave no replicas.
	Instances []Instance
	// Reach is how the key is reached from outside its target — by a neighbour that runs on another target,
	// where Address (a name inside the target's network) means nothing. Nil — the target does not say.
	Reach *Reach
}

// Reach is where a key can be reached from outside its target: the host of each instance (one for a key that
// does not scale) and the port of each named port at that host.
type Reach struct {
	Instances []string
	Ports     map[string]int
	// SamePorts: every port is reachable as itself, Ports says nothing (across hosts a port is published as
	// itself).
	SamePorts bool
}

// Instance is one replica: its name and how a neighbour reaches it.
type Instance struct {
	Name    string
	Address string
}

// Values is the operator's document.
type Values struct {
	Values  map[string]any      `json:"values"`
	Rollout *packschema.Rollout `json:"rollout,omitempty"`
	// Skip names keys to exclude from whatever the selection would otherwise
	// install — the explicit values an operator named, or the whole-bundle
	// fallback. It is a final override, checked AFTER the selection and its
	// closure settle: a key still required by another key that IS installed
	// cannot be skipped, and configure refuses rather than installing a
	// component with a required peer quietly missing.
	Skip []string `json:"skip,omitempty"`
}

// formula is one x-pack-compute, located in the instance document.
type formula struct {
	ptr      string // instance pointer of the leaf
	root     string // pointer of the subtree `values` means for it (the key, or a pack held under it)
	key      string // system key it belongs to
	ast      *cel.Ast
	reads    []string
	instance bool // reads pack.instance/index/ordinal — evaluated per replica only
}

// vrule is one x-pack-rules rule, located like a formula and evaluated the
// same way — the difference is what a false answer means.
type vrule struct {
	formula
	rule packschema.Rule
}

type render struct {
	b        *bundle.Bundle
	ctx      Context
	env      *cel.Env
	doc      map[string]any  // the effective document, whole
	userSet  map[string]bool // pointers the operator wrote
	formulas []formula
	rules    []vrule
	// keys is the SELECTION this render actually installs — b.Keys narrowed to
	// what the operator named in values plus the held packs that are required,
	// in the composition's declared order. Everything below configure
	// works off this, never off b.Keys, once it is set.
	keys   []string
	order  []string
	report Report
	// resolved is the target's answer about each selected key, asked once
	// the placements are settled (resolve); limits are what formulas
	// read of each key's resources.
	resolved map[string]*Resolved
	limits   map[string]map[string]any
	// keysVar is pack.keys: every selected key as `me` describes it, built
	// once the target has resolved them all
	keysVar map[string]map[string]any
	// target is the name of the target each key runs on; "" — the default one
	target map[string]string
	// credentials are what provide took from the CredentialSource
	credentials []Credential
}

// Render runs the link: configure → preMutate rules → provide → mutate →
// postMutate rules → validate → convert, and stops at the target-neutral
// Release a target turns into its own objects.
func Render(b *bundle.Bundle, v Values, ctx Context) (*Release, error) {
	if len(ctx.Installation) == 0 {
		return nil, errors.New("an installation name is required (--name)")
	}
	if ctx.Target == nil {
		return nil, errors.New("a target is required: the render cannot size or address a key without one")
	}
	env, err := compute.Env()
	if err != nil {
		return nil, err
	}
	r := &render{b: b, ctx: ctx, env: env, userSet: map[string]bool{},
		report: Report{Installation: ctx.Installation, Namespace: ctx.Target.Namespace(), Pack: b.Name}}
	if err := r.configure(v); err != nil {
		return nil, err
	}
	if err := r.resolve(); err != nil {
		return nil, err
	}
	if err := r.checkReach(); err != nil {
		return nil, err
	}
	if err := r.collect(); err != nil {
		return nil, err
	}
	// preMutate rules see the document as written — defaults and the
	// operator's values, nothing the render filled in (the credentials it
	// provides come after, the formulas after them) — so a rule never trips
	// on the render's own work.
	if err := r.checkRules(packschema.PreMutate); err != nil {
		return nil, err
	}
	if err := r.provide(); err != nil {
		return nil, err
	}
	if err := r.mutate(); err != nil {
		return nil, err
	}
	if err := r.checkRules(packschema.PostMutate); err != nil {
		return nil, err
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	return r.convert(v)
}

// configure: defaults ⊕ user values, the instances selected, the order derived.
func (r *render) configure(v Values) error {
	defaults, _, err := r.b.Composition.Defaults()
	if err != nil {
		return fmt.Errorf("defaults: %w", err)
	}
	doc, _ := defaults.(map[string]any)
	if doc == nil {
		doc = map[string]any{}
	}
	// the operator writes the settings of a pack held in a field in the field; the render works on one entry per
	// instance
	named := r.b.Split(v.Values)
	for _, key := range r.b.Keys {
		own, ok := named[key]
		if !ok {
			continue
		}
		pointers(own, "/"+key, r.userSet)
		cur, _ := doc[key].(map[string]any)
		if cur == nil {
			cur = map[string]any{}
			doc[key] = cur
		}
		schema.Overlay(cur, schema.DeepCopy(own).(map[string]any))
	}
	r.doc = doc
	for _, ptr := range slices.Sorted(maps.Keys(r.userSet)) {
		if _, n, err := r.b.Composition.Lookup(ptr); err == nil && n != nil && n.ReadOnly {
			return fmt.Errorf("%s is read-only: the pack's own, not the operator's to set", ptr)
		}
	}

	// Selection: the root is always installed. A pack held in a field is installed when the field is there: the
	// values name it (even as {}), the author gave it a default, or the field is not optional. Presence is the
	// ask, not the content. What a pack held by an absent pack holds is absent with it.
	selected := map[string]bool{}
	for i := len(r.b.Keys) - 1; i >= 0; i-- { // the pack that holds before the packs it holds
		key := r.b.Keys[i]
		in := r.b.Instances[key]
		if len(in.Parent) == 0 {
			selected[key] = true
			continue
		}
		_, named := named[key]
		if selected[in.Parent] && (named || in.Hold.Required || in.Hold.Default != nil) {
			selected[key] = true
		}
	}
	// Skip: an explicit override. What is not optional cannot be skipped; what the skipped one holds goes with it.
	for _, key := range v.Skip {
		in, ok := r.b.Instances[key]
		if !ok {
			return fmt.Errorf("--skip names %q, not a pack of this one", key)
		}
		if len(in.Parent) == 0 {
			return fmt.Errorf("--skip names %q, the pack itself", key)
		}
		if in.Hold.Required {
			return fmt.Errorf("cannot skip %s: %s holds it and does not work without it", key, in.Parent)
		}
		delete(selected, key)
	}
	for i := len(r.b.Keys) - 1; i >= 0; i-- {
		if in := r.b.Instances[r.b.Keys[i]]; len(in.Parent) > 0 && !selected[in.Parent] {
			delete(selected, in.Key)
		}
	}
	r.keys = make([]string, 0, len(selected))
	for _, key := range r.b.Keys { // held packs before the pack that holds them
		if selected[key] {
			r.keys = append(r.keys, key)
		}
	}

	// A key that was not selected gets no doc entry: nothing to default, mutate, validate or render for a pack
	// this release does not install.
	for key := range doc {
		if _, isKey := r.b.KeyPack[key]; isKey && !selected[key] {
			delete(doc, key)
		}
	}
	for _, key := range r.keys {
		if _, ok := doc[key]; !ok {
			doc[key] = map[string]any{}
		}
	}

	// order: a pack after every pack it holds
	edges := map[string][]string{}
	for _, key := range r.keys {
		if parent := r.b.Instances[key].Parent; len(parent) > 0 {
			edges[parent] = append(edges[parent], key)
		}
	}
	order, err := topo(r.keys, edges)
	if err != nil {
		return err
	}
	r.order = order
	r.report.Order = order
	r.report.Deps = map[string][]string{}
	for k, ds := range edges {
		r.report.Deps[k] = slices.Clone(ds)
		slices.Sort(r.report.Deps[k])
	}
	return nil
}

// topo sorts keys so that every key comes after the keys it depends on,
// keeping the composition's order among independent ones.
func topo(keys []string, deps map[string][]string) ([]string, error) {
	const white, grey, black = 0, 1, 2
	color := map[string]int{}
	var out []string
	var visit func(k string, trail []string) error
	visit = func(k string, trail []string) error {
		switch color[k] {
		case grey:
			return fmt.Errorf("ordering cycle: %s → %s", strings.Join(trail, " → "), k)
		case black:
			return nil
		}
		color[k] = grey
		for _, d := range deps[k] {
			if err := visit(d, append(trail, k)); err != nil {
				return err
			}
		}
		color[k] = black
		out = append(out, k)
		return nil
	}
	for _, k := range keys {
		if err := visit(k, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// collect gathers every formula and rule of a SELECTED key with its values
// root — a key this release does not install has no doc entry (configure
// already deleted it), so evaluating its formulas would compute against
// nothing and set() into a path that no longer exists.
func (r *render) collect() error {
	selected := map[string]bool{}
	roots := map[string]bool{}
	for _, key := range r.keys {
		selected[key] = true
		roots["/"+key] = true
	}
	return r.b.Composition.Visit(func(ptr string, s *schema.Schema, n *schema.Node) error {
		segs := schema.Split(ptr)
		if len(segs) == 0 || !selected[segs[0]] {
			return nil
		}
		if n.IsForeign() && strings.HasPrefix(n.Ref, packschema.PackScheme) && len(segs) >= 2 {
			roots[ptr] = true
		}
		locate := func(a *cel.Ast) formula {
			f := formula{ptr: ptr, key: segs[0], ast: a, reads: compute.Reads(a)}
			f.root = "/" + segs[0]
			for root := range roots {
				if strings.HasPrefix(ptr, root+"/") && len(root) > len(f.root) {
					f.root = root
				}
			}
			return f
		}
		if len(n.Compute) > 0 {
			a, err := compute.Compile(r.env, n.Compute)
			if err != nil {
				return fmt.Errorf("%s: x-pack-compute: %w", ptr, err)
			}
			f := locate(a)
			for _, rd := range f.reads {
				if rd == "pack.instance" || rd == "pack.index" || rd == "pack.ordinal" {
					f.instance = true
				}
			}
			r.formulas = append(r.formulas, f)
		}
		for _, rule := range n.Rules {
			a, err := compute.Compile(r.env, rule.Rule)
			if err != nil {
				return fmt.Errorf("%s: x-pack-rules: %w", ptr, err)
			}
			r.rules = append(r.rules, vrule{formula: locate(a), rule: rule})
		}
		return nil
	})
}

// where is a pointer of the flat view in the operator's words; the whole document is "/".
func (r *render) where(ptr string) string {
	if p := r.b.External(ptr); len(p) > 0 {
		return p
	}
	return "/"
}

// checkRules evaluates the rules of one phase against the document as it is
// right now: preMutate — the values as written, before any formula ran;
// postMutate — the effective document. A rule whose phases are not named runs
// at preMutate. Every violation is a refusal, with the rule's message; an
// evaluation error is a violation too, with the error as the message.
func (r *render) checkRules(phase packschema.Phase) error {
	var problems []Problem
	for i := range r.rules {
		ru := &r.rules[i]
		phases := ru.rule.Phases
		if len(phases) == 0 {
			phases = []packschema.Phase{packschema.PreMutate}
		}
		if !slices.Contains(phases, phase) {
			continue
		}
		v, err := r.eval(&ru.formula, r.doc, nil)
		switch {
		case err != nil:
			problems = append(problems, Problem{Kind: KindRule, Where: r.where(ru.ptr), Rule: ru.rule.ID, Message: err.Error()})
		default:
			holds, ok := v.(bool)
			if !ok {
				problems = append(problems, Problem{Kind: KindRule, Where: r.where(ru.ptr), Rule: ru.rule.ID, Message: fmt.Sprintf("the rule yielded %v, not a boolean", v)})
			} else if !holds {
				msg := ru.rule.Message
				if len(msg) == 0 {
					msg = "the rule does not hold"
				}
				problems = append(problems, Problem{Kind: KindRule, Where: r.where(ru.ptr), Rule: ru.rule.ID, Message: msg})
			}
		}
	}
	if len(problems) > 0 {
		return newProblems(string(phase), problems)
	}
	return nil
}

// mutate evaluates formulas — those a replica does not change — in an order
// where every values.* read is settled first; a leaf the operator wrote is
// left alone.
func (r *render) mutate() error {
	pending := map[string]*formula{}
	for i := range r.formulas {
		f := &r.formulas[i]
		if f.instance || r.userSet[f.ptr] || userWroteAbove(r.userSet, f.ptr) {
			continue
		}
		pending[f.ptr] = f
	}
	for len(pending) > 0 {
		progressed := false
		keys := make([]string, 0, len(pending))
		for k := range pending {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, ptr := range keys {
			f := pending[ptr]
			if r.blocked(f, pending) {
				continue
			}
			v, err := r.eval(f, r.doc, nil)
			if err != nil {
				return fmt.Errorf("%s: x-pack-compute %q: %w", f.ptr, f.ast.Source().Content(), err)
			}
			if v != nil {
				if err := set(r.doc, f.ptr, v); err != nil {
					return err
				}
				r.report.Computed = append(r.report.Computed, Computed{Key: f.key, Pointer: strings.TrimPrefix(f.ptr, "/"+f.key), Value: v})
			}
			delete(pending, ptr)
			progressed = true
		}
		if !progressed {
			left := make([]string, 0, len(pending))
			for k := range pending {
				left = append(left, k)
			}
			sort.Strings(left)
			return fmt.Errorf("x-pack-compute: a cycle among %s", strings.Join(left, ", "))
		}
	}
	return nil
}

// userWroteAbove: the operator set an ancestor as a whole (an object literal
// replacing the subtree) — leave the subtree as written.
func userWroteAbove(userSet map[string]bool, ptr string) bool {
	for p := range userSet {
		if len(p) > 0 && strings.HasPrefix(ptr, p+"/") {
			return true
		}
	}
	return false
}

// blocked reports whether f reads a leaf whose formula is still pending.
func (r *render) blocked(f *formula, pending map[string]*formula) bool {
	// pack.values is every key's values: it waits for every formula that does not read it
	// too — those cannot wait for each other, and see each other's leaves unsettled
	if readsPackValues(f.reads) {
		for p, g := range pending {
			if p != f.ptr && !readsPackValues(g.reads) {
				return true
			}
		}
	}
	for _, rd := range f.reads {
		var target string
		if rel, ok := strings.CutPrefix(rd, "values."); ok {
			target = f.root + "/" + strings.ReplaceAll(rel, ".", "/")
		} else {
			continue
		}
		for p := range pending {
			if p == f.ptr {
				continue
			}
			if p == target || strings.HasPrefix(p, target+"/") || strings.HasPrefix(target, p+"/") {
				return true
			}
		}
	}
	return false
}

// eval runs one formula against a document; instance, when non-nil, adds the
// per-replica fields to the pack context.
func (r *render) eval(f *formula, doc map[string]any, instance map[string]any) (any, error) {
	sub, _ := get(doc, f.root)
	vars := map[string]any{
		"values": orEmpty(sub),
		"limits": orEmpty(r.limits[f.key]),
		"me":     r.me(f.key),
		"pack":   r.packContext(f.key, instance),
	}
	if readsPackValues(f.reads) {
		vars["pack"].(map[string]any)["values"] = r.allValues(doc)
	}
	return compute.Eval(r.env, f.ast, vars)
}

func orEmpty(m any) any {
	if m == nil {
		return map[string]any{}
	}
	if mm, ok := m.(map[string]any); ok && mm == nil {
		return map[string]any{}
	}
	return m
}

// targetFor is the target a key runs on: the one its placement names, else the default.
func (r *render) targetFor(key string) (Target, string, error) {
	name := r.ctx.Keys[key].Target
	if len(name) == 0 {
		return r.ctx.Target, "", nil
	}
	tg, ok := r.ctx.Targets[name]
	if !ok {
		return nil, "", fmt.Errorf("%s: target %q is no target of the environment", key, name)
	}
	return tg, name, nil
}

// checkReach: a pack held by a pack on another target can be reached only where its target says how — the
// Address of a key is a name inside its own target.
func (r *render) checkReach() error {
	for _, key := range r.keys {
		for _, held := range r.report.Deps[key] {
			if r.target[key] != r.target[held] && r.resolved[held].Reach == nil {
				return fmt.Errorf("%s holds %s, which runs on another target and its target says nothing of how it is reached: "+
					"give reach to %s in the environment (keys.%s.reach)", key, held, held, held)
			}
		}
	}
	return nil
}

// resolve asks the target what each selected key is, given how the environment runs it: its names and
// addresses, its instances (a key the environment gave no replicas is one, addressed as the key) and the
// limits formulas may read.
func (r *render) resolve() error {
	r.resolved, r.limits, r.target = map[string]*Resolved{}, map[string]map[string]any{}, map[string]string{}
	for _, key := range r.keys {
		p := r.ctx.Keys[key]
		tg, name, err := r.targetFor(key)
		if err != nil {
			return err
		}
		r.target[key] = name
		if len(name) > 0 {
			if r.report.Targets == nil {
				r.report.Targets = map[string]string{}
			}
			r.report.Targets[key] = name
		}
		res, err := tg.Resolve(key, p)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if res.Reach == nil {
			res.Reach = p.Reach
		}
		r.resolved[key] = res
		if r.limits[key], err = limitsOf(p.Resources); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// limitsOf is what formulas read as `limits`: the key's resource limits, memory in bytes, cpu in millicores —
// cgroups, the pack's own words.
func limitsOf(res *Resources) (map[string]any, error) {
	out := map[string]any{}
	if res == nil {
		return out, nil
	}
	for name, div := range map[string]int64{"memory": 1000, "cpu": 1} {
		v, ok := res.Limits[name]
		if !ok {
			continue
		}
		q, err := compute.Quantity(v)
		if err != nil {
			return nil, fmt.Errorf("resources.limits.%s: %w", name, err)
		}
		out[name] = q / div
	}
	return out, nil
}

// replicas is 0 for a key the environment gave no replicas, n ≥ 1 where it did.
func (r *render) replicas(key string) int { return len(r.resolved[key].Instances) }

// me describes a key by names only: service, instances, replicas, ports.
func (r *render) me(key string) map[string]any { return r.meFor(key, key) }

// meFor describes key as reader sees it. On the reader's own target that is by the names inside it; on another
// target the instances are the hosts the key is reached at from outside and the ports the ports there (reach),
// which is all a neighbour of another target can use.
func (r *render) meFor(reader, key string) map[string]any {
	m := r.meOf(key)
	rc := r.resolved[key].Reach
	if r.target[reader] == r.target[key] || rc == nil {
		return m
	}
	ports := map[string]any{}
	if rc.SamePorts {
		maps.Copy(ports, m["ports"].(map[string]any))
	}
	for name, p := range rc.Ports {
		ports[name] = int64(p)
	}
	m["instances"], m["ports"] = m["reach"].(map[string]any)["instances"], ports
	return m
}

func (r *render) meOf(key string) map[string]any {
	res := r.resolved[key]
	svc, n := res.Service, len(res.Instances)
	var instances []any
	if n == 0 {
		instances = []any{res.Address}
	} else {
		for _, in := range res.Instances {
			instances = append(instances, in.Address)
		}
	}
	ports := map[string]any{}
	for name, p := range r.b.Packs[r.b.KeyPack[key]].Root.Ports {
		ports[name] = int64(p)
	}
	out := map[string]any{"service": svc, "instances": instances, "replicas": max(n, 1), "ports": ports}
	if rc := res.Reach; rc != nil {
		hosts := make([]any, len(rc.Instances))
		for i, h := range rc.Instances {
			hosts[i] = h
		}
		reachPorts := map[string]any{}
		if rc.SamePorts {
			maps.Copy(reachPorts, ports)
		}
		for name, p := range rc.Ports {
			reachPorts[name] = int64(p)
		}
		out["reach"] = map[string]any{"instances": hosts, "ports": reachPorts}
	}
	return out
}

// neighbourValues is a key's subtree of doc — what the operator
// wrote, the defaults, and the formulas settled so far — less every password
// leaf: a derived value is stored, a credential must not be one. Leaves that
// differ per replica are not in it; they exist only inside the replica's file.
func (r *render) neighbourValues(peer string, doc map[string]any) map[string]any {
	sub, _ := get(doc, "/"+peer)
	m, _ := schema.DeepCopy(sub).(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	r.dropPasswords("/"+peer, m)
	return m
}

func (r *render) dropPasswords(ptr string, m map[string]any) {
	for k, v := range m {
		p := schema.Pointer(append(schema.Split(ptr), k)...)
		if _, n, err := r.b.Composition.Lookup(p); err == nil && n != nil && n.IsPassword() {
			delete(m, k)
			continue
		}
		if sub, ok := v.(map[string]any); ok {
			r.dropPasswords(p, sub)
		}
	}
}

// allValues is pack.values: the values of every key of the installation, as
// neighbourValues has them — no passwords, no per-replica leaves.
func (r *render) allValues(doc map[string]any) map[string]any {
	out := make(map[string]any, len(r.keys))
	for _, k := range r.keys {
		out[k] = r.neighbourValues(k, doc)
	}
	return out
}

func readsPackValues(reads []string) bool {
	return slices.ContainsFunc(reads, func(rd string) bool { return rd == "pack.values" || strings.HasPrefix(rd, "pack.values.") })
}

// packContext is what formulas and templates read as `pack`: where and as
// what this key is installed — and, per replica, which one.
func (r *render) packContext(key string, instance map[string]any) map[string]any {
	h := map[string]any{
		"namespace":    r.targetOf(key).Namespace(),
		"installation": r.ctx.Installation,
		"component":    key,
		"service":      r.resolved[key].Service,
		"replicas":     max(r.replicas(key), 1),
		"keys":         r.keysContext(key),
	}
	maps.Copy(h, instance)
	return h
}

// keysContext is pack.keys: every key of the installation by name, as `me`
// describes it, plus `scaled` — whether its instances are replicas (a
// per-instance Secret key carries the index). Reading it binds nothing: a
// key that names every host of the installation (a CA, a realm's plan) runs
// before them, which a peer could not.
func (r *render) keysContext(reader string) map[string]any {
	// what a key sees of the others depends only on the target it runs on
	view := r.target[reader]
	if r.keysVar == nil {
		r.keysVar = map[string]map[string]any{}
	}
	if _, ok := r.keysVar[view]; !ok {
		m := map[string]any{}
		for _, k := range r.keys {
			e := r.meFor(reader, k)
			e["scaled"] = r.replicas(k) > 0
			m[k] = e
		}
		r.keysVar[view] = m
	}
	return r.keysVar[view]
}

// targetOf is the target a key runs on.
func (r *render) targetOf(key string) Target {
	if tg, ok := r.ctx.Targets[r.target[key]]; ok {
		return tg
	}
	return r.ctx.Target
}

// instanceVars are the per-replica fields of the pack context.
func (r *render) instanceVars(key string, i int) map[string]any {
	name := r.resolved[key].Service // a key that does not scale is its one instance
	if in := r.resolved[key].Instances; i < len(in) {
		name = in[i].Name
	}
	return map[string]any{"instance": name, "index": i, "ordinal": i + 1}
}

// instanceDoc re-evaluates the per-replica formulas of a key for replica i,
// returning a copy of the key's subtree with them applied.
func (r *render) instanceDoc(key string, i int) (map[string]any, error) {
	copyDoc := schema.DeepCopy(r.doc).(map[string]any)
	instance := r.instanceVars(key, i)
	for idx := range r.formulas {
		f := &r.formulas[idx]
		if !f.instance || f.key != key || r.userSet[f.ptr] {
			continue
		}
		v, err := r.eval(f, copyDoc, instance)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.ptr, err)
		}
		if v != nil {
			if err := set(copyDoc, f.ptr, v); err != nil {
				return nil, err
			}
		}
	}
	sub, _ := get(copyDoc, "/"+key)
	m, _ := sub.(map[string]any)
	return m, nil
}

// validate: the effective document against the composition's validating view, then the sizes the schema
// cannot express.
func (r *render) validate() error {
	// Under its own URL, not pack://<name>: a lone component's composition is named after the component, and the
	// ref pack://<name># must reach the component, not the composition wrapped around it.
	compiled, err := r.b.Composition.Compile("pack://composition/" + r.b.Name)
	if err != nil {
		return err
	}
	var problems []Problem
	for _, v := range compiled.Validate(r.doc) {
		kind := KindSchema
		if v.Required {
			kind = KindRequired
		}
		problems = append(problems, Problem{Kind: kind, Where: r.where(v.Path), Message: v.Message}) // the operator's words, not the flat view's
	}
	for _, key := range r.keys {
		var req map[string]string
		if res := r.ctx.Keys[key].Resources; res != nil {
			req = res.Requests
		}
		for name, min := range r.b.Packs[r.b.KeyPack[key]].Root.Minimum {
			have, ok := req[name]
			if !ok {
				problems = append(problems, Problem{Kind: KindEnvironment, Where: "environment: keys." + key + ".resources.requests." + name,
					Message: fmt.Sprintf("%s requires at least %s", r.b.KeyPack[key], min)})
				continue
			}
			hv, err1 := compute.Quantity(have)
			mv, err2 := compute.Quantity(min)
			if err1 == nil && err2 == nil && hv < mv {
				problems = append(problems, Problem{Kind: KindEnvironment, Where: "environment: keys." + key + ".resources.requests." + name,
					Message: fmt.Sprintf("%s is below the minimum of %s, %s", have, r.b.KeyPack[key], min)})
			}
		}
	}
	if len(problems) > 0 {
		return newProblems("", problems)
	}
	return nil
}
