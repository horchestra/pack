package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	packer "github.com/arenadata/oci-packer/types"
	"github.com/horchestra/pack/internal/adapter"
	"github.com/horchestra/pack/internal/graph"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/values"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/format"
	"sigs.k8s.io/yaml"
)

// Change is what apply and plan both work out: the installed version, the wanted one, and the plan between.
type Change struct {
	Ref         string
	Record      *saga.Record
	FromRel     *render.Release
	ToB         *bundle.Bundle
	ToRel       *render.Release
	Values      render.Values
	Credentials []render.Credential
	Plan        *plan.Plan
	Drifts      []Drift // keys the target lost or changed since the record; applied again
}

// Prepare reads what is installed and renders what the release wants. provision: the passwords the pack asks
// for are created (apply); without it they are only looked for (plan).
func (s *Session) Prepare(provision bool) (*Change, error) {
	rec, fromB, fromRel, err := s.Installed()
	if err != nil {
		return nil, err
	}
	var ref string
	var v render.Values
	var toB *bundle.Bundle
	if s.Bundle != nil {
		// the pack is in memory already: nothing is generated, written or read back
		toB = s.Bundle
		ref = toB.Dir
		if s.Values != nil {
			v = *s.Values
		}
	} else {
		if ref, err = s.Dir.Release.PackRef(); err != nil {
			return nil, err
		}
		if ref, err = s.Pinned(ref); err != nil { // a pack in a registry is the digest the lock says
			return nil, err
		}
		if v, err = values.Load(s.Dir.Release.ValuesFiles()); err != nil {
			return nil, err
		}
		if toB, err = s.Pack(ref); err != nil {
			return nil, err
		}
	}
	c := &Change{Ref: ref, Record: rec, FromRel: fromRel, ToB: toB}
	if provision {
		sec := s.Env.Secrets
		if c.Credentials, err = render.Provision(toB, &v, s.ID, render.Provider(sec.Provider), sec.Provision); err != nil {
			return nil, err
		}
	}
	if c.ToRel, err = render.Render(toB, v, s.Ctx); err != nil {
		return nil, err
	}
	if provision {
		c.Credentials = append(c.Credentials, c.ToRel.Credentials...)
		if err := render.WriteBack(&v, c.ToRel.Credentials); err != nil {
			return nil, err
		}
	}
	c.Values = v
	// state: observe — the plan is derived from what the targets say they hold, not from a record
	var held map[string]target.Hold
	if s.Dir.Release.State == workdir.StateObserve {
		if held, err = s.holdOf(c.ToRel, c.Credentials); err != nil {
			return nil, err
		}
		for _, key := range s.Force {
			if _, ok := held[key]; ok {
				held[key] = target.HoldDiffers
			}
		}
	}
	if c.Drifts, err = s.Drifts(s.ctx, rec, fromRel); err != nil {
		return nil, err
	}
	var drifted map[string]string
	if len(c.Drifts) > 0 {
		drifted = map[string]string{}
		for _, d := range c.Drifts {
			drifted[d.Key] = d.Why
		}
	}
	c.Plan, err = plan.Build(plan.Input{From: fromB, To: toB, FromOut: fromRel, ToOut: c.ToRel, Drifted: drifted,
		Held:   held,
		Policy: plan.Policy{Strict: s.Env.Run.Strict, NoRollbackAll: s.Env.Run.NoRollback}})
	if err == nil {
		for _, d := range c.Drifts {
			c.Plan.Warnings = append(c.Plan.Warnings, "drift: "+d.String())
		}
		c.Plan.Revision = release.Revision(s.Dir.Release.Dir)
		plan.Seal(c.Plan, fromRel, c.ToRel)
	}
	return c, err
}

// holdOf asks the target of each key whether it already holds what the key's unit says (state: observe). Every
// target a key runs on must be able to tell (target.Inspector); one that cannot leaves the release to a recorded
// state (state: file, target or db).
func (s *Session) holdOf(rel *render.Release, creds []render.Credential) (map[string]target.Hold, error) {
	held := map[string]target.Hold{}
	for _, u := range rel.Units {
		x := any(s.Inst)
		if name := rel.Report.Targets[u.Key]; len(name) > 0 {
			var ok bool
			if x, ok = s.Execs[name]; !ok {
				return nil, fmt.Errorf("%s: runs on target %q, which is not open", u.Key, name)
			}
		}
		insp, ok := x.(target.Inspector)
		if !ok {
			return nil, fmt.Errorf("state: observe: the %s target cannot tell whether it holds %s: it needs the inspect capability (or keep a record: state: file, target or db)", s.Target, u.Key)
		}
		var keyCreds []render.Credential
		for _, c := range creds {
			if c.Key == u.Key {
				keyCreds = append(keyCreds, c)
			}
		}
		h, err := insp.Inspect(s.ctx, u, keyCreds)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u.Key, err)
		}
		held[u.Key] = h
	}
	return held, nil
}

