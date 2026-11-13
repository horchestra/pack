package plan

import (
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/sdk/packschema"

	"github.com/horchestra/pack/pkg/target"
)

// mkPack is a pack that runs an image of its name and version, with the root
// annotation given.
func mkPack(name, version string, root *packschema.Root) *bundle.Pack {
	if root == nil {
		root = &packschema.Root{}
	}
	return &bundle.Pack{Name: name, Version: version, ImageRef: "img/" + name + ":" + version,
		SchemaRaw: []byte(name + "@" + version), Root: root}
}

// bundleOf is a bundle whose root pack holds every given pack (keys in the
// given order, held first).
func bundleOf(version string, savepoints []string, packs ...*bundle.Pack) *bundle.Bundle {
	root := &bundle.Pack{Name: "sys", Version: version, SchemaRaw: []byte("sys@" + version),
		Root: &packschema.Root{Savepoints: savepoints}}
	b := &bundle.Bundle{Name: "sys", Version: version, Root: root,
		Packs: map[string]*bundle.Pack{"sys": root}, KeyPack: map[string]string{}}
	for _, p := range packs {
		b.Keys = append(b.Keys, p.Name)
		b.KeyPack[p.Name] = p.Name
		b.Packs[p.Name] = p
	}
	return b
}

// releaseOf is b rendered: a unit per key, nothing else settled.
func releaseOf(b *bundle.Bundle, units ...render.Unit) *render.Release {
	return &render.Release{Pack: b.Name, Version: b.Version, Units: units,
		Report: render.Report{Order: slices.Clone(b.Keys)}}
}

func unitOf(key, image string) render.Unit {
	return render.Unit{Key: key, Component: key, Image: image}
}

func kinds(p *Plan) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, string(s.Kind)+":"+s.Key)
	}
	return out
}

func TestInstall(t *testing.T) {
	db := mkPack("db", "1.0.0", &packschema.Root{
		Readiness: &packschema.Readiness{TCP: "pg"},
		Hooks: []packschema.Hook{
			{Name: "format", Phase: packschema.PreInstall, Command: []string{"init"}},
			{Name: "seed", Phase: packschema.PostInstall, Command: []string{"seed"}, Idempotent: true},
			{Name: "migrate", Phase: packschema.PreUpgrade, Command: []string{"migrate"}},
		},
	})
	app := mkPack("app", "1.0.0", nil)
	b := bundleOf("1.0.0", nil, db, app)
	rel := releaseOf(b, unitOf("db", db.ImageRef), unitOf("app", app.ImageRef))

	p, err := Build(Input{To: b, ToOut: rel})
	if err != nil {
		t.Fatal(err)
	}
	if p.Direction != "install" || len(p.From) > 0 || p.To != "1.0.0" {
		t.Fatalf("direction = %+v", p)
	}
	for _, c := range p.Changes {
		if c.Kind != "added" || c.Direction != "install" {
			t.Fatalf("change = %+v", c)
		}
	}
	want := []string{"hook:db", "apply:db", "wait:db", "hook:db", "apply:app"}
	if !slices.Equal(kinds(p), want) {
		t.Fatalf("steps = %v", kinds(p))
	}
	// install hooks around the apply, the upgrade hook not selected
	if p.Steps[0].Name != "format" || p.Steps[3].Name != "seed" {
		t.Fatalf("hooks = %+v", p.Steps)
	}
	// no savepoints said: every changed key is one
	if len(p.Units) != 2 || p.Units[0].Savepoint != "db" || p.Units[1].Savepoint != "app" {
		t.Fatalf("units = %+v", p.Units)
	}
}

