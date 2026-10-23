package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initDomain feeds persisted credentials and run pins to Pi. The process leaf
// copies the actual injected credential into a failed assistant's errorMessage.
func TestUsabilityPiSecretFreeErrorsRoot(t *testing.T) {
	const secret = "pi-failure-root-secret"
	const model = "openai/gpt-5-sol"
	for _, reason := range []string{"error", "aborted"} {
		for _, exitCode := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/exit=%d", reason, exitCode), func(t *testing.T) {
				dir := t.TempDir()
				mode, argvLog := filepath.Join(dir, "mode"), filepath.Join(dir, "argv")
				script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo '0.87.0'; exit 0; fi
printf '%%s\n' "$*" >> %q
[ "$*" = '--print --mode json --no-session --model openai/gpt-5-sol' ] || exit 42
[ "$OPENAI_API_KEY" = %q ] || exit 43
case "$PWD" in *workspaces/instances/*) ;; *) exit 44 ;; esac
[ "$(cat)" = 'hello' ] || exit 45
mode=$(cat %q)
if [ "$mode" = 'failed' ]; then
  printf '{"type":"message_end","message":{"role":"assistant","provider":"openai","model":"gpt-5-sol","stopReason":%q,"errorMessage":"credential rejected: %%s","content":[{"type":"text","text":"unaccepted output"}]}}\n' "$OPENAI_API_KEY"
fi
# A later successful message cannot erase the earlier failure.
printf '%%s\n' '{"type":"message_end","message":{"role":"assistant","provider":"openai","model":"gpt-5-sol","stopReason":"stop","content":[{"type":"text","text":"verified output"}]}}'
if [ "$mode" = 'failed' ]; then exit %d; fi
[ "$mode" = 'exact' ] || exit 46
`, argvLog, secret, mode, reason, exitCode)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700))
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				ds := bootSinkRoot(t, "", func(workspace string) {
					writeWorkflow(t, workspace, "pi-errors", `{"name":"pi-errors","nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"backend":"pi","model_profile":"proof","templateFile":"templates/p.md"}}],"edges":[]}`)
					writePinTemplate(t, workspace, "pi-errors")
				})
				ctx := adminCtxForLocalUser(t, ds)
				require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "pi-api", Provider: "openai", BaseURL: "https://api.openai.com"}, secret))
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "proof", Kind: provider.ProfileKindAgent, Backend: "claude-code", ConnectionRef: "pi-api", Model: model})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(mode, []byte("failed"), 0o600))
				id, _, err := ds.eng.Trigger(ctx, "pi-errors", nil, "normal")
				require.NoError(t, err)
				saved := waitTerminal(t, ds, id)
				require.Equal(t, instance.StateFailed, saved.State)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				require.NotNil(t, trace[0].Error)
				if exitCode == 0 {
					assert.Contains(t, fmt.Sprint(trace[0].Error), "pi: turn failed")
				} else {
					// A pinned run surfaces only the sanitized process failure.
					assert.Contains(t, fmt.Sprint(trace[0].Error), "pi: mission failed")
					assert.Contains(t, fmt.Sprint(trace[0].Error), "harness process failed")
				}
				assert.NotContains(t, fmt.Sprint(trace[0].Error), secret)
				assert.NotContains(t, fmt.Sprint(trace[0].Error), "credential rejected")
				assert.Empty(t, trace[0].Output)
				assert.Empty(t, trace[0].LLMConversation)
				require.Len(t, readArgvLog(t, argvLog), 1)

				// The same persisted run must remain retryable with exact proof.
				require.NoError(t, os.WriteFile(mode, []byte("exact"), 0o600))
				require.NoError(t, ds.eng.Retry(ctx, id, nil))
				saved = waitTerminal(t, ds, id)
				require.Equal(t, instance.StateCompleted, saved.State)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
				trace, err = ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 2)
				var success int
				for _, node := range trace {
					assert.NotContains(t, fmt.Sprint(node.Error), secret)
					assert.NotContains(t, fmt.Sprint(node.Output), secret)
					assert.NotContains(t, fmt.Sprint(node.Output), "unaccepted output")
					if node.Status == instance.NodeStatusSucceeded {
						success++
						assert.Contains(t, fmt.Sprint(node.Output), "verified output")
						require.NotNil(t, node.LLMModel)
						assert.Equal(t, model, *node.LLMModel)
					}
				}
				assert.Equal(t, 1, success)
				require.Len(t, readArgvLog(t, argvLog), 2)
			})
		}
	}
}
