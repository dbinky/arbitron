package notification

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PR #18 Consider item 14 (comment 4614961905 / SSRF): the Teams webhook URL is
// operator-configured but the dial-time guard closes the DNS-rebinding window —
// a hostname that passes a write-time check can later resolve to an internal IP.
// blockedDialAddress runs on the RESOLVED ip:port just before connect.
func TestBlockedDialAddress(t *testing.T) {
	blocked := []struct {
		name string
		addr string
	}{
		{"loopback v4", "127.0.0.1:443"},
		{"loopback v6", "[::1]:443"},
		{"unspecified", "0.0.0.0:443"},
		{"link-local", "169.254.0.1:443"},
		{"cloud metadata", "169.254.169.254:443"},
		{"rfc1918 10/8", "10.1.2.3:443"},
		{"rfc1918 192.168/16", "192.168.1.1:443"},
		{"rfc1918 172.16/12", "172.16.5.5:443"},
		{"unique-local v6", "[fd00::1]:443"},
	}
	for _, tc := range blocked {
		t.Run("blocks "+tc.name, func(t *testing.T) {
			err := blockedDialAddress(tc.addr)
			require.Error(t, err, "internal address %q must be blocked at dial time", tc.addr)
			assert.ErrorIs(t, err, ErrBlockedDialTarget)
		})
	}

	allowed := []string{
		"93.184.216.34:443", // example.com public IP
		"[2606:2800:220:1:248:1893:25c8:1946]:443",
	}
	for _, addr := range allowed {
		t.Run("allows public "+addr, func(t *testing.T) {
			assert.NoError(t, blockedDialAddress(addr))
		})
	}

	t.Run("a literal hostname (unresolved) is rejected defensively", func(t *testing.T) {
		// The Control hook receives a resolved ip:port; a non-IP host means the
		// resolver produced something unexpected — fail closed.
		err := blockedDialAddress("evil.example.com:443")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBlockedDialTarget)
	})
}
