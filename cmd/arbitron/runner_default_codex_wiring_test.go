package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only the external harness is replaced. The profile, executor, routing
// selectors, placement, SQLite ledger and evidence verifier are production code.
func TestRunnerWiring_DefaultCodexInteractiveUsesOpenAISource(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv")
	home := filepath.Join(dir, "account")
	sessions := filepath.Join(home, "sessions", "2026", "11", "20")
	require.NoError(t, os.MkdirAll(sessions, 0o700))
	bin := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(bin, []byte(fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo 'codex-cli 0.170.0'; exit 0; fi
if [ "$1" = 'login' ]; then echo 'Logged in using ChatGPT'; exit 0; fi
printf '%%s\n' "$*" >> %q
[ -n "$(cat)" ] || exit 43
[ -z "$OPENAI_API_KEY" ] || exit 44
m=''
while [ "$#" -gt 0 ]; do
 if [ "$1" = '-m' ]; then shift; m="$1"; fi
 shift
done
[ -n "$m" ] || exit 45
id='routing-default-codex'
echo '{"type":"session_meta","payload":{"id":"'"$id"'"}}' > %q/rollout-test-"$id".jsonl
echo '{"type":"turn_context","payload":{"model":"'"$m"'"}}' >> %q/rollout-test-"$id".jsonl
echo '{"type":"thread.started","thread_id":"'"$id"'"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"served '"$m"'"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`, argvLog, sessions, sessions)), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "must-not-reach-subscription")
	t.Setenv(codexInteractiveVerifiedEnv, "true")
	ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n", func(ws string) { rwWriteWorkflows(t, ws) })
	ctx := adminCtxForLocalUser(t, ds)
	profile := provider.Profile{Alias: "rr-agent", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol", Effort: provider.EffortLow}
	require.Empty(t, profile.Backend, "exercise deployment default, not an explicit codex backend")
	_, _, err := ds.profileSvc.Create(ctx, profile)
	require.NoError(t, err)
	id, _, err := ds.eng.Trigger(ctx, "rr-talk", nil, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, id)
		return err == nil && (inst.State == instance.StateWaiting || inst.State.IsTerminal())
	}, 20*time.Second, 25*time.Millisecond)
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, instance.StateWaiting, inst.State)
	require.Eventually(t, func() bool {
		jobs := rwJobs(t, ds.db, id)
		return rwEveryOpPlacedOnBuiltin(jobs, map[string]string{"talk": "stream"})
	}, 5*time.Second, 25*time.Millisecond)
	jobs := rwJobs(t, ds.db, id)
	require.Len(t, jobs, 1)
	assert.Equal(t, string(runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredOpenAISubscription, "")), jobs[0].SourceID)
	stored, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
	require.NoError(t, err)
	assert.Contains(t, fmt.Sprint(stored.LLMConversation), "served gpt-5-sol")
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 1)
	assert.Contains(t, argv[0], "-m gpt-5-sol")
	assert.Contains(t, argv[0], "model_reasoning_effort=low")
	assert.Contains(t, argv[0], "workspace-write")
}

