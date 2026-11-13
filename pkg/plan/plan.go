// Package plan derives the saga of a pack: the first installation, or the
// transition between two versions — which keys change and how, which hooks
// the transition selects, in what order the steps run, where the savepoints
// are and therefore how far a failure rolls back. Nobody writes the plan; the
// component authors wrote hooks, the composition author wrote savepoints, and
// this is what follows from them.
//
// The plan is a pure function of the two bundles, their renders and the
// policy. Executing it needs a cluster and is phase 2.
package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/semver"
)

type Kind string

const (
	Stop   Kind = "stop"
	Hook   Kind = "hook"
	Apply  Kind = "apply"
	Wait   Kind = "wait"
	Remove Kind = "remove"
	// Exec is a command in the key's first instance as it runs: an action of the pack (pack run), not a step of a
	// saga.
	Exec Kind = "exec"
)

// Step is one line of the plan.
type Step struct {
	N            int    `json:"n"`
	Key          string `json:"key"`
	Kind         Kind   `json:"kind"`
	Name         string `json:"name,omitempty"`   // hook name
	Detail       string `json:"detail,omitempty"` // what the operator should see
	Phase        string `json:"phase,omitempty"`
	Timeout      string `json:"timeout,omitempty"`
	Idempotent   bool   `json:"idempotent,omitempty"`
	Exclusive    bool   `json:"exclusive,omitempty"`
	Compensate   string `json:"compensate,omitempty"` // how it is undone; "" for an irreversible hook
	Irreversible bool   `json:"irreversible,omitempty"`
	// Target is the target the step runs on, where its key does not run on the default one.
	Target string `json:"target,omitempty"`

	// What a hook runs, resolved to images — for whoever executes the plan,
	// not part of what it prints.
	Image             string   `json:"-"`
	Command           []string `json:"-"`
	CompensateImage   string   `json:"-"`
	CompensateCommand []string `json:"-"`
}

// Change is what happened to one key between the two versions.
type Change struct {
	Key       string `json:"key"`
	Component string `json:"component"`
	From      string `json:"from,omitempty"` // version
	To        string `json:"to,omitempty"`
	Kind      string `json:"kind"`                // none | image | schema | config | drift | added | removed
	Direction string `json:"direction,omitempty"` // install | upgrade | downgrade
}

// Unit is a rollback unit: the steps between two savepoints.
type Unit struct {
	From          int      `json:"from"`
	To            int      `json:"to"`
	Savepoint     string   `json:"savepoint"`
	Keys          []string `json:"keys"`
	RollbackOrder []string `json:"rollbackOrder"` // reverse plan order within the unit
	// StopsAt is where an automatic rollback of this unit stops (--no-rollback):
	// the keys before it in RollbackOrder are rolled back, it and the rest are
	// left as they are. "" — the whole unit rolls back; "*" — nothing does.
	StopsAt string `json:"stopsAt,omitempty"`
}

// stopsAt resolves where an automatic rollback of the unit stops, from the policy.
func (u *Unit) stopsAt(policy Policy) {
	if policy.NoRollbackAll {
		u.StopsAt = "*"
		return
	}
	for _, k := range u.RollbackOrder {
		if slices.Contains(policy.NoRollback, k) {
			u.StopsAt = k
			break
		}
	}
}

// Policy is what the operator decides before the saga starts; it is recorded
// with the saga and inherited by a resume.
type Policy struct {
	Savepoints    string   `json:"savepoints,omitempty"`    // "" (composition) | auto | none | k1,k2
	NoRollback    []string `json:"noRollback,omitempty"`    // where the rollback stops
	NoRollbackAll bool     `json:"noRollbackAll,omitempty"` // roll nothing back
	Strict        bool     `json:"strict,omitempty"`        // refuse a plan with an irreversible step
}

