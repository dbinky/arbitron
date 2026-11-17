package provider

import (
	"errors"
	"net/url"
	"strings"
)

// LunaRouteHostedRoot is the only LunaRoute destination this release accepts.
// LR-01 (decided 2026-11-10, docs/designs/2026-11-10-lunaroute-provider-decisions.md):
// verified against docs.lunaroute.com; the evidence register records the
// source note and the live regression check.
const LunaRouteHostedRoot = "https://gw.lunaroute.com"

const lunaRouteHost = "gw.lunaroute.com"

// ErrLunaRouteEndpoint rejects any LunaRoute base URL that does not normalize
// to the hosted root. The message never echoes the submitted URL.
var ErrLunaRouteEndpoint = errors.New("provider: LunaRoute connections must use the hosted endpoint https://gw.lunaroute.com (leave base_url empty or set it to that root)")

// IsLunaRoute reports whether a provider string canonicalizes to lunaroute.
func IsLunaRoute(p string) bool { return Canonicalize(p) == CanonicalLunaRoute }

// NormalizeLunaRouteBaseURL maps every accepted spelling of the hosted root
// (empty, trailing slash, trailing /v1, mixed-case host or scheme) to
// LunaRouteHostedRoot. Any other scheme, host, port, userinfo, query,
// fragment, escaped path or path is ErrLunaRouteEndpoint.
func NormalizeLunaRouteBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return LunaRouteHostedRoot, nil
	}
	if strings.ContainsAny(s, "#?") {
		return "", ErrLunaRouteEndpoint
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", ErrLunaRouteEndpoint
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil ||
		u.RawPath != "" || u.Port() != "" || !strings.EqualFold(u.Host, lunaRouteHost) {
		return "", ErrLunaRouteEndpoint
	}
	switch u.Path {
	case "", "/", "/v1", "/v1/":
		return LunaRouteHostedRoot, nil
	default:
		return "", ErrLunaRouteEndpoint
	}
}

// LunaRouteMessagesBaseURL is the Anthropic Messages base: the hosted root.
// Claude Code appends /v1/messages itself.
func LunaRouteMessagesBaseURL(raw string) (string, error) {
	return NormalizeLunaRouteBaseURL(raw)
}

// LunaRouteOpenAIBaseURL is the OpenAI-dialect base (chat completions,
// Responses and /models): the hosted root plus exactly one /v1.
func LunaRouteOpenAIBaseURL(raw string) (string, error) {
	root, err := NormalizeLunaRouteBaseURL(raw)
	if err != nil {
		return "", err
	}
	return root + "/v1", nil
}
