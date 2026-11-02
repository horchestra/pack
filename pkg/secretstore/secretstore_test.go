package secretstore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeKV2 is a KV v2 server in a map.
func fakeKV2(t *testing.T, token string) (*httptest.Server, map[string]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	data := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != token {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		path, ok := strings.CutPrefix(r.URL.Path, "/v1/secret/data/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			d, ok := data[path]
			if !ok {
				http.Error(w, `{"errors":[]}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": d}})
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.Unmarshal(raw, &body)
			data[path] = body.Data
			_, _ = w.Write([]byte(`{"data":{"version":1}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, data
}

func TestKV2PutGet(t *testing.T) {
	srv, data := fakeKV2(t, "tok")
	t.Setenv(EnvToken, "tok")
	st, err := Open(Config{Provider: "openbao", Addr: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, ok, err := st.Get(ctx, "pack/prod/kafka"); ok || err != nil {
		t.Fatalf("absent: %v %v", ok, err)
	}
	if err := st.Put(ctx, "pack/prod/kafka", map[string]string{"password": "s3"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Get(ctx, "pack/prod/kafka")
	if err != nil || !ok || !reflect.DeepEqual(got, map[string]string{"password": "s3"}) {
		t.Fatalf("%v %v %v", got, ok, err)
	}
	if data["pack/prod/kafka"]["password"] != "s3" {
		t.Errorf("the server holds %v", data)
	}

	// the same under the name vault
	vault, err := Open(Config{Provider: "vault", Addr: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := vault.Get(ctx, "pack/prod/kafka"); !ok || got["password"] != "s3" {
		t.Errorf("vault: %v", got)
	}
}

func TestOpenRefusals(t *testing.T) {
	t.Setenv(EnvToken, "")
	t.Setenv(EnvVaultToken, "")
	if st, err := Open(Config{}); st != nil || err != nil {
		t.Errorf("no store named: %v %v", st, err)
	}
	for name, c := range map[string]Config{
		"unknown kind": {Provider: "nowhere"},
		"no address":   {Provider: "openbao", Token: "t"},
		"no token":     {Provider: "openbao", Addr: "http://x"},
		"bad cacert":   {Provider: "openbao", Addr: "http://x", Token: "t", CACert: "/no/such/file"},
	} {
		if _, err := Open(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	srv, _ := fakeKV2(t, "right")
	st, _ := Open(Config{Provider: "openbao", Addr: srv.URL, Token: "wrong"})
	if _, _, err := st.Get(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a wrong token: %v", err)
	}
	t.Setenv(EnvVaultToken, "vt")
	srv2, _ := fakeKV2(t, "vt")
	if st, err := Open(Config{Provider: "openbao", Addr: srv2.URL}); err != nil {
		t.Errorf("VAULT_TOKEN is the fallback: %v", err)
	} else if _, _, err := st.Get(context.Background(), "x"); err != nil {
		t.Error(err)
	}
}

func TestPaths(t *testing.T) {
	c := Config{}
	if c.PathOf("prod", "kafka-jaas") != "pack/prod/kafka-jaas" {
		t.Error(c.PathOf("prod", "kafka-jaas"))
	}
	c.Prefix = "/team/x/"
	if c.PathOf("prod", "a") != "team/x/a" {
		t.Error(c.PathOf("prod", "a"))
	}
}
