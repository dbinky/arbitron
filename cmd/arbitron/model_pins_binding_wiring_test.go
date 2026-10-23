package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The subprocess refuses success unless the real root supplies the current
// connection credentials and axes, alongside the run's original model.
func TestModelPinsWiringInteractiveLiveBinding(t *testing.T) {
	dir := t.TempDir()
	expected := filepath.Join(dir, "expected")
	argvLog := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte(fmt.Sprintf(`#!/bin/sh
cat >/dev/null
echo "$*" >> %q
actual="$ANTHROPIC_BASE_URL|$ANTHROPIC_API_KEY|$CLAUDE_CODE_EFFORT_LEVEL|$CLAUDE_CODE_AUTO_COMPACT_WINDOW|$CLAUDE_CODE_DISABLE_1M_CONTEXT"
if [ "$actual" != "$(cat %q)" ]; then
  echo 'binding mismatch' >&2
  exit 1
fi
m=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '--model' ]; then shift; m="$1"; fi
  shift
done
[ -n "$m" ] || exit 2
echo '{"type":"result","is_error":false,"result":"ok","session_id":"binding-session","modelUsage":{"'"$m"'":{}}}'
`, argvLog, expected)), 0o700))

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-binding", `{
  "name":"pin-binding","nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"agent","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, workspaceDir, "pin-binding")
		require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "users", localDefaultUser.String(), "pin-binding", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	conn := provider.Connection{Name: "gateway", Provider: "anthropic", BaseURL: "https://api.anthropic.com"}
	require.NoError(t, ds.connSvc.Create(ctx, conn, "fixture-key-one"))
	profile := provider.Profile{Alias: "agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", ConnectionRef: conn.Name,
		Model: "claude-opus-4-1", Effort: provider.EffortLow, ContextWindow: provider.Context200K, AllowedTools: []string{"Read"}}
	_, _, err := ds.profileSvc.Create(ctx, profile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(expected, []byte(conn.BaseURL+"|fixture-key-one|low|200000|1"), 0o600))

	trigger := func() uuid.UUID {
		t.Helper()
		id, _, err := ds.eng.Trigger(ctx, "pin-binding", nil, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, err := ds.instanceRepo.Get(ctx, id)
			return err == nil && (inst.State == instance.StateWaiting || inst.State == instance.StateFailed)
		}, 20*time.Second, 25*time.Millisecond)
		inst, err := ds.instanceRepo.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, instance.StateWaiting, inst.State, "the subprocess rejected a missing/stale binding")
		return id
	}
	old := trigger()

	key := "fixture-key-two"
	conn.BaseURL = "https://gateway.anthropic.com"
	require.NoError(t, ds.connSvc.Update(ctx, conn, &key))
	profile.Model, profile.Effort, profile.ContextWindow = "claude-opus-4-5", provider.EffortHigh, provider.Context1M
	profile.AllowedTools = []string{"Glob"}
	_, _, err = ds.profileSvc.Update(ctx, profile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(expected, []byte(conn.BaseURL+"|"+key+"|high|1000000|"), 0o600))
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, old, "continue"))
	fresh := trigger()
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, fresh, "continue"))

	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 4)
	for _, line := range argv[:2] {
		assert.Contains(t, line, "--model claude-opus-4-1")
	}
	for _, line := range argv[2:] {
		assert.Contains(t, line, "--model claude-opus-4-5")
	}
	for _, i := range []int{1, 3} {
		assert.Contains(t, argv[i], "--resume binding-session")
	}
	for _, line := range argv[1:] {
		assert.Contains(t, line, "--allowedTools Glob")
		assert.NotContains(t, line, "fixture-key")
	}
	for id, model := range map[uuid.UUID]string{old: "claude-opus-4-1", fresh: "claude-opus-4-5"} {
		saved, err := ds.instanceRepo.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"agent": model}, saved.ModelPins)
	}

	// A credential read failure cannot launch with a partial/subscription env
	// or persist an assistant response on the waiting run.
	_, err = ds.db.ExecContext(ctx, `UPDATE connections SET api_key_cipher = X'00' WHERE name = ?`, conn.Name)
	require.NoError(t, err)
	before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, old, "talk")
	require.NoError(t, err)
	err = ds.conversationHandler.HandleMessage(ctx, old, "must not launch")
	require.Error(t, err)
	assert.Len(t, readArgvLog(t, argvLog), 4)
	after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, old, "talk")
	require.NoError(t, err)
	assert.Equal(t, before.LLMConversation, after.LLMConversation)
}
