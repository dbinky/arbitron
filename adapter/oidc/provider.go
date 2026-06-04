// Package oidc adapts an OIDC identity provider to the identity.OIDCProvider
// port. It wraps coreos/go-oidc (discovery + ID-token verification) and
// x/oauth2 (the authorization-code exchange). The OIDC library is confined to
// this package; callers depend only on identity.OIDCProvider.
package oidc

import (
	"context"
	"fmt"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/arbitron/arbitron/domain/identity"
)

// Provider implements identity.OIDCProvider for a standards-compliant issuer.
//
// SECURITY: the IdP is a LOGIN mechanism only. Exchange verifies the ID token
// AT LOGIN and projects Claims; the access/refresh tokens are discarded and
// never stored on this struct, so the IdP is never on the request hot path.
type Provider struct {
	oauth    oauth2.Config
	verifier *coreoidc.IDTokenVerifier
}

var _ identity.OIDCProvider = (*Provider)(nil)

// Config is the adapter's construction input, sourced from cfg.Auth.OIDC.
type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string // openid is forced if absent
}

// New performs OIDC discovery against cfg.IssuerURL and builds the verifier and
// oauth2 config. It makes a network call (discovery) and returns an error if the
// issuer is unreachable or non-compliant -- surfaced fail-fast at boot.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	prov, err := coreoidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", cfg.IssuerURL, err)
	}
	scopes := cfg.Scopes
	if !contains(scopes, coreoidc.ScopeOpenID) {
		scopes = append([]string{coreoidc.ScopeOpenID}, scopes...)
	}
	return &Provider{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     prov.Endpoint(),
			Scopes:       scopes,
		},
		verifier: prov.Verifier(&coreoidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

// AuthCodeURL builds the IdP redirect, binding state (CSRF) and nonce (replay).
func (p *Provider) AuthCodeURL(state, nonce string) string {
	return p.oauth.AuthCodeURL(state, coreoidc.Nonce(nonce))
}

// Exchange swaps the authorization code for tokens, verifies the ID token
// signature against the issuer JWKS, checks issuer/audience, and projects the
// verified claims. The access/refresh tokens are intentionally discarded:
// Arbitron is the session authority, so the IdP tokens are never stored or
// replayed.
//
// NOTE on nonce: verifier.Verify enforces signature/issuer/audience. The nonce
// EQUALITY check against the per-login value lives in the /auth/callback handler
// (Task 7), which is the only place that holds the minted nonce. This split is
// intentional and documented so the contract is unambiguous.
func (p *Provider) Exchange(ctx context.Context, code string) (identity.Claims, error) {
	tok, err := p.oauth.Exchange(ctx, code)
	if err != nil {
		return identity.Claims{}, fmt.Errorf("oidc code exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return identity.Claims{}, fmt.Errorf("oidc: no id_token in token response")
	}
	idTok, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return identity.Claims{}, fmt.Errorf("oidc: id_token verification: %w", err)
	}
	var c struct {
		Subject           string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		Nonce             string `json:"nonce"`
	}
	if err := idTok.Claims(&c); err != nil {
		return identity.Claims{}, fmt.Errorf("oidc: decoding claims: %w", err)
	}
	// idTok.Subject is the verified subject; prefer it over the raw claim.
	subject := idTok.Subject
	if subject == "" {
		subject = c.Subject
	}
	if subject == "" {
		return identity.Claims{}, fmt.Errorf("oidc: empty subject claim")
	}
	username := firstNonEmpty(c.PreferredUsername, c.Name, c.Email, subject)
	return identity.Claims{Subject: subject, Username: username, Email: c.Email}, nil
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
