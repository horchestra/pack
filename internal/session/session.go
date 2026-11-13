// Package session is a command's connection to the world: the release's environment, the target it names bound
// to the installation, the other targets of the environment, and the place the record is kept. Everything comes
// from the release's files (release.yaml, env.yaml); the command line adds nothing to it.
package session

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/horchestra/pack/internal/graph"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	_ "github.com/horchestra/pack/internal/target/kube"   // runtime k8s: a target's image in a Pod
	_ "github.com/horchestra/pack/internal/target/memory" // the targets there are
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/logging"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/secretstore"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote" // a target that is not compiled in is a program of its own
)

// Session is what a command works with.
type Session struct {
	Dir    *workdir.Dir
	ID     string // the installation's UUID
	Env    *environment.Environment
	Target string
	Inst   target.Installation
	Execs  map[string]target.Executor // the environment's other targets by name
	Ctx    render.Context
	Store  saga.Store
	// Force lists keys that must run their apply and notify hooks even when observation finds them unchanged.
	Force []string
	// Bundle is the release's pack already in memory — an embedded apply of a pack the process holds as Go
	// (bundle.FromPack): no pack directory is generated, written or read. Values then replaces the values files.
	Bundle *bundle.Bundle
	Values *render.Values
	// Hear hears the entries of a saga the session runs, after the line a person reads of each is printed: the
	// events of --stream. Nil — none.
	Hear func(saga.Entry)

	ctx context.Context
}

// Open reads the environment, opens the targets it names and binds them to the installation.
// ctx is the command's: an interrupt cuts short what the session reads from a registry or a store.
// readOnly: the secret store is only read — plan looks, apply writes.
func Open(ctx context.Context, d *workdir.Dir, readOnly bool) (*Session, error) {
	id, err := d.InstallationID()
	if err != nil {
		return nil, err
	}
	env, err := environment.Load(ctx, d.Release.Config(), nil)
	if err != nil {
		return nil, err
	}
	if len(env.Target) == 0 {
		return nil, fmt.Errorf("no target: write `target:` in env.yaml (known: %s)", strings.Join(target.Names(), ", "))
	}
	s := &Session{ctx: ctx, Dir: d, ID: id, Env: env, Target: env.Target}
	// the store is every target's to use: it goes into each one's set, the token with it, in memory
	scfg := secretstore.Config{Provider: env.Store.Provider, Addr: env.Store.Addr, Mount: env.Store.Mount,
		Prefix: env.Store.Prefix, CACert: env.Store.CACert, Path: env.Store.Path}
	storeSet := scfg.Settings()
	if storeSet == nil {
		storeSet = map[string]string{}
	}
	// every target is told how the record is kept: with state: db its own file would not outlive the run
	kind := d.Release.State
	if len(kind) == 0 {
		kind = workdir.StateFile
	}
	storeSet[target.SettingPackState] = kind
	if hc := env.HistoryCount; hc != nil {
		if *hc < 0 {
			return nil, fmt.Errorf("history_count: %d is below zero", *hc)
		}
		storeSet["history_count"] = strconv.Itoa(*hc)
		if *hc == 0 {
			env.Run.NoRollback = true // no snapshots: a rollback has nothing to restore a file from
		}
	}
	maps.Copy(env.Settings, storeSet)

	lock, err := release.ReadLock(d.Release.Dir)
	if err != nil {
		return nil, err
	}
	if err := pinRuntime(s.Target, env.Settings, lock, d.Path); err != nil {
		return nil, err
	}
	log := logging.For("session")
	log.Debug().Str("installation", id).Str("target", s.Target).Str("store", env.Store.Provider).Msg("open")
	if s.Inst, err = target.Open(s.Target, id, env.Settings); err != nil {
		return nil, err
	}
	if _, ok := env.Targets[render.DefaultTarget]; ok {
		return nil, fmt.Errorf("targets.%s: the name is pack's, for the default target (its facts): name the entry otherwise", render.DefaultTarget)
	}
	facts, warnings, err := LoadFacts(d)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		_, _ = fmt.Fprintln(os.Stderr, "pack: facts:", w)
	}
	s.Ctx = render.Context{Installation: id, Target: s.Inst, Keys: Placements(env.Keys)}
	for name, f := range facts {
		if s.Ctx.Facts == nil {
			s.Ctx.Facts = map[string]map[string]any{}
		}
		s.Ctx.Facts[name] = f.Map()
	}
	s.Execs = map[string]target.Executor{}
	for _, tn := range slices.Sorted(maps.Keys(env.Targets)) {
		t := env.Targets[tn]
		maps.Copy(t.Settings, storeSet)
		if err := pinRuntime(t.Target, t.Settings, lock, d.Path); err != nil {
			return nil, fmt.Errorf("targets.%s: %w", tn, err)
		}
		log.Debug().Str("entry", tn).Str("target", t.Target).Msg("open another target")
		oi, err := target.Open(t.Target, id, t.Settings)
		if err != nil {
			return nil, fmt.Errorf("targets.%s: %w", tn, err)
		}
		if s.Ctx.Targets == nil {
			s.Ctx.Targets = map[string]render.Target{}
		}
		s.Ctx.Targets[tn] = oi
		s.Execs[tn] = oi
	}

	st, err := secretstore.Open(scfg)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if st != nil {
		s.Ctx.Secrets = &storeSource{ctx: ctx, store: st, cfg: scfg, installation: id, readOnly: readOnly}
	}
	rec, _ := s.Inst.(target.Recorder)
	if s.Store, err = d.Store(rec); err != nil {
		return nil, err
	}
	// A command that installs keeps the installation's id from here on: the passwords it makes and the objects the
	// target names are known by it, and a first apply that fails before its first step must not lose it.
	if !readOnly {
		if cur, err := s.Store.Load(); err != nil {
			return nil, err
		} else if cur == nil {
			if err := s.Store.Save(&saga.Record{ID: id}); err != nil {
				return nil, fmt.Errorf("keep the installation's id: %w", err)
			}
		}
	}
	if !readOnly {
		s.pruneJournal()
	}
	return s, nil
}

