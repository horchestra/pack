// Package authn is who a call is (C9): the sessions of the UI (an httpOnly cookie, a CSRF token of the
// session against a forged site) and the API tokens of CI (a bearer, sha256 in the database — the token
// itself is said once, at creation). Local users prove themselves by a bcrypt password; an OIDC login is
// the provider's id token. What a user may is authz's, not here.
package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/horchestra/pack/operator/db"
)

// Store is the sessions and the tokens in the operator's database.
type Store struct {
	db *db.DB
}

// NewStore is the store on the database.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

// SessionTTL is how long a login holds; TokenPrefix is what a CI token begins with.
const (
	SessionTTL  = 24 * time.Hour
	TokenPrefix = "pko_"
)

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// NewSession opens a session for user: its id (the cookie) and its CSRF token.
func (s *Store) NewSession(ctx context.Context, user string) (id, csrf string, err error) {
	id, csrf = randHex(32), randHex(32)
	_, err = s.db.ExecContext(ctx, s.db.Bind(`insert into sessions (id, username, csrf, expires_at) values (?, ?, ?, ?)`),
		id, user, csrf, time.Now().Add(SessionTTL))
	return id, csrf, err
}

// Session is who the session id is and its CSRF token; an expired one is none.
func (s *Store) Session(ctx context.Context, id string) (user, csrf string, ok bool) {
	err := s.db.QueryRowContext(ctx, s.db.Bind(`select username, csrf from sessions where id = ? and expires_at > ?`), id, time.Now()).
		Scan(&user, &csrf)
	return user, csrf, err == nil && len(user) > 0
}

// DropSession ends the session.
func (s *Store) DropSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.db.Bind(`delete from sessions where id = ?`), id)
	return err
}

// Token is an API token as the list shows it — never the token itself.
type Token struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	User      string     `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

// NewToken mints a token for user: the row and the token itself — shown once, only its sha256 is kept.
// ttl 0 — no expiry.
func (s *Store) NewToken(ctx context.Context, name, user string, ttl time.Duration) (*Token, string, error) {
	secret := TokenPrefix + randHex(32)
	sum := sha256.Sum256([]byte(secret))
	t := &Token{ID: randHex(16), Name: name, User: user, CreatedAt: time.Now()}
	var expires any
	if ttl > 0 {
		e := t.CreatedAt.Add(ttl)
		t.ExpiresAt = &e
		expires = e
	}
	_, err := s.db.ExecContext(ctx, s.db.Bind(`insert into api_tokens (id, name, username, hash, expires_at) values (?, ?, ?, ?, ?)`),
		t.ID, name, user, hex.EncodeToString(sum[:]), expires)
	if err != nil {
		return nil, "", fmt.Errorf("the token: %w", err)
	}
	return t, secret, nil
}

// TokenUser is who the token is of; an unknown or expired one is nobody. Its use is stamped.
func (s *Store) TokenUser(ctx context.Context, secret string) (string, bool) {
	sum := sha256.Sum256([]byte(secret))
	var user string
	err := s.db.QueryRowContext(ctx, s.db.Bind(
		`update api_tokens set last_used = ? where hash = ? and (expires_at is null or expires_at > ?) returning username`),
		time.Now(), hex.EncodeToString(sum[:]), time.Now()).Scan(&user)
	if err != nil && s.db.SQLite() {
		// SQLite has no RETURNING before 3.35 — and modernc's is newer; keep the update and the read apart for both
		_, _ = s.db.ExecContext(ctx, s.db.Bind(
			`update api_tokens set last_used = ? where hash = ? and (expires_at is null or expires_at > ?)`),
			time.Now(), hex.EncodeToString(sum[:]), time.Now())
		err = s.db.QueryRowContext(ctx, s.db.Bind(
			`select username from api_tokens where hash = ? and (expires_at is null or expires_at > ?)`),
			hex.EncodeToString(sum[:]), time.Now()).Scan(&user)
	}
	return user, err == nil && len(user) > 0
}

// Tokens is the user's tokens, newest first.
func (s *Store) Tokens(ctx context.Context, user string) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Bind(
		`select id, name, username, created_at, expires_at, last_used from api_tokens where username = ? order by created_at desc`), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DropToken revokes the user's token.
func (s *Store) DropToken(ctx context.Context, id, user string) error {
	res, err := s.db.ExecContext(ctx, s.db.Bind(`delete from api_tokens where id = ? and username = ?`), id, user)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no token %s of %s", id, user)
	}
	return nil
}
