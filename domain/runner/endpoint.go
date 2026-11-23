package runner

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
)

var errInvalidEndpoint = errors.New("runner: invalid endpoint")

// EndpointFingerprint identifies a resolved route without exposing credentials.
// It does not admit custom LunaRoute destinations or replace binding validation.
// Callers supply the effective backend, as they do for GroupFor.
func EndpointFingerprint(b provider.ResolvedBinding) (string, error) {
	var route string
	var err error
	switch {
	case provider.IsLunaRoute(b.Provider):
		route, err = provider.NormalizeLunaRouteBaseURL(b.BaseURL)
	case strings.TrimSpace(b.ConnectionRef) == "" && strings.TrimSpace(b.BaseURL) == "":
		route = "local-subscription:" + strings.TrimSpace(b.Backend)
	default:
		route, err = normalizedEndpoint(b.BaseURL)
	}
	if err != nil {
		return "", errInvalidEndpoint
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(provider.Canonicalize(b.Provider)+"\n"+route))), nil
}

func normalizedEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "server-default", nil
	}
	if strings.ContainsAny(raw, "?#") {
		return "", errInvalidEndpoint
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" {
		return "", errInvalidEndpoint
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errInvalidEndpoint
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errInvalidEndpoint
		}
		if (u.Scheme == "https" && n == 443) || (u.Scheme == "http" && n == 80) {
			port = ""
		}
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}
