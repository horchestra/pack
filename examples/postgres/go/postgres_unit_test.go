package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target/memory"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/compute"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/semver"
)

// The defaults are what an operator who wrote nothing gets.
func TestDefaults(t *testing.T) {
	p := &Postgres{}
	p.SetDefaults()
	if p.Superuser.Name != "postgres" {
		t.Errorf("superuser.name = %q", p.Superuser.Name)
	}
	if p.Conf.MaxConnections != 100 || p.Conf.ListenAddresses != "*" || p.Conf.Port != 5432 {
		t.Errorf("conf = %+v", p.Conf)
	}
	// what the type cannot say by being zero — and nothing more
	if len(p.Database) > 0 || p.Superuser.Password.SecretRef != nil || len(p.Conf.SharedBuffers) > 0 {
		t.Errorf("the operator's own are not defaulted: %+v", p)
	}
}

func TestMeta(t *testing.T) {
	m := (&Postgres{}).Meta()
	if m.Name != "postgres" || m.Version != "16.4" || m.Description != "PostgreSQL" || m.License != "PostgreSQL" ||
		m.Image != "oci://images/postgres:16.4" || m.Artifact != "oci://packs/postgres" {
		t.Errorf("meta = %+v", m)
	}
	if len(m.Annotations) > 0 || len(m.Created) > 0 {
		t.Errorf("nothing beyond the identity: %+v", m)
	}
}

// The formula is on the leaf the author pointed at, and it computes what the
// comment promises: a quarter of the memory limit, 128MB where there is none.
func TestCompute(t *testing.T) {
	formulas := (&Postgres{}).PackCompute()
	if len(formulas) != 1 {
		t.Fatalf("formulas = %v", formulas)
	}
	expr, ok := formulas["/conf/shared_buffers"]
	if !ok {
		t.Fatalf("the pointer is the field's: %v", formulas)
	}
	env, err := compute.Env()
	if err != nil {
		t.Fatal(err)
	}
	a, err := compute.Compile(env, expr)
	if err != nil {
		t.Fatal(err)
	}
	v, err := compute.Eval(env, a, map[string]any{"limits": map[string]any{"memory": int64(1 << 30)}})
	if err != nil || v != "256MB" {
		t.Errorf("1Gi of memory: %v (%v), want 256MB", v, err)
	}
	v, err = compute.Eval(env, a, map[string]any{})
	if err != nil || v != "128MB" {
		t.Errorf("no limits: %v (%v), want 128MB", v, err)
	}
}

// PackRoot is the pack's contract with the surrounding system; every name it
// drops — a port, a constraint, a script — must exist.
func TestRoot(t *testing.T) {
	root := (&Postgres{}).PackRoot()

	if root.Ports["pg"] != 5432 {
		t.Errorf("ports = %v", root.Ports)
	}
	if root.Readiness == nil || root.Readiness.TCP != "pg" {
		t.Errorf("readiness = %+v", root.Readiness)
	}
	data, ok := root.Data["data"]
	if !ok || !strings.HasPrefix(data.MountPath, "/") {
		t.Fatalf("data = %v", root.Data)
	}
	if q, err := compute.Quantity(data.Size); err != nil || q <= 0 {
		t.Errorf("data size %q: %v", data.Size, err)
	}
	if len(root.Minimum) == 0 {
		t.Error("a minimum the environment is held to")
	}
	for name, qty := range root.Minimum {
		if _, err := compute.Quantity(qty); err != nil {
			t.Errorf("minimum %s: %v", name, err)
		}
	}

	// the upgrade the pack promises: from 15 on, and 15.0 does satisfy it
	cons, err := semver.ParseConstraint(root.Upgrade.From)
	if err != nil {
		t.Fatalf("upgrade.from: %v", err)
	}
	v15, _ := semver.Parse("15.0")
	if v16, _ := semver.Parse("16.0"); !cons.Check(v15) || !cons.Check(v16) {
		t.Errorf("from %q must take the versions the hooks upgrade from and are", root.Upgrade.From)
	}

	if len(root.Hooks) != 3 {
		t.Fatalf("hooks = %v", root.Hooks)
	}
	names := map[string]bool{}
	for _, h := range root.Hooks {
		if len(h.Name) == 0 || names[h.Name] {
			t.Errorf("hook names are unique and given: %v", root.Hooks)
		}
		names[h.Name] = true
		if !slices.Contains(packschema.Phases, h.Phase) {
			t.Errorf("%s: phase %q", h.Name, h.Phase)
		}
		if len(h.Command) == 0 {
			t.Errorf("%s: no command", h.Name)
		}
		if len(h.Timeout) > 0 {
			if _, err := time.ParseDuration(h.Timeout); err != nil {
				t.Errorf("%s: timeout %q: %v", h.Name, h.Timeout, err)
			}
		}
		if h.When != nil {
			for _, c := range []string{h.When.From, h.When.To} {
				if _, err := semver.ParseConstraint(c); err != nil {
					t.Errorf("%s: when: %v", h.Name, err)
				}
			}
		}
		if packschema.IsInstall(h.Phase) {
			if h.Exclusive || h.Image == "old" || (h.When != nil && len(h.When.From) > 0) {
				t.Errorf("%s: an install hook has nothing to stop, no old image, no version to come from", h.Name)
			}
		}
	}

	// the saga of the 15 → 16 upgrade: pg_upgrade with the component stopped,
	// and the way back
	var up *packschema.Hook
	for i := range root.Hooks {
		if root.Hooks[i].Name == "pg-upgrade" {
			up = &root.Hooks[i]
		}
	}
	if up == nil || !up.Exclusive || up.Phase != packschema.PreUpgrade {
		t.Fatalf("pg-upgrade = %+v", up)
	}
	if up.When == nil || up.When.From != "<16" || up.When.To != ">=16" {
		t.Errorf("pg-upgrade.when = %+v", up.When)
	}
	if up.Compensate == nil || len(up.Compensate.Command) == 0 || up.Compensate.Image != "old" {
		t.Fatalf("a failed pg_upgrade takes the data back: %+v", up.Compensate)
	}

	// the scripts the hooks run are the pack's own files
	for _, script := range []string{"init-extensions.sh", "restore-15.sh"} {
		b, err := os.ReadFile(filepath.Join("content", script))
		if err != nil || len(b) == 0 {
			t.Errorf("content/%s: %v", script, err)
		}
	}
	if !strings.HasSuffix(up.Compensate.Command[0], "restore-15.sh") {
		t.Errorf("the compensate runs the restore script: %v", up.Compensate.Command)
	}
}

