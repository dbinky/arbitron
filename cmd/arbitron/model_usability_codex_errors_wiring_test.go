package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real root supplies pins and the actual Codex account environment. Only
// the process leaf is replaced; its rejected rollout contains the account token.
func TestUsabilityCodexRolloutSecretFreeErrorsRoot(t *testing.T) {
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	const model = "gpt-5-sol"
	const thread = "019f00b8-f154-7bb1-ae4b-2adb20c4dac9"
	const secret = "codex-root-account-access-token"
	for _, field := range []string{"model", "session"} {
		for _, path := range []string{"batch", "interactive", "resume preflight"} {
			t.Run(field+"/"+path, func(t *testing.T) {
				dir := t.TempDir()
				home := filepath.Join(dir, "account")
				sessions := filepath.Join(home, "sessions", "2026", "10", "16")
				require.NoError(t, os.MkdirAll(sessions, 0o700))
				auth := map[string]any{"tokens": map[string]string{"access_token": secret}}
				authJSON, err := json.Marshal(auth)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), authJSON, 0o600))
				mode, argvLog := filepath.Join(dir, "mode"), filepath.Join(dir, "argv")
				rollout := filepath.Join(sessions, "rollout-test-"+thread+".jsonl")
				script := fmt.Sprintf(`#!%s
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
if args == ["--version"]:
    print("codex-cli 0.171.0")
    sys.exit(0)
with open(%q, "a") as log:
    log.write(" ".join(args) + "\n")
assert "OPENAI_API_KEY" not in os.environ
assert os.environ["CODEX_HOME"] == %q
assert "workspaces/instances/" in os.getcwd()
assert sys.stdin.read().strip()
assert args[0] == "exec"
assert "--json" in args
assert args[args.index("-m") + 1] == "gpt-5-sol"
assert "model_reasoning_effort=low" in args
if "--skip-git-repo-check" in args:
    assert "sandbox_workspace_write.network_access=false" in args
else:
    assert args[1] == "--dangerously-bypass-approvals-and-sandbox"
if "resume" in args:
    assert args[args.index("resume") + 1] == %q
else:
    assert Path(args[args.index("--cd") + 1]).resolve() == Path.cwd().resolve()
with open(Path(os.environ["CODEX_HOME"]) / "auth.json") as account:
    token = json.load(account)["tokens"]["access_token"]
assert token == %q
mode = Path(%q).read_text()
records = [
    {"type": "session_meta", "payload": {"id": token if mode == "session" else %q}},
    {"type": "turn_context", "payload": {"model": token if mode == "model" else "gpt-5-sol"}},
]
# A matching later context cannot erase the rejected context.
if mode == "model":
    records.append({"type": "turn_context", "payload": {"model": "gpt-5-sol"}})
rollout = Path(%q)
if "resume" in args:
    records = [records[1]]
with rollout.open("a" if "resume" in args else "w") as proof:
    for record in records:
        proof.write(json.dumps(record) + "\n")
print(json.dumps({"type": "thread.started", "thread_id": %q}))
print(json.dumps({"type": "item.completed", "item": {"type": "agent_message", "text": "verified codex output"}}))
print(json.dumps({"type": "turn.completed", "usage": {"input_tokens": 1, "output_tokens": 1}}))
`, python, argvLog, home, thread, secret, mode, thread, rollout, thread)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700))
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("CODEX_HOME", home)
				t.Setenv("OPENAI_API_KEY", "must-not-reach-codex")
				t.Setenv(codexInteractiveVerifiedEnv, "true")
				kind := "agent_call"
				if path != "batch" {
					kind = "interactive_agent"
				}
				ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n", func(workspace string) {
					writeWorkflow(t, workspace, "codex-errors", fmt.Sprintf(`{"name":"codex-errors","nodes":[{"id":"work","type":%q,"entrypoint":true,"config":{"model_profile":"proof","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, kind))
					writePinTemplate(t, workspace, "codex-errors")
					require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "codex-errors", "templates", "o.md"), []byte("orchestrate"), 0o600))
				})
				ctx := adminCtxForLocalUser(t, ds)
				_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "proof", Kind: provider.ProfileKindAgent, Model: model, Effort: provider.EffortLow})
				require.NoError(t, err)
				initial := field
				if path == "resume preflight" {
					initial = "exact"
				}
				require.NoError(t, os.WriteFile(mode, []byte(initial), 0o600))
				id, _, err := ds.eng.Trigger(ctx, "codex-errors", nil, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					saved, err := ds.instanceRepo.Get(ctx, id)
					return err == nil && (saved.State.IsTerminal() || saved.State == instance.StateWaiting)
				}, 20*time.Second, 25*time.Millisecond)
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
				before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "work")
				require.NoError(t, err)
				if path == "resume preflight" {
					require.Equal(t, instance.StateWaiting, saved.State)
					require.Contains(t, fmt.Sprint(before.LLMConversation), "verified codex output")
					good, err := os.ReadFile(rollout)
					require.NoError(t, err)
					bad := map[string]any{"type": "turn_context", "payload": map[string]string{"model": secret}}
					if field == "session" {
						bad = map[string]any{"type": "session_meta", "payload": map[string]string{"id": secret}}
					}
					badJSON, err := json.Marshal(bad)
					require.NoError(t, err)
					corrupt := append(append([]byte(nil), good...), append(badJSON, '\n')...)
					if field == "session" {
						// A single wrong identity reaches the session-correlation guard.
						corrupt = append(badJSON, '\n')
					}
					require.NoError(t, os.WriteFile(rollout, corrupt, 0o600))
					err = ds.conversationHandler.HandleMessage(ctx, id, "must not resume")
					require.ErrorContains(t, err, "executed-model evidence rejected")
					assert.NotContains(t, err.Error(), secret)
					after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "work")
					require.NoError(t, err)
					assert.Equal(t, before.LLMConversation, after.LLMConversation)
					require.Len(t, readArgvLog(t, argvLog), 1, "corrupt evidence must refuse before spawn")
					require.NoError(t, os.WriteFile(rollout, good, 0o600))
					require.NoError(t, os.WriteFile(mode, []byte("exact"), 0o600))
					require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "retry exact"))
					after, err = ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "work")
					require.NoError(t, err)
					messages := after.LLMConversation["messages"].([]any)
					require.Len(t, messages, 4)
					assert.Equal(t, map[string]any{"role": "user", "content": "retry exact"}, messages[2])
					assert.Equal(t, map[string]any{"role": "assistant", "content": "verified codex output"}, messages[3])
					assert.Equal(t, thread, after.LLMConversation["sessionId"])
					assert.NotContains(t, fmt.Sprint(after), secret)
					argv := readArgvLog(t, argvLog)
					require.Len(t, argv, 2)
					assert.Contains(t, argv[1], "exec resume "+thread)
				} else {
					require.Equal(t, instance.StateFailed, saved.State)
					assert.Equal(t, instance.NodeStatusFailed, before.Status)
					assert.Contains(t, fmt.Sprint(before.Error), "executed-model evidence rejected")
					assert.NotContains(t, fmt.Sprint(before.Error), secret)
					assert.NotContains(t, fmt.Sprint(before.Output, before.LLMConversation), "verified codex output")
					require.Len(t, readArgvLog(t, argvLog), 1)
					// Exact proof must still execute and reach the durable sink.
					require.NoError(t, os.WriteFile(mode, []byte("exact"), 0o600))
					require.NoError(t, ds.eng.Retry(ctx, id, nil))
					require.Eventually(t, func() bool {
						saved, err := ds.instanceRepo.Get(ctx, id)
						return err == nil && (saved.State == instance.StateCompleted || saved.State == instance.StateWaiting)
					}, 20*time.Second, 25*time.Millisecond)
					trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
					require.NoError(t, err)
					require.Len(t, trace, 2)
					var successes int
					for _, node := range trace {
						assert.NotContains(t, fmt.Sprint(node.Error, node.Output, node.LLMConversation), secret)
						if node.Status == instance.NodeStatusSucceeded {
							successes++
							assert.Contains(t, fmt.Sprint(node.Output, node.LLMConversation), "verified codex output")
							require.NotNil(t, node.LLMModel)
							assert.Equal(t, model, *node.LLMModel)
						}
					}
					assert.Equal(t, 1, successes)
					require.Len(t, readArgvLog(t, argvLog), 2)
				}
				saved, err = ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				want := instance.StateCompleted
				if path != "batch" {
					want = instance.StateWaiting
				}
				assert.Equal(t, want, saved.State)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
			})
		}
	}
}
