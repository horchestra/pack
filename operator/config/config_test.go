package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const full = `
listen: ":9090"
db: "sqlite:///var/lib/pack-operator/operator.db"
state_dsn: "postgres://pack@db/pack"
auth: none
dev_user: alice
pack: /usr/local/bin/pack
secrets:
  provider: openbao
  addr: "https://bao:8200"
  mount: secret
  token_env: PACK_OPERATOR_BAO_TOKEN
encryption_key_env: PACK_OPERATOR_KEY
git:
  author: "pack-operator <pack-operator@example.org>"
  fetch_interval: 45s
projects:
  - id: dev
    repo: "ssh://git@git.example.org/infra/packs.git"
    credentials: secret:git/dev
    flow: review
    runtime: k8s
    labels:
      site: dc1
    approve_keys:
      - "ssh-ed25519 AAAA alice"
    gates:
      - release: releases/prod/openbao
        test_release: releases/stage/openbao
`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "operator.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	c, err := Load(write(t, full))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9090" || c.DB != "sqlite:///var/lib/pack-operator/operator.db" || c.StateDSN != "postgres://pack@db/pack" ||
		c.Auth != "none" || c.DevUser != "alice" || c.Pack != "/usr/local/bin/pack" || c.EncryptionKeyEnv != "PACK_OPERATOR_KEY" {
		t.Errorf("the head: %+v", c)
	}
	if c.Secrets.Provider != "openbao" || c.Secrets.Mount != "secret" || c.Secrets.TokenEnv != "PACK_OPERATOR_BAO_TOKEN" {
		t.Errorf("secrets: %+v", c.Secrets)
	}
	if c.Git.Author != "pack-operator <pack-operator@example.org>" || c.Git.FetchInterval.Duration != 45*time.Second {
		t.Errorf("git: %+v", c.Git)
	}
	if len(c.Projects) != 1 {
		t.Fatalf("projects: %+v", c.Projects)
	}
	p := c.Projects[0]
	if p.ID != "dev" || p.Branch != "main" || p.Flow != "review" || p.Runtime != "k8s" || p.Labels["site"] != "dc1" ||
		len(p.ApproveKeys) != 1 || p.Gates[0].TestRelease != "releases/stage/openbao" {
		t.Errorf("the project: %+v", p)
	}
}

func TestDefaultsAndRefusals(t *testing.T) {
	c, err := Load(write(t, "db: sqlite://x.db\nauth: none\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != DefaultListen || c.DevUser != DefaultDevUser || c.Pack != "pack" || c.Git.FetchInterval.Duration != DefaultFetchInterval {
		t.Errorf("the defaults: %+v", c)
	}
	for body, want := range map[string]string{
		"auth: none\n":                                            "db is required",
		"db: sqlite://x.db\n":                                     "auth is required",
		"db: sqlite://x.db\nauth: oidc\n":                         "oidc.issuer",
		"db: sqlite://x.db\nauth: local\n":                        "local.users",
		"db: sqlite://x.db\nauth: none\nwat: 1\n":                 "unknown field",
		"db: sqlite://x.db\nauth: none\nprojects:\n  - repo: r\n": "id and repo are required",
		"db: sqlite://x.db\nauth: none\nprojects:\n  - id: p\n    repo: r\n    flow: mr\n": "flow is direct or review",
	} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}