// The schema the operator validates against: what is required, where the
// passwords and the files go, and the formula on shared_buffers.
func TestSchema(t *testing.T) {
	raw, err := sdk.Schema(&Postgres{})
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	props := s["properties"].(map[string]any)

	required := map[string]bool{}
	for _, r := range s["required"].([]any) {
		required[r.(string)] = true
	}
	for _, name := range []string{"database", "superuser", "conf"} {
		if !required[name] {
			t.Errorf("%s must be required (required = %v)", name, s["required"])
		}
	}
	if _, hasDefault := props["database"].(map[string]any)["default"]; hasDefault {
		t.Error("the database name is the operator's, no default")
	}

	su := props["superuser"].(map[string]any)
	sup := su["properties"].(map[string]any)
	if req := su["required"].([]any); len(req) != 1 || req[0] != "password" {
		t.Errorf("superuser.required = %v: the password, the name is defaulted", req)
	}
	pw := sup["password"].(map[string]any)
	if pw["format"] != "password" || pw["x-pack-env"] != "POSTGRES_PASSWORD" {
		t.Errorf("password = %v", pw)
	}
	if name := sup["name"].(map[string]any); name["default"] != "postgres" || name["x-pack-env"] != "POSTGRES_USER" {
		t.Errorf("name = %v", name)
	}

	conf := props["conf"].(map[string]any)
	file := conf["x-pack-file"].(map[string]any)
	if file["path"] != "/etc/postgresql/postgresql.conf" || file["format"] != "properties" {
		t.Errorf("conf file = %v", file)
	}
	sb := conf["properties"].(map[string]any)["shared_buffers"].(map[string]any)
	for _, expr := range (&Postgres{}).PackCompute() {
		if sb["x-pack-compute"] != expr {
			t.Errorf("shared_buffers compute = %v, want %q", sb["x-pack-compute"], expr)
		}
	}

	// the scripts ship as they are, executable
	for name, tmpl := range map[string]string{"init_extensions": "init-extensions.sh", "restore_15": "restore-15.sh"} {
		f := props[name].(map[string]any)["x-pack-file"].(map[string]any)
		if f["format"] != "raw" || f["template"] != tmpl || f["mode"] != "0755" {
			t.Errorf("%s file = %v", name, f)
		}
		if !strings.HasSuffix(f["path"].(string), "/"+tmpl) {
			t.Errorf("%s path = %v", name, f["path"])
		}
	}

	x := s["x-pack"].(map[string]any)
	if ports := x["ports"].(map[string]any); ports["pg"] != 5432.0 {
		t.Errorf("ports = %v", ports)
	}
	if x["readiness"].(map[string]any)["tcp"] != "pg" {
		t.Errorf("readiness = %v", x["readiness"])
	}
	if x["upgrade"].(map[string]any)["from"] != ">=15" {
		t.Errorf("upgrade = %v", x["upgrade"])
	}
	if hooks := x["hooks"].([]any); len(hooks) != 3 {
		t.Errorf("hooks = %v", hooks)
	}
}

// ---- the pack renders

// values of a minimal installation: the database name and where the
// superuser's password is kept.
func values(sharedBuffers string) map[string]any {
	conf := map[string]any{}
	if len(sharedBuffers) > 0 {
		conf["shared_buffers"] = sharedBuffers
	}
	return map[string]any{
		"database":  "app",
		"superuser": map[string]any{"password": map[string]any{"secretRef": map[string]any{"name": "pg", "key": "password"}}},
		"conf":      conf,
	}
}

