package identity

import "context"

// EdgeVerifier verifies a single edge-issued identity token (e.g. a Cloudflare
// Access JWT) and returns the authenticated email. It is the domain port the
// auth middleware depends on in edge mode so the verification adapter is
// swappable and testable. Verification is STATELESS (per request). On ANY
// failure — malformed, bad signature, wrong issuer/audience, expired/not-yet-
// valid, or a missing/empty email claim — it returns ("", err); the caller maps
// a non-nil error to a fail-closed 401. The returned email is the verified
// identity; the raw token is never logged or returned.
type EdgeVerifier interface {
	Verify(ctx context.Context, rawToken string) (email string, err error)
}
