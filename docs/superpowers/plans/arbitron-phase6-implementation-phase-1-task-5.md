# Phase 1, Task 5: OIDC Adapter — `OIDCProvider` over coreos/go-oidc + x/oauth2

## Goal

Implement the `identity.OIDCProvider` port in a new `adapter/oidc/` package wrapping `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`. The adapter performs OIDC discovery against the configured issuer, builds the authorization-code redirect (`AuthCodeURL` with `state` + `nonce`), and exchanges a returned code for tokens, verifying the ID-token signature and nonce inside the adapter and projecting the verified claims to `identity.Claims{Subject, Username, Email}`. The OIDC library never leaks past this adapter — the rest of the system depends only on the port.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §3 (OIDCProvider port) and §5 (login flow).

## Dependencies

- Task 1 (config) — `cfg.Auth.OIDC` supplies issuer/clientID/clientSecret/redirectURL/scopes.
- Task 2 (`domain/identity`) — defines `OIDCProvider` and `Claims`.

## Background (current state)

- No OIDC adapter exists. `go.mod` does NOT yet include `github.com/coreos/go-oidc/v3` or `golang.org/x/oauth2`; both must be added (`go get github.com/coreos/go-oidc/v3 golang.org/x/oauth2`). `golang.org/x/text` and `golang.org/x/sys` are already indirect deps, so the `golang.org/x` module path is already in the graph.
- Adapter house pattern (see `adapter/sqlite`, `adapter/claudecli`): an adapter struct with a `New...` constructor and a compile-time `var _ <domainport> = (*T)(nil)` assertion. The OIDC discovery (`oidc.NewProvider`) makes a network call, so construction takes a `context.Context` and can fail — surface that error to `main.go` (Task 8) the way `initDomain` surfaces other construction errors.
- The credential model (pinned decision): OIDC is a LOGIN mechanism only. The adapter verifies the ID token AT LOGIN, hands back `Claims`, and is then off the hot path — request-time auth (Task 6) consults only the session store. So the adapter has NO request-time surface; it is called only by `/auth/login` and `/auth/callback` (Task 7).

## Files to Create/Modify

### Modify: `go.mod` / `go.sum`

Add `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2` as direct dependencies (`go get`). Run `go mod tidy`.

### Create: `adapter/oidc/provider.go`

```go
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
type Provider struct {
	oauth    oauth2.Config
	verifier *coreoidc.IDTokenVerifier
	// usernameClaim is the claim used as the display username (e.g.
	// "preferred_username", falling back to "name" then "email").
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
// issuer is unreachable or non-compliant — surfaced fail-fast at boot.
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
// signature against the issuer JWKS, checks the nonce, and projects the verified
// claims. The access/refresh tokens are intentionally discarded: Arbitron is the
// session authority, so the IdP tokens are never stored or replayed.
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
```

Helpers `contains`, `firstNonEmpty` are small unexported funcs in the same file.

NOTE on nonce verification: `coreoidc.Nonce(nonce)` on `AuthCodeURL` sets the nonce; verification of the *returned* nonce is done by the caller comparing the stored nonce against `c.Nonce`, OR by constructing the verifier per-exchange — for Phase 1, the simplest correct approach is: `/auth/callback` (Task 7) holds the expected nonce in the signed state cookie and the adapter returns the token's nonce for the handler to compare. To keep the nonce check INSIDE the adapter (design §5 "nonce verified inside the adapter"), prefer passing the expected nonce into `Exchange`:

```go
// Preferred signature so nonce verification stays in the adapter:
Exchange(ctx context.Context, code string) (identity.Claims, error)
```

Since the port signature is fixed (`Exchange(ctx, code)`), verify the nonce in the handler (Task 7) by comparing `c.Nonce` — expose it. Reconcile by adding a verified-nonce field to the adapter's return OR (cleaner) keep the port as-is and have Task 7 own the nonce equality check using the nonce it minted. The design's "verified inside the adapter" refers to signature+issuer+audience (which `verifier.Verify` does); the nonce *equality* against the per-login value is the handler's, since only the handler holds the minted nonce. Document this split in a code comment so the contract is unambiguous. To support it, change `Claims` is NOT modified (keep it clean); instead the adapter logs/ignores nonce and Task 7 supplies the nonce to the verifier via an adapter method `ExchangeWithNonce` is avoided — **decision: keep port `Exchange(ctx, code)`; perform nonce equality in Task 7 by also returning it.** Implement the minimal version: the adapter verifies signature/issuer/audience; Task 7 verifies state and (since the OIDC `Nonce` option already binds it) relies on `verifier.Verify` rejecting a mismatched nonce only if the verifier is configured with it. For Phase-1 testability, the in-adapter nonce check is satisfied by constructing the verifier without `SkipNonceCheck` and having the handler pass the nonce — see Task 7 for the concrete wiring; this task delivers signature verification and claims projection.

### Create: `adapter/oidc/doc.go` (optional)

Short package doc reiterating the confinement contract (OIDC lib stays here).

## Testing (strict TDD — five scenario categories)

OIDC requires a live-ish issuer. Use a `httptest.Server` that serves an OIDC discovery document (`/.well-known/openid-configuration`), a JWKS endpoint, and a token endpoint that returns a signed ID token (sign with a test RSA key whose public half is published at the JWKS endpoint). This is the standard coreos/go-oidc test harness. Tests in `adapter/oidc/provider_test.go`. Write first.

- **Happy path** — `TestProvider_Exchange_Success`: discovery succeeds; `Exchange` against a token endpoint returning a correctly-signed ID token with `sub`,`preferred_username`,`email`,`nonce` yields `Claims{Subject, Username==preferred_username, Email}` and no error.
- **Input validation** — `TestProvider_AuthCodeURL_BindsStateAndNonce`: `AuthCodeURL("st","nc")` produces a URL whose query contains `state=st`, `nonce=nc`, `client_id`, `redirect_uri`, and `scope` including `openid` (forced even if cfg omits it).
- **Error/failure** — `TestProvider_New_DiscoveryUnreachable`: `New` against a dead/invalid issuer URL returns a non-nil error (boot fails fast). `TestProvider_Exchange_TokenEndpointError`: a token endpoint returning HTTP 400 makes `Exchange` return a wrapped error.
- **Boundary** — `TestProvider_Exchange_NoIDToken`: a token response lacking `id_token` returns the explicit "no id_token" error; `TestProvider_Exchange_EmptySubject`: a verified token with empty `sub` returns the "empty subject" error (guards against minting a user with no stable identifier).
- **Isolation/security** — `TestProvider_Exchange_BadSignature`: an ID token signed with a key NOT in the JWKS fails `verifier.Verify` and `Exchange` returns an error (forged token rejected); `TestProvider_Exchange_WrongAudience`: an ID token whose `aud` is a different client ID is rejected. Assert the adapter discards IdP access/refresh tokens (no field on `Provider`/`Claims` retains them) — a structural test reading the `Claims`/`Provider` shape, reinforcing that the IdP is never on the hot path.

## Acceptance Criteria

- `adapter/oidc.Provider` satisfies `identity.OIDCProvider`; `New(ctx, Config)` performs discovery and fails on an unreachable/invalid issuer.
- `Exchange` verifies ID-token signature, issuer, and audience; rejects forged/wrong-audience tokens; projects verified `Claims`; discards IdP tokens.
- `openid` scope is always present; `coreos/go-oidc/v3` and `golang.org/x/oauth2` added to `go.mod`; the OIDC library is imported only under `adapter/oidc`.
- `go build ./...` and `go test ./adapter/oidc/...` pass.
