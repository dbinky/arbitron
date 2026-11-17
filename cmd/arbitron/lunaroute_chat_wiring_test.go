package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrChatCaps = `lunaroute_models:
  glm-5.3:
    tasks:
      chat: {support: supported, evidence: "ev#chat"}
    output_tokens: {tokens: 32768, evidence: "ev#out"}
    efforts:
      chat: {levels: [low, high], evidence: "ev#eff"}
    request_settings:
      output_schema: {support: unsupported, evidence: "ev#schema"}
      temperature: {support: unsupported, evidence: "ev#temp"}
  glm-5.3-flash:
    tasks:
      chat: {support: unsupported, evidence: "ev#flash-chat"}
`
const lrRootPrompt = "nonce-root-8401: answer ping"

func writeLunaRouteChatWorkflow(t *testing.T, ws, name, alias string, max int, schema bool) {
	t.Helper()
	config := map[string]any{"model_profile": alias, "templateFile": "templates/p.md", "maxTokens": max}
	if schema {
		config["outputSchema"] = map[string]any{"type": "object"}
	}
	manifest, err := json.Marshal(map[string]any{"name": name, "inputs": []any{}, "nodes": []any{map[string]any{"id": "ask", "type": "llm_call", "entrypoint": true, "config": config}}, "edges": []any{}})
	require.NoError(t, err)
	writeWorkflow(t, ws, name, string(manifest))
	dir := filepath.Join(ws, "users", localDefaultUser.String(), name, "templates")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.md"), []byte(lrRootPrompt), 0o600))
}
func lunaRouteCapsFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caps.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}
func triggerLunaRouteChat(t *testing.T, ds *domainStack, name string) (uuid.UUID, *instance.Instance) {
	t.Helper()
	id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), name, map[string]any{}, "normal")
	require.NoError(t, err, "trigger-time policy must receive the loaded table")
	return id, waitTerminal(t, ds, id)
}

