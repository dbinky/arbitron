package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The embedded output bound must reach request preflight and the persisted
// failure sink, not merely the discovery projection. Empty root input would
// send this request and complete instead of rejecting it.
func TestLunaRouteEmbeddedOutputLimitWiring(t *testing.T) {
	for _, tc := range []struct {
		model  string
		output int
	}{
		{"glm-5.3", 131072}, {"glm-5.3-flash", 163840}, {"deepseek-4.1-flash", 262144},
	} {
		t.Run(tc.model, func(t *testing.T) {
			gw := newFakeLunaRouteGateway(t, "lr_embedded_limit", tc.model)
			gw.ExpectChat(tc.model, lrRootPrompt, tc.output+1, "")
			ds := bootLunaRouteRoot(t, "", gw, func(ws string) {
				writeLunaRouteChatWorkflow(t, ws, "lr-limit", "lr-limit", tc.output+1, false)
				writeLunaRouteChatWorkflow(t, ws, "lr-at-limit", "lr-limit", tc.output, false)
			})
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_embedded_limit"))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-limit", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: tc.model})
			require.NoError(t, err)
			id, inst := triggerLunaRouteChat(t, ds, "lr-limit")
			require.Equal(t, instance.StateFailed, inst.State)
			assert.Contains(t, nodeError(t, ds, id), fmt.Sprintf("max tokens %d exceeds the model's known output limit %d", tc.output+1, tc.output))
			assert.Empty(t, gw.Requests(), "known source bound refuses before credential-bearing HTTP")

			gw.ExpectChat(tc.model, lrRootPrompt, tc.output, "")
			id, inst = triggerLunaRouteChat(t, ds, "lr-at-limit")
			require.Equal(t, instance.StateCompleted, inst.State, "the exact bound remains usable")
			assert.Equal(t, map[string]any{"response": "pong"}, successfulNodeOutput(t, ds, id, "ask"))
			assert.Equal(t, map[string]string{"lr-limit": tc.model}, inst.ModelPins)
			require.Len(t, gw.Requests(), 1)
			assert.Equal(t, "Bearer lr_embedded_limit", gw.Requests()[0].Authorization)
		})
	}
}

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
