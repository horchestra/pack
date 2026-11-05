package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	git "github.com/go-git/go-git/v6"
	"github.com/horchestra/pack/operator/config"
)

// The rigs of C9: an operator with auth local (alice an admin, carol a viewer, bob nobody) or auth oidc
// against a test provider that signs its id tokens itself.

func hash(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// newAuthRig is an operator with the auth the case needs, one project (dev), no worker: the endpoints of
// C9 need no jobs.
func newAuthRig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	bin, err := packBin()
	if err != nil {
		t.Fatalf("the pack binary: %v", err)
	}
	bare := t.TempDir() + "/repo.git"
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatal(err)
	}
	cfg.DB = "sqlite://" + t.TempDir() + "/operator.db"
	cfg.Pack = bin
	cfg.DataDir = t.TempDir()
	cfg.StateDSN = "sqlite://" + t.TempDir() + "/state.db"
	cfg.EncryptionKeyEnv = "PACK_OPERATOR_TEST_KEY"
	cfg.Git = config.Git{FetchInterval: config.Duration{Duration: time.Hour}}
	cfg.Projects = []config.Project{{ID: "dev", Repo: "file://" + bare, Branch: "main", Runtime: "native"}}
	t.Setenv("PACK_OPERATOR_TEST_KEY", "test-key")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := NewServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	go func() { _ = srv.Serve(ctx, ln) }()
	return "http://" + ln.Addr().String()
}

func localConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Auth: "local",
		Local: config.Local{Users: []config.LocalUser{
			{Name: "alice", PasswordHash: hash(t, "alice-pw"), Roles: map[string]string{"*": "admin"}},
			{Name: "carol", PasswordHash: hash(t, "carol-pw"), Roles: map[string]string{"*": "viewer"}},
			{Name: "bob", PasswordHash: hash(t, "bob-pw")},
		}},
	}
}

// jarClient is a client that keeps the cookies and follows no redirects (the OIDC dance is walked by hand).
func jarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func login(t *testing.T, base *url.URL, c *http.Client, user, password string) (string, error) {
	t.Helper()
	resp, err := c.Post(base.String()+"/api/login", "application/json", strings.NewReader(
		fmt.Sprintf(`{"user":%q,"password":%q}`, user, password)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var doc map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login: %s: %v", resp.Status, doc)
	}
	return doc["csrf"], nil
}

// The login, the cookie, the CSRF: a mutation with the cookie and no token is refused, with it goes;
// a wrong password never opens a session.
func TestLocalLoginCSRF(t *testing.T) {
	base, _ := url.Parse(newAuthRig(t, localConfig(t)))
	c := jarClient(t)
	// a wrong password
	if _, err := login(t, base, c, "alice", "nope"); err == nil {
		t.Fatal("a wrong password logged in")
	}
	// who are you? — no session yet
	resp, err := c.Get(base.String() + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session: %s", resp.Status)
	}
	csrf, err := login(t, base, c, "alice", "alice-pw")
	if err != nil {
		t.Fatal(err)
	}
	// the session reads
	resp, err = c.Get(base.String() + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	var projects []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&projects)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(projects) != 1 {
		t.Fatalf("the session's read: %s %v", resp.Status, projects)
	}
	// a mutation with the cookie and no CSRF token is refused
	resp, err = c.Post(base.String()+"/api/tokens", "application/json", strings.NewReader(`{"name":"ci"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no CSRF: %s", resp.Status)
	}
	// with the session's token it mints
	req, _ := http.NewRequest("POST", base.String()+"/api/tokens", strings.NewReader(`{"name":"ci"}`))
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var minted struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&minted)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(minted.Secret, "pko_") {
		t.Fatalf("the mint: %s", resp.Status)
	}
	// the bearer needs no cookie and no CSRF, and reads as alice
	req, _ = http.NewRequest("GET", base.String()+"/api/projects", nil)
	req.Header.Set("Authorization", "Bearer "+minted.Secret)
	resp, err = (&http.Client{}).Do(req) // no jar: the bearer alone
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&projects)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(projects) != 1 {
		t.Fatalf("the bearer's read: %s %v", resp.Status, projects)
	}
}

// X-Pack-User with a bearer: an admin's token acts as another user (pack-ui's way, К7 stage 3); a
// viewer's may not.
func TestImpersonation(t *testing.T) {
	base, _ := url.Parse(newAuthRig(t, localConfig(t)))
	mint := func(user, pw string) string {
		c := jarClient(t)
		csrf, err := login(t, base, c, user, pw)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("POST", base.String()+"/api/tokens", strings.NewReader(`{"name":"ci"}`))
		req.Header.Set("X-CSRF-Token", csrf)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var minted struct {
			Secret string `json:"secret"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&minted)
		resp.Body.Close()
		return minted.Secret
	}
	projectsAs := func(token, imp string) ([]map[string]any, int) {
		req, _ := http.NewRequest("GET", base.String()+"/api/projects", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if len(imp) > 0 {
			req.Header.Set("X-Pack-User", imp)
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var projects []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&projects)
		resp.Body.Close()
		return projects, resp.StatusCode
	}
	admin, viewer := mint("alice", "alice-pw"), mint("carol", "carol-pw")
	if projects, _ := projectsAs(admin, "bob"); len(projects) != 0 {
		t.Errorf("the admin's token as bob: %v", projects) // bob may read nothing
	}
	if projects, _ := projectsAs(admin, "carol"); len(projects) != 1 {
		t.Errorf("the admin's token as carol: %v", projects)
	}
	if projects, _ := projectsAs(viewer, "alice"); len(projects) != 1 {
		t.Errorf("the viewer's token may not be alice: %v", projects) // still carol's view
	}
}

// POST /api/grants: an admin's token gives bob the viewer's role; bob's session then reads the project.
func TestGrants(t *testing.T) {
	base, _ := url.Parse(newAuthRig(t, localConfig(t)))
	c := jarClient(t)
	csrf, err := login(t, base, c, "alice", "alice-pw")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", base.String()+"/api/grants",
		strings.NewReader(`{"user":"bob","role":"viewer","dom":"dev"}`))
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the grant: %s", resp.Status)
	}
	c2 := jarClient(t)
	if _, err := login(t, base, c2, "bob", "bob-pw"); err != nil {
		t.Fatal(err)
	}
	resp, err = c2.Get(base.String() + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	var projects []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&projects)
	resp.Body.Close()
	if len(projects) != 1 {
		t.Fatalf("bob's projects after the grant: %v", projects)
	}
}

