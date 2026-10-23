package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaudeEnvBinary is fakeClaudeBinary without the first-call gate, also
// logging the per-call connection env the adapter injected.
func fakeClaudeEnvBinary(t *testing.T, dir, argvLog string) string {
	t.Helper()
	bin := filepath.Join(dir, "fake-claude-env.sh")
	script := "#!/bin/sh\n" +
		"echo \"$* BASE=$ANTHROPIC_BASE_URL KEY=$ANTHROPIC_API_KEY\" >> '" + argvLog + "'\n" +
		"cat > /dev/null\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

// TestModelPinsWiringInteractiveAdminEditThroughMCP (paired:8, Area 6 ↔ Area 7):
// a run started on X with Read/Grep over connection A parks at an interactive
// node; an admin then edits the profile through the registered MCP
// profile_update (model Y, tools Glob, connection B). The resumed turn keeps the
// pinned X, uses only Glob and B's endpoint and key, and persists the turn.
// Clearing the tools with an explicit empty list then gives the continuation
// exactly the scope a fresh run gets from the live profile (the configured
// adapter scope, design phase 1: tool scope keeps live-resolution semantics),
// never the revoked Glob or the starting Read/Grep.
func TestModelPinsWiringInteractiveAdminEditThroughMCP(t *testing.T) {
	const modelX, modelY = "claude-opus-4-1", "claude-opus-4-5"
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	bin := fakeClaudeEnvBinary(t, scratch, argvLog)

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n  allowedtools: [Bash]\n", func(dir string) {
		writeWorkflow(t, dir, "pin-admin-edit", `{
  "name":"pin-admin-edit","inputs":[],"nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"agent-alias","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, dir, "pin-admin-edit")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "users", localDefaultUser.String(), "pin-admin-edit", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "conn-a", Provider: "anthropic", BaseURL: "https://a.gateway.test"}, "key-a"))
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "conn-b", Provider: "anthropic", BaseURL: "https://b.gateway.test"}, "key-b"))

	call := mcpToolCaller(t, ctx, ds)
	res, text := call("profile_create", map[string]any{
		"alias": "agent-alias", "kind": "agent", "backend": "claude-code",
		"connection_ref": "conn-a", "model": modelX, "allowed_tools": []any{"Read", "Grep"},
	})
	require.False(t, res.IsError, text)

	trigger := func() uuid.UUID {
		t.Helper()
		id, _, err := ds.eng.Trigger(ctx, "pin-admin-edit", map[string]any{}, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, err := ds.instanceRepo.Get(ctx, id)
			return err == nil && inst.State == instance.StateWaiting
		}, 20*time.Second, 25*time.Millisecond, "the run never parked at the interactive node")
		return id
	}
	last := func() string {
		t.Helper()
		lines := readArgvLog(t, argvLog)
		require.NotEmpty(t, lines)
		return lines[len(lines)-1]
	}

	id := trigger()
	first := last()
	assert.Contains(t, first, "--model "+modelX)
	assert.Contains(t, first, "--allowedTools Read,Grep")
	assert.Contains(t, first, "BASE=https://a.gateway.test KEY=key-a")

	res, text = call("profile_update", map[string]any{
		"alias": "agent-alias", "model": modelY, "allowed_tools": []any{"Glob"}, "connection_ref": "conn-b",
	})
	require.False(t, res.IsError, text)

	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "after the admin edit"))
	resumed := last()
	assert.Contains(t, resumed, "--resume s")
	assert.Contains(t, resumed, "--model "+modelX, "the resumed turn keeps the pin, not the edited model")
	assert.NotContains(t, resumed, modelY)
	assert.Contains(t, resumed, "--allowedTools Glob", "the resumed turn uses the narrowed live scope")
	for _, revoked := range []string{"Read", "Grep", "Bash"} {
		assert.NotContains(t, resumed, revoked, "revoked or global tool %s reached the resumed turn", revoked)
	}
	assert.Contains(t, resumed, "BASE=https://b.gateway.test KEY=key-b", "the resumed turn uses the edited connection")
	assert.NotContains(t, resumed, "a.gateway.test")
	assert.NotContains(t, resumed, "key-a")

	exec, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	conversation, err := json.Marshal(exec.LLMConversation)
	require.NoError(t, err)
	assert.Contains(t, string(conversation), "after the admin edit", "the successful turn is persisted")
	saved, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"agent-alias": modelX}, saved.ModelPins, "the admin edit never rewrites the run's pin")

	// Explicit empty tool list: the continuation and a fresh run resolve the same
	// live scope, which is the configured adapter scope, not Glob nor Read/Grep.
	res, text = call("profile_update", map[string]any{"alias": "agent-alias", "allowed_tools": []any{}})
	require.False(t, res.IsError, text)
	stored, err := ds.profileSvc.Get(ctx, "agent-alias")
	require.NoError(t, err)
	require.Empty(t, stored.AllowedTools)
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "after the clear"))
	cleared := last()
	fresh := trigger()
	freshFirst := last()
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, fresh, "fresh follow-up"))
	freshResumed := last()

	scopeOf := func(line string) string {
		t.Helper()
		_, after, ok := strings.Cut(line, "--allowedTools ")
		require.True(t, ok, "no --allowedTools in %s", line)
		return strings.Fields(after)[0]
	}
	assert.Equal(t, "Bash", scopeOf(cleared), "a cleared scope resolves live to the configured adapter scope")
	assert.Equal(t, scopeOf(freshFirst), scopeOf(cleared), "continuation and fresh run see the same live scope")
	assert.Equal(t, scopeOf(freshResumed), scopeOf(cleared))
	assert.Contains(t, cleared, "--model "+modelX, "the pin survives a tools-only edit")
	assert.Contains(t, freshFirst, "--model "+modelY, "a fresh run pins the edited model")
	assert.Contains(t, freshResumed, "--model "+modelY)
	for _, line := range []string{cleared, freshFirst, freshResumed} {
		assert.Contains(t, line, "BASE=https://b.gateway.test")
	}
}

// An admin edit that moves the alias to a live route that cannot serve the
// pinned spelling (claude-code → codex) refuses the resumed turn before any
// harness launches, through provider/pinned_route.go:validatePinnedRoute; it
// never falls back to the edited model or another version (design phase 1).
func TestModelPinsWiringInteractiveAdminEditIncompatibleRouteRefused(t *testing.T) {
	const modelX = "claude-opus-4-1"
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	bin := fakeClaudeEnvBinary(t, scratch, argvLog)
	t.Setenv(codexInteractiveVerifiedEnv, "1")
	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n", func(dir string) {
		writeWorkflow(t, dir, "pin-admin-route", `{
  "name":"pin-admin-route","inputs":[],"nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"agent-alias","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, dir, "pin-admin-route")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "users", localDefaultUser.String(), "pin-admin-route", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)

	call := mcpToolCaller(t, ctx, ds)
	res, text := call("profile_create", map[string]any{"alias": "agent-alias", "kind": "agent", "backend": "claude-code", "model": modelX})
	require.False(t, res.IsError, text)
	id, _, err := ds.eng.Trigger(ctx, "pin-admin-route", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, id)
		return err == nil && inst.State == instance.StateWaiting
	}, 20*time.Second, 25*time.Millisecond)
	before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	beforeJSON, err := json.Marshal(before)
	require.NoError(t, err)

	res, text = call("profile_update", map[string]any{"alias": "agent-alias", "backend": "codex", "model": "gpt-5.1-sol"})
	require.False(t, res.IsError, text)
	launches := len(readArgvLog(t, argvLog))
	err = ds.conversationHandler.HandleMessage(ctx, id, "after the route edit")
	require.Error(t, err, "a live route that cannot serve the pin must refuse the turn")
	assert.Contains(t, err.Error(), "incompatible with live route")
	assert.Len(t, readArgvLog(t, argvLog), launches, "the harness is never launched for a refused turn")
	after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	afterJSON, err := json.Marshal(after)
	require.NoError(t, err)
	assert.JSONEq(t, string(beforeJSON), string(afterJSON), "a refused turn persists nothing")
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, inst.State)
	assert.Equal(t, map[string]string{"agent-alias": modelX}, inst.ModelPins)
}
