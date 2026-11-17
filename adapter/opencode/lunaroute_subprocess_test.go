package opencode

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

// The production loader feeds the real os/exec launch and its workspace sink.
// MCP enablement and persisted node evidence are owned by P3-T3.
func TestLunaRouteLoadedFactsReachOpenCodeSubprocessSink(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "capabilities.yaml")
			require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`lunaroute_models:
  %s:
    context_tokens: {tokens: 202752, evidence: fixture#context}
    output_tokens: {tokens: 32768, evidence: fixture#output}
`, model)), 0600))
			rules, err := configadapter.LoadCapabilityRules(path)
			require.NoError(t, err)
			script := `#!/bin/sh
set -eu
if [ "$1" = --version ]; then
  [ "$#" = 1 ]
  printf 'opencode 1.19.5\n'
  exit
fi
[ "$LUNAROUTE_API_KEY" = 'lr_live_opencode_Secret987' ]
[ -z "${OPENAI_API_KEY+x}" ]
[ -z "${ANTHROPIC_API_KEY+x}" ]
[ "$PWD" -ef '@DIR@' ]
[ "$OPENCODE_CONFIG_CONTENT" = '{"$schema":"https://opencode.ai/config.json","provider":{"lunaroute":{"npm":"@ai-sdk/openai-compatible","options":{"apiKey":"{env:LUNAROUTE_API_KEY}","baseURL":"https://gw.lunaroute.com/v1"},"models":{"@MODEL@":{"limit":{"context":202752,"output":32768},"name":"@MODEL@","tool_call":true}}}},"permission":{"*":"deny","read":"allow","write":"allow"},"model":"lunaroute/@MODEL@","small_model":"lunaroute/@MODEL@","enabled_providers":["lunaroute"]}' ]
if [ "$1" = export ]; then
  [ "$#" = 3 ]; [ "$2" = ses_run ]; [ "$3" = --pure ]
  [ -f completed.txt ]
  printf '{"info":{"id":"ses_run"},"messages":[{"info":{"id":"msg_answer","sessionID":"ses_run","role":"assistant","providerID":"lunaroute","modelID":"@MODEL@","time":{"completed":2}}}]}\n'
  exit
fi
[ "$#" = 8 ]
[ "$1" = run ]; [ "$2" = 'mission nonce: @MODEL@' ]
[ "$3" = -m ]; [ "$4" = 'lunaroute/@MODEL@' ]
[ "$5" = --dangerously-skip-permissions ]; [ "$6" = --format ]; [ "$7" = json ]; [ "$8" = --pure ]
printf '%s' "$2" > completed.txt
printf '{"type":"text","sessionID":"ses_run","part":{"sessionID":"ses_run","messageID":"msg_answer","type":"text","text":"workspace complete"}}\n'
`
			script = strings.ReplaceAll(strings.ReplaceAll(script, "@MODEL@", model), "@DIR@", dir)
			bin := filepath.Join(dir, "opencode")
			require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
			calls := 0
			b := lrOCBinding("https://gw.lunaroute.com", model, lrKey, &calls)
			b.AllowedTools = []string{"Read", "Write"}
			res, err := New(Config{Binding: b, CapabilityRules: rules, BinaryPath: bin}).RunAgent(context.Background(), engine.AgentMission{Prompt: "mission nonce: " + model, WorkDir: dir})
			require.NoError(t, err)
			assert.Equal(t, 1, calls)
			raw, err := os.ReadFile(filepath.Join(dir, "completed.txt"))
			require.NoError(t, err)
			assert.Equal(t, "mission nonce: "+model, string(raw))
			assert.Equal(t, "workspace complete", res.Summary)
			assert.Equal(t, model, res.Model)
			raw, err = json.Marshal(res.ModelEvidence)
			require.NoError(t, err)
			assert.JSONEq(t, fmt.Sprintf(`[{"model":%q,"provider":"lunaroute","source":"opencode_export","kind":"routing_intent"}]`, model), string(raw))
			assert.NotContains(t, string(raw), lrKey)
		})
	}
}
