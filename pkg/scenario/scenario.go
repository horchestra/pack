// Package scenario runs the scenarios of a pack — facts, values, an environment, what they should give — in this
// process, on the memory target (nothing is installed anywhere) or on the target of a release. pack test is its
// command line.
package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/adapter"
	"github.com/horchestra/pack/internal/graph"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/target/memory"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/diagnose"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote"
)

// Stand is what scenarios run on: the memory target and a pack — or a release's target, which installs for real.
type Stand struct {
	Pack    string         // the pack's reference: a directory (absolute) or oci://…
	Release map[string]any // release.yaml besides the pack: registry, targets (a release's)
	Env     map[string]any // env.yaml before the scenario's: the memory target, or the release's
	Lock    []byte         // release.lock.json (a release's)
	Bin     string         // the programs of the targets (a release's .pack/bin)
	Real    bool           // a release's target: what installs is to be taken away again
}

// MemoryStand is the memory target and the pack of dir.
func MemoryStand(dir string) (*Stand, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &Stand{Pack: abs, Env: map[string]any{"target": "memory", "observe": "true"}}, nil
}

// ReleaseStand is the target of the release of dir, refreshed: its pack, release.yaml, env.yaml and lock.
func ReleaseStand(dir string) (*Stand, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	read := func(name string) (map[string]any, error) {
		b, err := os.ReadFile(filepath.Join(abs, name))
		if err != nil {
			return nil, fmt.Errorf("--release: %w", err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("--release: %s: %w", name, err)
		}
		return m, nil
	}
	relDoc, err := read(release.File)
	if err != nil {
		return nil, err
	}
	env, err := read("env.yaml")
	if err != nil {
		return nil, err
	}
	pack, _ := relDoc["pack"].(string)
	if len(pack) == 0 {
		return nil, fmt.Errorf("--release %s: release.yaml names no pack", dir)
	}
	if !oci.IsRef(pack) && !filepath.IsAbs(pack) {
		pack = filepath.Join(abs, pack)
	}
	lock, err := os.ReadFile(filepath.Join(abs, release.LockFile))
	if err != nil {
		return nil, fmt.Errorf("--release %s: no lock — run pack refresh there: %w", dir, err)
	}
	delete(relDoc, "pack")
	return &Stand{Pack: pack, Release: relDoc, Env: env, Lock: lock, Bin: remote.BinDir(), Real: true}, nil
}

// TestInstallation is the id of the installation of every scenario. The memory target keeps what it holds of an
// installation (observe) for the process: a scenario starts by forgetting it (memory.Reset), so that one
// scenario's keys are never another's.
const TestInstallation = "00000000-0000-4000-8000-000000000000"

// Files are the scenario files the arguments name: files, and the *.yaml of directories; none — tests/.
func Files(args []string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"tests"}
	}
	var out []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			out = append(out, a)
			continue
		}
		m, err := filepath.Glob(filepath.Join(a, "*.yaml"))
		if err != nil {
			return nil, err
		}
		if len(m) == 0 {
			return nil, fmt.Errorf("no scenarios in %s: no *.yaml", a)
		}
		out = append(out, m...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scenarios in %s", strings.Join(args, ", "))
	}
	return out, nil
}

// scenario is one file of tests/.
type scenario struct {
	Name   string         `json:"name"`
	Facts  any            `json:"facts,omitempty"` // a fixture file, or {target: facts} inline
	Values map[string]any `json:"values,omitempty"`
	Env    map[string]any `json:"env,omitempty"`
	From   *struct {
		Pack   string         `json:"pack,omitempty"` // a directory, the scenario file's; none — the pack under test
		Values map[string]any `json:"values,omitempty"`
	} `json:"from,omitempty"`
	Logs      map[string]map[string][]string `json:"logs,omitempty"`
	FailAt    string                         `json:"fail_at,omitempty"`
	FailError string                         `json:"fail_error,omitempty"`
	Expect    expectation                    `json:"expect"`
}

type expectation struct {
	Suggest  map[string]any `json:"suggest,omitempty"`
	Problems *[]wantProblem `json:"problems,omitempty"`
	Files    string         `json:"files,omitempty"`
	Plan     string         `json:"plan,omitempty"`
	Apply    *wantApply     `json:"apply,omitempty"`
	Diagnose *wantDiagnosis `json:"diagnose,omitempty"`
}

