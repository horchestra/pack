package release

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/oci"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscovery(t *testing.T) {
	d := t.TempDir()
	write(t, d, "release.yaml", "pack: ../adh-show\nvalues: [extra.yaml]\n")
	write(t, d, "values.yaml", "a: 1\n")
	write(t, d, "kafka.values.yaml", "b: 2\n")
	write(t, d, "values/zk.yaml", "c: 3\n")
	write(t, d, "env.yaml", "target: docker\n")
	write(t, d, "env.prod.yaml", "target: k8s\n")

	w, err := Load(d, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.Pack != "../adh-show" || filepath.Base(w.env) != "env.yaml" {
		t.Fatalf("%+v", w)
	}
	want := []string{"values.yaml", "kafka.values.yaml", "values", "extra.yaml"}
	if !slices.Equal(w.values, want) {
		t.Fatalf("values = %v, want %v", w.values, want)
	}
	files := w.ValuesFiles()
	if files[0] != filepath.Join(d, "values.yaml") {
		t.Fatalf("files = %v", files)
	}

	w, err = Load(d, "prod")
	if err != nil || filepath.Base(w.env) != "env.prod.yaml" {
		t.Fatalf("prod: %+v %v", w, err)
	}
	if _, err := Load(d, "nope"); err == nil {
		t.Fatal("an environment without a file must fail")
	}
}

func TestPackAndConfig(t *testing.T) {
	w := &Release{Pack: "p", env: "env.yaml"}
	if r, _ := w.PackRef(); r != "p" {
		t.Fatal("the release's pack")
	}
	if w.Config() != "env.yaml" {
		t.Fatal("the release's environment")
	}
	if _, err := (&Release{}).PackRef(); err == nil {
		t.Fatal("no pack: an error")
	}
}

func TestBadFile(t *testing.T) {
	d := t.TempDir()
	write(t, d, "release.yaml", "nmae: typo\n")
	if _, err := Load(d, ""); err == nil {
		t.Fatal("an unknown key in release.yaml must fail")
	}
}

// A target's source is its URL, or the URL with its options; the options of one are not another's.
func TestTargetSources(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, File, `---
targets:
  docker:
    url: oci://localhost:5001/targets/docker:1
    plain_http: true
  ansible: oci://registry.example/targets/ansible:1
`)
	r, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if d := r.Targets["docker"]; d.URL != "oci://localhost:5001/targets/docker:1" || !d.PlainHTTP {
		t.Errorf("docker = %+v", d)
	}
	if a := r.Targets["ansible"]; a.URL != "oci://registry.example/targets/ansible:1" || a.PlainHTTP {
		t.Errorf("ansible = %+v", a)
	}

	write(t, dir, File, "targets:\n  docker:\n    url: oci://h/r:1\n    plainhttp: true\n")
	if _, err := Load(dir, ""); err == nil {
		t.Error("an option a source does not have must be refused")
	}
}

// Credentials are named per source and reach only their own registry: the other one is asked anonymously.
func TestCredentialsReachOnlyTheirRegistry(t *testing.T) {
	t.Setenv("E2E_USER", "me")
	t.Setenv("E2E_TOKEN", "secret")
	dir := t.TempDir()

	var private, public []string
	registry := func(seen *[]string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			*seen = append(*seen, req.Header.Get("Authorization"))
			w.Header().Set("WWW-Authenticate", `Basic realm="r"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		return strings.TrimPrefix(srv.URL, "http://")
	}
	privateHost, publicHost := registry(&private), registry(&public)
	write(t, dir, File, `---
targets:
  docker:
    url: oci://`+privateHost+`/targets/docker:1
    plain_http: true
    username_env: E2E_USER
    password_env: E2E_TOKEN
  ansible:
    url: oci://`+publicHost+`/targets/ansible:1
    plain_http: true
`)
	r, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker", "ansible"} {
		s := r.Targets[name]
		o, err := s.Options()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := oci.PullFile(context.Background(), s.URL, "pack-target-"+name, o); err == nil {
			t.Fatalf("%s: the registries refuse everyone", name)
		}
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me:secret"))
	if !slices.Contains(private, want) {
		t.Errorf("the private registry saw %q, want %q among them", private, want)
	}
	if len(public) == 0 {
		t.Error("the public registry was not asked")
	}
	for _, h := range public {
		if len(h) > 0 {
			t.Errorf("the public registry got credentials: %q", h)
		}
	}
}

func TestOptions(t *testing.T) {
	t.Setenv("E2E_USER", "me")
	t.Setenv("E2E_EMPTY", "")
	for _, tc := range []struct {
		name string
		r    Registry
		want string
	}{
		{"only the username", Registry{UsernameEnv: "E2E_USER"}, "go together"},
		{"only the password", Registry{PasswordEnv: "E2E_USER"}, "go together"},
		{"an empty username", Registry{UsernameEnv: "E2E_EMPTY", PasswordEnv: "E2E_USER"}, "E2E_EMPTY is not set"},
		{"an empty password", Registry{UsernameEnv: "E2E_USER", PasswordEnv: "E2E_EMPTY"}, "E2E_EMPTY is not set"},
	} {
		if _, err := tc.r.Options(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	if o, err := (Registry{PlainHTTP: true}).Options(); err != nil || !o.PlainHTTP || len(o.User) > 0 {
		t.Errorf("anonymous: %+v, %v", o, err)
	}
}
