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
  claude-sonnet-5-lr1:
    tasks:
      chat: {support: unsupported, evidence: "ev#alias-chat"}
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
	gw := newFakeLunaRouteGateway(t, "lr_live_chat", "glm-5.3", "claude-sonnet-5-lr1")
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
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-conflict", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "claude-sonnet-5-lr1"})
	require.ErrorIs(t, err, provider.ErrLunaRouteIncompatible)
	assert.Contains(t, err.Error(), "ev#alias-chat")
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

func TestLunaRouteChatWiringAliasEvidence(t *testing.T) {
	caps := strings.Replace(lrChatCaps, `chat: {support: unsupported, evidence: "ev#alias-chat"}`, `chat: {support: supported, evidence: "ev#alias-chat"}`, 1)
	gw := newFakeLunaRouteGateway(t, "lr_alias_chat", "claude-sonnet-5-lr1")
	gw.ExpectChat("claude-sonnet-5-lr1", lrRootPrompt, 1234, "")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, caps)+"\n", gw, func(ws string) { writeLunaRouteChatWorkflow(t, ws, "lr-alias", "lr-alias", 1234, false) })
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_alias_chat"))
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-alias", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "claude-sonnet-5-lr1"})
	require.NoError(t, err)
	for _, reported := range []string{"claude-sonnet-5-5", "glm-5.3"} {
		gw.Reply("POST /v1/chat/completions", lunaRouteGatewayReply{Status: 200, Body: fmt.Sprintf(`{"model":%q,"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`, reported)})
		id, inst := triggerLunaRouteChat(t, ds, "lr-alias")
		if reported == "claude-sonnet-5-5" {
			require.Equal(t, instance.StateCompleted, inst.State)
			assert.Equal(t, map[string]any{"response": "pong"}, successfulNodeOutput(t, ds, id, "ask"))
			var model string
			require.NoError(t, ds.db.QueryRow(`SELECT llm_model FROM node_executions WHERE instance_id=? AND node_id='ask'`, id.String()).Scan(&model))
			assert.Equal(t, reported, model, "record actual execution, not the configured alias")
			pinned, err := ds.instanceRepo.Get(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, instance.ModelPinsVersionSnapshot, pinned.ModelPinsVersion)
			assert.Equal(t, map[string]string{"lr-alias": "claude-sonnet-5-lr1"}, pinned.ModelPins, "pin retains selected executable")
		} else {
			assert.Equal(t, instance.StateFailed, inst.State)
			assert.Contains(t, nodeError(t, ds, id), "execution evidence")
		}
	}
	assert.Len(t, gw.Requests(), 2)
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