func renderPostgres(t *testing.T, b *bundle.Bundle, vals map[string]any, limits map[string]string) (*render.Release, error) {
	t.Helper()
	res := &render.Resources{Requests: map[string]string{"cpu": "500m", "memory": "512Mi"}, Limits: limits}
	return render.Render(b, render.Values{Values: vals}, render.Context{
		Installation: "t",
		Target:       memory.New(),
		Keys:         map[string]render.Placement{"postgres": {Resources: res}},
	})
}

func loadPostgres(t *testing.T) *bundle.Bundle {
	t.Helper()
	dir := t.TempDir()
	if err := sdk.Generate(dir, &Postgres{}); err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Load(filepath.Join(dir, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fileOf(t *testing.T, rel *render.Release, path string) render.File {
	t.Helper()
	for _, u := range rel.Units {
		for _, f := range u.Files {
			if f.Path == path {
				return f
			}
		}
	}
	t.Fatalf("no file %s in the release", path)
	return render.File{}
}

// The whole link: values in, postgresql.conf out — with the formula's answer
// in it, the operator's word above it, the scripts as shipped.
func TestRender(t *testing.T) {
	b := loadPostgres(t)

	rel, err := renderPostgres(t, b, values(""), map[string]string{"memory": "1Gi"})
	if err != nil {
		t.Fatal(err)
	}
	conf := string(fileOf(t, rel, "/etc/postgresql/postgresql.conf").Content)
	for _, want := range []string{"max_connections=100", "port=5432", "listen_addresses=*", "shared_buffers=256MB"} {
		if !strings.Contains(conf, want) {
			t.Errorf("postgresql.conf misses %q:\n%s", want, conf)
		}
	}
	var computed bool
	for _, c := range rel.Report.Computed {
		if c.Pointer == "/conf/shared_buffers" {
			computed = c.Value == "256MB"
		}
	}
	if !computed {
		t.Errorf("the formula's answer is reported: %v", rel.Report.Computed)
	}

	// no limit — the formula's fallback
	rel, err = renderPostgres(t, b, values(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if conf := string(fileOf(t, rel, "/etc/postgresql/postgresql.conf").Content); !strings.Contains(conf, "shared_buffers=128MB") {
		t.Errorf("no limits, 128MB:\n%s", conf)
	}

	// the operator's word stands above the formula
	rel, err = renderPostgres(t, b, values("512MB"), map[string]string{"memory": "1Gi"})
	if err != nil {
		t.Fatal(err)
	}
	if conf := string(fileOf(t, rel, "/etc/postgresql/postgresql.conf").Content); !strings.Contains(conf, "shared_buffers=512MB") {
		t.Errorf("the operator wrote 512MB:\n%s", conf)
	}
	if len(rel.Report.Computed) != 0 {
		t.Errorf("nothing left to compute: %v", rel.Report.Computed)
	}

	// the environment: the user is a default, the password stays a reference
	unit := rel.Units[0]
	envs := map[string]render.EnvVar{}
	for _, e := range unit.Env {
		envs[e.Name] = e
	}
	if envs["POSTGRES_USER"].Value != "postgres" {
		t.Errorf("env = %v", unit.Env)
	}
	if pw := envs["POSTGRES_PASSWORD"]; pw.Secret == nil || pw.Secret.Name != "pg" || pw.Secret.Key != "password" {
		t.Errorf("the password is a reference, never a value: %+v", pw)
	}

	// the scripts land as shipped, executable
	script := fileOf(t, rel, "/opt/pack/init-extensions.sh")
	shipped, _ := os.ReadFile(filepath.Join("content", "init-extensions.sh"))
	if string(script.Content) != string(shipped) {
		t.Errorf("init-extensions.sh is not the shipped file")
	}
	if script.Mode == nil || *script.Mode != 0o755 {
		t.Errorf("mode = %v", script.Mode)
	}
}

// What the schema requires, the render refuses to do without.
func TestRenderRefuses(t *testing.T) {
	b := loadPostgres(t)

	noDB := values("")
	delete(noDB, "database")
	if _, err := renderPostgres(t, b, noDB, nil); err == nil || !strings.Contains(err.Error(), "database") {
		t.Errorf("no database: %v", err)
	}

	noPW := values("")
	noPW["superuser"] = map[string]any{}
	if _, err := renderPostgres(t, b, noPW, nil); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("no password: %v", err)
	}

	// the minimum is the environment's to meet
	_, err := render.Render(b, render.Values{Values: values("")}, render.Context{
		Installation: "t",
		Target:       memory.New(),
		Keys: map[string]render.Placement{"postgres": {
			Resources: &render.Resources{Requests: map[string]string{"cpu": "100m", "memory": "512Mi"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "minimum") {
		t.Errorf("cpu below the minimum: %v", err)
	}
}
