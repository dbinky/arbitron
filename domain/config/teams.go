package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// RedactedSecret is the single placeholder echoed in place of any sensitive
// value (webhook URLs, API keys) on every read surface. Both transports use
// this constant so the redacted marker never drifts.
const RedactedSecret = "**REDACTED**"

// RedactWebhookURL returns the redaction placeholder when raw is non-empty, or
// "" when it is empty. "A webhook URL is sensitive" is domain policy decided in
// one place; the HTTP and MCP read surfaces both call this so they cannot drift.
func RedactWebhookURL(raw string) string {
	if raw == "" {
		return ""
	}
	return RedactedSecret
}

// ValidateTeamsWebhookURL reports whether raw is acceptable as a Teams webhook
// URL, returning a descriptive error otherwise. An empty string is acceptable
// (it clears the webhook).
//
// SECURITY (SSRF): the engine POSTs instance metadata to this URL server-side,
// so an attacker who can set it (admin-gated) could otherwise probe internal
// services. A non-empty value must therefore be an absolute https URL whose
// host is a public destination — loopback, link-local (incl. the cloud
// metadata address 169.254.169.254), unspecified, RFC1918, and ULA targets are
// rejected. This is a parse-time blocklist for IP-literal hosts plus the
// "localhost" name; a DNS-rebinding-safe dial-time guard is deliberately left
// as a follow-up (the engine's outbound client would carry it).
func ValidateTeamsWebhookURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhookUrl must be a valid URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("webhookUrl must be an absolute https URL")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhookUrl must include a host")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("webhookUrl must not target a loopback address")
	}
	if ip := net.ParseIP(host); ip != nil {
		return validatePublicIP(ip)
	}
	return nil
}

// validatePublicIP rejects IP literals that point at non-public ranges.
func validatePublicIP(ip net.IP) error {
	switch {
	case ip.IsLoopback():
		return fmt.Errorf("webhookUrl must not target a loopback address")
	case ip.IsUnspecified():
		return fmt.Errorf("webhookUrl must not target the unspecified address")
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return fmt.Errorf("webhookUrl must not target a link-local address")
	case ip.IsPrivate(): // RFC1918 (IPv4) and ULA fc00::/7 (IPv6)
		return fmt.Errorf("webhookUrl must not target a private address")
	}
	return nil
}
