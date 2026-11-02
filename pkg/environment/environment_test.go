package environment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// vault serves KV v2 at /v1/secret/data/<name> to the token "t" only.
func vault(t *testing.T, data map[string]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "t" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{"permission denied"}})
			return
		}
		kv, ok := data[strings.TrimPrefix(r.URL.Path, "/v1/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": kv, "metadata": map[string]any{"version": 1}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The file, Vault's KV over it, the command line over both; the token is
// never in the set.
func TestLoad(t *testing.T) {
	srv := vault(t, map[string]map[string]any{
		"secret/data/pack/common": {"namespace": "from-common", "cluster": map[string]any{"zone": "ci"}},
		"secret/data/pack/prod":   {"NAMESPACE": "prod", "kubeconfig": "apiVersion: v1"},
	})
	file := write(t, "prod.yaml", `
target: k8s
vault:
  addr: `+srv.URL+`
  paths: [secret/data/pack/common, secret/data/pack/prod]
namespace: from-file
replicas: 3
`)
	t.Setenv(EnvVaultToken, "t")
	env, err := Load(context.Background(), file, map[string]string{"Replicas": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if env.Target != "k8s" {
		t.Errorf("what pack reads: %+v", env)
	}
	if !reflect.DeepEqual(env.Vault.Paths, []string{"secret/data/pack/common", "secret/data/pack/prod"}) {
		t.Errorf("vault paths: %v", env.Vault.Paths)
	}
	want := map[string]string{
		"target":     "k8s",
		"vault_addr": srv.URL, "vault_paths": "secret/data/pack/common,secret/data/pack/prod",
		"namespace": "prod", "cluster_zone": "ci", "kubeconfig": "apiVersion: v1", "replicas": "5",
	}
	if !reflect.DeepEqual(env.Settings, want) {
		t.Errorf("the set:\n got %v\nwant %v", env.Settings, want)
	}
	for k, v := range env.Settings {
		if v == "t" || strings.Contains(k, "token") {
			t.Errorf("the Vault token leaked into the set: %s", k)
		}
	}
}

func TestLoad_Refusals(t *testing.T) {
	srv := vault(t, nil)
	file := write(t, "prod.yaml", "vault: {addr: "+srv.URL+", paths: [secret/data/x]}\n")
	t.Setenv(EnvVaultToken, "")
	if _, err := Load(context.Background(), file, nil); err == nil || !strings.Contains(err.Error(), EnvVaultToken) {
		t.Errorf("Vault paths without a token: %v", err)
	}
	t.Setenv(EnvVaultToken, "wrong")
	if _, err := Load(context.Background(), file, nil); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("a refused token: %v", err)
	}
	if _, err := Load(context.Background(), write(t, "prod.toml", "x = 1"), nil); err == nil {
		t.Error("an unknown format must be refused")
	}
	env, err := Load(context.Background(), "", map[string]string{"target": "fake"})
	if err != nil || env.Target != "fake" {
		t.Errorf("no file, the command line alone: %+v %v", env, err)
	}
}

// The other targets of the environment: targets_<name>_<setting> is <setting> of target <name>, and none of it is
// in the default target's set.
func TestLoad_Targets(t *testing.T) {
	file := write(t, "env.yaml", `
target: docker
network: main
targets:
  edge:
    target: k8s
    namespace: data
    kubeconfig: /k
  hosts:
    target: docker
    network_subnet: 10.0.0.0/24
`)
	env, err := Load(context.Background(), file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"target": "docker", "network": "main"}; !reflect.DeepEqual(env.Settings, want) {
		t.Errorf("the default target's set: %v", env.Settings)
	}
	if len(env.Targets) != 2 || env.Targets["edge"].Target != "k8s" || env.Targets["edge"].Settings["namespace"] != "data" ||
		env.Targets["hosts"].Settings["network_subnet"] != "10.0.0.0/24" {
		t.Errorf("the other targets: %+v", env.Targets)
	}
	// --set reaches them too
	env, err = Load(context.Background(), file, map[string]string{"targets_edge_namespace": "ops"})
	if err != nil || env.Targets["edge"].Settings["namespace"] != "ops" {
		t.Errorf("--set over a target: %v %+v", err, env.Targets["edge"])
	}

	bad := write(t, "bad.yaml", "target: docker\ntargets:\n  edge:\n    namespace: data\n")
	if _, err := Load(context.Background(), bad, nil); err == nil || !strings.Contains(err.Error(), "which target") {
		t.Errorf("a target that does not say what it is: %v", err)
	}
}

// target: names an entry of targets: the entry is the default target, and a key naming it runs on the default one.
func TestLoad_DefaultTargetFromAnEntry(t *testing.T) {
	file := write(t, "env.yaml", `
target: main
targets:
  main:
    target: docker
    network: main
  edge:
    target: k8s
keys:
  pg:
    target: main
  web:
    target: edge
`)
	env, err := Load(context.Background(), file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"target": "docker", "network": "main"}; env.Target != "docker" || !reflect.DeepEqual(env.Settings, want) {
		t.Errorf("default target = %s %v", env.Target, env.Settings)
	}
	if len(env.Targets) != 1 || env.Targets["edge"] == nil {
		t.Errorf("the other targets: %+v", env.Targets)
	}
	if env.Keys["pg"].Target != "" || env.Keys["web"].Target != "edge" {
		t.Errorf("keys = %+v", env.Keys)
	}

	beside := write(t, "beside.yaml", "target: main\nnetwork: top\ntargets:\n  main:\n    target: docker\n")
	if _, err := Load(context.Background(), beside, nil); err == nil || !strings.Contains(err.Error(), "network") {
		t.Errorf("a setting beside the entry: %v", err)
	}
}

func TestLoad_Store(t *testing.T) {
	file := write(t, "env.yaml", `
target: docker
store:
  provider: openbao
  addr: https://bao.example:8200
  prefix: pack/prod
  cacert: /ca.crt
targets:
  edge:
    target: k8s
`)
	env, err := Load(context.Background(), file, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Store{Provider: "openbao", Addr: "https://bao.example:8200", Prefix: "pack/prod", CACert: "/ca.crt"}
	if env.Store != want {
		t.Errorf("store: %+v", env.Store)
	}
	for k := range env.Settings {
		if strings.HasPrefix(k, "store") {
			t.Errorf("a target gets %s, the store's", k)
		}
	}
	if env.Targets["edge"] == nil || len(env.Targets["edge"].Settings) != 1 {
		t.Errorf("the other target: %+v", env.Targets)
	}
}

func TestPacksOwnSectionsAreNotTheTargets(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "env.yaml")
	body := "target: docker\nnamespace: data\nrun_as_user: app\nregistry_auth: basic\nrun:\n  jobs: 4\n  strict: true\n  no_rollback: true\n  keep: [pg, hive]\n  journal_keep: 7\nsecrets:\n  provider: generate\n  provision: [/a/pass, /b/pass]\n"
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := Load(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.Run.Jobs != 4 || !env.Run.Strict || !env.Run.NoRollback ||
		env.Secrets.Provider != "generate" || len(env.Secrets.Provision) != 2 || len(env.Run.Keep) != 2 || env.Run.JournalKeep != 7 {
		t.Fatalf("env = %+v", env)
	}
	for k := range env.Settings {
		if ownSettings[k] || strings.HasPrefix(k, "keys_") {
			t.Errorf("%s is pack's own, a provider must not get it", k)
		}
	}
	if env.Settings["namespace"] != "data" {
		t.Errorf("settings = %v", env.Settings)
	}
	// only the sections' own fields are cut out: what merely begins like them is the target's
	if env.Settings["run_as_user"] != "app" || env.Settings["registry_auth"] != "basic" {
		t.Errorf("the target's own settings were taken away: %v", env.Settings)
	}
}

func TestKeysSection(t *testing.T) {
	f := filepath.Join(t.TempDir(), "env.yaml")
	body := `target: docker
keys:
  namenode:
    replicas: 2
    resources: {requests: {memory: 2Gi}, limits: {memory: 4Gi, cpu: "2"}}
    env: [{name: A, value: "1"}, {name: B, secretRef: {name: s, key: k}}]
    labels: {tier: data}
    reach: {instances: [nn.example], ports: {http: 9870}}
    hosts: [a, b]
    publish: {http: 9870}
  zookeeper: {}
`
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := Load(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	nn := env.Keys["namenode"]
	if nn.Replicas == nil || *nn.Replicas != 2 || nn.Resources.Limits["memory"] != "4Gi" || len(nn.Env) != 2 ||
		nn.Env[1].SecretRef == nil || nn.Reach.Ports["http"] != 9870 || nn.Labels["tier"] != "data" {
		t.Fatalf("namenode = %+v", nn)
	}
	if hosts, _ := nn.Raw["hosts"].([]any); len(hosts) != 2 || nn.Raw["publish"] == nil {
		t.Fatalf("the target's own words stay raw: %+v", nn.Raw)
	}
	if _, ok := env.Keys["zookeeper"]; !ok {
		t.Fatal("an empty entry is still a key")
	}
	for k := range env.Settings {
		if strings.HasPrefix(k, "keys_") {
			t.Errorf("%s: a key's placement is not a target setting", k)
		}
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("keys:\n  nn: {resouces: {}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a misspelt common word is the target's own (raw), not an error: the target refuses what it does not know
	if _, err := Load(context.Background(), bad, nil); err != nil {
		t.Fatal(err)
	}
	worse := filepath.Join(t.TempDir(), "worse.yaml")
	if err := os.WriteFile(worse, []byte("keys:\n  nn: {replicas: many}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), worse, nil); err == nil {
		t.Fatal("replicas must be a number")
	}
}

// Vault and the overrides are flat, so they give the keys as the text of one field, merged over the file's.
func TestKeysFromVaultAndOverrides(t *testing.T) {
	srv := vault(t, map[string]map[string]any{
		"secret/data/pack/prod": {"keys": `{"postgres": {"replicas": 3}, "cache": {"labels": {"tier": "hot"}}}`},
	})
	file := write(t, "env.yaml", `
target: docker
vault:
  addr: `+srv.URL+`
  paths: [secret/data/pack/prod]
keys:
  postgres:
    labels: {tier: data}
    hosts: [a]
`)
	t.Setenv(EnvVaultToken, "t")
	env, err := Load(context.Background(), file, map[string]string{"keys": "postgres: {replicas: 5}\nextra: {}\n"})
	if err != nil {
		t.Fatal(err)
	}
	pg := env.Keys["postgres"]
	if pg.Replicas == nil || *pg.Replicas != 5 || pg.Labels["tier"] != "data" || pg.Raw["hosts"] == nil {
		t.Fatalf("the override's replicas over Vault's, the file's labels and hosts kept: %+v", pg)
	}
	if c := env.Keys["cache"]; c.Labels["tier"] != "hot" {
		t.Fatalf("a key only Vault names: %+v", c)
	}
	if _, ok := env.Keys["extra"]; !ok {
		t.Fatal("a key only the override names")
	}
	if _, has := env.Settings["keys"]; has {
		t.Fatal("the keys' text is not a target's setting")
	}
}

func TestKeysTextThatIsNotKeysIsRefused(t *testing.T) {
	file := write(t, "env.yaml", "target: docker\n")
	for name, text := range map[string]string{"not a document": "{{", "replicas as text": `{"pg": {"replicas": "many"}}`, "a list": `[1]`} {
		if _, err := Load(context.Background(), file, map[string]string{"keys": text}); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// A flat layer cannot hold the keys entry by entry: keys_pg_replicas is a refusal, in an override and in Vault.
func TestFlatKeysAreRefused(t *testing.T) {
	file := write(t, "env.yaml", "target: docker\n")
	if _, err := Load(context.Background(), file, map[string]string{"keys_pg_replicas": "3"}); err == nil ||
		!strings.Contains(err.Error(), "one field `keys`") {
		t.Errorf("override: err = %v", err)
	}
	srv := vault(t, map[string]map[string]any{
		"secret/data/pack/prod": {"keys_pg_replicas": "3"},
	})
	vfile := write(t, "env-vault.yaml", "target: docker\nvault:\n  addr: "+srv.URL+"\n  paths: [secret/data/pack/prod]\n")
	t.Setenv(EnvVaultToken, "t")
	if _, err := Load(context.Background(), vfile, nil); err == nil || !strings.Contains(err.Error(), "one field `keys`") {
		t.Errorf("vault: err = %v", err)
	}
}

// ownSettings is kept by hand: it must be exactly the fields of the three sections, or a new setting of a section
// leaks into a target's set (or a target's setting goes missing).
func TestOwnSettingsAreTheSectionsFields(t *testing.T) {
	want := map[string]bool{}
	et := reflect.TypeFor[Environment]()
	for sec := range et.Fields() {
		switch sec.Type {
		case reflect.TypeFor[Store](), reflect.TypeFor[Run](), reflect.TypeFor[Secrets]():
		default:
			continue
		}
		prefix := sec.Tag.Get("envPrefix")
		if len(prefix) == 0 {
			t.Fatalf("%s has no envPrefix", sec.Name)
		}
		for field := range sec.Type.Fields() {
			name := field.Tag.Get("env")
			if len(name) == 0 {
				t.Fatalf("%s.%s has no env tag", sec.Name, field.Name)
			}
			want[strings.ToLower(prefix+name)] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("no section found")
	}
	for k := range want {
		if !ownSettings[k] {
			t.Errorf("%s is a field of a section and is missing from ownSettings: it would reach the target", k)
		}
	}
	for k := range ownSettings {
		if !want[k] {
			t.Errorf("%s is in ownSettings and is no field of a section", k)
		}
	}
}