// pruneJournal drops the oldest sagas of the installation beyond run.journal_keep; a journal that cannot be
// pruned is no reason to stop an apply.
func (s *Session) pruneJournal() {
	keep := s.Env.Run.JournalKeep
	if keep == 0 {
		keep = environment.DefaultJournalKeep
	}
	if j, err := s.Dir.JournalReaderFor(s.ID); err == nil {
		_, _ = j.Prune(keep)
		if c, ok := j.(io.Closer); ok {
			_ = c.Close()
		}
	}
}

// Placements are the environment's keys as the render takes them.
func Placements(keys map[string]environment.Key) map[string]render.Placement {
	out := make(map[string]render.Placement, len(keys))
	for name, k := range keys {
		p := render.Placement{Target: k.Target, Replicas: k.Replicas, Labels: k.Labels, Annotations: k.Annotations, Raw: k.Raw}
		if k.Resources != nil {
			p.Resources = &render.Resources{Requests: k.Resources.Requests, Limits: k.Resources.Limits}
		}
		for _, e := range k.Env {
			ev := render.EnvVar{Name: e.Name, Value: e.Value}
			if e.SecretRef != nil {
				ev.Secret = &render.SecretRef{Name: e.SecretRef.Name, Key: e.SecretRef.Key}
			}
			p.Env = append(p.Env, ev)
		}
		if k.Reach != nil {
			p.Reach = &render.Reach{Instances: k.Reach.Instances, Ports: k.Reach.Ports}
		}
		out[name] = p
	}
	return out
}

// Close releases the session: a target that is a program of its own (target/remote) is stopped.
func (s *Session) Close() {
	if c, ok := s.Store.(io.Closer); ok {
		_ = c.Close() // a state database's store holds its connection
	}
	closeTarget(s.Inst)
	for _, x := range s.Execs {
		closeTarget(x)
	}
}

func closeTarget(t any) {
	if c, ok := t.(io.Closer); ok {
		_ = c.Close()
	}
}

// Installed is the record and the installed version rendered with its recorded values, against the
// environment as it is now; rec is nil when nothing is recorded, rel nil for a record without a version.
func (s *Session) Installed() (rec *saga.Record, b *bundle.Bundle, rel *render.Release, err error) {
	if rec, err = s.Store.Load(); err != nil {
		return nil, nil, nil, err
	}
	if rec != nil && len(rec.Pack) > 0 {
		if b, rel, err = s.Version(rec.Installation); err != nil {
			return nil, nil, nil, fmt.Errorf("the installed version %s: %w", rec.Pack, err)
		}
	}
	return rec, b, rel, nil
}

// Run is a saga over the graph with the executor the session gives it.
func (s *Session) Run(g *graph.Graph, ex saga.Executor) *saga.Run {
	return &saga.Run{
		Graph:      g,
		Exec:       ex,
		Store:      s.Store,
		ID:         s.ID,
		Jobs:       s.Env.Run.Jobs,
		NoRollback: s.Env.Run.NoRollback,
		Keep:       s.Env.Run.Keep,
		// as many revisions as sagas in the journal: each has its log there
		HistoryKeep: s.Env.Run.JournalKeep,
		Revision:    release.Revision(s.Dir.Release.Dir),
		Journal:     s.Dir.JournalFor(s.ID),
		Hear: func(e saga.Entry) {
			if line, ok := e.Line(); ok {
				fmt.Println(line)
			}
			if s.Hear != nil {
				s.Hear(e)
			}
		},
	}
}

// pinRuntime gives a target that runs in a container the image its lock pins (pack refresh) — the one pack runs,
// whatever the tag has become — and a home in the run's directory (.pack/home/<name>), which outlives the container.
// Both are pack's to say, not the file's.
func pinRuntime(name string, settings map[string]string, lock *release.Lock, packDir string) error {
	if slices.Contains(target.Names(), name) {
		for k := range settings {
			if strings.HasPrefix(k, remote.RuntimePrefix) {
				return fmt.Errorf("target %s is in pack itself: it runs in pack's process, it has no runtime: (%s)", name, k)
			}
		}
		return nil
	}
	if _, ok := settings[remote.SettingImage]; ok {
		return fmt.Errorf("runtime.image: the image is release.yaml's (targets: %s: {image: oci://…}), locked by pack refresh", name)
	}
	if _, ok := settings[remote.SettingHome]; ok {
		return fmt.Errorf("runtime.home: the container's home is pack's, %s/home/%s", packDir, name)
	}
	kind := settings[remote.RuntimePrefix+"type"]
	if !remote.FromImage(kind) {
		return nil
	}
	var tl release.TargetLock
	if lock != nil {
		tl = lock.Targets[name]
	}
	if len(tl.Image) == 0 || len(tl.Digest) == 0 {
		return fmt.Errorf("target %s: runtime %s, and its image is not locked: %w", name, kind, target.ErrNotBrought)
	}
	pin, err := oci.WithDigest(tl.Image, tl.Digest)
	if err != nil {
		return fmt.Errorf("target %s: %w", name, err)
	}
	settings[remote.SettingImage] = strings.TrimPrefix(pin, "oci://")
	if kind != remote.RuntimeDocker {
		return nil // a Pod has no directory of the machine for its home
	}
	home, err := filepath.Abs(filepath.Join(packDir, "home", name))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("target %s: its home: %w", name, err)
	}
	settings[remote.SettingHome] = home
	return nil
}