func TestUpgrade(t *testing.T) {
	hooks := []packschema.Hook{
		{Name: "dump", Phase: packschema.PreUpgrade, Command: []string{"dump"}, Compensate: &packschema.Step{Command: []string{"undump"}}},
		{Name: "vacuum", Phase: packschema.PostUpgrade, Command: []string{"vacuum"}},
		{Name: "restore", Phase: packschema.PreDowngrade, Command: []string{"restore"}},
	}
	from := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", &packschema.Root{Hooks: hooks}))
	to := bundleOf("2.0.0", nil, mkPack("db", "2.0.0", &packschema.Root{Hooks: hooks}))
	fromRel := releaseOf(from, unitOf("db", from.Packs["db"].ImageRef))
	toRel := releaseOf(to, unitOf("db", to.Packs["db"].ImageRef))

	p, err := Build(Input{From: from, To: to, FromOut: fromRel, ToOut: toRel})
	if err != nil {
		t.Fatal(err)
	}
	if p.Direction != "upgrade" || p.From != "1.0.0" {
		t.Fatalf("direction = %+v", p)
	}
	if c := p.Changes[0]; c.Kind != "image" || c.From != "1.0.0" || c.To != "2.0.0" {
		t.Fatalf("change = %+v", c)
	}
	want := []string{"hook:db", "apply:db", "hook:db"}
	if !slices.Equal(kinds(p), want) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if p.Steps[0].Image != "img/db:2.0.0" { // a hook's image defaults to new
		t.Fatalf("hook image = %s", p.Steps[0].Image)
	}
	// undoing the apply runs the installed version's downgrade hooks around the previous unit
	kr, ok := p.Rollback["db"]
	if !ok || len(kr.Pre) != 1 || kr.Pre[0].Name != "restore" || kr.Pre[0].Image != "img/db:1.0.0" {
		t.Fatalf("rollback = %+v", kr)
	}
}

func TestUpgradeFromRefused(t *testing.T) {
	from := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", nil))
	to := bundleOf("3.0.0", nil, mkPack("db", "3.0.0", &packschema.Root{Upgrade: &packschema.Upgrade{From: ">=2"}}))
	_, err := Build(Input{From: from, To: to,
		FromOut: releaseOf(from, unitOf("db", from.Packs["db"].ImageRef)),
		ToOut:   releaseOf(to, unitOf("db", to.Packs["db"].ImageRef))})
	if err == nil || !strings.Contains(err.Error(), "cannot be upgraded from") {
		t.Fatalf("err = %v", err)
	}
}

func TestHookWhen(t *testing.T) {
	hooks := []packschema.Hook{
		{Name: "always", Phase: packschema.PreUpgrade, Command: []string{"a"}},
		{Name: "only-from-1", Phase: packschema.PreUpgrade, Command: []string{"b"}, When: &packschema.When{From: ">=1, <2"}},
		{Name: "only-to-3", Phase: packschema.PreUpgrade, Command: []string{"c"}, When: &packschema.When{To: ">=3"}},
	}
	from := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", &packschema.Root{Hooks: hooks}))
	to := bundleOf("2.0.0", nil, mkPack("db", "2.0.0", &packschema.Root{Hooks: hooks}))
	p, err := Build(Input{From: from, To: to,
		FromOut: releaseOf(from, unitOf("db", from.Packs["db"].ImageRef)),
		ToOut:   releaseOf(to, unitOf("db", to.Packs["db"].ImageRef))})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range p.Steps {
		if s.Kind == Hook {
			names = append(names, s.Name)
		}
	}
	if !slices.Equal(names, []string{"always", "only-from-1"}) {
		t.Fatalf("hooks = %v", names)
	}
}

func TestConfigOnlyChange(t *testing.T) {
	from := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", &packschema.Root{
		Hooks: []packschema.Hook{{Name: "migrate", Phase: packschema.PreUpgrade, Command: []string{"m"}}},
	}))
	to := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", &packschema.Root{
		Hooks: []packschema.Hook{{Name: "migrate", Phase: packschema.PreUpgrade, Command: []string{"m"}}},
	}))
	fromRel := releaseOf(from, unitOf("db", from.Packs["db"].ImageRef))
	changed := unitOf("db", to.Packs["db"].ImageRef)
	changed.Replicas = 3 // same version, same image, another placement
	toRel := releaseOf(to, changed)

	p, err := Build(Input{From: from, To: to, FromOut: fromRel, ToOut: toRel})
	if err != nil {
		t.Fatal(err)
	}
	if c := p.Changes[0]; c.Kind != "config" {
		t.Fatalf("change = %+v", c)
	}
	if !slices.Equal(kinds(p), []string{"apply:db"}) { // no version transition, no hooks
		t.Fatalf("steps = %v", kinds(p))
	}
}