// Real root -> save -> pin capture -> resolver -> factory -> HTTP -> persisted
// output. No test-only source/selector replaces the composition root's ports.
func TestLunaRouteChatWiring(t *testing.T) {
	gw := newFakeLunaRouteGateway(t, "lr_live_chat", "glm-5.3", "glm-5.3-flash")
	gw.ExpectChat("glm-5.3", lrRootPrompt, 1234, "low")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, lrChatCaps)+"\n", gw, func(ws string) {
		writeLunaRouteChatWorkflow(t, ws, "lr-chat", "lr-chat", 1234, false)
		writeLunaRouteChatWorkflow(t, ws, "lr-chat-big", "lr-legacy", 40000, false)
		writeLunaRouteChatWorkflow(t, ws, "lr-chat-schema", "lr-legacy", 1234, true)
	})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_live_chat"))
	// A legacy seed bypasses service save validation. The normal create below
	// reloads the real cache, and request-level checks must still protect it.
	require.NoError(t, adaptsqlite.NewProfileRepo(ds.db).Create(ctx, provider.Profile{Alias: "lr-legacy", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "glm-5.3", Effort: provider.EffortLow}))
	_, eff, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-chat", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "glm-5.3", Effort: provider.EffortLow})
	require.NoError(t, err)
	assert.Equal(t, provider.EffortLow, eff.Effort)
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-conflict", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "glm-5.3-flash"})
	require.ErrorIs(t, err, provider.ErrLunaRouteIncompatible)
	assert.Contains(t, err.Error(), "ev#flash-chat")
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-med", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "glm-5.3", Effort: provider.EffortMedium})
	require.ErrorIs(t, err, provider.ErrLunaRouteAxisUnsupported)
	var refused int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias IN ('lr-conflict','lr-med')`).Scan(&refused))
	assert.Zero(t, refused)

	id, inst := triggerLunaRouteChat(t, ds, "lr-chat")
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, map[string]any{"response": "pong"}, successfulNodeOutput(t, ds, id, "ask"))
	var model string
	require.NoError(t, ds.db.QueryRow(`SELECT llm_model FROM node_executions WHERE instance_id=? AND node_id='ask'`, id.String()).Scan(&model))
	assert.Equal(t, "glm-5.3", model)
	reqs := gw.Requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, "POST", r.Method)
	assert.Equal(t, "https://gw.lunaroute.com/v1/chat/completions", r.URL)
	assert.Equal(t, "Bearer lr_live_chat", r.Authorization)
	for _, h := range []string{"HTTP-Referer", "X-Title", "LUNAROUTE-PROJECT-ID", "x-api-key", "LUNAROUTE-API-KEY"} {
		assert.Empty(t, r.Header.Get(h), h)
	}
	var body map[string]any
	require.NoError(t, json.Unmarshal(r.Body, &body))
	assert.Equal(t, "glm-5.3", body["model"])
	assert.Equal(t, "low", body["reasoning_effort"])
	assert.Equal(t, float64(1234), body["max_tokens"])
	assert.Equal(t, []any{map[string]any{"role": "user", "content": lrRootPrompt}}, body["messages"])

	for _, tc := range []struct{ name, want string }{
		{"lr-chat-big", "exceeds the model's known output limit 32768"},
		{"lr-chat-schema", provider.ErrLunaRouteSettingConflict.Error()},
	} {
		id, inst = triggerLunaRouteChat(t, ds, tc.name)
		assert.Equal(t, instance.StateFailed, inst.State)
		assert.Contains(t, nodeError(t, ds, id), tc.want)
		assert.Len(t, gw.Requests(), 1, "known conflict is refused before HTTP")
	}
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{401, `{"error":"bad key lr_live_chat"}`, provider.ErrLunaRouteCredentialsRejected.Error()},
		{503, `{"error":"upstream down"}`, provider.ErrLunaRouteServiceFailure.Error()},
		{400, `{"code":"MISSING_LUNAROUTE_HEADER","message":"lr_live_chat"}`, provider.ErrLunaRouteCredentialsRejected.Error()},
		{404, `{"type":"error","error":{"code":"INFERENCE_MODEL_NOT_FOUND","message":"lr_live_chat"}}`, provider.ErrLunaRouteModelUnavailable.Error()},
		{404, `{"code":"MODEL_NOT_FOUND"}`, provider.ErrLunaRouteCauseUnknown.Error()},
		{429, `{"type":"error","error":{"code":"BYOK_SPEND_CAP_REACHED","message":"lr_live_chat"}}`, provider.ErrLunaRouteRequestRefused.Error()},
	} {
		before := len(gw.Requests())
		gw.Reply("POST /v1/chat/completions", lunaRouteGatewayReply{Status: tc.status, Body: tc.body})
		id, inst = triggerLunaRouteChat(t, ds, "lr-chat")
		assert.Equal(t, instance.StateFailed, inst.State)
		msg := nodeError(t, ds, id)
		assert.Contains(t, msg, tc.want)
		assert.NotContains(t, msg, "lr_live_chat")
		assert.Len(t, gw.Requests(), before+1, "no substitution/retry")
	}
}

func TestLunaRouteChatWiringFixedEvidence(t *testing.T) {
	for _, selected := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		t.Run(selected, func(t *testing.T) {
			caps := fmt.Sprintf("lunaroute_models:\n  %s:\n    output_tokens: {tokens: 4096, evidence: 'ev#out'}\n    efforts:\n      chat: {levels: [high], evidence: 'ev#eff'}\n", selected)
			gw := newFakeLunaRouteGateway(t, "lr_fixed_chat", selected)
			gw.ExpectChat(selected, lrRootPrompt, 1234, "high")
			ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, caps)+"\n", gw, func(ws string) { writeLunaRouteChatWorkflow(t, ws, "lr-fixed", "lr-fixed", 1234, false) })
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_fixed_chat"))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-fixed", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: selected, Effort: provider.EffortHigh})
			require.NoError(t, err)
			for _, reported := range []string{selected, selected + "-changed", "claude-sonnet-5-5"} {
				gw.Reply("POST /v1/chat/completions", lunaRouteGatewayReply{Status: 200, Body: fmt.Sprintf(`{"model":%q,"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`, reported)})
				id, inst := triggerLunaRouteChat(t, ds, "lr-fixed")
				if reported == selected {
					require.Equal(t, instance.StateCompleted, inst.State)
					assert.Equal(t, map[string]any{"response": "pong"}, successfulNodeOutput(t, ds, id, "ask"))
					var model string
					require.NoError(t, ds.db.QueryRow(`SELECT llm_model FROM node_executions WHERE instance_id=? AND node_id='ask'`, id.String()).Scan(&model))
					assert.Equal(t, reported, model, "record actual execution evidence")
					pinned, err := ds.instanceRepo.Get(context.Background(), id)
					require.NoError(t, err)
					assert.Equal(t, instance.ModelPinsVersionSnapshot, pinned.ModelPinsVersion)
					assert.Equal(t, map[string]string{"lr-fixed": selected}, pinned.ModelPins)
				} else {
					assert.Equal(t, instance.StateFailed, inst.State)
					assert.Contains(t, nodeError(t, ds, id), "execution evidence")
				}
			}
			assert.Len(t, gw.Requests(), 3, "exact selection, never a fallback or retry")
		})
	}
}