type wantProblem struct {
	Where   string `json:"where"`
	Kind    string `json:"kind,omitempty"`
	Rule    string `json:"rule,omitempty"`
	Message string `json:"message,omitempty"` // a part of it
}

type wantApply struct {
	State    string   `json:"state"`
	InEffect []string `json:"in_effect,omitempty"`
}

type wantDiagnosis struct {
	Class     string       `json:"class"`
	Diagnosis string       `json:"diagnosis,omitempty"`
	Remedies  []wantRemedy `json:"remedies,omitempty"`
}

type wantRemedy struct {
	Patch   string `json:"patch,omitempty"`
	Value   any    `json:"value,omitempty"`
	Command string `json:"command,omitempty"`
}

// Result is a scenario as it ran.
type Result struct {
	Name     string   `json:"name"`
	File     string   `json:"file"`
	OK       bool     `json:"ok"`
	Failures []string `json:"failures,omitempty"`
	Skipped  []string `json:"skipped,omitempty"` // what is not checked on a release's target
}

// RunScenario plays the scenario of file on the stand; update writes the golden files instead of comparing.
func RunScenario(ctx context.Context, on *Stand, file string, update bool) Result {
	r := Result{Name: file, File: file}
	fail := func(format string, a ...any) { r.Failures = append(r.Failures, fmt.Sprintf(format, a...)) }
	var sc scenario
	if b, err := os.ReadFile(file); err != nil {
		fail("%v", err)
	} else if err := yaml.UnmarshalStrict(b, &sc); err != nil {
		fail("%s: %v", file, err)
	}
	if len(sc.Name) > 0 {
		r.Name = sc.Name
	}
	if len(r.Failures) == 0 {
		// what the sagas say on the way is not the scenario's result: it goes nowhere. os.Stdout is the process's —
		// scenarios run one after another, never at once; it comes back however a scenario ends
		out := os.Stdout
		if null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			os.Stdout = null
			defer func() { os.Stdout = out; _ = null.Close() }()
		}
		skip := func(what string) { r.Skipped = append(r.Skipped, what) }
		if err := sc.run(ctx, on, filepath.Dir(file), update, fail, skip); err != nil {
			fail("%v", err)
		}
	}
	r.OK = len(r.Failures) == 0
	return r
}