type Plan struct {
	Pack      string   `json:"pack"`
	From      string   `json:"from,omitempty"` // "" for a first installation
	To        string   `json:"to"`
	Direction string   `json:"direction"` // install | upgrade | downgrade | uninstall | undo | action
	Order     []string `json:"order"`
	Changes   []Change `json:"changes"`
	Steps     []Step   `json:"steps"`
	Units     []Unit   `json:"units"`
	Policy    Policy   `json:"policy"`
	Warnings  []string `json:"warnings,omitempty"`
	// Rollback says how an apply of a key that changes version is undone
	// beyond applying the previous unit: the downgrade hooks around it.
	Rollback map[string]KeyRollback `json:"rollback,omitempty"`
	// Revision is the revision of the inputs (the commit) the plan was made from — the digest covers it, so a plan
	// of another revision is another plan, and --approve of its digest is refused.
	Revision string `json:"revision,omitempty"`
	// RollbackTimeout is how long undoing the plan may take when the apply is cut short (SIGTERM, Ctrl-C): the
	// steps' timeouts, capped at RollbackCap — what an operator sets terminationGracePeriodSeconds from (Seal).
	RollbackTimeout string `json:"rollback_timeout,omitempty"`
	// Risks are what an apply should not do unasked, Digest what --approve names (Seal).
	Risks  []Risk `json:"risks,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// RollbackCap is how long a saga lets a rollback run after a failure; pkg/saga keeps the same cap.
const RollbackCap = 10 * time.Minute

// KeyRollback is what undoing a key's apply runs around applying its previous
// unit: the installed version's hooks for the reverse transition — the
// component's (preDowngrade/postDowngrade of an upgrade, preUpgrade/
// postUpgrade of a downgrade) and the composition's — and a stop first when
// a pre hook is exclusive. Images: new is the version being restored, old
// the one being rolled back.
type KeyRollback struct {
	Stop bool   `json:"stop,omitempty"`
	Pre  []Step `json:"pre,omitempty"`
	Post []Step `json:"post,omitempty"`
}

// Input is everything the plan is derived from. From nil is a first
// installation: every key is added, the install hooks run around the first
// apply. FromOut may be nil when the old version cannot be rendered with the
// new values — then config-only changes are not detected and a warning says so.
// To nil is an uninstall: every key of the installed release is removed.
type Input struct {
	From, To       *bundle.Bundle
	FromOut, ToOut *render.Release
	Policy         Policy
	// Drifted are the keys the target does not hold as the record says (key → how); a key that is otherwise
	// unchanged is applied again.
	Drifted map[string]string
	// Held is what the target says it already holds of a key, when the installation keeps no record
	// (state: observe): a key that matches is no change, one that differs is a config change, one that is absent
	// is added. Consulted only when From is nil.
	Held map[string]target.Hold
}

func Build(in Input) (*Plan, error) {
	if in.To == nil {
		return uninstall(in), nil
	}
	install := in.From == nil
	p := &Plan{Pack: in.To.Name, To: in.To.Version, Policy: in.Policy, Direction: "install"}
	if !install {
		p.From = in.From.Version
		p.Direction = direction(in.From.Version, in.To.Version)
	}
	order := append([]string{}, in.ToOut.Report.Order...)
	if p.Direction == "downgrade" {
		slices.Reverse(order)
	}
	p.Order = order
	if !install && in.FromOut == nil {
		p.Warnings = append(p.Warnings, "the previous version does not render with these values: config-only changes are not detected")
	}

	// changes
	changed := map[string]Change{}
	for _, key := range order {
		c, err := change(in, key)
		if err != nil {
			return nil, err
		}
		p.Changes = append(p.Changes, c)
		if c.Kind != "none" {
			changed[key] = c
		}
	}
	for _, key := range fromKeys(in) {
		if _, still := in.To.KeyPack[key]; !still {
			c := Change{Key: key, Component: in.From.KeyPack[key], From: in.From.Packs[in.From.KeyPack[key]].Version, Kind: "removed"}
			p.Changes = append(p.Changes, c)
			changed[key] = c
		}
	}

	// steps
	n := 0
	add := func(s Step) {
		n++
		s.N = n
		p.Steps = append(p.Steps, s)
	}
	stepsOf := map[string][]int{}
	for _, key := range order {
		c, ok := changed[key]
		if !ok {
			continue
		}
		first := len(p.Steps) + 1
		comp := in.To.Packs[in.To.KeyPack[key]]
		dir := c.Direction
		if len(dir) == 0 {
			dir = p.Direction
		}
		if c.Kind == "image" || c.Kind == "added" {
			if err := upgradeFrom(comp, c, p); err != nil {
				return nil, err
			}
		}
		pre, post := selectHooks(comp, c, dir, p)
		if c.Kind == "config" || c.Kind == "schema" || c.Kind == "drift" {
			pre, post = nil, nil // no version transition, no hooks
		}
		exclusive := false
		for _, h := range pre {
			exclusive = exclusive || h.Exclusive
		}
		if exclusive && dir != "install" {
			add(Step{Key: key, Kind: Stop, Detail: "exclusive hook follows"})
		}
		for _, h := range pre {
			add(hookStep(key, h, c, images(in, key)))
		}
		add(Step{Key: key, Kind: Apply, Detail: applyDetail(c, install), Compensate: applyCompensate(c)})
		switch {
		case comp.Root.RestartPolicy == "Never":
			add(Step{Key: key, Kind: Wait, Detail: "finished (run to completion)"})
		case comp.Root.Readiness != nil:
			add(Step{Key: key, Kind: Wait, Detail: "ready (" + readiness(comp.Root.Readiness) + ")"})
		}
		for _, h := range post {
			add(hookStep(key, h, c, images(in, key)))
		}
		for i := first; i <= len(p.Steps); i++ {
			stepsOf[key] = append(stepsOf[key], i)
		}
	}
	for _, c := range p.Changes {
		if c.Kind == "removed" {
			add(Step{Key: c.Key, Kind: Remove, Detail: "no longer in the pack; children are weeded, PVs stay", Compensate: "apply " + c.From})
			stepsOf[c.Key] = []int{len(p.Steps)}
		}
	}

	// how each changed version is undone
	for _, key := range order {
		c, ok := changed[key]
		if !ok || c.Kind != "image" || in.From == nil {
			continue
		}
		dir := c.Direction
		if len(dir) == 0 {
			dir = p.Direction
		}
		if kr, ok := rollbackOf(in, key, c, dir, p); ok {
			if p.Rollback == nil {
				p.Rollback = map[string]KeyRollback{}
			}
			p.Rollback[key] = kr
		}
	}

	// savepoints → units
	savepoints, err := savePoints(in, changed)
	if err != nil {
		return nil, err
	}
	start := 1
	var unitKeys []string
	flush := func(sp string, end int) {
		if end < start {
			return
		}
		u := Unit{From: start, To: end, Savepoint: sp, Keys: append([]string{}, unitKeys...)}
		u.RollbackOrder = append([]string{}, unitKeys...)
		slices.Reverse(u.RollbackOrder)
		u.stopsAt(in.Policy)
		p.Units = append(p.Units, u)
		start = end + 1
		unitKeys = nil
	}
	for _, key := range order {
		if _, ok := changed[key]; !ok {
			continue
		}
		unitKeys = append(unitKeys, key)
		if savepoints[key] {
			flush(key, last(stepsOf[key]))
		}
	}
	for _, c := range p.Changes {
		if c.Kind == "removed" {
			unitKeys = append(unitKeys, c.Key)
		}
	}
	flush("end", len(p.Steps))

	if in.Policy.Strict {
		for _, s := range p.Steps {
			if s.Irreversible {
				return nil, fmt.Errorf("--strict: step %d (%s %s) is irreversible: no compensate", s.N, s.Key, s.Name)
			}
		}
	}
	p.assignTargets(in.ToOut.Report.Targets, fromTargets(in))
	return p, nil
}

// assignTargets puts on each step the target of its key: the target version's, and for a key only the installed
// version has, that one's.
func (p *Plan) assignTargets(to, from map[string]string) {
	for i := range p.Steps {
		s := &p.Steps[i]
		if t, ok := to[s.Key]; ok {
			s.Target = t
		} else if t, ok := from[s.Key]; ok {
			s.Target = t
		}
	}
}

func fromTargets(in Input) map[string]string {
	if in.FromOut == nil {
		return nil
	}
	return in.FromOut.Report.Targets
}

// uninstall is the plan of pack delete: every key of the installed release
// removed, children first — the reverse of the order it renders in — all of
// it one unit, so a failed removal is rolled back like any plan's. PVs and
// credentials stay; deleting the record is the saga's last act, not a step.
func uninstall(in Input) *Plan {
	p := &Plan{Pack: in.From.Name, From: in.From.Version, Policy: in.Policy, Direction: "uninstall"}
	order := append([]string{}, fromKeys(in)...)
	if in.FromOut != nil {
		order = append([]string{}, in.FromOut.Report.Order...)
	}
	slices.Reverse(order)
	p.Order = order
	var keys []string
	for _, key := range order {
		comp := in.From.Packs[in.From.KeyPack[key]]
		p.Changes = append(p.Changes, Change{Key: key, Component: comp.Name, From: comp.Version, Kind: "removed"})
		p.Steps = append(p.Steps, Step{N: len(p.Steps) + 1, Key: key, Kind: Remove,
			Detail: "the pack is deleted; PVs and credentials stay", Compensate: "apply " + comp.Version})
		keys = append(keys, key)
	}
	if len(keys) > 0 {
		u := Unit{From: 1, To: len(p.Steps), Savepoint: "end", Keys: keys,
			RollbackOrder: slices.Clone(keys)}
		slices.Reverse(u.RollbackOrder)
		u.stopsAt(in.Policy)
		p.Units = []Unit{u}
	}
	p.assignTargets(nil, fromTargets(in))
	return p
}

func fromKeys(in Input) []string {
	if in.From == nil {
		return nil
	}
	return in.From.Keys
}

// change classifies one key of the new version against the old. A key the old
// version did not have — or every key of a first installation — is added: it
// is installed, whatever the direction of the rest of the plan. A first
// installation that keeps no record (Held) classifies from what the target
// holds: what is already there exactly is no change, what differs is a config
// change.
func change(in Input, key string) (Change, error) {
	toComp := in.To.Packs[in.To.KeyPack[key]]
	c := Change{Key: key, Component: toComp.Name, To: toComp.Version, Kind: "none"}
	if in.From == nil {
		switch in.Held[key] {
		case target.HoldMatches:
			return c, nil
		case target.HoldDiffers:
			c.Kind = "config"
			return c, nil
		}
		c.Kind, c.Direction = "added", "install"
		return c, nil
	}
	fromName, existed := in.From.KeyPack[key]
	if !existed {
		c.Kind, c.Direction = "added", "install"
		return c, nil
	}
	fromComp := in.From.Packs[fromName]
	c.From = fromComp.Version
	if fromName != toComp.Name {
		c.Kind = "image"
		c.Direction = "upgrade"
		return c, nil
	}
	c.Direction = direction(c.From, c.To)
	switch {
	case fromComp.ImageRef != toComp.ImageRef || (c.From != c.To && len(c.From) > 0 && len(c.To) > 0):
		c.Kind = "image"
	case digestOf(fromComp.SchemaRaw) != digestOf(toComp.SchemaRaw):
		c.Kind = "schema"
	case in.FromOut != nil && (!sameFiles(in.FromOut, in.ToOut, key) || !sameUnit(in.FromOut, in.ToOut, key)):
		c.Kind = "config"
	}
	if _, drifted := in.Drifted[key]; drifted && c.Kind == "none" {
		c.Kind = "drift"
	}
	return c, nil
}

// sameFiles compares what a key's config files hold, not what they are
// called: a target names them stably, so a name says nothing about a change.
func sameFiles(a, b *render.Release, key string) bool {
	files := func(rel *render.Release) ([]render.File, []render.SecretFile) {
		for _, u := range rel.Units {
			if u.Key == key {
				return u.Files, u.SecretFiles
			}
		}
		return nil, nil
	}
	af, as := files(a)
	bf, bs := files(b)
	return reflect.DeepEqual(localURLs(af), localURLs(bf)) && reflect.DeepEqual(as, bs)
}

// localURLs is the files with the directory part of a file:// URL cut off: it names the machine's directory the
// pack was loaded from — the live one for the wanted release, the cache copy for the recorded one — so the same
// pack would forever differ from itself. The file it names is identified by what follows (templates/<name>), the
// content says the rest.
func localURLs(fs []render.File) []render.File {
	var out []render.File
	for i, f := range fs {
		if j := strings.LastIndex(f.URL, "/templates/"); strings.HasPrefix(f.URL, "file://") && j >= 0 {
			if out == nil {
				out = slices.Clone(fs)
			}
			out[i].URL = "file://" + f.URL[j:]
		}
	}
	if out == nil {
		return fs
	}
	return out
}

// sameUnit compares the rest of what a key runs as — its placement (replicas,
// where it runs), env, resources, command — as JSON: a number read back from
// a record and the same number from a values file are one value.
func sameUnit(a, b *render.Release, key string) bool {
	rest := func(rel *render.Release) []byte {
		for _, u := range rel.Units {
			if u.Key == key {
				u.Files, u.SecretFiles = nil, nil
				u.Packages = maps.Clone(u.Packages)
				for osName, p := range u.Packages {
					p.Repo, p.Files = localURLs(p.Repo), localURLs(p.Files)
					u.Packages[osName] = p
				}
				j, _ := json.Marshal(u)
				return j
			}
		}
		return nil
	}
	return bytes.Equal(rest(a), rest(b))
}

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func direction(from, to string) string {
	fv, err1 := semver.Parse(from)
	tv, err2 := semver.Parse(to)
	if err1 != nil || err2 != nil {
		return "upgrade"
	}
	if tv.Compare(fv) < 0 {
		return "downgrade"
	}
	return "upgrade"
}

// upgradeFrom refuses a transition the target component does not accept.
func upgradeFrom(comp *bundle.Pack, c Change, p *Plan) error {
	if comp.Root.Upgrade == nil || len(c.From) == 0 {
		return nil
	}
	cons, err := semver.ParseConstraint(comp.Root.Upgrade.From)
	if err != nil {
		return fmt.Errorf("%s: upgrade.from: %w", c.Key, err)
	}
	v, err := semver.Parse(c.From)
	if err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s: version %q is not semver; upgrade.from not checked", c.Key, c.From))
		return nil
	}
	if !cons.Check(v) {
		return fmt.Errorf("%s: %s %s cannot be upgraded from %s (upgrade.from: %s) — go through an intermediate version", c.Key, comp.Name, c.To, c.From, comp.Root.Upgrade.From)
	}
	return nil
}

// phases are the hook phases a direction selects.
func phases(dir string) (pre, post packschema.Phase) {
	switch dir {
	case "install":
		return "preInstall", "postInstall"
	case "downgrade":
		return "preDowngrade", "postDowngrade"
	}
	return "preUpgrade", "postUpgrade"
}

// selectHooks picks the component's hooks for this transition: the phases of
// the direction, filtered by `when` against the two versions (an install hook
// sees only `to`).
func selectHooks(comp *bundle.Pack, c Change, dir string, p *Plan) (pre, post []packschema.Hook) {
	prePhase, postPhase := phases(dir)
	for _, h := range comp.Root.Hooks {
		if h.Phase != prePhase && h.Phase != postPhase {
			continue
		}
		if h.When != nil {
			ok, known := whenMatches(h.When, c.From, c.To)
			if !known {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: hook %s has `when` but a version is unknown (from %q, to %q); skipped", c.Key, h.Name, c.From, c.To))
				continue
			}
			if !ok {
				continue
			}
		}
		if h.Phase == prePhase {
			pre = append(pre, h)
		} else {
			post = append(post, h)
		}
	}
	return pre, post
}

func whenMatches(w *packschema.When, from, to string) (ok, known bool) {
	check := func(cons, version string) (bool, bool) {
		if len(cons) == 0 {
			return true, true
		}
		c, err := semver.ParseConstraint(cons)
		if err != nil {
			return false, true
		}
		v, err := semver.Parse(version)
		if err != nil {
			return false, false
		}
		return c.Check(v), true
	}
	okFrom, knownFrom := check(w.From, from)
	okTo, knownTo := check(w.To, to)
	if len(from) == 0 && len(w.From) == 0 { // an installation: there is no version to come from
		okFrom, knownFrom = true, true
	}
	return okFrom && okTo, knownFrom && knownTo
}

// rollbackOf is the reverse of a key's version transition: the installed
// version's hooks for it, their images resolved against the two versions
// the other way round.
func rollbackOf(in Input, key string, c Change, dir string, p *Plan) (KeyRollback, bool) {
	fromName, ok := in.From.KeyPack[key]
	if !ok {
		return KeyRollback{}, false
	}
	fromComp := in.From.Packs[fromName]
	revDir := "downgrade"
	if dir == "downgrade" {
		revDir = "upgrade"
	}
	rev := Change{Key: key, Component: fromComp.Name, From: c.To, To: c.From, Kind: c.Kind, Direction: revDir}
	scratch := &Plan{}
	pre, post := selectHooks(fromComp, rev, revDir, scratch)
	for _, w := range scratch.Warnings {
		p.Warnings = append(p.Warnings, "rollback: "+w)
	}
	if len(pre) == 0 && len(post) == 0 {
		return KeyRollback{}, false
	}
	image := func(sel string) string {
		switch sel {
		case "", "new":
			return fromComp.ImageRef
		case "old":
			return in.To.Packs[in.To.KeyPack[key]].ImageRef
		}
		if m, ok := in.From.Packs[sel]; ok {
			return m.ImageRef
		}
		return ""
	}
	var kr KeyRollback
	for _, h := range pre {
		kr.Stop = kr.Stop || h.Exclusive
		kr.Pre = append(kr.Pre, hookStep(key, h, rev, image))
	}
	for _, h := range post {
		kr.Post = append(kr.Post, hookStep(key, h, rev, image))
	}
	return kr, true
}

// images resolves a hook's image for a key: new (the default) — the key's
// image in the target version, old — in the installed one, anything else —
// the member of that name (a composition hook).
func images(in Input, key string) func(sel string) string {
	return func(sel string) string {
		switch sel {
		case "", "new":
			return in.To.Packs[in.To.KeyPack[key]].ImageRef
		case "old":
			if in.From == nil {
				return ""
			}
			if name, ok := in.From.KeyPack[key]; ok {
				return in.From.Packs[name].ImageRef
			}
			return ""
		}
		if m, ok := in.To.Packs[sel]; ok {
			return m.ImageRef
		}
		return ""
	}
}

func hookStep(key string, h packschema.Hook, c Change, image func(string) string) Step {
	s := Step{Key: key, Kind: Hook, Name: h.Name, Phase: string(h.Phase), Timeout: h.Timeout,
		Idempotent: h.Idempotent, Exclusive: h.Exclusive,
		Image: image(h.Image), Command: h.Command}
	switch {
	case len(c.From) > 0 && len(c.To) > 0:
		s.Detail = fmt.Sprintf("%s → %s", c.From, c.To)
	case len(c.To) > 0:
		s.Detail = c.To
	}
	if h.Compensate != nil {
		s.Compensate = strings.Join(h.Compensate.Command, " ")
		s.CompensateImage, s.CompensateCommand = image(h.Compensate.Image), h.Compensate.Command
	} else if !h.Idempotent {
		s.Irreversible = true
	}
	return s
}

func applyDetail(c Change, install bool) string {
	switch c.Kind {
	case "added":
		if install {
			return c.To
		}
		return strings.TrimSpace(c.To + " (new key)")
	case "config":
		return "config only"
	case "schema":
		return "schema changed, image the same"
	case "drift":
		return "the target lost it: applied again"
	}
	if len(c.To) > 0 {
		return c.To
	}
	return "image changed"
}

// applyCompensate is how an apply is undone: the previous revision, or — for
// a key that did not exist — removal (the children are weeded, the PVs stay).
func applyCompensate(c Change) string {
	switch {
	case c.Kind == "added":
		return "remove (PVs stay)"
	case len(c.From) > 0:
		return "apply " + c.From
	}
	return "apply the previous revision"
}

func readiness(r *packschema.Readiness) string {
	switch {
	case len(r.TCP) > 0:
		return "tcp:" + r.TCP
	case len(r.HTTP) > 0:
		return "http:" + r.HTTP
	}
	return "exec"
}

// savePoints resolves the policy: the composition's list, or auto (every
// changed key), or none, or the operator's list.
func savePoints(in Input, changed map[string]Change) (map[string]bool, error) {
	out := map[string]bool{}
	spec := in.Policy.Savepoints
	var list []string
	switch spec {
	case "auto":
		for k := range changed {
			out[k] = true
		}
		return out, nil
	case "none":
		return out, nil
	case "":
		list = in.To.Root.Root.Savepoints
		if len(list) == 0 {
			for k := range changed {
				out[k] = true
			}
			return out, nil
		}
	default:
		list = strings.Split(spec, ",")
	}
	for _, k := range list {
		k = strings.TrimSpace(k)
		if _, ok := in.To.KeyPack[k]; !ok {
			return nil, fmt.Errorf("savepoint %q is not a pack this pack holds", k)
		}
		out[k] = true
	}
	return out, nil
}

// Text renders the plan the way the operator reads it before saying yes.
func (p *Plan) Text() string {
	var b strings.Builder
	switch p.Direction {
	case "install":
		_, _ = fmt.Fprintf(&b, "%s: install %s\n", p.Pack, orUnknown(p.To))
	case "uninstall":
		_, _ = fmt.Fprintf(&b, "%s: uninstall %s\n", p.Pack, orUnknown(p.From))
	default:
		_, _ = fmt.Fprintf(&b, "%s: %s → %s (%s)\n", p.Pack, orUnknown(p.From), orUnknown(p.To), p.Direction)
	}
	unchanged := 0
	for _, c := range p.Changes {
		if c.Kind == "none" {
			unchanged++
		}
	}
	if unchanged > 0 {
		_, _ = fmt.Fprintf(&b, "unchanged: %d key(s)\n", unchanged)
	}
	if len(p.Steps) == 0 {
		b.WriteString("nothing to do\n")
		return b.String()
	}
	unitEnd := map[int]Unit{}
	for _, u := range p.Units {
		unitEnd[u.To] = u
	}
	var irreversible []string
	for _, s := range p.Steps {
		right := s.Detail
		switch s.Kind {
		case Hook:
			extra := ""
			if len(s.Compensate) > 0 {
				extra = "compensate: " + s.Compensate
			} else if s.Idempotent {
				extra = "idempotent"
			} else {
				extra = "IRREVERSIBLE"
				irreversible = append(irreversible, fmt.Sprint(s.N))
			}
			if len(s.Timeout) > 0 {
				extra += "   ~" + s.Timeout
			}
			name := s.Name
			if len(s.Detail) > 0 {
				name += " (" + s.Detail + ")"
			}
			right = fmt.Sprintf("%-40s %s", name, extra)
			_, _ = fmt.Fprintf(&b, "%3d. %-16s hook  %s\n", s.N, label(s), right)
		default:
			_, _ = fmt.Fprintf(&b, "%3d. %-16s %-5s %s\n", s.N, label(s), s.Kind, right)
		}
		if u, ok := unitEnd[s.N]; ok {
			line := fmt.Sprintf("     ── savepoint: %s ──   unit %d–%d", u.Savepoint, u.From, u.To)
			if u.Savepoint == "end" {
				line = fmt.Sprintf("     ── end ──   unit %d–%d", u.From, u.To)
			}
			if len(u.RollbackOrder) > 1 {
				line += "; rollback order: " + strings.Join(u.RollbackOrder, " → ")
			}
			switch u.StopsAt {
			case "":
			case "*":
				line += "; NO automatic rollback"
			default:
				line += "; rollback stops at " + u.StopsAt
			}
			b.WriteString(line + "\n")
		}
	}
	policy := "auto, to last savepoint"
	switch {
	case p.Policy.NoRollbackAll:
		policy = "none"
	case len(p.Policy.NoRollback) > 0:
		policy = "auto, stops at " + strings.Join(p.Policy.NoRollback, ", ")
	}
	irr := "none"
	if len(irreversible) > 0 {
		irr = "steps " + strings.Join(irreversible, ", ")
	}
	_, _ = fmt.Fprintf(&b, "rollback policy: %s; irreversible: %s\n", policy, irr)
	if len(p.RollbackTimeout) > 0 {
		_, _ = fmt.Fprintf(&b, "cut short (SIGTERM, Ctrl-C): the rollback takes up to %s — a Pod's terminationGracePeriodSeconds at least that\n", p.RollbackTimeout)
	}
	for _, key := range p.Order {
		kr, ok := p.Rollback[key]
		if !ok {
			continue
		}
		var parts []string
		if kr.Stop {
			parts = append(parts, "stop")
		}
		for _, s := range kr.Pre {
			parts = append(parts, s.Phase+" "+s.Name)
		}
		parts = append(parts, "apply the previous unit")
		for _, s := range kr.Post {
			parts = append(parts, s.Phase+" "+s.Name)
		}
		_, _ = fmt.Fprintf(&b, "rollback of %s: %s\n", key, strings.Join(parts, " → "))
	}
	for _, w := range p.Warnings {
		_, _ = fmt.Fprintf(&b, "warning: %s\n", w)
	}
	for _, r := range p.Risks {
		_, _ = fmt.Fprintf(&b, "risk: %s: %s\n", r.Kind, r.Why)
	}
	if len(p.Digest) > 0 {
		_, _ = fmt.Fprintf(&b, "plan %s\n", p.Digest)
	}
	return b.String()
}

func orUnknown(v string) string {
	if len(v) == 0 {
		return "?"
	}
	return v
}

func last(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	return xs[len(xs)-1]
}

// label is a step's key, and the target where it is not the default one: namenode@edge.
func label(s Step) string {
	if len(s.Target) > 0 {
		return s.Key + "@" + s.Target
	}
	return s.Key
}