// The OIDC dance against a test provider that signs its id tokens itself: login redirects to the provider
// with the state in a cookie; the callback's code is exchanged, the id token verified, the groups mapped
// to the roles — the session's read shows the project.
func TestOIDC(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 provider.URL,
				"authorization_endpoint": provider.URL + "/authorize",
				"token_endpoint":         provider.URL + "/token",
				"jwks_uri":               provider.URL + "/keys",
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}}})
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			jwt := signJWT(t, key, map[string]any{
				"iss": provider.URL, "aud": "pack-operator", "sub": "dave-id",
				"preferred_username": "dave", "groups": []string{"devs"},
				"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
			})
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "at", "token_type": "Bearer", "id_token": jwt,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)

	cfg := &config.Config{
		Auth: "oidc",
		OIDC: config.OIDC{
			Issuer: provider.URL, ClientID: "pack-operator", ClientSecret: "secret",
			RedirectURL: "http://operator/api/auth/oidc/callback",
			Groups:      map[string]string{"devs": "viewer"},
		},
	}
	base, _ := url.Parse(newAuthRig(t, cfg))
	c := jarClient(t)
	// login: off to the provider, the state in the jar
	resp, err := c.Get(base.String() + "/api/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login: %s", resp.Status)
	}
	to, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(to.String(), provider.URL+"/authorize") {
		t.Fatalf("the redirect: %v, %v", to, err)
	}
	state := to.Query().Get("state")
	if len(state) == 0 {
		t.Fatal("no state")
	}
	// the provider answers with a code; the callback makes the session and grants the group's role
	resp, err = c.Get(base.String() + "/api/auth/oidc/callback?state=" + state + "&code=any")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var doc map[string]string
	_ = json.Unmarshal(raw, &doc)
	if resp.StatusCode != http.StatusOK || doc["user"] != "dave" {
		t.Fatalf("the callback: %s %s", resp.Status, raw)
	}
	// dave's group mapped to viewer: the project is his to read
	resp, err = c.Get(base.String() + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	var projects []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&projects)
	resp.Body.Close()
	if len(projects) != 1 {
		t.Fatalf("dave's projects: %v", projects)
	}
	// a forged state is nobody
	c2 := jarClient(t)
	resp, err = c2.Get(base.String() + "/api/auth/oidc/callback?state=forged&code=any")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a forged state: %s", resp.Status)
	}
}

// signJWT is an RS256 id token of the test provider.
func signJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	payload, _ := json.Marshal(claims)
	b64 := base64.RawURLEncoding.EncodeToString
	input := b64(head) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}