// run plays the scenario in a release of its own; base is the directory its paths are of.
func (sc *scenario) run(ctx context.Context, on *Stand, base string, update bool, fail func(string, ...any), skip func(string)) error {
	if on.Real && (len(sc.FailAt) > 0 || len(sc.Logs) > 0) {
		skip("fail_at and logs are the memory target's: the scenario")
		return nil
	}
	tmp, err := os.MkdirTemp("", "pack-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	of := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}

	memory.Reset(TestInstallation)
	defer memory.Reset(TestInstallation)
	env := maps.Clone(on.Env)
	maps.Copy(env, sc.Env)
	if on.Real {
		if len(on.Bin) > 0 && len(os.Getenv(remote.EnvPath)) == 0 {
			_ = os.Setenv(remote.EnvPath, on.Bin)
			defer func() { _ = os.Unsetenv(remote.EnvPath) }()
		}
		if err := os.WriteFile(filepath.Join(tmp, release.LockFile), on.Lock, 0o644); err != nil {
			return err
		}
	}
	if len(sc.Logs) > 0 {
		b, err := json.Marshal(sc.Logs)
		if err != nil {
			return err
		}
		logs := filepath.Join(tmp, "logs.json")
		if err := os.WriteFile(logs, b, 0o644); err != nil {
			return err
		}
		env["logs"] = logs
	}
	if len(sc.FailAt) > 0 {
		env["fail_at"], env["fail_error"] = sc.FailAt, sc.FailError
	}
	write := func(name string, v any) error {
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(tmp, name), b, 0o644)
	}
	// the version installed first runs without the failure and its logs: what fails is the scenario's own apply
	first := maps.Clone(env)
	delete(first, "fail_at")
	delete(first, "fail_error")
	delete(first, "logs")
	if err := write("env.yaml", first); err != nil {
		return err
	}
	var d *workdir.Dir
	enter := func(pack string, values map[string]any) error {
		doc := maps.Clone(on.Release)
		if doc == nil {
			doc = map[string]any{}
		}
		doc["pack"] = pack
		if err := write("release.yaml", doc); err != nil {
			return err
		}
		if values == nil {
			values = map[string]any{}
		}
		if err := write("values.yaml", values); err != nil {
			return err
		}
		rel, err := release.Load(tmp, "")
		if err != nil {
			return err
		}
		d = workdir.New(rel, "")
		d.Path, d.Journal = filepath.Join(tmp, ".pack"), "" // the scenario's, wherever it runs
		d.Ensure()
		// one installation id for every run: what is rendered of it (the names of credentials) is the same each time
		if rec, err := d.FileState().Load(); err != nil {
			return err
		} else if rec == nil {
			return d.FileState().Save(&saga.Record{ID: TestInstallation})
		}
		return nil
	}
	open := func(ctx context.Context, readOnly bool) (*session.Session, error) {
		return session.Open(ctx, d, readOnly)
	}

	// the facts, as pack discover leaves them
	if sc.Facts != nil {
		if err := enter(on.Pack, nil); err != nil {
			return err
		}
		facts, err := FactsOf(sc.Facts, of)
		if err != nil {
			return err
		}
		for name, f := range facts {
			if err := session.SaveFacts(d, name, f); err != nil {
				return err
			}
		}
	}

	if sc.From != nil {
		pack := on.Pack
		if len(sc.From.Pack) > 0 {
			pack = of(sc.From.Pack)
		}
		if err := enter(pack, sc.From.Values); err != nil {
			return err
		}
		if err := applyHere(ctx, open); err != nil {
			return fmt.Errorf("from: the version installed first: %w", err)
		}
	}
	if err := write("env.yaml", env); err != nil {
		return err
	}
	if err := enter(on.Pack, sc.Values); err != nil {
		return err
	}
	if on.Real {
		defer func() {
			if err := takeAway(ctx, open); err != nil {
				fail("taking away what the scenario installed: %v", err)
			}
		}()
		if len(sc.Expect.Files) > 0 {
			skip("files: the memory target's")
		}
		if len(sc.Expect.Plan) > 0 {
			skip("plan: the memory target's")
		}
		sc.Expect.Files, sc.Expect.Plan = "", ""
	}
	s, err := open(ctx, false)
	if err != nil {
		return err
	}
	closed := false
	closeSession := func() {
		if !closed {
			closed = true
			s.Close()
		}
	}
	defer closeSession()
	ex := sc.Expect

	if ex.Suggest != nil {
		ref, err := s.Dir.Release.PackRef()
		if err != nil {
			return err
		}
		b, err := s.Pack(ref)
		if err != nil {
			return err
		}
		qs, err := render.Ask(b, render.Values{Values: sc.Values}, s.Ctx)
		if err != nil {
			return err
		}
		got := map[string]any{}
		for _, q := range qs.Questions {
			for _, c := range q.Candidates {
				if c.Source == render.FromSuggest {
					got[q.Pointer] = c.Value
				}
			}
		}
		for _, ptr := range slices.Sorted(maps.Keys(ex.Suggest)) {
			want := ex.Suggest[ptr]
			g, ok := got[ptr]
			switch {
			case want == nil && ok:
				fail("suggest %s: %v offered, none expected", ptr, show(g))
			case want != nil && !ok:
				fail("suggest %s: nothing offered, %v expected", ptr, show(want))
			case want != nil && !sameJSON(g, want):
				fail("suggest %s: %v, %v expected", ptr, show(g), show(want))
			}
		}
	}

	_, rel, renderErr := s.Render()
	var ps *render.Problems
	if renderErr != nil && !errors.As(renderErr, &ps) {
		return renderErr
	}
	if ex.Problems != nil {
		var got []render.Problem
		if ps != nil {
			got = ps.Problems
		}
		matched := make([]bool, len(got))
		for _, w := range *ex.Problems {
			i := slices.IndexFunc(got, func(p render.Problem) bool {
				return p.Where == w.Where && (len(w.Kind) == 0 || p.Kind == w.Kind) && (len(w.Rule) == 0 || p.Rule == w.Rule) &&
					strings.Contains(p.Message, w.Message)
			})
			if i < 0 {
				fail("problems: none at %s%s", w.Where, describe(w))
				continue
			}
			matched[i] = true
		}
		for i, p := range got {
			if !matched[i] {
				fail("problems: not expected: %s", show(p))
			}
		}
	}
	if ps != nil && (len(ex.Files) > 0 || len(ex.Plan) > 0 || ex.Apply != nil || ex.Diagnose != nil) {
		// what is expected of a render cannot be had: a failure, never a pass that checked nothing
		fail("the values do not render, and files, plan, apply or diagnose are expected: %v", renderErr)
		return nil
	}

	if len(ex.Files) > 0 {
		out := filepath.Join(tmp, "files")
		if err := emitQuiet(rel, out); err != nil {
			return err
		}
		if err := golden(of(ex.Files), out, update, fail); err != nil {
			return err
		}
	}

	if len(ex.Plan) > 0 {
		c, err := s.Prepare(false)
		if err != nil {
			return fmt.Errorf("plan: %w", err)
		}
		text := filepath.Join(tmp, "plan.txt")
		if err := os.WriteFile(text, []byte(c.Plan.Text()), 0o644); err != nil {
			return err
		}
		if err := goldenFile(of(ex.Plan), text, update, fail); err != nil {
			return err
		}
	}

	if ex.Apply != nil || ex.Diagnose != nil {
		closeSession() // the apply opens the release itself: one session at a time holds its targets
		applyErr := applyHere(ctx, open)
		s2, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s2.Close()
		rec, err := s2.Store.Load()
		if err != nil {
			return err
		}
		if ex.Apply != nil {
			state, inEffect := "none", []string(nil)
			if rec != nil {
				state = rec.State
				if rec.Saga != nil {
					for _, n := range rec.Saga.Done {
						if !strings.HasPrefix(n, graph.SavepointPrefix) { // the keys, not the savepoints
							inEffect = append(inEffect, n)
						}
					}
				} else if rel != nil {
					for _, u := range rel.Units {
						inEffect = append(inEffect, u.Key)
					}
				}
			}
			slices.Sort(inEffect)
			want := slices.Clone(ex.Apply.InEffect)
			slices.Sort(want)
			if state != ex.Apply.State {
				fail("apply: %s (%v), %s expected", state, applyErr, ex.Apply.State)
			}
			if ex.Apply.InEffect != nil && !slices.Equal(inEffect, want) {
				fail("apply: in effect %v, %v expected", inEffect, want)
			}
		}
		if ex.Diagnose != nil {
			if rec == nil || !rec.Unfinished() {
				fail("diagnose: no failed saga to diagnose (the apply: %v)", applyErr)
			} else if c, err := diagnose.CaseOf(ctx, d, s2, rec, true, ""); err != nil {
				fail("diagnose: %v", err)
			} else {
				checkCase(c, *ex.Diagnose, fail)
			}
		}
	}
	return nil
}