func TestRunnerWiring_DefaultCodexRestrictedLaunchCarriesToolsAndContext(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused=%v", refused), func(t *testing.T) {
			dir := t.TempDir()
			argvLog := filepath.Join(dir, "argv")
			home := filepath.Join(dir, "account")
			sessions := filepath.Join(home, "sessions", "2026", "11", "20")
			require.NoError(t, os.MkdirAll(sessions, 0o700))
			bin := filepath.Join(dir, "codex")
			require.NoError(t, os.WriteFile(bin, []byte(fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo 'codex-cli 0.170.0'; exit 0; fi
if [ "$1" = 'login' ]; then echo 'Logged in using ChatGPT'; exit 0; fi
case " $* " in
 *' mcp list --json '*) printf 'mcp: %%s\n' "$*" >> %q; echo '[{"name":"github"}]'; exit 0;;
esac
printf 'turn: %%s\n' "$*" >> %q
[ -n "$(cat)" ] || exit 43
[ -z "$OPENAI_API_KEY" ] || exit 44
m=''
while [ "$#" -gt 0 ]; do
 if [ "$1" = '-m' ]; then shift; m="$1"; fi
 shift
done
[ -n "$m" ] || exit 45
id='routing-default-codex'
echo '{"type":"session_meta","payload":{"id":"'"$id"'"}}' > %q/rollout-test-"$id".jsonl
echo '{"type":"turn_context","payload":{"model":"'"$m"'"}}' >> %q/rollout-test-"$id".jsonl
echo '{"type":"thread.started","thread_id":"'"$id"'"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"served '"$m"'"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`, argvLog, argvLog, sessions, sessions)), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
			t.Setenv("CODEX_HOME", home)
			t.Setenv("OPENAI_API_KEY", "must-not-reach-subscription")
			t.Setenv(codexInteractiveVerifiedEnv, "true")
			caps := filepath.Join(dir, "capabilities.yaml")
			require.NoError(t, os.WriteFile(caps, []byte("backends:\n  codex:\n    context_tiers: [default, 200k]\n    effort_levels: [default, low]\n"), 0o600))
			ds := bootSinkRoot(t, "capabilitiespath: "+caps+"\nllm:\n  defaultagentbackend: codex\n", func(ws string) { rwWriteWorkflows(t, ws) })
			ctx := adminCtxForLocalUser(t, ds)
			profile := provider.Profile{Alias: "rr-agent", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol", Effort: provider.EffortLow, ContextWindow: provider.Context200K, AllowedTools: []string{"Read", "Grep", "Glob", "Bash"}}
			if refused {
				profile.AllowedTools = []string{"Read"}
			}
			require.Empty(t, profile.Backend, "exercise deployment default, not an explicit codex backend")
			_, _, err := ds.profileSvc.Create(ctx, profile)
			require.NoError(t, err)
			id, _, err := ds.eng.Trigger(ctx, "rr-talk", nil, "normal")
			require.NoError(t, err)
			t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
			require.Eventually(t, func() bool {
				inst, err := ds.instanceRepo.Get(ctx, id)
				return err == nil && (inst.State == instance.StateWaiting || inst.State.IsTerminal())
			}, 20*time.Second, 25*time.Millisecond)
			inst, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			if refused {
				require.True(t, inst.State.IsTerminal())
				stored, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
				require.NoError(t, err)
				assert.Contains(t, fmt.Sprint(stored), "tool scope cannot be enforced")
				if data, err := os.ReadFile(argvLog); err == nil {
					assert.NotContains(t, string(data), "turn:")
				}
				return
			}
			require.Equal(t, instance.StateWaiting, inst.State)
			require.Eventually(t, func() bool {
				jobs := rwJobs(t, ds.db, id)
				return rwEveryOpPlacedOnBuiltin(jobs, map[string]string{"talk": "stream"})
			}, 5*time.Second, 25*time.Millisecond)
			jobs := rwJobs(t, ds.db, id)
			require.Len(t, jobs, 1)
			assert.Equal(t, string(runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredOpenAISubscription, "")), jobs[0].SourceID)
			stored, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
			require.NoError(t, err)
			assert.Contains(t, fmt.Sprint(stored.LLMConversation), "served gpt-5-sol")
			argv := readArgvLog(t, argvLog)
			require.Len(t, argv, 2)
			assert.Contains(t, argv[0], "mcp:")
			argv = argv[1:]
			assert.Contains(t, argv[0], "-m gpt-5-sol")
			assert.Contains(t, argv[0], "model_reasoning_effort=low")
			for _, want := range []string{"model_context_window=200000", "--strict-config", "features.plugins=false", "features.apps=false", `web_search="disabled"`, `mcp_servers={"github"={enabled=false}}`, "read-only"} {
				assert.Contains(t, argv[0], want)
			}
			assert.NotContains(t, argv[0], "workspace-write")

		})
	}
}
