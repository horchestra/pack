package git

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/horchestra/pack-operator/db"
)

// Store is where the operator keeps the git credentials of its projects (К5): OpenBao, or the operator's
// database encrypted with the key of encryption_key_env.
type Store interface {
	// Credentials reads the credentials a reference names ("git/dev", or "<mount>:git/dev" for OpenBao).
	Credentials(ctx context.Context, ref string) (*Credentials, error)
}

// OpenBao reads credentials from an OpenBao KV v2 mount: addr, a token in the environment tokenEnv.
type OpenBao struct {
	Addr     string // https://bao:8200
	Mount    string // secret
	TokenEnv string // the variable the token is in
	Client   *http.Client
}

func (s OpenBao) Credentials(ctx context.Context, ref string) (*Credentials, error) {
	mount, path := s.Mount, ref
	if m, p, ok := strings.Cut(ref, ":"); ok {
		mount, path = m, p
	}
	token := os.Getenv(s.TokenEnv)
	if len(token) == 0 {
		return nil, fmt.Errorf("openbao: no token in %s", s.TokenEnv)
	}
	url := strings.TrimSuffix(s.Addr, "/") + "/v1/" + mount + "/data/" + path
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openbao %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("openbao %s: %s", path, resp.Status)
	}
	var doc struct {
		Data struct {
			Data Credentials `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("openbao %s: %w", path, err)
	}
	return &doc.Data.Data, nil
}

// DBSecrets keeps credentials in the operator's database (operator_secrets, migration 0003), every name's
// fields encrypted AES-256-GCM with the key of keyEnv (64 hex digits, or any string — its sha256).
type DBSecrets struct {
	db  *db.DB
	key []byte
}

// NewDBSecrets is the store on the operator's database, the key from the environment.
func NewDBSecrets(d *db.DB, keyEnv string) (*DBSecrets, error) {
	raw := os.Getenv(keyEnv)
	if len(raw) == 0 {
		return nil, fmt.Errorf("git credentials in the database, but %s is empty (encryption_key_env)", keyEnv)
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		sum := sha256.Sum256([]byte(raw))
		key = sum[:]
	}
	return &DBSecrets{db: d, key: key}, nil
}

// Put writes the credentials of a name, encrypted.
func (s *DBSecrets) Put(ctx context.Context, name string, c *Credentials) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	blob := base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, raw, nil))
	_, err = s.db.ExecContext(ctx, s.bind(`insert into operator_secrets (name, blob, updated_at) values (?, ?, current_timestamp)
		on conflict (name) do update set blob = excluded.blob, updated_at = excluded.updated_at`), name, blob)
	return err
}

// Credentials reads them back.
func (s *DBSecrets) Credentials(ctx context.Context, ref string) (*Credentials, error) {
	var blob string
	if err := s.db.QueryRowContext(ctx, s.bind(`select blob from operator_secrets where name = ?`), ref).Scan(&blob); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no credentials %s in the database", ref)
		}
		return nil, err
	}
	c, err := s.open(blob)
	if err != nil {
		return nil, fmt.Errorf("credentials %s: %w", ref, err)
	}
	return c, nil
}

// SecretInfo is a secret without its values (К4: the secrets endpoint lists names and keys only).
type SecretInfo struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

// List is the store's secrets of a prefix, values never leaving the store.
func (s *DBSecrets) List(ctx context.Context, prefix string) ([]SecretInfo, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`select name, blob from operator_secrets where name like ? order by name`), prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretInfo
	for rows.Next() {
		var name, blob string
		if err := rows.Scan(&name, &blob); err != nil {
			return nil, err
		}
		info := SecretInfo{Name: name, Keys: []string{}}
		if c, err := s.open(blob); err == nil && c != nil {
			raw, _ := json.Marshal(c)
			var fields map[string]any
			if json.Unmarshal(raw, &fields) == nil {
				for k := range fields {
					info.Keys = append(info.Keys, k)
				}
				sort.Strings(info.Keys)
			}
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// open decrypts one blob.
func (s *DBSecrets) open(blob string) (*Credentials, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("the blob is short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return nil, fmt.Errorf("the key does not open it")
	}
	var c Credentials
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// bind numbers the ? of a query for PostgreSQL ($1, $2 …); SQLite takes them as they are.
func (s *DBSecrets) bind(query string) string {
	if s.db.SQLite() {
		return query
	}
	var out strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
