package claudecli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This tests the real config-loader -> adapter -> os/exec -> workspace sink.
// Full MCP/root enablement and persisted node traces belong to P3-T3.
func TestLunaRouteLoadedFactsReachSubprocessSink(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			dir := t.TempDir()
			// Distinctive per-model facts are loaded through the production loader, not
			// hand-fed into the adapter. They authorize only this offline fixture.
			path := filepath.Join(dir, "capabilities.yaml")
			require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`lunaroute_models:
  %s:
    context_tokens: {tokens: 202752, evidence: fixture#context}
    output_tokens: {tokens: 32768, evidence: fixture#output}
    efforts:
      claude-code: {levels: [xhigh], evidence: fixture#effort}
`, model)), 0600))
			rules, err := configadapter.LoadCapabilityRules(path)
			require.NoError(t, err)
			script := `#!/bin/sh
set -eu
[ "$ANTHROPIC_BASE_URL" = 'https://gw.lunaroute.com' ]
[ "$ANTHROPIC_AUTH_TOKEN" = 'lr_live_claude_Secret123' ]
[ -z "${ANTHROPIC_API_KEY+x}" ]
[ "$ANTHROPIC_MODEL" = '@MODEL@' ]
[ "$ANTHROPIC_DEFAULT_HAIKU_MODEL" = "$ANTHROPIC_MODEL" ]
[ "$ANTHROPIC_SMALL_FAST_MODEL" = "$ANTHROPIC_MODEL" ]
[ "$ANTHROPIC_DEFAULT_SONNET_MODEL" = "$ANTHROPIC_MODEL" ]
[ "$ANTHROPIC_DEFAULT_OPUS_MODEL" = "$ANTHROPIC_MODEL" ]
[ "$CLAUDE_CODE_SUBAGENT_MODEL" = "$ANTHROPIC_MODEL" ]
[ "$CLAUDE_CODE_GATEWAY_HINT_HEADERS" = 1 ]
[ "$CLAUDE_CODE_MAX_CONTEXT_TOKENS" = 202752 ]
[ "$CLAUDE_CODE_MAX_OUTPUT_TOKENS" = 32768 ]
[ "$CLAUDE_CODE_EFFORT_LEVEL" = xhigh ]
[ -z "${MAX_THINKING_TOKENS+x}" ]
[ -z "${CLAUDE_CODE_DISABLE_1M_CONTEXT+x}" ]
[ -z "${CLAUDE_CODE_AUTO_COMPACT_WINDOW+x}" ]
[ "$#" = 11 ]
[ "$1" = --print ]; shift
[ "$1" = --verbose ]; shift
[ "$1" = --output-format ]; shift
[ "$1" = stream-json ]; shift
[ "$1" = --dangerously-skip-permissions ]; shift
[ "$1" = --allowedTools ]; shift
[ "$1" = Read,Write ]; shift
[ "$1" = --model ]; shift
[ "$1" = "$ANTHROPIC_MODEL" ]; shift
[ "$1" = --setting-sources ]; shift
[ "$1" = '' ]
prompt=$(/bin/cat)
[ "$prompt" = 'mission nonce: @MODEL@' ]
printf '%s' "$prompt" > completed.txt
printf '{"type":"result","result":"workspace complete","session_id":"s1","modelUsage":{"%s":{}}}\n' "$ANTHROPIC_MODEL"
`
			// The fixture validates every load-bearing input before publishing success.
			script = strings.ReplaceAll(script, "@MODEL@", model)
			bin := filepath.Join(dir, "claude")
			require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
			calls := 0
			b := lrBinding(model, lrTestKey, nil, &calls)
			b.Effort = "xhigh"
			res, err := NewAgentAdapterForBinding(Config{BinaryPath: bin, CapabilityRules: rules, WorkspaceRoot: dir}, b).RunAgent(context.Background(), engine.AgentMission{Prompt: "mission nonce: " + model, WorkDir: dir, ToolScope: []string{"Read", "Write"}})
			require.NoError(t, err)
			assert.Equal(t, 1, calls)
			raw, err := os.ReadFile(filepath.Join(dir, "completed.txt"))
			require.NoError(t, err)
			assert.Equal(t, "mission nonce: "+model, string(raw))
			assert.Equal(t, "workspace complete", res.Summary)
			encoded, err := json.Marshal(res.ModelEvidence)
			require.NoError(t, err)
			assert.JSONEq(t, fmt.Sprintf(`[{"model":%q,"source":"claude_model_usage"}]`, model), string(encoded))
			assert.NotContains(t, string(encoded), lrTestKey)
		})
	}
}
