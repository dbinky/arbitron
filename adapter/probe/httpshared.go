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
	"strings"
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
	// errResponseTooLarge guards the bounded read (OOM DoS backstop).
	errResponseTooLarge = errors.New("probe: response exceeds MaxResponseBytes")
)

// limitedReader is io.LimitReader that errors (not EOF) past the cap.
type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, errResponseTooLarge
	}
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		return int(l.remaining + int64(n)), errResponseTooLarge
	}
	return n, err
}

// boundedReader caps r at max bytes; max <= 0 means no cap.
func boundedReader(r io.Reader, max int64) io.Reader {
	if max <= 0 {
		return r
	}
	return &limitedReader{r: r, remaining: max}
}

// scrubControl replaces ASCII control chars with a space so an upstream-shaped
// snippet cannot inject newlines/escapes into logs. Mirrors openrouter.scrubControl.
func scrubControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0 && r < 0x20) || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// classifyStatus maps an HTTP status to a classified sentinel, reading a
// bounded, scrubbed snippet of the body for diagnostics. 401/403 ->
// ErrCredentialsRejected; everything else non-2xx -> ErrUpstreamStatus.
func classifyStatus(status int, body io.Reader) error {
	snippet, _ := io.ReadAll(body)
	const max = 512
	if len(snippet) > max {
		snippet = snippet[:max]
	}
	scrubbed := scrubControl(string(snippet))
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
