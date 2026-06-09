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
	// GroupClaim is the token claim name carrying group membership. "" disables
	// group extraction entirely (every user falls back to the default group). The
	// claim value may be a JSON string or an array of strings; both normalize to
	// []string. A missing or malformed claim yields nil groups — it is NEVER a
	// verification failure, so a user without group info is not locked out.
	GroupClaim string
}

// Verifier implements identity.EdgeVerifier for a Cloudflare-Access-style issuer.
type Verifier struct {
	verifier   *coreoidc.IDTokenVerifier
	audiences  []string
	groupClaim string
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
	return &Verifier{
		verifier:   verifier,
		audiences:  audiences,
		groupClaim: strings.TrimSpace(cfg.GroupClaim),
	}, nil
}

// Verify checks the token signature, issuer, expiry/not-before, and audience
// membership, then returns the verified EdgeIdentity. On ANY of those failures
// (or a missing email claim) it returns (EdgeIdentity{}, err) so the caller fails
// closed (401). A missing/malformed GROUP claim is NOT a failure: it yields
// Groups: nil. The raw token is never logged.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (identity.EdgeIdentity, error) {
	if strings.TrimSpace(rawToken) == "" {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: empty token")
	}
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: token verification: %w", err)
	}
	if !audienceMatches(tok.Audience, v.audiences) {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: token audience %v not in configured audiences", tok.Audience)
	}
	var c struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(&c); err != nil {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: decoding claims: %w", err)
	}
	email := strings.TrimSpace(c.Email)
	if email == "" {
		return identity.EdgeIdentity{}, fmt.Errorf("edge: missing email claim")
	}
	// tok.Subject is the verifier-checked "sub" claim. When the Access IdP exposes
	// a stable subject it is the trustworthy admin-matching key (an email may be
	// unverified at the IdP); downstream provisioning prefers it over email. It is
	// "" when the token omits a subject — provisioning then falls back to email.
	return identity.EdgeIdentity{
		Subject: strings.TrimSpace(tok.Subject),
		Email:   email,
		Groups:  v.extractGroups(tok),
	}, nil
}

// extractGroups decodes the configured group claim from the already-verified
// token. It tolerates the claim being a single JSON string OR an array of
// strings; any other shape (object, number, absent) yields nil. Non-string array
// elements and blank strings are dropped. Returns nil when no group claim is
// configured. The raw token is never logged.
func (v *Verifier) extractGroups(tok *coreoidc.IDToken) []string {
	if v.groupClaim == "" {
		return nil
	}
	// Decode all claims into a generic map so an arbitrary claim name can be read.
	// tok.Claims into a map cannot error here for a well-formed JWT body; on the
	// off chance it does, treat it as "no groups" (fail open to default group, not
	// a lockout — a missing group must never 401 a valid user).
	var all map[string]any
	if err := tok.Claims(&all); err != nil {
		return nil
	}
	raw, ok := all[v.groupClaim]
	if !ok {
		return nil
	}
	switch val := raw.(type) {
	case string:
		if s := strings.TrimSpace(val); s != "" {
			return []string{s}
		}
		return nil
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			s, isStr := item.(string)
			if !isStr {
				continue
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
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
