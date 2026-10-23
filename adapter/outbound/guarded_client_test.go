package outbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuardDialAddress(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:80", "[::1]:80", "0.0.0.0:80", "[::]:80",
		"10.0.0.1:80", "172.16.0.1:80", "192.168.1.1:80", "[fd00::1]:80",
		"169.254.169.254:80", "[fe80::1]:80", "224.0.0.1:80",
		"[ff02::1]:80", "100.64.0.1:80", "[::ffff:127.0.0.1]:80",
		"rebound.example:80", "not-an-address",
	} {
		t.Run(address, func(t *testing.T) {
			assert.ErrorIs(t, CheckDialAddress(address), ErrBlockedDialTarget)
		})
	}
	for _, address := range []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443"} {
		assert.NoError(t, CheckDialAddress(address))
	}
}

// Transition mechanisms can route an apparently global IPv6 address to an
// embedded IPv4 destination. Reject the whole range, including public embeds,
// rather than depending on how the deployment's translator or tunnel routes it.
func TestGuardRejectsIPv4TransitionTargets(t *testing.T) {
	for _, host := range []string{
		"64:ff9b::7f00:1", "64:ff9b::a00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::5db8:d822",
		"2002:7f00:1::", "2002:a00:1::", "2002:a9fe:a9fe::", "2002:5db8:d822::",
	} {
		t.Run(host, func(t *testing.T) {
			assert.ErrorIs(t, CheckDialAddress(net.JoinHostPort(host, "80")), ErrBlockedDialTarget)
			client := NewGuardedClient(time.Second, 3)
			dials := 0
			client.Transport.(*guardedTransport).base.(*http.Transport).DialContext = func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, errors.New("unexpected fixture dial")
			}
			raw := "http://" + net.JoinHostPort(host, "80") + "/catalog"
			_, err := client.Get(raw)
			assert.ErrorIs(t, err, ErrBlockedDialTarget)
			assert.Zero(t, dials, "transition target must be rejected before connect")
		})
	}
}

func TestGuardClientRejectsInternalDial(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewGuardedClient(time.Second, 3)
	// Known local hostnames are rejected before the HTTP transport runs.
	address := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	_, err := client.Get(address)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedDialTarget)
	assert.Zero(t, calls, "blocked dial must never reach the HTTP sink")
	transport := client.Transport.(*guardedTransport).base.(*http.Transport)
	assert.Nil(t, transport.Proxy, "environment proxies must not bypass target checks")
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	for _, address := range []string{server.Listener.Addr().String(), net.JoinHostPort("localhost", port)} {
		conn, err := transport.DialContext(context.Background(), "tcp", address)
		if conn != nil {
			_ = conn.Close()
		}
		assert.ErrorIs(t, err, ErrBlockedDialTarget, "resolved dial target must be checked after DNS resolution")
	}
}

func TestGuardURLAndRedirects(t *testing.T) {
	client := NewGuardedClient(time.Second, 3)
	for _, raw := range []string{
		"file:///etc/passwd", "ftp://example.com/models", "https://user:secret@example.com/models",
		"http://127.0.0.1/models", "http://[::1]/models", "http://10.1.2.3/models",
		"http://localhost/models", "http://localhost./models", "https:///models",
		"http://catalog.local/models", "http://catalog.localhost/models",
	} {
		t.Run(raw, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, raw, nil)
			require.NoError(t, err)
			assert.Error(t, client.CheckRedirect(req, []*http.Request{{}}))
		})
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/catalog", nil)
	require.NoError(t, err)
	assert.NoError(t, client.CheckRedirect(req, make([]*http.Request, 3)))
	assert.Error(t, client.CheckRedirect(req, make([]*http.Request, 4)))
	assert.Equal(t, time.Second, client.Timeout)
}

func TestGuardRejectsCredentialsBeforeTransport(t *testing.T) {
	client := NewGuardedClient(time.Second, 3)
	_, err := client.Get("http://user:secret@127.0.0.1/catalog")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafeURL)
}

func TestGuardRedirectSink(t *testing.T) {
	for _, target := range []string{"private", "nat64", "6to4", "credentials", "scheme", "limit"} {
		t.Run(target, func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch target {
				case "private":
					http.Redirect(w, r, "http://127.0.0.1/forbidden", http.StatusFound)
				case "nat64":
					http.Redirect(w, r, "http://[64:ff9b::7f00:1]/forbidden", http.StatusFound)
				case "6to4":
					http.Redirect(w, r, "http://[2002:7f00:1::]/forbidden", http.StatusFound)
				case "credentials":
					http.Redirect(w, r, "http://user:secret@catalog.example/forbidden", http.StatusFound)
				case "scheme":
					http.Redirect(w, r, "file:///forbidden", http.StatusFound)
				case "limit":
					step, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
					if !assert.NoError(t, err) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					http.Redirect(w, r, "http://catalog.example/"+strconv.Itoa(step+1), http.StatusFound)
				}
			}))
			defer server.Close()
			client := NewGuardedClient(time.Second, 3)
			// Substitute only connect I/O for one exact public host. All URL and
			// redirect handling is production code; a foreign target is an error.
			client.Transport.(*guardedTransport).base.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "catalog.example:80" {
					t.Errorf("unexpected dial target: %s", address)
					return nil, errors.New("unexpected fixture dial target")
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			_, err := client.Get("http://catalog.example/0")
			require.Error(t, err)
			if target == "limit" {
				assert.Equal(t, []string{"/0", "/1", "/2", "/3"}, paths)
			} else {
				assert.Equal(t, []string{"/0"}, paths, "unsafe redirect must not reach the sink")
			}
		})
	}
}

func TestGuardDialCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := GuardedDialContext(time.Second)(ctx, "tcp", "93.184.216.34:443")
	assert.True(t, errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed), "%v", err)
}
