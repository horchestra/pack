package secretstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/horchestra/pack/pkg/logging"
)

func init() {
	// OpenBao is the fork of Vault and keeps its KV v2 API: one implementation serves both names.
	Register("openbao", openKV2)
	Register("vault", openKV2)
}

// kv2 is a store over the KV v2 API of OpenBao or Vault: GET and POST of <addr>/v1/<mount>/data/<path>.
type kv2 struct {
	addr, mount, token string
	client             *http.Client
}

func openKV2(c Config) (Store, error) {
	if len(c.Addr) == 0 {
		return nil, fmt.Errorf("store.addr: the %s store needs the address of the server", c.Provider)
	}
	if _, err := url.Parse(c.Addr); err != nil {
		return nil, fmt.Errorf("store.addr: %w", err)
	}
	if len(c.Token) == 0 {
		return nil, fmt.Errorf("the %s store needs a token: %s (or %s)", c.Provider, EnvToken, EnvVaultToken)
	}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if len(c.CACert) > 0 {
		pem, err := os.ReadFile(c.CACert)
		if err != nil {
			return nil, fmt.Errorf("store.cacert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("store.cacert %s: no certificate in it", c.CACert)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	mount := strings.Trim(c.Mount, "/")
	if len(mount) == 0 {
		mount = defaultMount
	}
	return &kv2{addr: strings.TrimRight(c.Addr, "/"), mount: mount, token: c.Token,
		client: &http.Client{Transport: logging.Transport("store", tr), Timeout: 30 * time.Second}}, nil
}

func (s *kv2) url(path string) string {
	return s.addr + "/v1/" + s.mount + "/data/" + strings.Trim(path, "/")
}

func (s *kv2) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.url(path), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.client.Do(req)
}

func (s *kv2) Get(ctx context.Context, path string) (fields map[string]string, found bool, err error) {
	defer func() {
		// the names of the fields, never their values
		logging.For("store").Debug().Str("op", "get").Str("path", path).Bool("found", found).Int("fields", len(fields)).Err(err).Msg("secret")
	}()
	resp, err := s.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, nil
	case resp.StatusCode != http.StatusOK:
		return nil, false, fmt.Errorf("store: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(raw)))
	}
	var doc struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("store: GET %s: %w", path, err)
	}
	if doc.Data.Data == nil {
		return nil, false, nil // the latest version is deleted
	}
	out := make(map[string]string, len(doc.Data.Data))
	for k, v := range doc.Data.Data {
		if str, ok := v.(string); ok {
			out[k] = str
		} else {
			b, _ := json.Marshal(v)
			out[k] = string(b)
		}
	}
	return out, true, nil
}

func (s *kv2) Put(ctx context.Context, path string, fields map[string]string) (err error) {
	defer func() {
		logging.For("store").Debug().Str("op", "put").Str("path", path).Int("fields", len(fields)).Err(err).Msg("secret")
	}()
	body, err := json.Marshal(map[string]any{"data": fields})
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("store: PUT %s: %s: %s", path, resp.Status, strings.TrimSpace(string(raw)))
	}
	return nil
}