func TestNoChange(t *testing.T) {
	from := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", nil))
	to := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", nil))
	u := unitOf("db", from.Packs["db"].ImageRef)
	p, err := Build(Input{From: from, To: to, FromOut: releaseOf(from, u), ToOut: releaseOf(to, u)})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 0 || p.Changes[0].Kind != "none" {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(p.Text(), "nothing to do") {
		t.Fatalf("text = %q", p.Text())
	}
}

// A directory pack's Source files are named file://<dir>/templates/<name>, and the recorded release renders from
// the cache copy while the wanted one renders from the live directory: the directory part says nothing, the same
// pack is no change.
func TestLocalFileURLDirectoriesAreNoChange(t *testing.T) {
	from := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", nil))
	to := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", nil))
	cached := unitOf("db", from.Packs["db"].ImageRef)
	cached.Files = []render.File{{Path: "/etc/db.conf", URL: "file:///cache/packs/dir-aaa/db/templates/db.conf", Content: []byte("c")}}
	cached.Packages = map[string]render.Packages{"debian": {Files: []render.File{
		{Path: "/etc/db2.conf", URL: "file:///cache/packs/dir-aaa/db/templates/db2.conf", Content: []byte("c2")}}}}
	live := unitOf("db", to.Packs["db"].ImageRef)
	live.Files = []render.File{{Path: "/etc/db.conf", URL: "file:///home/user/packs/db/templates/db.conf", Content: []byte("c")}}
	live.Packages = map[string]render.Packages{"debian": {Files: []render.File{
		{Path: "/etc/db2.conf", URL: "file:///home/user/packs/db/templates/db2.conf", Content: []byte("c2")}}}}
	p, err := Build(Input{From: from, To: to, FromOut: releaseOf(from, cached), ToOut: releaseOf(to, live)})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 0 || p.Changes[0].Kind != "none" {
		t.Fatalf("plan = %+v", p)
	}
}

func TestUninstall(t *testing.T) {
	from := bundleOf("1.0.0", nil, mkPack("db", "1.0.0", nil), mkPack("app", "1.0.0", nil))
	fromRel := releaseOf(from, unitOf("db", from.Packs["db"].ImageRef), unitOf("app", from.Packs["app"].ImageRef))
	p, err := Build(Input{From: from, FromOut: fromRel})
	if err != nil {
		t.Fatal(err)
	}
	if p.Direction != "uninstall" {
		t.Fatalf("direction = %s", p.Direction)
	}
	// children first: the reverse of the order it renders in
	if !slices.Equal(kinds(p), []string{"remove:app", "remove:db"}) {
		t.Fatalf("steps = %v", kinds(p))
	}
	if len(p.Units) != 1 || p.Units[0].Savepoint != "end" || !slices.Equal(p.Units[0].RollbackOrder, []string{"db", "app"}) {
		t.Fatalf("units = %+v", p.Units)
	}
}

func TestSavepointUnits(t *testing.T) {
	db := mkPack("db", "1.0.0", nil)
	app := mkPack("app", "1.0.0", nil)
	b := bundleOf("1.0.0", []string{"db"}, db, app)
	rel := releaseOf(b, unitOf("db", db.ImageRef), unitOf("app", app.ImageRef))
	p, err := Build(Input{To: b, ToOut: rel})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Units) != 2 {
		t.Fatalf("units = %+v", p.Units)
	}
	if p.Units[0].Savepoint != "db" || !slices.Equal(p.Units[0].Keys, []string{"db"}) {
		t.Fatalf("unit 0 = %+v", p.Units[0])
	}
	if p.Units[1].Savepoint != "end" || !slices.Equal(p.Units[1].Keys, []string{"app"}) {
		t.Fatalf("unit 1 = %+v", p.Units[1])
	}
	// an unknown savepoint is refused
	if _, err := Build(Input{To: b, ToOut: rel, Policy: Policy{Savepoints: "nope"}}); err == nil {
		t.Fatal("an unknown savepoint must be refused")
	}
}

