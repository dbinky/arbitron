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

// Dropping the first turn's pin marker before stream selection must fail this
// regression: argv still requests X, but the harness's own record proves Y.
// Exercise Trigger through the root-built executor and inspect every durable
// execution, rather than mistaking a streamed token or a failed latest row for
// proof that no successful assistant conversation was written.
func TestModelPinsWiringInteractiveFirstTurnRejectsFallback(t *testing.T) {
	for _, tc := range []struct {
		backend, model, fallback, flag, evidenceError string
	}{
		{"claude-code", "claude-opus-4-1", "claude-opus-4-5", "--model", "modelUsage"},
		{"codex", "gpt-5-sol", "gpt-6-sol", "-m", "executed-model evidence rejected"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			dir := t.TempDir()
			argvLog, executed := filepath.Join(dir, "argv"), filepath.Join(dir, "executed-model")
			home := filepath.Join(dir, "account")
			sessions := filepath.Join(home, "sessions", "2026", "10", "20")
			require.NoError(t, os.MkdirAll(sessions, 0o700))
			bin := filepath.Join(dir, "codex")
			// The fake replaces only the external harness. It consumes the real
			// model argument and prompt, then independently records a fallback.
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then if [ %q = 'codex' ]; then echo 'codex-cli 0.171.0'; else echo '2.1.330 (Claude Code)'; fi; exit 0; fi
case "$1" in auth) echo '{"loggedIn":true}'; exit 0 ;; esac
echo "$*" >> %q
[ -n "$(cat)" ] || exit 43
m=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = %q ]; then shift; m="$1"; fi
  shift
done
[ -n "$m" ] || exit 44
[ ! -f %q ] || m=$(cat %q)
if [ %q = 'codex' ]; then
  id='first-turn-session'
  echo '{"type":"session_meta","payload":{"id":"'"$id"'"}}' > %q/rollout-test-"$id".jsonl
  echo '{"type":"turn_context","payload":{"model":"'"$m"'"}}' >> %q/rollout-test-"$id".jsonl
  echo '{"type":"thread.started","thread_id":"'"$id"'"}'
  echo '{"type":"item.completed","item":{"type":"agent_message","text":"first turn from '"$m"'"}}'
  echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
else
  echo '{"type":"assistant","session_id":"first-turn-session","message":{"content":[{"type":"text","text":"first turn from '"$m"'"}]}}'
  echo '{"type":"result","session_id":"first-turn-session","is_error":false,"modelUsage":{"'"$m"'":{}},"usage":{"input_tokens":1,"output_tokens":1}}'
fi
`, tc.backend, argvLog, tc.flag, executed, executed, tc.backend, sessions, sessions)
			require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CODEX_HOME", home)
			t.Setenv(codexInteractiveVerifiedEnv, "true")
			ds := bootSinkRoot(t, "llm:\n  binarypath: "+bin+"\n", func(workspaceDir string) {
				writeWorkflow(t, workspaceDir, "pin-first-turn", `{
  "name":"pin-first-turn","nodes":[
    {"id":"talk","type":"interactive_agent","entrypoint":true,
     "config":{"model_profile":"first-turn-agent","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}
  ],"edges":[]}`)
				writePinTemplate(t, workspaceDir, "pin-first-turn")
				require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "users", localDefaultUser.String(), "pin-first-turn", "templates", "o.md"), []byte("orchestrate"), 0o600))
			})
			ctx := adminCtxForLocalUser(t, ds)
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{
				Alias: "first-turn-agent", Kind: provider.ProfileKindAgent, Backend: tc.backend, Model: tc.model,
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(executed, []byte(tc.fallback), 0o600))
			id, _, err := ds.eng.Trigger(ctx, "pin-first-turn", nil, "normal")
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				inst, err := ds.instanceRepo.Get(ctx, id)
				return err == nil && (inst.State == instance.StateWaiting || inst.State.IsTerminal())
			}, 20*time.Second, 25*time.Millisecond)
			inst, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			require.Equal(t, instance.StateFailed, inst.State, "a mismatched first turn must fail, never park or complete")
			assert.Equal(t, map[string]string{"first-turn-agent": tc.model}, inst.ModelPins)
			assert.NotContains(t, inst.Variables, "talk", "no session completion output")
			execs, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			require.NotEmpty(t, execs, "refusal must be durably recorded")
			for _, execution := range execs {
				assert.Equal(t, instance.NodeStatusFailed, execution.Status)
				assert.Empty(t, execution.LLMConversation, "streamed fallback text must never become assistant history")
				assert.Empty(t, execution.Output)
				require.NotNil(t, execution.Error)
				assert.Contains(t, fmt.Sprint(execution.Error), tc.evidenceError)
			}
			argv := readArgvLog(t, argvLog)
			require.Len(t, argv, 1)
			assert.Contains(t, argv[0], tc.flag+" "+tc.model, "the mismatch is in harness evidence, not the requested model")
			assert.NotContains(t, argv[0], "resume", "this regression must exercise the initial turn")

			// A harness that actually serves X must still persist its assistant
			// turn and park, ruling out an unrelated launch/configuration failure.
			require.NoError(t, os.Remove(executed))
			controlID, _, err := ds.eng.Trigger(ctx, "pin-first-turn", nil, "normal")
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				inst, err := ds.instanceRepo.Get(ctx, controlID)
				return err == nil && (inst.State == instance.StateWaiting || inst.State.IsTerminal())
			}, 20*time.Second, 25*time.Millisecond)
			control, err := ds.instanceRepo.Get(ctx, controlID)
			require.NoError(t, err)
			require.Equal(t, instance.StateWaiting, control.State)
			stored, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, controlID, "talk")
			require.NoError(t, err)
			assert.Equal(t, instance.NodeStatusSucceeded, stored.Status)
			assert.Contains(t, fmt.Sprint(stored.LLMConversation), "first turn from "+tc.model)
			assert.NotContains(t, fmt.Sprint(stored.LLMConversation), "first turn from "+tc.fallback)
			assert.Len(t, readArgvLog(t, argvLog), 2)
		})
	}
}
