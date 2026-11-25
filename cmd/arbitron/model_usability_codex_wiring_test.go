package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real initDomain -> persisted profile -> trigger pin -> factory -> Codex ->
// durable run/node output. Only the external process is replaced.
func TestUsabilityCodexBatchExactModelRoot(t *testing.T) {
	const model = "gpt-5-sol"
	const thread = "019f00b8-f154-7bb1-ae4b-2adb20c4dac9"
	for _, actual := range []string{"exact", "fallback", "missing", "truncated", "conflicting session", "failed", "incomplete"} {
		t.Run(actual, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "account")
			sessions := filepath.Join(home, "sessions", "2026", "10", "16")
			require.NoError(t, os.MkdirAll(sessions, 0o700))
			encode := func(v any) string { b, err := json.Marshal(v); require.NoError(t, err); return string(b) }
			meta := func(id string) string {
				return encode(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id}})
			}
			contextModel := model
			if actual == "fallback" {
				contextModel = "gpt-6-sol"
			}
			rollout := []string{meta(thread), encode(map[string]any{"type": "turn_context", "payload": map[string]any{"model": contextModel}})}
			if actual == "truncated" {
				rollout = append(rollout, `{"type":"turn_context","payload":`)
			}
			if actual == "conflicting session" {
				rollout = append(rollout, meta("another-session"))
			}
			evidence := filepath.Join(dir, "evidence")
			require.NoError(t, os.WriteFile(evidence, []byte(strings.Join(rollout, "\n")+"\n"), 0o600))
			events := []string{
				encode(map[string]any{"type": "thread.started", "thread_id": thread}),
				encode(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "verified codex output"}}),
			}
			switch actual {
			case "failed":
				events = append(events, `{"type":"turn.failed","error":{"message":"refused"}}`)
			case "incomplete":
			default:
				events = append(events, `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
			}
			stdout := filepath.Join(dir, "stdout")
			require.NoError(t, os.WriteFile(stdout, []byte(strings.Join(events, "\n")+"\n"), 0o600))
			argvLog := filepath.Join(dir, "argv")
			copyEvidence := fmt.Sprintf("cp %q %q", evidence, filepath.Join(sessions, "rollout-test-"+thread+".jsonl"))
			if actual == "missing" {
				copyEvidence = ":"
			}
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo 'codex-cli 0.171.0'; exit 0; fi
printf '%%s\n' "$*" >> %q
[ -z "$OPENAI_API_KEY" ] || exit 40
[ "$CODEX_HOME" = %q ] || exit 41
case "$PWD" in *workspaces/instances/*) ;; *) exit 42 ;; esac
[ "$1" = 'exec' ] && [ "$2" = '--dangerously-bypass-approvals-and-sandbox' ] || exit 43
[ "$3" = '-m' ] && [ "$4" = 'gpt-5-sol' ] || exit 44
[ "$5" = '-c' ] && [ "$6" = 'model_reasoning_effort=low' ] || exit 45
[ "$7" = '--cd' ] && [ "$(cd "$8" && pwd -P)" = "$(pwd -P)" ] && [ "$9" = '--json' ] && [ "$#" = 9 ] || exit 45
[ "$(cat)" = 'hello' ] || exit 46
%s
cat %q
`, argvLog, home, copyEvidence, stdout)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CODEX_HOME", home)
			t.Setenv("OPENAI_API_KEY", "must-not-reach-codex")
			ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n", func(workspace string) {
				writeWorkflow(t, workspace, "usability-codex", `{"name":"usability-codex","nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"proof","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "usability-codex")
			})
			ctx := adminCtxForLocalUser(t, ds)
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "proof", Kind: provider.ProfileKindAgent, Model: model, Effort: provider.EffortLow})
			require.NoError(t, err)
			id, _, err := ds.eng.Trigger(ctx, "usability-codex", nil, "normal")
			require.NoError(t, err)
			saved := waitTerminal(t, ds, id)
			trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			require.Len(t, trace, 1)
			assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
			if actual == "exact" {
				require.Equal(t, instance.StateCompleted, saved.State, "trace: %+v", trace)
				assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Output), "verified codex output")
			} else {
				require.Equal(t, instance.StateFailed, saved.State, "unverified output reached durable sink: %+v", trace)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Error), "executed-model evidence rejected")
				assert.NotContains(t, fmt.Sprint(trace[0].Output), "verified codex output")
			}
			require.Len(t, readArgvLog(t, argvLog), 1)
		})
	}
}
