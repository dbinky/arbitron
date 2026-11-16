package codex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Each clause of safeLunaRouteIdentity is pinned by an input that only that
// clause rejects, so dropping any one clause fails at least one case (G10).
func TestSafeLunaRouteIdentity_EachClause(t *testing.T) {
	for name, tc := range map[string]struct {
		in string
		ok bool
	}{
		"plain id":           {"glm-5.3", true},
		"max length":         {strings.Repeat("x", 256), true},
		"empty":              {"", false},
		"over 256 bytes":     {strings.Repeat("x", 257), false},
		"double quote":       {"glm-5.3\"", false},
		"backslash":          {"glm\\5.3", false},
		"ascii space":        {"glm 5.3", false},
		"unicode space":      {"glm-5.3 ", false},
		"control character":  {"glm-5.3\x07", false},
		"newline":            {"glm-5.3\n", false},
		"lr key material":    {"glm-5.3-" + lrKey, false},
		"lr prefix no space": {"lr_private", false},
	} {
		t.Run(name, func(t *testing.T) { assert.Equal(t, tc.ok, safeLunaRouteIdentity(tc.in)) })
	}
}
