package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This fixture validates the root's emitted inputs and performs real workspace
// commands. It is not an installed-binary contract or gateway-traffic proof.
func acceptanceOpencodeFixture(t *testing.T, prompt, nonce, mode string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" = --version ]; then echo 'opencode 1.19.5'; exit 0; fi
[ "$LUNAROUTE_API_KEY" = %s ] || exit 40
case "$PWD" in */instances/*) ;; *) exit 41 ;; esac
case "$*" in *"$LUNAROUTE_API_KEY"*) exit 42 ;; esac
for field in '"baseURL":"https://gw.lunaroute.com/v1"' '"apiKey":"{env:LUNAROUTE_API_KEY}"' '"npm":"@ai-sdk/openai-compatible"' '"small_model":"lunaroute/glm-5.3"' '"model":"lunaroute/glm-5.3"' '"enabled_providers":["lunaroute"]' '"limit":{"context":202752,"output":32768}' '"tool_call":true' '"permission":{"*":"deny","bash":"allow","read":"allow","write":"allow"}'; do
 case "$OPENCODE_CONFIG_CONTENT" in *"$field"*) ;; *) exit 43 ;; esac
done
case "$OPENCODE_CONFIG_CONTENT" in *"$LUNAROUTE_API_KEY"*) exit 44 ;; esac
if [ "$1" = export ]; then
 [ "$*" = 'export ses_acceptance --pure' ] || exit 45
 echo '{"info":{"id":"ses_acceptance"},"messages":[{"info":{"id":"msg_answer","sessionID":"ses_acceptance","role":"assistant","providerID":"lunaroute","modelID":"glm-5.3","time":{"created":1,"completed":2}},"parts":[]}]}'
 exit 0
fi
[ "$1" = run ] && [ "$2" = %s ] || exit 46
shift 2
[ "$*" = '-m lunaroute/glm-5.3 --dangerously-skip-permissions --format json --pure' ] || exit 47
[ ! -e lunaroute-ok.txt ] && [ ! -e listing.txt ] || exit 48
if [ %s != missing-file ]; then
 printf '%%s' %s > lunaroute-ok.txt
 wc -c < lunaroute-ok.txt > listing.txt
fi
if [ %s = wrong-command ]; then printf '0\n' > listing.txt; fi
if [ %s != no-tool ]; then
 echo '{"type":"tool_use","sessionID":"ses_acceptance","part":{"sessionID":"ses_acceptance","messageID":"msg_answer","type":"tool","tool":"bash","state":{"status":"completed"}}}'
fi
echo '{"type":"text","sessionID":"ses_acceptance","part":{"id":"prt_text","messageID":"msg_answer","sessionID":"ses_acceptance","type":"text","text":"done"}}'
`, strconv.Quote(acceptanceFixtureKey), strconv.Quote(prompt), strconv.Quote(mode), strconv.Quote(nonce), strconv.Quote(mode), strconv.Quote(mode))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLunaRouteAcceptanceEvidence_TemplateWorkflowChatSink(t *testing.T) {
	const prompt = "Reply with exactly PONG for this acceptance template."
	gw := newFakeLunaRouteGateway(t, acceptanceFixtureKey, "glm-5.3")
	gw.ExpectChat("glm-5.3", prompt, 1024, "")
	gw.Reply("POST /v1/chat/completions", lunaRouteGatewayReply{Status: 200, Body: `{"model":"glm-5.3","choices":[{"message":{"content":"PONG"},"finish_reason":"stop"}]}`})
	ds := bootLunaRouteRoot(t, "", gw, func(ws string) {
		writeTemplateWorkflow(t, ws, "acceptance-template", "llm_call", "acceptance-template", prompt)
	})
	call := lunaRouteToolCaller(t, ds)
	bad, _ := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": acceptanceFixtureKey})
	require.False(t, bad, "connection_create failed")
	bad, _ = call("profile_create", map[string]any{"alias": "acceptance-template", "kind": "chat", "connection_ref": "lr", "model": "glm-5.3"})
	require.False(t, bad, "profile_create failed")
	id := triggerViaMCP(t, call, "acceptance-template")
	inst := waitLunaRouteTerminal(t, ds, id)
	assert.Equal(t, map[string]string{"acceptance-template": "glm-5.3"}, inst.ModelPins)
	node, err := ds.nodeExecRepo.GetByInstanceAndNode(adminCtxForLocalUser(t, ds), id, "work")
	require.NoError(t, err)
	require.NotNil(t, node)
	proof, err := collectAcceptanceNode(acceptanceNodeInput{RunID: id, Model: "glm-5.3", ExpectedOutput: "PONG", Node: *node}, acceptanceFixtureKey)
	require.NoError(t, err)
	assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Source: "chat_response"}}, proof.Models)
	reqs := gw.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "https://gw.lunaroute.com/v1/chat/completions", reqs[0].URL)
	assert.Equal(t, "Bearer "+acceptanceFixtureKey, reqs[0].Authorization)
	require.NoError(t, checkAcceptanceLog(ds.cfg.Logging.File, acceptanceFixtureKey))
}

func TestLunaRouteAcceptanceEvidence_RealMCPAgentWorkspaceSink(t *testing.T) {
	for _, mode := range []string{"success", "missing-file", "wrong-command", "no-tool"} {
		t.Run(mode, func(t *testing.T) {
			nonce := "acceptance-" + uuid.NewString()
			prompt := fmt.Sprintf("Create lunaroute-ok.txt containing exactly %s, run wc -c < lunaroute-ok.txt > listing.txt, then reply done.", nonce)
			acceptanceOpencodeFixture(t, prompt, nonce, mode)
			gw := newFakeLunaRouteGateway(t, acceptanceFixtureKey, "glm-5.3")
			ds := bootLunaRouteRoot(t, "capabilitiespath: "+writeLunaRouteCaps(t)+"\n", gw, func(ws string) {
				writeTemplateWorkflow(t, ws, "acceptance-agent", "agent_call", "acceptance-agent", prompt)
			})
			call := lunaRouteToolCaller(t, ds)
			bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": acceptanceFixtureKey})
			require.False(t, bad, "connection_create failed")
			assert.NotContains(t, text, acceptanceFixtureKey)
			bad, text = call("connection_models", map[string]any{"name": "lr"})
			require.False(t, bad, "connection_models failed")
			var discovery provider.DiscoveryView
			require.NoError(t, json.Unmarshal([]byte(text), &discovery))
			require.Len(t, discovery.Models, 1)
			assert.Equal(t, "glm-5.3", discovery.Models[0].ID)
			bad, _ = call("profile_create", map[string]any{"alias": "acceptance-agent", "kind": "agent", "connection_ref": "lr", "model": "glm-5.3", "backend": "opencode", "allowed_tools": []string{"Read", "Write", "Bash"}})
			require.False(t, bad, "profile_create failed")
			id := triggerViaMCP(t, call, "acceptance-agent")
			inst := waitLunaRouteTerminal(t, ds, id)
			assert.Equal(t, map[string]string{"acceptance-agent": "glm-5.3"}, inst.ModelPins)
			ws, ok := inst.Variables["workspace"].(string)
			require.True(t, ok)
			require.NotEmpty(t, ws, "workspace must come from the real instance producer")
			node, err := ds.nodeExecRepo.GetByInstanceAndNode(adminCtxForLocalUser(t, ds), id, "work")
			require.NoError(t, err)
			require.NotNil(t, node)
			in := acceptanceNodeInput{RunID: id, Backend: "opencode", Model: "glm-5.3", ExpectedOutput: "done", Nonce: nonce, Workspace: ws, Node: *node}
			proof, err := collectAcceptanceNode(in, acceptanceFixtureKey)
			if mode != "success" {
				require.Error(t, err, "a completed node/pins alone must not satisfy agent acceptance")
				assert.Empty(t, proof.Models)
			} else {
				require.NoError(t, err)
				assert.True(t, proof.FileOK && proof.CommandOK)
				assert.Equal(t, id, proof.RunID)
				assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Provider: "lunaroute", Source: "opencode_export"}}, proof.Models)
				for _, drop := range []string{"workspace", "trace", "identity"} {
					broken := in
					switch drop {
					case "workspace":
						broken.Workspace = ""
					case "trace":
						broken.Node.LLMConversation = nil
					case "identity":
						broken.Node.LLMModel = nil
					}
					_, err := collectAcceptanceNode(broken, acceptanceFixtureKey)
					require.Error(t, err, "missing production feed: "+drop)
				}
			}
			require.NoError(t, checkAcceptanceLog(ds.cfg.Logging.File, acceptanceFixtureKey))
			reqs := gw.Requests()
			require.Len(t, reqs, 1, "successful fixture execution needs no diagnosis or fallback")
			assert.Equal(t, "https://gw.lunaroute.com/v1/models", reqs[0].URL)
			assert.Equal(t, "Bearer "+acceptanceFixtureKey, reqs[0].Authorization)
			assert.NotContains(t, fmt.Sprint(node.LLMConversation), acceptanceFixtureKey)
		})
	}
}
