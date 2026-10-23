package modelidentity_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityPolicyRejectsMalformedNumericCaptureGrammar(t *testing.T) {
	for _, capture := range []string{
		`()`, `(\d*)`, `(\d?)`, `(\d{0,3})`, `(\.+)`, `([0-9.]+)`,
		`(\.\d+)`, `(\d+\.)`, `(\d+\.\.\d+)`, `(\d+|\.)`,
		`((?:\d+\.)*\d*)`, `(\d+(?:\.\d*)?)`,
	} {
		t.Run(capture, func(t *testing.T) {
			rules := []modelidentity.PatternRule{{Providers: []string{"openai"}, Tier: "sol", Pattern: `gpt-` + capture + `-sol`, VersionGroups: []int{1}}}
			assert.ErrorIs(t, modelidentity.ValidateRules(rules), modelidentity.ErrInvalid,
				"every participating numeric capture must be a nonempty dotted numeric tuple")
			_, err := modelidentity.CompileRules(rules)
			assert.ErrorIs(t, err, modelidentity.ErrInvalid)
		})
	}
}

func TestIdentityPolicyAcceptsNumericCaptureGrammar(t *testing.T) {
	for _, capture := range []string{
		`(\d+)`, `([0-9]{1,3})`, `(5|6\.1)`, `(\d+(?:\.\d+)?)`,
		`(\d+(?:\.\d+)*)`, `((?:1|[2-9])(?:\.[0-9]+){0,3})`,
		`(\d+)(?:-(\d+))?`,
	} {
		t.Run(capture, func(t *testing.T) {
			rule := modelidentity.PatternRule{Providers: []string{"openai"}, Tier: "sol", Pattern: `gpt-` + capture + `-sol`, VersionGroups: []int{1}}
			if capture == `(\d+)(?:-(\d+))?` {
				rule.VersionGroups = []int{1, 2}
			}
			matcher, err := modelidentity.CompileRules([]modelidentity.PatternRule{rule})
			require.NoError(t, err)
			hits := matcher.Match("openai", "gpt-5-sol")
			require.Len(t, hits, 1)
			assert.Equal(t, "5", hits[0].Captures[0])
		})
	}
}
