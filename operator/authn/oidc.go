package authn

import (
	"context"
	"fmt"
	"net/http"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/horchestra/pack/operator/config"
)

// OIDC is the OpenID Connect login (auth: oidc): the provider discovered at the issuer, the code flow of
// the callback. The id token's username claim is the user, its groups claim the roles' source.
type OIDC struct {
	cfg      config.OIDC
	provider *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	oauth    oauth2.Config
}

// NewOIDC discovers the issuer and readies the flow. ctx times the discovery out.
func NewOIDC(ctx context.Context, cfg config.OIDC) (*OIDC, error) {
	if len(cfg.UsernameClaim) == 0 {
		cfg.UsernameClaim = "preferred_username"
	}
	if len(cfg.GroupsClaim) == 0 {
		cfg.GroupsClaim = "groups"
	}
	provider, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("the OIDC issuer %s: %w", cfg.Issuer, err)
	}
	return &OIDC{
		cfg:      cfg,
		provider: provider,
		verifier: provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{gooidc.ScopeOpenID, "profile", "email"},
		},
	}, nil
}

// stateCookie is where the flow's state waits out the redirect.
const stateCookie = "pack_oidc_state"

// Login sends the user to the provider, the state in an httpOnly cookie against a forged callback.
func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	state := randHex(16)
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int((10 * time.Minute).Seconds()), Secure: r.TLS != nil,
	})
	http.Redirect(w, r, o.oauth.AuthCodeURL(state), http.StatusFound)
}

// Identity is who the id token says: the user and the groups.
type Identity struct {
	User   string
	Groups []string
}

// Callback checks the state and the code, verifies the id token and says who it is. The redirect after
// the login is the caller's.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) (*Identity, error) {
	cookie, err := r.Cookie(stateCookie)
	if err != nil || cookie.Value == "" || cookie.Value != r.URL.Query().Get("state") {
		return nil, fmt.Errorf("the state does not match — the login began elsewhere")
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/", MaxAge: -1})
	tok, err := o.oauth.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		return nil, fmt.Errorf("the code's exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("the answer holds no id_token")
	}
	id, err := o.verifier.Verify(r.Context(), raw)
	if err != nil {
		return nil, fmt.Errorf("the id token: %w", err)
	}
	var claims map[string]any
	if err := id.Claims(&claims); err != nil {
		return nil, fmt.Errorf("the id token's claims: %w", err)
	}
	out := &Identity{}
	if v, _ := claims[o.cfg.UsernameClaim].(string); len(v) > 0 {
		out.User = v
	} else {
		out.User = id.Subject
	}
	if gs, ok := claims[o.cfg.GroupsClaim].([]any); ok {
		for _, g := range gs {
			if s, ok := g.(string); ok {
				out.Groups = append(out.Groups, s)
			}
		}
	}
	return out, nil
}
