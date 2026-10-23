package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptprovider "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Supported input reaches the real launcher's argv sink. An unproved numeric
// alias on the same persisted route must fail before any row or process exists.
func TestVersionRejectsNumericAliasesBeforeHarness(t *testing.T) {
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	goFile := filepath.Join(scratch, "go")
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	bin := fakeClaudeBinary(t, scratch, argvLog, goFile)
	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "version-agent", `{
  "name": "version-agent", "nodes": [
    {"id": "work", "type": "agent_call", "entrypoint": true,
     "config": {"model_profile": "agent-alias", "templateFile": "templates/p.md"}}
  ], "edges": []
}`)
		writePinTemplate(t, workspaceDir, "version-agent")
	})
	ctx := adminCtxForLocalUser(t, ds)
	err := ds.connSvc.Create(ctx, provider.Connection{Name: "anthropic", Provider: "anthropic", BaseURL: "https://api.anthropic.com"}, "k")
	require.NoError(t, err)
	p := provider.Profile{Alias: "agent-alias", Kind: provider.ProfileKindAgent, ConnectionRef: "anthropic", Backend: workflow.BackendClaudeCode, Model: "claude-opus-5-5"}
	_, _, err = ds.profileSvc.Create(ctx, p)
	require.NoError(t, err)
	id, _, err := ds.eng.Trigger(ctx, "version-agent", nil, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 1)
	require.Contains(t, argv[0], "--model "+p.Model)

	p.Model = "production-v2"
	storeProfileUnchecked(t, ctx, ds, p)
	before := countInstances(t, ds)
	id, started, err := ds.eng.Trigger(ctx, "version-agent", nil, "normal")
	require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
	assert.Equal(t, uuid.Nil, id)
	assert.False(t, started)
	assert.Equal(t, before, countInstances(t, ds))
	assert.Equal(t, argv, readArgvLog(t, argvLog), "no harness process may launch for the rejected alias")
}

// The root's real snapshotter reads the persisted connection's routing provider,
// not a catalog target or a test-installed resolver. Valid input reaches HTTP;
// an unproved numeric alias must fail before persistence and provider invocation.
func TestVersionRejectsNumericAliasesAtTrigger(t *testing.T) {
	ds := bootSinkStack(t)
	sink := newModelSink(t)
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "api", Provider: "openai", BaseURL: sink.srv.URL}, "k"))
	p := provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "api", Model: "gpt-6-sol"}
	_, _, err = ds.profileSvc.Create(ctx, p)
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "pin-llm", nil, "normal")
	require.NoError(t, err)
	run := waitTerminal(t, ds, id)
	require.Equal(t, instance.StateCompleted, run.State)
	require.Equal(t, map[string]string{"pin-alias": p.Model}, run.ModelPins)
	require.Equal(t, []string{p.Model, p.Model}, sink.seen())

	for _, tc := range []struct {
		route, model string
		wantErr      error
	}{
		{"openai", "production-v2", provider.ErrSpecificVersionUnavailable},
		// Inventory rejects an unsupported route before version validation.
		{"custom-proxy", "gpt-6-sol", adaptprovider.ErrUnknownProvider},
		{"litellm", "gpt-6-sol", provider.ErrSpecificVersionUnavailable},
	} {
		t.Run(tc.route, func(t *testing.T) {
			conn, err := conns.Get(ctx, "api")
			require.NoError(t, err)
			conn.Provider = tc.route
			conn.Extras = map[string]string{"api-version": "2024-10-21"}
			require.NoError(t, conns.Update(ctx, conn, nil))
			p.Model = tc.model
			storeProfileUnchecked(t, ctx, ds, p)
			before := countInstances(t, ds)
			id, started, err := ds.eng.Trigger(ctx, "pin-llm", nil, "normal")
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, uuid.Nil, id)
			assert.False(t, started)
			assert.Equal(t, before, countInstances(t, ds))
			assert.Equal(t, []string{"gpt-6-sol", "gpt-6-sol"}, sink.seen(), "rejected input must never reach the provider")
		})
	}
}
