package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var ErrBlockedDialTarget = errors.New("outbound: blocked dial to internal address")
var ErrUnsafeURL = errors.New("outbound: unsafe URL")

// Non-public ranges not covered by IsPrivate or IsGlobalUnicast.
var reservedRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	// IPv4 transition ranges can reach destinations hidden from IPv6 address
	// classification. Fail closed regardless of translator/tunnel routing.
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64
	netip.MustParsePrefix("2002::/16"),    // 6to4
}

// CheckDialAddress checks a resolved IP:port, never an unresolved hostname.
// Use at connect time, not only when validating URLs, to prevent DNS rebinding.
func CheckDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrBlockedDialTarget
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return ErrBlockedDialTarget
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ErrBlockedDialTarget
	}
	for _, prefix := range reservedRanges {
		if prefix.Contains(ip) {
			return ErrBlockedDialTarget
		}
	}
	return nil
}

func GuardedDialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return CheckDialAddress(address)
		},
	}
	return d.DialContext
}

func validateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ErrUnsafeURL
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return ErrBlockedDialTarget
	}
	if net.ParseIP(host) != nil || strings.Contains(host, ":") {
		return CheckDialAddress(net.JoinHostPort(host, u.Port()))
	}
	return nil
}

type guardedTransport struct{ base http.RoundTripper }

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateURL(req.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func (t *guardedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// NewGuardedClient validates initial and redirected URLs and checks the actual
// resolved address immediately before each connect. Environment proxies are
// disabled: checking a public proxy's address would not check its destination.
func NewGuardedClient(timeout time.Duration, maxRedirects int) *http.Client {
	return NewGuardedClientWithTransport(timeout, maxRedirects, nil)
}

// NewGuardedClientWithTransport replaces only HTTP I/O for composition-root
// tests. An explicit transport owns dialing; URL and redirect guards still run.
// Production callers pass nil to retain guarded dialing and disable proxies.
func NewGuardedClientWithTransport(timeout time.Duration, maxRedirects int, transport http.RoundTripper) *http.Client {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = GuardedDialContext(timeout)
		transport = t
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &guardedTransport{base: transport},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := validateURL(req.URL); err != nil {
				return err
			}
			if len(via) > maxRedirects {
				return errors.New("outbound: redirect limit exceeded")
			}
			return nil
		},
	}
}

// RequireHTTPS refuses any URL whose scheme is not https. Use it where the
// response's integrity matters (a source that decides what runs), not only its
// destination.
func RequireHTTPS(u *url.URL) error {
	if u == nil || u.Scheme != "https" {
		return fmt.Errorf("%w: https required", ErrUnsafeURL)
	}
	return nil
}

// NewGuardedHTTPSClientWithTransport is NewGuardedClientWithTransport that also
// refuses every redirect off https, so an https source cannot be downgraded to
// plaintext mid-chain. The caller checks the initial URL with RequireHTTPS.
func NewGuardedHTTPSClientWithTransport(timeout time.Duration, maxRedirects int, transport http.RoundTripper) *http.Client {
	c := NewGuardedClientWithTransport(timeout, maxRedirects, transport)
	guard := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := guard(req, via); err != nil {
			return err
		}
		return RequireHTTPS(req.URL)
	}
	return c
}