func TestStrictRefusesIrreversible(t *testing.T) {
	db := mkPack("db", "1.0.0", &packschema.Root{
		Hooks: []packschema.Hook{{Name: "format", Phase: packschema.PreInstall, Command: []string{"init"}}}, // no compensate, not idempotent
	})
	b := bundleOf("1.0.0", nil, db)
	rel := releaseOf(b, unitOf("db", db.ImageRef))
	if _, err := Build(Input{To: b, ToOut: rel, Policy: Policy{Strict: true}}); err == nil ||
		!strings.Contains(err.Error(), "irreversible") {
		t.Fatalf("err = %v", err)
	}
	// without strict the plan carries the mark instead
	p, err := Build(Input{To: b, ToOut: rel})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Steps[0].Irreversible {
		t.Fatalf("steps = %+v", p.Steps)
	}
}

func TestNoRollbackPolicy(t *testing.T) {
	db := mkPack("db", "1.0.0", nil)
	app := mkPack("app", "1.0.0", nil)
	b := bundleOf("1.0.0", []string{"db"}, db, app)
	rel := releaseOf(b, unitOf("db", db.ImageRef), unitOf("app", app.ImageRef))
	p, err := Build(Input{To: b, ToOut: rel, Policy: Policy{NoRollback: []string{"app"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Units[1].StopsAt != "app" {
		t.Fatalf("stopsAt = %q", p.Units[1].StopsAt)
	}
	p, err = Build(Input{To: b, ToOut: rel, Policy: Policy{NoRollbackAll: true}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Units[0].StopsAt != "*" || p.Units[1].StopsAt != "*" {
		t.Fatalf("stopsAt = %+v", p.Units)
	}
}

// rollback_timeout: the steps' timeouts, capped at the saga's cap — what a Pod's terminationGracePeriodSeconds
// is set from.
func TestRollbackEstimate(t *testing.T) {
	p := &Plan{Steps: []Step{{N: 1, Timeout: "30s"}, {N: 2, Timeout: "2m"}, {N: 3}}}
	Seal(p, nil, nil)
	if p.RollbackTimeout != "2m30s" {
		t.Fatalf("rollback_timeout = %q", p.RollbackTimeout)
	}
	if !strings.Contains(p.Text(), "terminationGracePeriodSeconds") {
		t.Fatalf("the text says it:\n%s", p.Text())
	}
	p = &Plan{Steps: []Step{{N: 1, Timeout: "1h"}}}
	Seal(p, nil, nil)
	if p.RollbackTimeout != RollbackCap.String() {
		t.Fatalf("capped: %q", p.RollbackTimeout)
	}
	p = &Plan{Steps: []Step{{N: 1}}}
	Seal(p, nil, nil)
	if len(p.RollbackTimeout) > 0 {
		t.Fatalf("no timeouts, no estimate: %q", p.RollbackTimeout)
	}
}

// An installation that keeps no record (state: observe) classifies a first apply from what the target holds: a
// key that matches is no change, one that differs is a config change without hooks, one that is absent is added.
func TestInstallHeld(t *testing.T) {
	hooks := []packschema.Hook{{Name: "seed", Phase: packschema.PostInstall, Command: []string{"seed"}, Idempotent: true}}
	db := mkPack("db", "1.0.0", &packschema.Root{Hooks: hooks})
	app := mkPack("app", "1.0.0", nil)
	web := mkPack("web", "1.0.0", &packschema.Root{Hooks: hooks})
	b := bundleOf("1.0.0", nil, db, app, web)
	rel := releaseOf(b, unitOf("db", db.ImageRef), unitOf("app", app.ImageRef), unitOf("web", web.ImageRef))

	p, err := Build(Input{To: b, ToOut: rel, Held: map[string]target.Hold{
		"db": target.HoldDiffers, "app": target.HoldMatches, "web": target.HoldAbsent}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"db": "config", "app": "none", "web": "added"}
	for _, c := range p.Changes {
		if c.Kind != want[c.Key] {
			t.Errorf("%s: kind = %q", c.Key, c.Kind)
		}
	}
	// the key that matches has no steps; the one that differs is applied, without hooks; the absent one is
	// installed with its hooks
	wantSteps := []string{"apply:db", "apply:web", "hook:web"}
	if !slices.Equal(kinds(p), wantSteps) {
		t.Fatalf("steps = %v", kinds(p))
	}
}