// Render is the release's pack rendered with the release's values and nothing else: no installed version is read,
// no password is made (a password the store does not hold yet is left unfilled). What template and graph show.
func (s *Session) Render() (*bundle.Bundle, *render.Release, error) {
	if s.Bundle != nil {
		var v render.Values
		if s.Values != nil {
			v = *s.Values
		}
		rel, err := render.Render(s.Bundle, v, s.Ctx)
		return s.Bundle, rel, err
	}
	ref, err := s.Dir.Release.PackRef()
	if err != nil {
		return nil, nil, err
	}
	if ref, err = s.Pinned(ref); err != nil {
		return nil, nil, err
	}
	v, err := values.Load(s.Dir.Release.ValuesFiles())
	if err != nil {
		return nil, nil, err
	}
	b, err := s.Pack(ref)
	if err != nil {
		return nil, nil, err
	}
	rel, err := render.Render(b, v, s.Ctx)
	return b, rel, err
}

// Apply runs the saga of the change; the keys the installed version had and the new one has not go afterwards.
func (s *Session) Apply(ctx context.Context, c *Change) error {
	g, err := graph.New(adapter.Specs(c.ToRel, c.Plan))
	if err != nil {
		return err
	}
	vals, err := json.Marshal(c.Values)
	if err != nil {
		return err
	}
	run := s.Run(g, &adapter.Exec{Plan: c.Plan, From: c.FromRel, To: c.ToRel, Credentials: c.Credentials, X: s.Inst, Execs: s.Execs})
	record := RecordRef(c.Ref)
	if s.Dir.Release.State != workdir.StateObserve {
		if record, err = recordRef(c.Ref, c.ToB); err != nil {
			return err
		}
	}
	run.To = saga.Installation{Pack: record, Version: c.ToB.Version, Values: vals}
	run.Finish = func(ctx context.Context) error { return s.RemoveDropped(ctx, c.FromRel, c.ToRel) }
	return run.Execute(ctx)
}

// unlocked is a Store seen only as a Store: the saga that holds the lock runs another over the same record.
type unlocked struct{ saga.Store }

// credentialsOf are the credentials the releases took, for the steps that put a version back.
func credentialsOf(rels ...*render.Release) []render.Credential {
	var out []render.Credential
	for _, r := range rels {
		if r != nil {
			out = append(out, r.Credentials...)
		}
	}
	return out
}

// RemoveDropped takes away the keys the installed version has and the new one does not, dependents first.
func (s *Session) RemoveDropped(ctx context.Context, from, to *render.Release) error {
	if from == nil || to == nil {
		return nil
	}
	have := map[string]bool{}
	for _, u := range to.Units {
		have[u.Key] = true
	}
	dropped := &render.Release{Report: from.Report}
	for _, u := range from.Units {
		if !have[u.Key] {
			dropped.Units = append(dropped.Units, u)
		}
	}
	if len(dropped.Units) == 0 {
		return nil
	}
	g, err := graph.New(adapter.Specs(dropped, nil))
	if err != nil {
		return err
	}
	run := s.Run(g, &adapter.Exec{From: dropped, X: s.Inst, Execs: s.Execs})
	run.Store = unlocked{run.Store} // called from a saga that holds the lock
	return run.RemoveNodes(ctx)
}

// Remove takes the whole installation away, dependents first; data and credentials stay. The record goes.
// destroy: the keys' OS packages go too (pack delete --destroy) — a host the installation shares keeps them
// without it.
func (s *Session) Remove(ctx context.Context, g *graph.Graph, rel *render.Release, destroy ...bool) error {
	return s.Run(g, &adapter.Exec{From: rel, X: s.Inst, Execs: s.Execs, Destroy: len(destroy) > 0 && destroy[0]}).Remove(ctx)
}

// Undo takes an unfinished saga back, whole.
func (s *Session) Undo(ctx context.Context, g *graph.Graph, from, to *render.Release) error {
	return s.Run(g, &adapter.Exec{From: from, To: to, Credentials: credentialsOf(from, to), X: s.Inst, Execs: s.Execs}).Undo(ctx)
}

// RollForward runs a saga to a version the record names (the one the last apply replaced).
func (s *Session) RollForward(ctx context.Context, p *plan.Plan, from, to *render.Release, in saga.Installation) error {
	g, err := graph.New(adapter.Specs(to, p))
	if err != nil {
		return err
	}
	run := s.Run(g, &adapter.Exec{Plan: p, From: from, To: to, Credentials: credentialsOf(from, to), X: s.Inst, Execs: s.Execs})
	run.To = in
	run.Finish = func(ctx context.Context) error { return s.RemoveDropped(ctx, from, to) }
	return run.Execute(ctx)
}

// Version renders a version a record names with the values it recorded, against the environment as it is now.
func (s *Session) Version(in saga.Installation) (*bundle.Bundle, *render.Release, error) {
	if len(in.Pack) == 0 {
		return nil, nil, nil
	}
	var v render.Values
	if len(in.Values) > 0 {
		if err := json.Unmarshal(in.Values, &v); err != nil {
			return nil, nil, fmt.Errorf("the recorded values: %w", err)
		}
	}
	b, err := s.Pack(in.Pack)
	if err != nil {
		return nil, nil, err
	}
	if strings.HasPrefix(in.Pack, "sdk://") && len(in.Version) > 0 && b.Version != in.Version {
		return nil, nil, fmt.Errorf("%s %s: the installed version was recorded without a cache snapshot; the process holds %s", in.Pack, in.Version, b.Version)
	}
	rel, err := render.Render(b, v, s.Ctx)
	if err != nil {
		return nil, nil, err
	}
	return b, rel, nil
}