func TestLunaRouteChatWiringEffectiveAxesNeverFloor(t *testing.T) {
	// Generic native override must not floor a gateway-supported high effort.
	caps := `backends:
  "":
    context_tiers: [default]
    effort_levels: [default, low]
    model_overrides:
      glm-5.3:
        effort_floor: {high: low}
` + lrChatCaps
	gw := newFakeLunaRouteGateway(t, "lr_high_chat", "glm-5.3")
	gw.ExpectChat("glm-5.3", lrRootPrompt, 1234, "high")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, caps)+"\n", gw, func(ws string) { writeLunaRouteChatWorkflow(t, ws, "lr-high", "lr-high", 1234, false) })
	// Control: this is the backend key the generic chat axis path would use.
	_, nativeEffort, err := ds.capabilityRules.Validate("", "glm-5.3", provider.ContextDefault, provider.EffortHigh)
	require.NoError(t, err)
	require.Equal(t, provider.EffortLow, nativeEffort)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_high_chat"))
	p, eff, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-high", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "glm-5.3", Effort: provider.EffortHigh})
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, p.Effort)
	assert.Equal(t, provider.EffortHigh, eff.Effort)
	var effort string
	require.NoError(t, ds.db.QueryRow(`SELECT effort FROM profiles WHERE alias='lr-high'`).Scan(&effort))
	assert.Equal(t, "high", effort)
	id, inst := triggerLunaRouteChat(t, ds, "lr-high")
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, map[string]any{"response": "pong"}, successfulNodeOutput(t, ds, id, "ask"))
	assert.Len(t, gw.Requests(), 1)
}