// takeAway undoes a saga left unfinished and removes what is installed, in the release open opens: a scenario on a
// release's target leaves nothing behind.
func takeAway(ctx context.Context, open func(context.Context, bool) (*session.Session, error)) error {
	s, err := open(ctx, false)
	if err != nil {
		return err
	}
	defer s.Close()
	rec, _, curRel, err := s.Installed()
	if err != nil || rec == nil {
		return err
	}
	if rec.Unfinished() {
		_, toRel, err := s.Version(rec.Saga.Installation)
		if err != nil {
			return err
		}
		g, err := graph.New(adapter.Specs(toRel, nil))
		if err != nil {
			return err
		}
		if err := s.Undo(ctx, g, curRel, toRel); err != nil {
			return err
		}
		if rec, _, curRel, err = s.Installed(); err != nil || rec == nil {
			return err
		}
	}
	if len(rec.Pack) == 0 || curRel == nil {
		return nil
	}
	g, err := graph.New(adapter.Specs(curRel, nil))
	if err != nil {
		return err
	}
	return s.Remove(ctx, g, curRel)
}

// applyHere is an apply of the release open opens, as pack apply does it, asking for no approval.
func applyHere(ctx context.Context, open func(context.Context, bool) (*session.Session, error)) error {
	s, err := open(ctx, false)
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := s.Prepare(true)
	if err != nil {
		return err
	}
	return s.Apply(ctx, c)
}

// FactsOf are the facts of a scenario: a fixture file of pack discover --save-fixture, or the same inline.
func FactsOf(v any, of func(string) string) (map[string]*target.Facts, error) {
	var raw []byte
	if p, ok := v.(string); ok {
		b, err := os.ReadFile(of(p))
		if err != nil {
			return nil, err
		}
		raw = b
	} else {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var out map[string]*target.Facts
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("facts: {target: facts}: %w", err)
	}
	return out, nil
}

