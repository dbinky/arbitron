// Package probe implements provider.ConnectionProbe per provider family and the
// provider.ProbeSelector that dispatches by binding.Provider. Each probe reuses
// its phase's chat-adapter auth + bounded-read + no-leak patterns. SECURITY: a
// revealed key sets the auth header only and never appears in returned errors,
// which carry a control-scrubbed, length-capped upstream snippet.
package probe

import (
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/arbitron/arbitron/adapter/procenv"
)

// Classification sentinels distinguish configuration faults from transport
// faults so the CLI can print a specific, actionable result and pick an exit
// code. They never carry the secret.
var (
	// ErrCredentialsRejected maps a 401/403 (and Bedrock SigV4 rejection).
	ErrCredentialsRejected = errors.New("probe: credentials rejected")
	// ErrUnreachable maps a transport error (connection refused, DNS, TLS,
	// timeout) — the endpoint could not be reached at all.
	ErrUnreachable = errors.New("probe: endpoint unreachable")
	// ErrRegionConfig maps a Bedrock region/credential configuration fault
	// (missing region/access-key-id, or an endpoint-derivation failure).
	ErrRegionConfig = errors.New("probe: region/configuration error")
	// ErrUpstreamStatus maps any other non-2xx upstream status (400/404/429/5xx).
	ErrUpstreamStatus = errors.New("probe: upstream returned an error status")
	// errResponseTooLarge guards the bounded read (OOM DoS backstop). It aliases
	// the shared procenv sentinel so errors.Is matches either name.
	errResponseTooLarge = procenv.ErrResponseTooLarge
)

// boundedReader caps r at max bytes; max <= 0 means no cap. It delegates to the
// shared procenv bounded read (over-cap yields procenv.ErrResponseTooLarge).
func boundedReader(r io.Reader, max int64) io.Reader {
	return procenv.BoundedReader(r, max)
}

// scrubControl replaces ASCII control chars with a space so an upstream-shaped
// snippet cannot inject newlines/escapes into logs. Delegates to the shared
// procenv.ScrubControl.
func scrubControl(s string) string {
	return procenv.ScrubControl(s)
}

// classifyStatus maps an HTTP status to a classified sentinel, reading a
// bounded, scrubbed snippet of the body for diagnostics. 401/403 ->
// ErrCredentialsRejected; everything else non-2xx -> ErrUpstreamStatus.
func classifyStatus(status int, body io.Reader) error {
	scrubbed := procenv.StatusSnippet(body)
	switch status {
	case 401, 403:
		return fmt.Errorf("%w: status=%d body=%s", ErrCredentialsRejected, status, scrubbed)
	default:
		return fmt.Errorf("%w: status=%d body=%s", ErrUpstreamStatus, status, scrubbed)
	}
}

// checkScheme rejects a non-http(s) base_url scheme (defensive belt-and-suspenders;
// the authoritative SSRF policy is enforced at connection write time).
func checkScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("probe: parse base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("probe: base_url scheme must be http or https: got %q", u.Scheme)
	}
	return nil
}
