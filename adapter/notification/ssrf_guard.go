package notification

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// ErrBlockedDialTarget is returned by the dial-time SSRF guard when an outbound
// connection would target an internal/loopback/link-local/private address.
var ErrBlockedDialTarget = errors.New("notification: blocked dial to internal address")

// ssrfGuardedDialer returns a net.Dialer whose Control hook rejects connections
// to internal addresses.
//
// SECURITY (PR #18 Consider item 14, comment 4614961905): the Teams webhook URL
// is operator-configured, but a dial-time guard is strictly stronger than a
// write-time URL check — it closes the DNS-rebinding TOCTOU window. The Control
// callback runs AFTER DNS resolution on the actual resolved address, right
// before connect, so a hostname that resolved to a public IP at validation time
// cannot later rebind to 169.254.169.254 / 127.0.0.1 / an RFC1918 host to
// exfiltrate via the metadata endpoint or reach an internal service.
func ssrfGuardedDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return blockedDialAddress(address)
		},
	}
}

// blockedDialAddress returns a non-nil error (wrapping ErrBlockedDialTarget)
// when address (a resolved "ip:port") names an internal/loopback/link-local/
// private/metadata target. It fails closed: a non-IP host (which should never
// reach the Control hook, since it runs on resolved addresses) is also blocked.
func blockedDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable dial address %q", ErrBlockedDialTarget, address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// The Control hook receives a resolved IP literal; a hostname here means
		// something unexpected — fail closed rather than allow.
		return fmt.Errorf("%w: dial host %q is not a resolved IP", ErrBlockedDialTarget, host)
	}
	// 169.254.169.254 is the canonical cloud instance-metadata SSRF target. It
	// is link-local, but call it out explicitly.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return fmt.Errorf("%w: %s is the cloud metadata endpoint", ErrBlockedDialTarget, ip)
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsPrivate() {
		return fmt.Errorf("%w: %s is an internal address", ErrBlockedDialTarget, ip)
	}
	return nil
}

// newSSRFGuardedTransport builds an http.RoundTripper-ready dial function that
// applies the SSRF guard. Exposed via the default client constructed in
// NewTeamsNotifier when the caller does not inject its own client.
func ssrfGuardedDialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := ssrfGuardedDialer(timeout)
	return d.DialContext
}
