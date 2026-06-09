// Package edge adapts a Cloudflare-Access-style edge proxy to the
// identity.EdgeVerifier port. It independently verifies a Cloudflare Access JWT
// (signature against the team-domain JWKS, issuer, audience, expiry/not-before)
// and extracts the verified email claim. It reuses github.com/coreos/go-oidc/v3
// exactly as adapter/oidc does — no hand-rolled JWT crypto, no new dependency.
//
// SECURITY: verification is INDEPENDENT, not header trust. A forged header
// (e.g. a direct/SSRF request that fabricates Cf-Access-Jwt-Assertion) lacks a
// valid Cloudflare signature and is rejected. The raw token and key material
// are never logged or returned; the only output is the verified email.
package edge

import (
	"context"
	"fmt"
	"strings"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/identity"
)

// Config is the adapter's construction input, sourced from cfg.Auth.Edge.
type Config struct {
	// TeamDomain is the Cloudflare Access team domain, used BOTH as the OIDC
	// discovery root / JWKS source and as the expected token issuer.
	TeamDomain string
	// Audiences is the set of application AUD tag(s) to accept. A token whose aud
	// claim contains none of these is rejected.
	Audiences []string
}

// Verifier implements identity.EdgeVerifier for a Cloudflare-Access-style issuer.
type Verifier struct {
	verifier  *coreoidc.IDTokenVerifier
	audiences []string
}

var _ identity.EdgeVerifier = (*Verifier)(nil)

// New performs OIDC discovery against cfg.TeamDomain and builds the JWT
// verifier. It makes a network call (discovery) and returns an error if the team
// domain is unreachable or non-compliant — surfaced fail-fast at boot, exactly
// like adapter/oidc.New. The audience check is performed in Verify (over the
// configured set) rather than via the single-valued go-oidc ClientID check, so a
// token whose aud matches ANY configured audience is accepted.
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	// Bound the HTTP client used for OIDC discovery AND the lazy JWKS refresh that
	// go-oidc's RemoteKeySet performs during Verify (it captures this client from
	// the context at provider creation). Without a timeout, a hung Cloudflare JWKS
	// endpoint would stall the per-request auth hot path; a bounded client makes
	// that a fast fail-closed 401 instead of a goroutine pile-up.
	ctx = coreoidc.ClientContext(ctx, procenv.BoundedHTTPClient(procenv.DefaultOIDCDiscoveryTimeout))
	prov, err := coreoidc.NewProvider(ctx, cfg.TeamDomain)
	if err != nil {
		return nil, fmt.Errorf("edge: discovery for %q: %w", cfg.TeamDomain, err)
	}
	audiences := make([]string, 0, len(cfg.Audiences))
	for _, a := range cfg.Audiences {
		if a = strings.TrimSpace(a); a != "" {
			audiences = append(audiences, a)
		}
	}
	if len(audiences) == 0 {
		return nil, fmt.Errorf("edge: at least one non-empty audience must be configured")
	}
	// SkipClientIDCheck: the go-oidc audience check accepts only a single
	// ClientID, but edge mode allows a SET of audiences; we verify membership
	// ourselves below. Signature, issuer, expiry and not-before are still
	// enforced by the verifier.
	verifier := prov.Verifier(&coreoidc.Config{SkipClientIDCheck: true})
	return &Verifier{verifier: verifier, audiences: audiences}, nil
}

// Verify checks the token signature, issuer, expiry/not-before, and audience
// membership, then returns the verified email. On ANY failure it returns
// ("", err) so the caller fails closed (401). The raw token is never logged.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (string, error) {
	if strings.TrimSpace(rawToken) == "" {
		return "", fmt.Errorf("edge: empty token")
	}
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", fmt.Errorf("edge: token verification: %w", err)
	}
	if !audienceMatches(tok.Audience, v.audiences) {
		return "", fmt.Errorf("edge: token audience %v not in configured audiences", tok.Audience)
	}
	var c struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(&c); err != nil {
		return "", fmt.Errorf("edge: decoding claims: %w", err)
	}
	email := strings.TrimSpace(c.Email)
	if email == "" {
		return "", fmt.Errorf("edge: missing email claim")
	}
	return email, nil
}

// audienceMatches reports whether any value in the token's aud claim is in the
// configured audience set. An empty configured set never matches (fail closed).
func audienceMatches(tokenAud, configured []string) bool {
	for _, want := range configured {
		for _, got := range tokenAud {
			if got == want {
				return true
			}
		}
	}
	return false
}