// RecordRef is how the installed version is found again: a pack in a registry by its digest, a directory as an
// absolute path.
func RecordRef(ref string) string {
	if oci.IsRef(ref) || strings.HasPrefix(ref, "sdk://") {
		return ref
	}
	if abs, err := filepath.Abs(ref); err == nil {
		return abs
	}
	return ref
}

// recordRef is RecordRef that can see the bundle: a directory pack goes into the cache (PACK_CACHE/packs), and an
// in-memory pack is snapshotted there, with the packs each holds beside it. The path named by the digest of the
// files outlives the checkout or process; a later run reads the installed version there.
func recordRef(ref string, b *bundle.Bundle) (string, error) {
	if oci.IsRef(ref) || b == nil {
		return RecordRef(ref), nil
	}
	cached, err := cachePack(b)
	if err != nil {
		if strings.HasPrefix(ref, "sdk://") {
			return "", fmt.Errorf("cache the embedded pack: %w", err)
		}
		return RecordRef(ref), nil
	}
	return cached, nil
}

// snapshotFiles renders an in-memory pack as the directory bundle.Load reads.
func snapshotFiles(b *bundle.Bundle, p *bundle.Pack) (map[string][]byte, error) {
	pf := packer.Pack{
		Metadata: packer.Metadata{Annotations: map[string]string{
			bundle.AnnotationName: p.Name, bundle.AnnotationVersion: p.Version,
		}},
		Type:  bundle.ArtifactPack,
		Items: []packer.Descriptor{{From: "file://" + format.SchemaFile, Type: bundle.ArtifactSchema}},
	}
	if len(p.Templates) > 0 {
		pf.Items = append(pf.Items, packer.Descriptor{From: "dir://" + format.TemplatesDir + "/", Type: bundle.ArtifactTemplate})
	}
	if len(p.ImageRef) > 0 {
		pf.Items = append(pf.Items, packer.Descriptor{From: "oci://" + p.ImageRef, Annotations: map[string]string{bundle.AnnotationImage: "true"}})
	}
	for _, h := range p.Holds {
		held := b.Packs[h.Pack]
		if held == nil {
			return nil, fmt.Errorf("pack %q holds %q, which is not in the bundle", p.Name, h.Pack)
		}
		pf.Items = append(pf.Items, packer.Descriptor{From: fmt.Sprintf("oci://%s/%s:%s", sdk.DefaultRepo, held.Name, held.Version)})
	}
	raw, err := yaml.Marshal(&pf)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{format.SchemaFile: p.SchemaRaw, format.PackFile: raw}
	for name, data := range p.Templates {
		files[filepath.Join(format.TemplatesDir, filepath.FromSlash(name))] = data
	}
	if p.Image != nil {
		if files["image.json"], err = json.Marshal(p.Image); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// cachePack copies the bundle's packs into the cache, each a sibling of the others the way the holds resolve,
// under packs/<digest of every file>/; the record names the root's directory in it. An in-memory pack is snapshotted
// in the same form, so a later process can still render and roll back the version that was actually installed.
func cachePack(b *bundle.Bundle) (string, error) {
	h := sha256.New()
	files := map[string]map[string][]byte{} // pack name → relative path → content
	var names []string
	for name, p := range b.Packs {
		names = append(names, name)
		files[name] = map[string][]byte{}
		if len(p.Dir) == 0 {
			var err error
			if files[name], err = snapshotFiles(b, p); err != nil {
				return "", err
			}
			continue
		}
		if err := filepath.WalkDir(p.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(p.Dir, path)
			if err != nil {
				return err
			}
			files[name][rel] = data
			return nil
		}); err != nil {
			return "", err
		}
	}
	slices.Sort(names)
	for _, name := range names {
		for _, f := range slices.Sorted(maps.Keys(files[name])) {
			data := files[name][f]
			_, _ = fmt.Fprintf(h, "%s/%s %d\n", name, filepath.ToSlash(f), len(data))
			_, _ = h.Write(data)
		}
	}
	dest := filepath.Join(remote.CacheDir(), "packs", "dir-"+hex.EncodeToString(h.Sum(nil)))
	root := filepath.Join(dest, b.Name)
	if _, err := os.Stat(root); err == nil {
		return root, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".pack-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for _, name := range names {
		for _, f := range slices.Sorted(maps.Keys(files[name])) {
			to := filepath.Join(tmp, name, f)
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(to, files[name][f], 0o644); err != nil {
				return "", err
			}
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if _, statErr := os.Stat(root); statErr == nil {
			return root, nil // another run was first: the same digest, the same files
		}
		return "", err
	}
	return root, nil
}