func TestLunaRouteChatWiringMissingEffortFactsRefuse(t *testing.T) {
	for _, facts := range []string{"missing", "unsupported"} {
		t.Run(facts, func(t *testing.T) {
			caps := "lunaroute_models:\n  glm-5.3: {}\n"
			if facts == "unsupported" {
				caps = strings.Replace(lrChatCaps, "levels: [low, high]", "levels: [low]", 1)
			}
			gw := newFakeLunaRouteGateway(t, "lr_high_chat", "glm-5.3")
			gw.ExpectChat("glm-5.3", lrRootPrompt, 1234, "high")
			ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, caps)+"\n", gw, func(ws string) {
				writeLunaRouteChatWorkflow(t, ws, "lr-high", "lr-high", 1234, false)
			})
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_high_chat"))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-high", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "glm-5.3", Effort: provider.EffortHigh})
			require.ErrorIs(t, err, provider.ErrLunaRouteAxisUnsupported)
			var n int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias='lr-high'`).Scan(&n))
			assert.Zero(t, n)
			assert.Empty(t, gw.Requests())
		})
	}
}

func TestLunaRouteGatewayRejectsMutatedInputs(t *testing.T) {
	for _, field := range []string{"model", "auth", "messages", "max_tokens", "reasoning_effort"} {
		for _, omit := range []bool{true, false} {
			t.Run(fmt.Sprint(field, omit), func(t *testing.T) {
				gw := newFakeLunaRouteGateway(t, "lr_contract", "glm-5.3")
				gw.ExpectChat("glm-5.3", lrRootPrompt, 1234, "low")
				body := map[string]any{"model": "glm-5.3", "messages": []any{map[string]any{"role": "user", "content": lrRootPrompt}}, "max_tokens": 1234, "reasoning_effort": "low"}
				if field != "auth" {
					if omit {
						delete(body, field)
					} else {
						body[field] = "mutated"
					}
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				r, err := http.NewRequest(http.MethodPost, "https://gw.lunaroute.com/v1/chat/completions", bytes.NewReader(raw))
				require.NoError(t, err)
				auth := "Bearer lr_contract"
				if field == "auth" {
					if omit {
						auth = ""
					} else {
						auth = "Bearer lr_wrong"
					}
				}
				r.Header.Set("Authorization", auth)
				resp, err := gw.RoundTrip(r)
				require.NoError(t, err)
				defer resp.Body.Close()
				assert.GreaterOrEqual(t, resp.StatusCode, 400)
			})
		}
	}
}

func TestLunaRouteGatewayBodyReadError(t *testing.T) {
	gw := newFakeLunaRouteGateway(t, "lr_contract", "glm-5.3")
	r, err := http.NewRequest(http.MethodPost, "https://gw.lunaroute.com/v1/chat/completions", nil)
	require.NoError(t, err)
	r.Body = io.NopCloser(lunaRouteBrokenBody{})
	resp, err := gw.RoundTrip(r)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Nil(t, resp)
}

type lunaRouteBrokenBody struct{}

func (lunaRouteBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// paired:2 — Area 3 save ↔ Area 6 chat. Every effort saved through MCP (create
// and update) must reach the gateway body verbatim via the reloaded profile
// cache and run pinning resolver; the fake gateway answers 400 to any body that
// differs from the expectation, so a dropped, floored or remapped effort, or a
// model other than the pin, fails the run. xhigh, which the OpenAI-compatible
// emitter floors to high, must be refused at save even when the table lists it.
func TestLunaRouteChatWiringMCPSavedAxesReachBody(t *testing.T) {
	caps := strings.Replace(lrChatCaps, "levels: [low, high]", "levels: [low, high, xhigh]", 1)
	gw := newFakeLunaRouteGateway(t, "lr_mcp_chat", "glm-5.3", "glm-5.3-flash")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, caps)+"\n", gw, func(ws string) {
		writeLunaRouteChatWorkflow(t, ws, "lr-mcp", "lr-mcp", 1234, false)
	})
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": "lr_mcp_chat"})
	require.False(t, bad, text)
	stored := func() string {
		var effort, model string
		require.NoError(t, ds.db.QueryRow(`SELECT effort, model FROM profiles WHERE alias='lr-mcp'`).Scan(&effort, &model))
		require.Equal(t, "glm-5.3", model)
		return effort
	}
	runExpecting := func(effort string) {
		t.Helper()
		before := len(gw.Requests())
		gw.ExpectChat("glm-5.3", lrRootPrompt, 1234, effort)
		id, inst := triggerLunaRouteChat(t, ds, "lr-mcp")
		require.Equal(t, instance.StateCompleted, inst.State, "body must match the saved axes exactly")
		reqs := gw.Requests()
		require.Len(t, reqs, before+1)
		var body map[string]any
		require.NoError(t, json.Unmarshal(reqs[len(reqs)-1].Body, &body))
		assert.Equal(t, "glm-5.3", body["model"])
		if effort == "" {
			assert.NotContains(t, body, "reasoning_effort")
		} else {
			assert.Equal(t, effort, body["reasoning_effort"])
		}
		var model string
		require.NoError(t, ds.db.QueryRow(`SELECT llm_model FROM node_executions WHERE instance_id=? AND node_id='ask'`, id.String()).Scan(&model))
		assert.Equal(t, "glm-5.3", model)
	}

	bad, text = call("profile_create", map[string]any{"alias": "lr-mcp", "kind": "chat", "connection_ref": "lr", "model": "glm-5.3", "effort": "low"})
	require.False(t, bad, text)
	require.Equal(t, "low", stored())
	runExpecting("low")

	bad, text = call("profile_update", map[string]any{"alias": "lr-mcp", "effort": "high"})
	require.False(t, bad, text)
	require.Equal(t, "high", stored())
	runExpecting("high")

	for _, effort := range []string{"medium", "xhigh"} {
		bad, text = call("profile_update", map[string]any{"alias": "lr-mcp", "effort": effort})
		require.True(t, bad, "effort %s must be refused at save", effort)
		assert.Contains(t, text, effort)
		require.Equal(t, "high", stored(), "a refused update leaves the saved effort intact")
	}
	runExpecting("high")

	bad, text = call("profile_update", map[string]any{"alias": "lr-mcp", "effort": "default"})
	require.False(t, bad, text)
	runExpecting("")
}

// G21 (paired:2): a save whose post-write cache reload fails (an unrelated
// corrupt row makes the whole List fail) must still publish the committed axes
// before the next chat, so the run never succeeds with the stale effort. The
// fake gateway answers 400 to a body carrying any effort but the expected one.
func TestLunaRouteChatWiringReloadFailurePublishesSavedAxes(t *testing.T) {
	gw := newFakeLunaRouteGateway(t, "lr_mcp_chat", "glm-5.3", "glm-5.3-flash")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, lrChatCaps)+"\n", gw, func(ws string) {
		writeLunaRouteChatWorkflow(t, ws, "lr-mcp", "lr-mcp", 1234, false)
	})
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": "lr_mcp_chat"})
	require.False(t, bad, text)
	for _, alias := range []string{"lr-mcp", "unrelated"} {
		bad, text = call("profile_create", map[string]any{"alias": alias, "kind": "chat", "connection_ref": "lr", "model": "glm-5.3", "effort": "low"})
		require.False(t, bad, text)
	}
	run := func(effort string) {
		t.Helper()
		gw.ExpectChat("glm-5.3", lrRootPrompt, 1234, effort)
		_, inst := triggerLunaRouteChat(t, ds, "lr-mcp")
		require.Equal(t, instance.StateCompleted, inst.State, "the run must send the saved effort %q", effort)
		reqs := gw.Requests()
		var body map[string]any
		require.NoError(t, json.Unmarshal(reqs[len(reqs)-1].Body, &body))
		assert.Equal(t, effort, body["reasoning_effort"])
		assert.Equal(t, "glm-5.3", body["model"])
	}
	run("low")

	// Corrupt an unrelated row: every full reload now fails.
	_, err := ds.db.Exec(`UPDATE profiles SET context_window='not-a-tier' WHERE alias='unrelated'`)
	require.NoError(t, err)

	bad, text = call("profile_update", map[string]any{"alias": "lr-mcp", "effort": "high"})
	require.False(t, bad, text)
	var effort string
	require.NoError(t, ds.db.QueryRow(`SELECT effort FROM profiles WHERE alias='lr-mcp'`).Scan(&effort))
	require.Equal(t, "high", effort)
	run("high")
}
