package pi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test the safety predicate directly: exact-model matching would reject these
// malformed identities even if the safety checks disappeared. Length and
// control checks also overlap RedactLunaRoute's truncation and scrubbing.
func TestSafeModelIdentity_EachClause(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"plain model", "glm-5.3", true},
		{"plain provider", "lunaroute", true},
		{"256 ascii bytes", strings.Repeat("x", 256), true},
		{"256 utf8 bytes", strings.Repeat("é", 128), true},
		{"empty", "", false},
		{"257 ascii bytes", strings.Repeat("x", 257), false},
		{"257 utf8 bytes", strings.Repeat("é", 128) + "x", false},
		{"258 utf8 bytes", strings.Repeat("é", 129), false},
		{"embedded ascii space", "glm 5.3", false},
		{"embedded unicode space", "glm\u00a05.3", false},
		{"leading space", " glm-5.3", false},
		{"trailing space", "glm-5.3 ", false},
		{"tab", "glm\t5.3", false},
		{"newline", "glm-5.3\n", false},
		{"nul", "glm\x005.3", false},
		{"ascii control", "glm\x075.3", false},
		{"del", "glm\x7f5.3", false},
		{"unicode control", "glm\u009f5.3", false},
		{"lr key material", "glm-lr_private", false},
		{"lr prefix", "lr_private", false},
		{"auth header", "x-api-key=private", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, safeModelIdentity(tc.in))
		})
	}
}
