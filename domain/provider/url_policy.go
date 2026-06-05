package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// URLPolicy validates a connection's BaseURL at write time (create/update). It
// is the SSRF / credential-exfiltration guard: a connection's BaseURL drives
// outbound requests that carry the decrypted API key, so an unvalidated value
// lets a caller point credentialed traffic at an attacker host or an internal
// service. Production wiring uses StrictURLPolicy; tests that legitimately need
// loopback/non-https endpoints (httptest, fake routers) inject AllowAnyURLPolicy.
type URLPolicy interface {
	// ValidateURL returns nil when rawURL is acceptable, or a non-nil error
	// (wrapping ErrInvalidBaseURL) describing why it was rejected.
	ValidateURL(rawURL string) error
}

// ErrInvalidBaseURL wraps every BaseURL rejection so callers and adapters can
// match the class of failure with errors.Is regardless of the specific reason.
var ErrInvalidBaseURL = errors.New("connection base_url is invalid")

// strictURLPolicy enforces https-only and rejects obviously-internal hosts
// (loopback, link-local, the cloud metadata endpoint, and RFC1918 private
// ranges). It is the default production policy.
type strictURLPolicy struct{}

// StrictURLPolicy is the production URLPolicy: it requires an https scheme and a
// host that is not loopback, link-local, the 169.254.169.254 metadata address,
// or an RFC1918 / unique-local private range. It is enforced once at the
// connection write path so a credentialed adapter can never be pointed at an
// internal service.
var StrictURLPolicy URLPolicy = strictURLPolicy{}

func (strictURLPolicy) ValidateURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return fmt.Errorf("%w: base_url is empty", ErrInvalidBaseURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: parse: %v", ErrInvalidBaseURL, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not allowed (https only)", ErrInvalidBaseURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: base_url has no host", ErrInvalidBaseURL)
	}
	if isInternalHost(host) {
		return fmt.Errorf("%w: host %q resolves to an internal/loopback address", ErrInvalidBaseURL, host)
	}
	return nil
}

// allowAnyURLPolicy accepts any non-empty URL. It exists for tests and local
// fixtures (httptest loopback servers, fake routers) and MUST NOT be used in
// production wiring.
type allowAnyURLPolicy struct{}

// AllowAnyURLPolicy is a permissive URLPolicy for tests/fixtures only. It
// accepts any parseable, non-empty URL, including http and loopback hosts.
var AllowAnyURLPolicy URLPolicy = allowAnyURLPolicy{}

func (allowAnyURLPolicy) ValidateURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return fmt.Errorf("%w: base_url is empty", ErrInvalidBaseURL)
	}
	if _, err := url.Parse(rawURL); err != nil {
		return fmt.Errorf("%w: parse: %v", ErrInvalidBaseURL, err)
	}
	return nil
}

// isInternalHost reports whether host is a literal IP (or the textual
// "localhost") that names a loopback, link-local, metadata, or private-range
// address. It deliberately does NOT perform DNS resolution: resolving here would
// add a TOCTOU window and break offline tests; the policy guards against the
// obvious literal-internal-address forms an SSRF payload uses.
func isInternalHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		// Not a literal IP and not "localhost": treat as an external hostname.
		return false
	}
	// 169.254.169.254 is the cloud instance-metadata endpoint; it is link-local,
	// but call it out explicitly since it is the canonical SSRF credential target.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	return false
}

// providerOllama is the connection provider string that earns the narrow
// loopback carve-out below. Kept here (not imported from adapter/provider) so
// the domain policy has no adapter dependency.
const providerOllama = "ollama"

// ollamaLoopbackAllowed reports whether c earns the narrow local-Ollama
// carve-out: provider == "ollama" (case/whitespace-insensitive) AND the
// BaseURL host is a loopback literal (localhost, 127.0.0.0/8, or ::1), over
// http or https. It is deliberately NARROW: it does NOT accept the
// 169.254.169.254 metadata endpoint, link-local, unspecified, or RFC1918
// hosts, and it applies to NO other provider. Anything it does not accept
// falls through to the unchanged StrictURLPolicy.
func ollamaLoopbackAllowed(c Connection) bool {
	if strings.ToLower(strings.TrimSpace(c.Provider)) != providerOllama {
		return false
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// Loopback ONLY. Explicitly exclude the metadata endpoint and any
	// non-loopback internal class even though IsLoopback already excludes
	// them — the explicit guard documents the security boundary.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return false
	}
	return ip.IsLoopback()
}

// ValidateConnectionURL applies the given URLPolicy to a connection's BaseURL.
// It is the reusable, single-source write-time guard mirroring ValidateConnection
// (the Azure api-version rule): the connection store calls it on create/update
// so the rule is enforced once and covered by tests.
func ValidateConnectionURL(c Connection, policy URLPolicy) error {
	if policy == nil {
		policy = StrictURLPolicy
	}
	// Narrow, provider-aware carve-out: local Ollama (loopback host) may use
	// http + loopback, which StrictURLPolicy would otherwise reject. Scoped to
	// provider=="ollama" AND loopback ONLY; everything else is unchanged.
	if ollamaLoopbackAllowed(c) {
		return nil
	}
	if err := policy.ValidateURL(c.BaseURL); err != nil {
		return fmt.Errorf("connection %q: %w", c.Name, err)
	}
	return nil
}
