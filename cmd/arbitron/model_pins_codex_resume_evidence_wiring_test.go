package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelPinsWiringCodexInteractiveResumeRejectsFallback drives a pinned Codex
// interactive run from the real root: the first turn proves X, then the resumed
// turn's own rollout records Y (codex fell back to its config model). The
// follow-up must fail after the turn (afterTurn), leave the persisted
// NodeExecution untouched and the run waiting; the poisoned session is then
// refused before any further spawn (beforeResume).
func TestModelPinsWiringCodexInteractiveResumeRejectsFallback(t *testing.T) {
	dir := t.TempDir()
	argvLog, executed := filepath.Join(dir, "argv"), filepath.Join(dir, "executed-model")
	home := filepath.Join(dir, "account")
	sessions := filepath.Join(home, "sessions", "2026", "10", "17")
	require.NoError(t, os.MkdirAll(sessions, 0o700))
	bin := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(bin, []byte(fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo 'codex-cli 0.171.0'; exit 0; fi
echo "$*" >> %q
[ -z "$OPENAI_API_KEY" ] || exit 40
[ -n "$(cat)" ] || exit 43
id=''
if [ "$2" = 'resume' ]; then id="$3"; fi
m=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-m' ]; then shift; m="$1"; fi
  shift
done
[ -n "$m" ] || exit 44
[ ! -f %q ] || m=$(cat %q)
if [ -z "$id" ]; then
  id="pin-resume-session"
  echo '{"type":"session_meta","payload":{"id":"'"$id"'"}}' > %q/rollout-test-"$id".jsonl
fi
echo '{"type":"turn_context","payload":{"model":"'"$m"'"}}' >> %q/rollout-test-"$id".jsonl
echo '{"type":"thread.started","thread_id":"'"$id"'"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"output from '"$m"'"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`, argvLog, executed, executed, sessions, sessions)), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "must-not-reach-codex")
	t.Setenv(codexInteractiveVerifiedEnv, "true")
	ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-codex-resume", `{
  "name":"pin-codex-resume","nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"agent","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, workspaceDir, "pin-codex-resume")
		require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "users", localDefaultUser.String(), "pin-codex-resume", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "agent", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol"})
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "pin-codex-resume", nil, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, id)
		return err == nil && (inst.State == instance.StateWaiting || inst.State == instance.StateFailed)
	}, 20*time.Second, 25*time.Millisecond)
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, instance.StateWaiting, inst.State, "the exact first turn must park the run")
	before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	require.Contains(t, fmt.Sprint(before.LLMConversation), "output from gpt-5-sol")

	// The resumed turn is launched on X but its rollout records Y.
	require.NoError(t, os.WriteFile(executed, []byte("gpt-6-sol"), 0o600))
	err = ds.conversationHandler.HandleMessage(ctx, id, "follow-up")
	require.ErrorContains(t, err, "executed-model evidence rejected")
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 2)
	assert.Contains(t, argv[1], "exec resume pin-resume-session")
	assert.Contains(t, argv[1], "-m gpt-5-sol", "the continuation must request the pinned model")
	after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	assert.Equal(t, before.LLMConversation, after.LLMConversation, "a Y-served turn must not be persisted")
	assert.Equal(t, before.Status, after.Status)
	assert.Equal(t, before.LLMModel, after.LLMModel)
	assert.NotContains(t, fmt.Sprint(after), "output from gpt-6-sol")
	inst, err = ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, inst.State)
	assert.Equal(t, map[string]string{"agent": "gpt-5-sol"}, inst.ModelPins)

	// The stored session now records Y: the next follow-up is refused before
	// spawning, even though the harness would serve X again.
	require.NoError(t, os.Remove(executed))
	err = ds.conversationHandler.HandleMessage(ctx, id, "again")
	require.ErrorContains(t, err, "executed-model evidence rejected")
	assert.Len(t, readArgvLog(t, argvLog), 2, "a session recorded on another model must not be resumed")
	after, err = ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	assert.Equal(t, before.LLMConversation, after.LLMConversation)
}