func checkCase(c diagnose.Case, w wantDiagnosis, fail func(string, ...any)) {
	if c.Class != w.Class {
		fail("diagnose: class %s (%s), %s expected", c.Class, c.Cause, w.Class)
	}
	if len(w.Diagnosis) > 0 && c.Diagnosis != w.Diagnosis {
		fail("diagnose: diagnosis %q, %q expected", c.Diagnosis, w.Diagnosis)
	}
	for _, wr := range w.Remedies {
		if !slices.ContainsFunc(c.Remedies, func(r diagnose.Remedy) bool {
			return r.Patch == wr.Patch && r.Command == wr.Command && (wr.Value == nil || sameJSON(r.Value, wr.Value))
		}) {
			fail("diagnose: no remedy %s, the remedies: %s", show(wr), show(c.Remedies))
		}
	}
}

func describe(w wantProblem) string {
	var parts []string
	if len(w.Kind) > 0 {
		parts = append(parts, "kind "+w.Kind)
	}
	if len(w.Rule) > 0 {
		parts = append(parts, "rule "+w.Rule)
	}
	if len(w.Message) > 0 {
		parts = append(parts, fmt.Sprintf("message %q", w.Message))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// sameJSON: the two say the same as JSON (a number read from YAML and one worked out by CEL are one).
func sameJSON(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var x, y any
	_ = json.Unmarshal(ja, &x)
	_ = json.Unmarshal(jb, &y)
	return reflect.DeepEqual(x, y)
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// emitQuiet writes the files of the release under out, as pack template -o does.
func emitQuiet(rel *render.Release, out string) error {
	put := func(where, path string, b []byte, mode *os.FileMode) error {
		dst := filepath.Join(out, where, filepath.Clean("/"+path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if mode != nil {
			perm = *mode
		}
		return os.WriteFile(dst, b, perm)
	}
	for _, u := range rel.Units {
		for _, f := range u.Files {
			if f.PerInstance() {
				for i, b := range f.Instances {
					if b != nil {
						if err := put(u.Key+"/"+fmt.Sprint(i), f.Path, b, f.Mode); err != nil {
							return err
						}
					}
				}
				continue
			}
			if err := put(u.Key, f.Path, f.Content, f.Mode); err != nil {
				return err
			}
		}
		// the secret files are not written: their content is no file's
	}
	return nil
}

// golden compares the directory got with the golden directory want — every file, both ways; update writes want.
func golden(want, got string, update bool, fail func(string, ...any)) error {
	if update {
		if err := os.RemoveAll(want); err != nil {
			return err
		}
		return os.CopyFS(want, os.DirFS(got))
	}
	list := func(root string) (map[string][]byte, error) {
		out := map[string][]byte{}
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			b, err := os.ReadFile(p)
			out[filepath.ToSlash(rel)] = b
			return err
		})
		return out, err
	}
	w, err := list(want)
	if err != nil {
		return fmt.Errorf("files: the golden directory: %w (update writes it)", err)
	}
	g, err := list(got)
	if err != nil {
		return err
	}
	for _, p := range slices.Sorted(maps.Keys(w)) {
		gb, ok := g[p]
		switch {
		case !ok:
			fail("files: %s is not rendered", p)
		case !bytes.Equal(gb, w[p]):
			fail("files: %s differs from %s:\n%s", p, filepath.Join(want, p), firstDiff(w[p], gb))
		}
	}
	for _, p := range slices.Sorted(maps.Keys(g)) {
		if _, ok := w[p]; !ok {
			fail("files: %s is rendered and not in %s", p, want)
		}
	}
	return nil
}

// goldenFile compares the file got with want; update writes want.
func goldenFile(want, got string, update bool, fail func(string, ...any)) error {
	gb, err := os.ReadFile(got)
	if err != nil {
		return err
	}
	if update {
		if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
			return err
		}
		return os.WriteFile(want, gb, 0o644)
	}
	wb, err := os.ReadFile(want)
	if err != nil {
		return fmt.Errorf("plan: %w (update writes it)", err)
	}
	if !bytes.Equal(gb, wb) {
		fail("plan differs from %s:\n%s", want, firstDiff(wb, gb))
	}
	return nil
}

// firstDiff is the first line that differs, as want and got.
func firstDiff(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n  want %q\n  got  %q", i+1, w, g)
		}
	}
	return "(the same lines)"
}
