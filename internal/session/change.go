package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/horchestra/pack/internal/adapter"
	"github.com/horchestra/pack/internal/graph"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/values"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target/remote"
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
	ref, err := s.Dir.Release.PackRef()
	if err != nil {
		return nil, err
	}
	if ref, err = s.Pinned(ref); err != nil { // a pack in a registry is the digest the lock says
		return nil, err
	}
	rec, fromB, fromRel, err := s.Installed()
	if err != nil {
		return nil, err
	}
	v, err := values.Load(s.Dir.Release.ValuesFiles())
	if err != nil {
		return nil, err
	}
	toB, err := s.Pack(ref)
	if err != nil {
		return nil, err
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
		Policy: plan.Policy{Strict: s.Env.Run.Strict}})
	if err == nil {
		for _, d := range c.Drifts {
			c.Plan.Warnings = append(c.Plan.Warnings, "drift: "+d.String())
		}
		c.Plan.Revision = release.Revision(s.Dir.Release.Dir)
		plan.Seal(c.Plan, fromRel, c.ToRel)
	}
	return c, err
}

// Render is the release's pack rendered with the release's values and nothing else: no installed version is read,
// no password is made (a password the store does not hold yet is left unfilled). What template and graph show.
func (s *Session) Render() (*bundle.Bundle, *render.Release, error) {
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
	run.To = saga.Installation{Pack: recordRef(c.Ref, c.ToB), Version: c.ToB.Version, Values: vals}
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
func (s *Session) Remove(ctx context.Context, g *graph.Graph, rel *render.Release) error {
	return s.Run(g, &adapter.Exec{From: rel, X: s.Inst, Execs: s.Execs}).Remove(ctx)
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
	rel, err := render.Render(b, v, s.Ctx)
	if err != nil {
		return nil, nil, err
	}
	return b, rel, nil
}

// RecordRef is how the installed version is found again: a pack in a registry by its digest, a directory as an
// absolute path.
func RecordRef(ref string) string {
	if oci.IsRef(ref) {
		return ref
	}
	if abs, err := filepath.Abs(ref); err == nil {
		return abs
	}
	return ref
}

// recordRef is RecordRef that can see the bundle: a directory pack goes into the cache (PACK_CACHE/packs) with
// the packs it holds beside it, the path named by the digest of the files — a checkout the run was of does not
// outlive it, the cache does; a later run, on whatever checkout, reads the installed version there.
func recordRef(ref string, b *bundle.Bundle) string {
	if oci.IsRef(ref) || b == nil {
		return RecordRef(ref)
	}
	cached, err := cachePack(b)
	if err != nil {
		return RecordRef(ref)
	}
	return cached
}

// cachePack copies the bundle's packs into the cache, each a sibling of the others the way the holds resolve,
// under packs/<digest of every file>/; the record names the root's directory in it.
func cachePack(b *bundle.Bundle) (string, error) {
	h := sha256.New()
	files := map[string][]string{} // pack name → its files
	var names []string
	for name, p := range b.Packs {
		if len(p.Dir) == 0 {
			return "", fmt.Errorf("pack %q has no directory to cache", name)
		}
		names = append(names, name)
		if err := filepath.WalkDir(p.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				files[name] = append(files[name], path)
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	slices.Sort(names)
	for _, name := range names {
		p := b.Packs[name]
		slices.Sort(files[name])
		for _, f := range files[name] {
			data, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			rel, _ := filepath.Rel(p.Dir, f)
			fmt.Fprintf(h, "%s/%s %d\n", name, filepath.ToSlash(rel), len(data))
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
	defer os.RemoveAll(tmp)
	for _, name := range names {
		for _, f := range files[name] {
			rel, _ := filepath.Rel(b.Packs[name].Dir, f)
			to := filepath.Join(tmp, name, rel)
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return "", err
			}
			data, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(to, data, 0o644); err != nil {
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
