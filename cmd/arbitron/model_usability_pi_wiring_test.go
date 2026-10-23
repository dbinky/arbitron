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

// Only the external process is replaced. initDomain constructs the real
// inventory, snapshotter, resolver, factory and Pi adapter from persisted data.
func TestUsabilityPiExactModelRoot(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"openai", "gpt-5-sol"}, {"openai", "fallback"}, {"openai", ""},
		// Paired row 5: the right model served by another provider is not X.
		{"openrouter", "gpt-5-sol"},
	} {
		actual := tc.model
		t.Run("actual="+tc.provider+"/"+actual, func(t *testing.T) {
			dir := t.TempDir()
			argvLog := filepath.Join(dir, "argv.log")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then echo '0.87.0'; exit 0; fi
printf '%%s\n' "$*" >> %q
case " $* " in *" --print --mode json --no-session --model openai/gpt-5-sol "*) ;; *) exit 42 ;; esac
[ "$OPENAI_API_KEY" = 'pi-root-secret' ] || exit 43
case "$PWD" in *workspaces/instances/*) ;; *) exit 44 ;; esac
prompt=$(cat)
[ "$prompt" = 'hello' ] || exit 45
printf '%%s\n' '{"type":"message_end","message":{"role":"assistant","provider":%q,"model":%q,"stopReason":"stop","content":[{"type":"text","text":"verified output"}]}}'
`, argvLog, tc.provider, actual)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ds := bootSinkRoot(t, "", func(workspace string) {
				writeWorkflow(t, workspace, "usability-pi", `{"name":"usability-pi","nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"backend":"pi","model_profile":"agent","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "usability-pi")
			})
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "pi-api", Provider: "openai", BaseURL: "https://api.openai.com"}, "pi-root-secret"))
			_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", ConnectionRef: "pi-api", Model: "openai/gpt-5-sol"})
			require.NoError(t, err)
			id, _, err := ds.eng.Trigger(ctx, "usability-pi", nil, "normal")
			require.NoError(t, err)
			saved := waitTerminal(t, ds, id)
			trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			require.Len(t, trace, 1)
			assert.Equal(t, map[string]string{"agent": "openai/gpt-5-sol"}, saved.ModelPins)
			if tc.provider == "openai" && actual == "gpt-5-sol" {
				require.Equal(t, instance.StateCompleted, saved.State, "trace: %+v", trace)
				assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Output), "verified output")
				require.NotNil(t, trace[0].LLMModel)
				assert.Equal(t, "openai/gpt-5-sol", *trace[0].LLMModel)
			} else {
				require.Equal(t, instance.StateFailed, saved.State, "unverified output reached the durable run sink: %+v", trace)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Error), "model execution evidence")
				assert.NotContains(t, fmt.Sprint(trace[0].Output), "verified output")
			}
			require.Len(t, readArgvLog(t, argvLog), 1)
		})
	}
}
