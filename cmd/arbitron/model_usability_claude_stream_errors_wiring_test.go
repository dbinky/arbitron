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

// The process emits the actual injected credential in a rejected evidence field.
// It must not escape through the durable initial error or a continuation error.
func TestUsabilityClaudeStreamingSecretFreeErrorsRoot(t *testing.T) {
	const model = "claude-sonnet-4-6"
	const secret = "claude-stream-root-secret"
	for _, failure := range []string{"wrong-model", "failed-result", "unsupported-suffix", "unterminated-suffix", "suffix-trailing-data", "repeated-suffix"} {
		expectedReason := "modelUsage"
		if failure == "failed-result" {
			expectedReason = "model execution evidence missing or inconsistent"
		}
		for _, continuation := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/continuation=%v", failure, continuation), func(t *testing.T) {
				dir := t.TempDir()
				mode, argvLog := filepath.Join(dir, "mode"), filepath.Join(dir, "argv")
				bin := filepath.Join(dir, "claude")
				require.NoError(t, os.WriteFile(bin, []byte(fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo '2.1.301'; exit 0; fi
echo "$*" >> %q
[ "$ANTHROPIC_API_KEY" = %q ] || exit 41
[ "$ANTHROPIC_BASE_URL" = 'https://api.anthropic.com' ] || exit 42
case " $* " in *' --model claude-sonnet-4-6 '*) ;; *) exit 43 ;; esac
case " $* " in *' --output-format stream-json '*) ;; *) exit 44 ;; esac
case " $* " in *' --fallback-model '*) exit 45 ;; esac
case " $* " in *' --allowedTools Read '*) ;; *) exit 46 ;; esac
case "$PWD" in *workspaces/instances/*) ;; *) exit 47 ;; esac
[ -n "$(cat)" ] || exit 48
mode=$(cat %q)
case "$mode" in
  wrong-model)
    printf '{"type":"result","result":"unaccepted output","modelUsage":{"%%s":{}}}\n' "$ANTHROPIC_API_KEY" ;;
  failed-result)
    printf '{"type":"result","is_error":true,"result":"credential rejected: %%s","modelUsage":{"claude-sonnet-4-6":{}}}\n' "$ANTHROPIC_API_KEY" ;;
  unsupported-suffix)
    printf '{"type":"result","result":"unaccepted output","modelUsage":{"claude-sonnet-4-6[%%s]":{}}}\n' "$ANTHROPIC_API_KEY" ;;
  unterminated-suffix)
    echo '{"type":"result","result":"unaccepted output","modelUsage":{"claude-sonnet-4-6[1m":{}}}' ;;
  suffix-trailing-data)
    echo '{"type":"result","result":"unaccepted output","modelUsage":{"claude-sonnet-4-6[1m]-fallback":{}}}' ;;
  repeated-suffix)
    echo '{"type":"result","result":"unaccepted output","modelUsage":{"claude-sonnet-4-6[1m][1m]":{}}}' ;;
  exact)
    echo '{"type":"assistant","message":{"content":[{"type":"text","text":"verified output"}]}}'
    echo '{"type":"result","result":"verified output","session_id":"stream-proof","modelUsage":{"claude-sonnet-4-6[1m]":{},"claude-haiku-4-5":{}}}' ;;
  *) exit 49 ;;
esac
`, argvLog, secret, mode)), 0o700))
				ds := bootSinkRoot(t, "llm:\n  binarypath: "+bin+"\n", func(workspace string) {
					writeWorkflow(t, workspace, "stream-errors", `{"name":"stream-errors","nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"proof","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}],"edges":[]}`)
					writePinTemplate(t, workspace, "stream-errors")
					require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "stream-errors", "templates", "o.md"), []byte("orchestrate"), 0o600))
				})
				ctx := adminCtxForLocalUser(t, ds)
				require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "claude-api", Provider: "anthropic", BaseURL: "https://api.anthropic.com"}, secret))
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "proof", Kind: provider.ProfileKindAgent, Backend: "claude-code", ConnectionRef: "claude-api", Model: model, AllowedTools: []string{"Read"}})
				require.NoError(t, err)
				initialMode := failure
				if continuation {
					initialMode = "exact"
				}
				require.NoError(t, os.WriteFile(mode, []byte(initialMode), 0o600))
				id, _, err := ds.eng.Trigger(ctx, "stream-errors", nil, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					saved, err := ds.instanceRepo.Get(ctx, id)
					return err == nil && (saved.State == instance.StateWaiting || saved.State == instance.StateFailed)
				}, 20*time.Second, 25*time.Millisecond)
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
				before, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
				require.NoError(t, err)
				if continuation {
					require.Equal(t, instance.StateWaiting, saved.State)
					initialMessages := before.LLMConversation["messages"].([]any)
					require.Len(t, initialMessages, 2)
					assert.Equal(t, map[string]any{"role": "assistant", "content": "verified output"}, initialMessages[1])
					assert.Equal(t, "stream-proof", before.LLMConversation["sessionId"])
					require.NoError(t, os.WriteFile(mode, []byte(failure), 0o600))
					err = ds.conversationHandler.HandleMessage(ctx, id, "continue")
					if assert.Error(t, err) {
						assert.Contains(t, err.Error(), expectedReason)
						assert.NotContains(t, err.Error(), secret)
					}
					after, err := ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
					require.NoError(t, err)
					assert.Equal(t, before.LLMConversation, after.LLMConversation)
					assert.Equal(t, before.Output, after.Output)
					assert.NotContains(t, fmt.Sprint(after), secret)
					// The rejected turn must leave the working session resumable.
					require.NoError(t, os.WriteFile(mode, []byte("exact"), 0o600))
					require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "retry exact"))
					after, err = ds.nodeExecRepo.GetByInstanceAndNode(ctx, id, "talk")
					require.NoError(t, err)
					messages := after.LLMConversation["messages"].([]any)
					require.Len(t, messages, 4)
					assert.Equal(t, map[string]any{"role": "user", "content": "retry exact"}, messages[2])
					assert.Equal(t, map[string]any{"role": "assistant", "content": "verified output"}, messages[3])
					assert.Equal(t, "stream-proof", after.LLMConversation["sessionId"])
					argv := readArgvLog(t, argvLog)
					require.Len(t, argv, 3)
					assert.Contains(t, argv[1], "--resume stream-proof")
					assert.Contains(t, argv[2], "--resume stream-proof")
				} else {
					require.Equal(t, instance.StateFailed, saved.State)
					assert.Equal(t, instance.NodeStatusFailed, before.Status)
					require.NotNil(t, before.Error)
					assert.Contains(t, fmt.Sprint(before.Error), expectedReason)
					assert.NotContains(t, fmt.Sprint(before.Error), secret)
					assert.Empty(t, before.Output)
					assert.Empty(t, before.LLMConversation)
					require.Len(t, readArgvLog(t, argvLog), 1)
				}
				saved, err = ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
			})
		}
	}
}
