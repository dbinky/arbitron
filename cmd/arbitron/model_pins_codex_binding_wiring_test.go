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

func TestModelPinsWiringCodexInteractiveLiveBinding(t *testing.T) {
	dir := t.TempDir()
	argvLog, expected := filepath.Join(dir, "argv"), filepath.Join(dir, "expected-effort")
	home := filepath.Join(dir, "account")
	sessions := filepath.Join(home, "sessions", "2026", "10", "16")
	require.NoError(t, os.MkdirAll(sessions, 0o700))
	bin := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(bin, []byte(fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo 'codex-cli 0.171.0'; exit 0; fi
echo "$*" >> %q
[ -z "$OPENAI_API_KEY" ] || exit 40
[ "$CODEX_HOME" = %q ] || exit 41
case " $* " in *" model_reasoning_effort=$(cat %q) "*) ;; *) exit 42 ;; esac
[ -n "$(cat)" ] || exit 43
id=''
if [ "$2" = 'resume' ]; then id="$3"; fi
m=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-m' ]; then shift; m="$1"; fi
  shift
done
[ -n "$m" ] || exit 44
if [ -z "$id" ]; then
  n=$(wc -l < %q | tr -d ' ')
  id="pin-session-$n"
  echo '{"type":"session_meta","payload":{"id":"'"$id"'"}}' > %q/rollout-test-"$id".jsonl
fi
echo '{"type":"turn_context","payload":{"model":"'"$m"'"}}' >> %q/rollout-test-"$id".jsonl
echo '{"type":"thread.started","thread_id":"'"$id"'"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`, argvLog, home, expected, argvLog, sessions, sessions)), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "must-not-reach-codex")
	t.Setenv(codexInteractiveVerifiedEnv, "true")
	ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-codex", `{
  "name":"pin-codex","nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"agent","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
		writePinTemplate(t, workspaceDir, "pin-codex")
		require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "users", localDefaultUser.String(), "pin-codex", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	p := provider.Profile{Alias: "agent", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol", Effort: provider.EffortLow}
	_, _, err := ds.profileSvc.Create(ctx, p)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(expected, []byte("low"), 0o600))
	trigger := func() uuid.UUID {
		t.Helper()
		id, _, err := ds.eng.Trigger(ctx, "pin-codex", nil, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, err := ds.instanceRepo.Get(ctx, id)
			return err == nil && (inst.State == instance.StateWaiting || inst.State == instance.StateFailed)
		}, 20*time.Second, 25*time.Millisecond)
		inst, err := ds.instanceRepo.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, instance.StateWaiting, inst.State, "the subprocess rejected missing/stale effort")
		return id
	}
	old := trigger()
	p.Model, p.Effort = "gpt-6-sol", provider.EffortHigh
	_, _, err = ds.profileSvc.Update(ctx, p)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(expected, []byte("high"), 0o600))
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, old, "continue"))
	fresh := trigger()
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, fresh, "continue"))
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 4)
	assert.Contains(t, argv[0], "model_reasoning_effort=low")
	for i, line := range argv {
		model := "gpt-5-sol"
		if i >= 2 {
			model = "gpt-6-sol"
		}
		assert.Contains(t, line, "-m "+model)
		if i > 0 {
			assert.Contains(t, line, "model_reasoning_effort=high")
		}
	}
	assert.Contains(t, argv[1], "exec resume pin-session-1")
	assert.Contains(t, argv[3], "exec resume pin-session-3")
	for id, model := range map[uuid.UUID]string{old: "gpt-5-sol", fresh: "gpt-6-sol"} {
		inst, err := ds.instanceRepo.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"agent": model}, inst.ModelPins)
	}

	// A cached or stored session cannot override the pin: refuse corruption
	// before resuming, with no new exec invocation and no accepted transcript.
	rollout := filepath.Join(sessions, "rollout-test-pin-session-1.jsonl")
	f, err := os.OpenFile(rollout, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("{\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-6-sol\"}}\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, old, "talk")
	require.NoError(t, err)
	err = ds.conversationHandler.HandleMessage(ctx, old, "must not resume")
	require.ErrorContains(t, err, "executed-model evidence rejected")
	assert.Len(t, readArgvLog(t, argvLog), 4)
	after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, old, "talk")
	require.NoError(t, err)
	assert.Equal(t, before.LLMConversation, after.LLMConversation)
}
