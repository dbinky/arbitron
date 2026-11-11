package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise the file named in server config through the real composition root
// to the authorized ProfileService read port, not a separately loaded ruleset.
func TestLunaRouteCapabilityTableWiring(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		context   int
		reasoning provider.Support
		schema    provider.Support
	}{
		{"distinctive facts", `lunaroute_models:
  glm-5.3:
    context_tokens: {tokens: 202752, evidence: "ev#ctx"}
    reasoning: {support: unsupported, evidence: "ev#reasoning"}
    request_settings:
      output_schema: {support: unsupported, evidence: "ev#schema"}
`, 202752, provider.SupportUnsupported, provider.SupportUnsupported},
		{"explicit empty replacement", "lunaroute_models:\n  glm-5.3: {}\n", 0, provider.SupportUnknown, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := filepath.Join(t.TempDir(), "caps.yaml")
			require.NoError(t, os.WriteFile(caps, []byte(tc.body), 0o600))
			ds := bootSinkRoot(t, "capabilitiespath: "+caps+"\n", func(string) {})
			assert.Equal(t, caps, ds.cfg.CapabilitiesPath)
			rules, err := ds.profileSvc.Capabilities(adminCtxForLocalUser(t, ds))
			require.NoError(t, err)
			m, ok := rules.LunaRouteModel("glm-5.3")
			require.True(t, ok, "file-backed table reached ProfileService")
			assert.Equal(t, tc.context, m.ContextTokens.Tokens)
			assert.Equal(t, tc.reasoning, m.Reasoning.Support)
			assert.Equal(t, tc.schema, m.RequestSettings["output_schema"].Support)
			if tc.context > 0 {
				assert.Equal(t, "ev#ctx", m.ContextTokens.Evidence)
				assert.Equal(t, "ev#reasoning", m.Reasoning.Evidence)
				assert.Equal(t, "ev#schema", m.RequestSettings["output_schema"].Evidence)
			}
			shared, ok := ds.capabilityRules.LunaRouteModel("glm-5.3")
			require.True(t, ok)
			assert.Equal(t, m, shared, "root shares the loaded rules with its consumers")
		})
	}
}
