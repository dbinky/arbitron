package identity

import "context"

// EdgeIdentity is the verified identity an EdgeVerifier extracts from a single
// edge-issued token. Email is the verified email claim (never empty on success).
// Groups carries the RAW, un-normalized values of the configured group claim used
// downstream to derive department membership; it is nil when no group claim is
// configured or the token omits the claim (=> the user falls back to the default
// group, NOT a 401). Department normalization/mapping happens later in
// identity.ResolveDepartments — the verifier returns claim values verbatim.
type EdgeIdentity struct {
	Email  string
	Groups []string
}

// EdgeVerifier verifies a single edge-issued identity token (e.g. a Cloudflare
// Access JWT) and returns the authenticated identity. It is the domain port the
// auth middleware depends on in edge mode so the verification adapter is
// swappable and testable. Verification is STATELESS (per request). On ANY
// failure — malformed, bad signature, wrong issuer/audience, expired/not-yet-
// valid, or a missing/empty email claim — it returns (EdgeIdentity{}, err); the
// caller maps a non-nil error to a fail-closed 401. A missing/malformed GROUP
// claim is NOT a failure: it yields Groups: nil. The raw token is never logged
// or returned.
type EdgeVerifier interface {
	Verify(ctx context.Context, rawToken string) (EdgeIdentity, error)
}
