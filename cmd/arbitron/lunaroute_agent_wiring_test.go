package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrRootKey = "lr_live_root_key"

// Only the selected credential and the canonical listing route are accepted.
// The log records auth equality, never the credential itself.
type agentListingGateway struct {
	mu       sync.Mutex
	models   []string
	requests []string
}

func (g *agentListingGateway) RoundTrip(r *http.Request) (*http.Response, error) {
	auth := "bad"
	if r.Header.Get("Authorization") == "Bearer "+lrRootKey && r.Header.Get("x-api-key") == "" && r.Header.Get("LUNAROUTE-API-KEY") == "" {
		auth = "ok"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r.Method+" "+r.URL.String()+" auth="+auth)
	reply := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	if r.Method != http.MethodGet || r.URL.String() != provider.LunaRouteHostedRoot+"/v1/models" {
		return reply(404, `{}`)
	}
	if auth != "ok" {
		return reply(401, `{"code":"INVALID_API_KEY"}`)
	}
	data := make([]map[string]string, 0, len(g.models))
	for _, m := range g.models {
		data = append(data, map[string]string{"id": m})
	}
	raw, err := json.Marshal(map[string]any{"object": "list", "data": data})
	if err != nil {
		return nil, err
	}
	return reply(200, string(raw))
}
func (g *agentListingGateway) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.requests...)
}
func writeLunaRouteCaps(t *testing.T) string {
	t.Helper()
	return lunaRouteCapsFile(t, `lunaroute_models:
  glm-5.3:
    context_tokens: {tokens: 202752, evidence: "test-fixture#glm-5.3"}
    output_tokens: {tokens: 32768, evidence: "test-fixture#glm-5.3"}
    reasoning: {support: unsupported, evidence: "test-fixture#reasoning"}
  deepseek-4.1-flash: {} # known executable, deliberately unknown fixture limits
`)
}
func TestLunaRouteAgentProfileSaveWiring(t *testing.T) {
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+writeLunaRouteCaps(t)+"\n", gw, func(string) {})
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "", "api_key": lrRootKey})
	require.False(t, bad, text)
	save := func(alias, model, backend string, extra map[string]any) (bool, string) {
		args := map[string]any{"alias": alias, "kind": "agent", "connection_ref": "lr", "model": model, "backend": backend}
		for k, v := range extra {
			args[k] = v
		}
		return call("profile_create", args)
	}
	for _, b := range []string{"opencode", "claude-code", "pi"} {
		bad, text = save("lr-"+b, "glm-5.3", b, nil)
		require.False(t, bad, text)
		var model, backend, connection string
		require.NoError(t, ds.db.QueryRow(`SELECT model, backend, connection_ref FROM profiles WHERE alias=?`, "lr-"+b).Scan(&model, &backend, &connection))
		assert.Equal(t, "glm-5.3", model)
		assert.Equal(t, b, backend)
		assert.Equal(t, "lr", connection)
	}
	for _, tc := range []struct{ alias, model, backend, setting, value, want string }{
		{"unknown", "deepseek-4.1-flash", "claude-code", "", "", "claude-code on LunaRoute needs known context and output limits"},
		{"tier", "glm-5.3", "claude-code", "context_window", "200k", "Claude-native switch"},
		{"effort", "glm-5.3", "opencode", "effort", "low", "no exact emission on opencode"},
		{"pi-unknown", "deepseek-4.1-flash", "pi", "", "", "pi on LunaRoute needs known context and output limits"},
		{"codex", "glm-5.3", "codex", "effort", "none", "no exact emission on codex"},
		{"numeric", "glm-999.9", "opencode", "", "", "reviewed fixed LunaRoute id"},
		{"revoked", "claude-sonnet-5-lr1", "claude-code", "", "", "reviewed fixed LunaRoute id"},
	} {
		extra := map[string]any{}
		if tc.setting != "" {
			extra[tc.setting] = tc.value
		}
		bad, text = save("lr-"+tc.alias, tc.model, tc.backend, extra)
		assert.True(t, bad, tc.alias)
		assert.Contains(t, text, tc.want)
		assert.NotContains(t, text, lrRootKey)
		var n int
		require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias=?`, "lr-"+tc.alias).Scan(&n))
		assert.Zero(t, n)
	}
	assert.Empty(t, gw.seen(), "save performs no gateway I/O")
}

// These scripts are sinks, not replacement factories. They reject wrong
// prompt, workspace, tool policy and route before returning a terminal answer.
func fakeLunaRouteClaude(t *testing.T, dir string) (bin, argvLog, failFlag string) {
	t.Helper()
	bin = filepath.Join(dir, "fake-claude.sh")
	argvLog = filepath.Join(dir, "argv.log")
	failFlag = filepath.Join(dir, "FAIL")
	script := strings.NewReplacer("@ARGV@", argvLog, "@FAIL@", failFlag, "@KEY@", lrRootKey).Replace(`#!/bin/sh
set -eu
input=$(cat)
[ "$input" = hello ] || exit 54
case "$PWD" in */instances/*) ;; *) exit 55 ;; esac
printf '%s\n' "$PWD" >> '@ARGV@.cwd'
printf '%s\n' "$*" >> '@ARGV@'
check() { eval "v=\${$1-__unset__}"; [ "$v" = "$2" ] || { echo "env mismatch $1" >&2; exit 41; }; }
check ANTHROPIC_BASE_URL https://gw.lunaroute.com
check ANTHROPIC_AUTH_TOKEN '@KEY@'
check ANTHROPIC_API_KEY __unset__
prev=; iso=0; for a in "$@"; do if [ "$prev" = --setting-sources ] && [ -z "$a" ]; then iso=1; fi; prev=$a; done
[ "$iso" = 1 ] || exit 53
for v in ANTHROPIC_MODEL ANTHROPIC_DEFAULT_HAIKU_MODEL ANTHROPIC_SMALL_FAST_MODEL ANTHROPIC_DEFAULT_SONNET_MODEL ANTHROPIC_DEFAULT_OPUS_MODEL CLAUDE_CODE_SUBAGENT_MODEL; do check "$v" glm-5.3; done
check CLAUDE_CODE_GATEWAY_HINT_HEADERS 1
check CLAUDE_CODE_MAX_CONTEXT_TOKENS 202752
check CLAUDE_CODE_MAX_OUTPUT_TOKENS 32768
check CLAUDE_CODE_AUTO_COMPACT_WINDOW __unset__
check CLAUDE_CODE_DISABLE_1M_CONTEXT __unset__
case " $* " in *' --model glm-5.3 '*) ;; *) exit 49 ;; esac
case " $* " in *' --dangerously-skip-permissions '*) ;; *) exit 56 ;; esac
case " $* " in *' --allowedTools Read,Write,Bash '*) ;; *) exit 57 ;; esac
case "$*" in *'@KEY@'*) exit 50 ;; esac
if [ -f '@FAIL@' ]; then printf checked-route > failure-reached; echo 'upstream rejected key @KEY@' >&2; exit 1; fi
echo '{"type":"result","is_error":false,"result":"lunaroute ok","session_id":"s","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"glm-5.3":{}}}'
`)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return
}
func fakeLunaRouteOpencode(t *testing.T, dir string) (argvLog, cfgLog, failFlag string) {
	t.Helper()
	argvLog = filepath.Join(dir, "argv.log")
	cfgLog = filepath.Join(dir, "config.json")
	failFlag = filepath.Join(dir, "FAIL")
	script := strings.NewReplacer("@ARGV@", argvLog, "@CFG@", cfgLog, "@FAIL@", failFlag, "@KEY@", lrRootKey).Replace(`#!/bin/sh
set -eu
if [ "$1" = --version ]; then echo 'opencode 1.19.2'; exit 0; fi
case "$PWD" in */instances/*) ;; *) exit 55 ;; esac
printf '%s\n' "$PWD" >> '@ARGV@.cwd'
printf '%s\n' "$*" >> '@ARGV@'
printf '%s' "$OPENCODE_CONFIG_CONTENT" > '@CFG@'
[ "$LUNAROUTE_API_KEY" = '@KEY@' ] || exit 43
case "$OPENCODE_CONFIG_CONTENT" in *'@KEY@'*) exit 44 ;; esac
for field in '"baseURL":"https://gw.lunaroute.com/v1"' '"apiKey":"{env:LUNAROUTE_API_KEY}"' '"npm":"@ai-sdk/openai-compatible"' '"small_model":"lunaroute/glm-5.3"' '"models":{"glm-5.3":{"limit":{"context":202752,"output":32768},"name":"glm-5.3","tool_call":true}}' '"model":"lunaroute/glm-5.3"' '"enabled_providers":["lunaroute"]' '"permission":{"*":"deny","bash":"allow","read":"allow","write":"allow"}'; do
 case "$OPENCODE_CONFIG_CONTENT" in *"$field"*) ;; *) echo 'config mismatch' >&2; exit 45 ;; esac
done
case "$*" in *'@KEY@'*) exit 49 ;; esac
if [ "$1" = run ]; then
 [ "$2" = hello ] || exit 54
 case " $* " in *' -m lunaroute/glm-5.3 --dangerously-skip-permissions --format json --pure '*) ;; *) exit 50 ;; esac
 if [ -f '@FAIL@' ]; then printf checked-route > failure-reached; echo 'opaque failure @KEY@' >&2; exit 1; fi
 printf '%s\n' '{"type":"text","sessionID":"ses_run","part":{"id":"prt_text","messageID":"msg_answer","sessionID":"ses_run","type":"text","text":"verified output"}}'
 exit 0
fi
[ "$*" = 'export ses_run --pure' ] || exit 51
printf '%s\n' '{"info":{"id":"ses_run"},"messages":[{"info":{"id":"msg_answer","sessionID":"ses_run","role":"assistant","providerID":"lunaroute","modelID":"glm-5.3","time":{"created":1,"completed":2}},"parts":[]}]}'
`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o700))
	return
}
func bootLunaRouteAgent(t *testing.T, backend, bin string, gw *agentListingGateway) *domainStack {
	t.Helper()
	cfg := "capabilitiespath: " + writeLunaRouteCaps(t) + "\n"
	if bin != "" {
		cfg += "llm:\n  binarypath: " + bin + "\n"
	}
	ds := bootLunaRouteRoot(t, cfg, gw, func(ws string) {
		writeWorkflow(t, ws, "lr-agent", `{"name":"lr-agent","nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"lr-agent","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, ws, "lr-agent")
	})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, lrRootKey))
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-agent", Kind: provider.ProfileKindAgent, Backend: backend, ConnectionRef: "lr", Model: "glm-5.3", AllowedTools: []string{"Read", "Write", "Bash"}})
	require.NoError(t, err)
	return ds
}
func triggerLunaRouteAgent(t *testing.T, ds *domainStack) (uuid.UUID, *instance.Instance) {
	t.Helper()
	id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-agent", nil, "normal")
	require.NoError(t, err)
	return id, waitTerminal(t, ds, id)
}
func assertLunaRouteAgentSink(t *testing.T, ds *domainStack, gw *agentListingGateway, argvLog, answer, source, reportedProvider string) {
	t.Helper()
	id, inst := triggerLunaRouteAgent(t, ds)
	if inst.State == instance.StateFailed {
		t.Fatalf("subprocess rejected root inputs: %s", nodeError(t, ds, id))
	}
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, map[string]string{"lr-agent": "glm-5.3"}, inst.ModelPins)
	assert.Equal(t, map[string]any{"response": answer}, successfulNodeOutput(t, ds, id, "work"))
	cwd, err := os.ReadFile(argvLog + ".cwd")
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(cwd)), "\n") {
		assert.Equal(t, inst.Variables["workspace"], line, "sink cwd is the allocated instance workspace")
	}
	var raw string
	require.NoError(t, ds.db.QueryRow(`SELECT llm_conversation FROM node_executions WHERE instance_id=? AND node_id='work'`, id.String()).Scan(&raw))
	var conversation struct {
		Evidence []engine.ModelEvidence `json:"model_evidence"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &conversation))
	assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Provider: reportedProvider, Source: source}}, conversation.Evidence)
	assert.Empty(t, gw.seen(), "success is pass-through, with no diagnosis")
}
func assertLunaRouteAgentDiagnosis(t *testing.T, ds *domainStack, gw *agentListingGateway, failFlag string) {
	t.Helper()
	require.NoError(t, os.WriteFile(failFlag, nil, 0o600))
	id, inst := triggerLunaRouteAgent(t, ds)
	require.Equal(t, instance.StateFailed, inst.State)
	workspace, ok := inst.Variables["workspace"].(string)
	require.True(t, ok)
	marker, err := os.ReadFile(filepath.Join(workspace, "failure-reached"))
	require.NoError(t, err, "the fake must reach its deliberate failure after validating route, policy and workspace")
	assert.Equal(t, "checked-route", string(marker))
	msg := nodeError(t, ds, id)
	assert.Contains(t, msg, "a current observation, not proof of the original cause")
	assert.Contains(t, msg, "the credential was accepted and the model is listed now, so the cause is unknown")
	assert.NotContains(t, msg, lrRootKey)
	assert.Equal(t, []string{"GET https://gw.lunaroute.com/v1/models auth=ok"}, gw.seen())
	log, err := os.ReadFile(ds.cfg.Logging.File)
	require.NoError(t, err, "read the actual server log")
	assert.NotContains(t, string(log), lrRootKey)
	assert.NotContains(t, fmt.Sprint(gw.seen()), lrRootKey)
}
func TestLunaRouteClaudeCodeAgentSinkWiring(t *testing.T) {
	bin, argvLog, failFlag := fakeLunaRouteClaude(t, t.TempDir())
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds := bootLunaRouteAgent(t, "claude-code", bin, gw)
	assertLunaRouteAgentSink(t, ds, gw, argvLog, "lunaroute ok", "claude_model_usage", "")
	require.Len(t, readArgvLog(t, argvLog), 1)
	assertLunaRouteAgentDiagnosis(t, ds, gw, failFlag)
}
func TestLunaRouteOpencodeAgentSinkWiring(t *testing.T) {
	dir := t.TempDir()
	argvLog, cfgLog, _ := fakeLunaRouteOpencode(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds := bootLunaRouteAgent(t, "opencode", "", gw)
	assertLunaRouteAgentSink(t, ds, gw, argvLog, "verified output", "opencode_export", "lunaroute")
	calls := readArgvLog(t, argvLog)
	require.Len(t, calls, 2)
	assert.Equal(t, "export ses_run --pure", calls[1])
	cfg, err := os.ReadFile(cfgLog)
	require.NoError(t, err)
	assert.Contains(t, string(cfg), `"baseURL":"https://gw.lunaroute.com/v1"`)
	assert.NotContains(t, string(cfg), lrRootKey)
}
func TestLunaRouteOpencodeAgentFailureDiagnosisWiring(t *testing.T) {
	dir := t.TempDir()
	_, _, failFlag := fakeLunaRouteOpencode(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds := bootLunaRouteAgent(t, "opencode", "", gw)
	assertLunaRouteAgentDiagnosis(t, ds, gw, failFlag)
}
