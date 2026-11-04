package git

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/horchestra/pack-operator/db"
)

// The DB store: a put reads back byte for byte, a wrong key does not open it.
func TestDBSecrets(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	t.Setenv("TEST_SECRET_KEY", "the passphrase, any string")
	s, err := NewDBSecrets(d, "TEST_SECRET_KEY")
	if err != nil {
		t.Fatal(err)
	}
	want := &Credentials{Username: "operator", Token: "t0ken", SSHKey: "-----BEGIN OPENSSH PRIVATE KEY-----\n…", Passphrase: "p", KnownHosts: "h"}
	if err := s.Put(ctx, "git/dev", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Credentials(ctx, "git/dev")
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Errorf("roundtrip: %+v — want %+v", got, want)
	}
	if _, err := s.Credentials(ctx, "git/absent"); err == nil {
		t.Error("an absent name read")
	}
	t.Setenv("TEST_SECRET_KEY", "another passphrase")
	other, err := NewDBSecrets(d, "TEST_SECRET_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Credentials(ctx, "git/dev"); err == nil {
		t.Error("a wrong key opened the blob")
	}
}

// OpenBao: a KV v2 answer decodes, the token goes in the header, an error status is an error.
func TestOpenBao(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TEST_BAO_TOKEN", "root")
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Vault-Token")
		gotPath = r.URL.Path
		if gotPath == "/v1/secret/data/fail" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": map[string]string{"Username": "operator", "Token": "t0ken"}},
		})
	}))
	defer srv.Close()
	s := OpenBao{Addr: srv.URL, Mount: "secret", TokenEnv: "TEST_BAO_TOKEN"}
	c, err := s.Credentials(ctx, "git/dev")
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "operator" || c.Token != "t0ken" {
		t.Errorf("the credentials: %+v", c)
	}
	if gotAuth != "root" || gotPath != "/v1/secret/data/git/dev" {
		t.Errorf("the request: %q %q", gotAuth, gotPath)
	}
	// a "<mount>:<path>" reference names its mount
	if _, err := s.Credentials(ctx, "other:git/dev"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/other/data/git/dev" {
		t.Errorf("a mount of the reference: %q", gotPath)
	}
	if _, err := s.Credentials(ctx, "fail"); err == nil {
		t.Error("a 403 read")
	}
}
