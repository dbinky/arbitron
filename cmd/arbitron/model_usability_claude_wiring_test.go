package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initDomain supplies the real factory, binding, run pins and parser. Only the
// process leaf is replaced; it refuses missing model, prompt, cwd or agent key.
func TestUsabilityClaudeNonStreamingExactModelRoot(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, agent := range []bool{false, true} {
		for _, actual := range []string{"exact", "helper", "context-window", "unsupported-suffix", "unterminated-suffix", "suffix-trailing-data", "repeated-suffix", "fallback", "missing"} {
			t.Run(fmt.Sprintf("agent=%v/actual=%s", agent, actual), func(t *testing.T) {
				usage := map[string]any{}
				switch actual {
				case "exact", "helper":
					usage[model] = map[string]any{}
					if actual == "helper" {
						usage["claude-haiku-4-5"] = map[string]any{}
					}
				case "context-window":
					usage[model+"[1m]"] = map[string]any{}
				case "unsupported-suffix":
					usage[model+"[claude-root-secret]"] = map[string]any{}
				case "unterminated-suffix":
					usage[model+"[1m"] = map[string]any{}
				case "suffix-trailing-data":
					usage[model+"[1m]-fallback"] = map[string]any{}
				case "repeated-suffix":
					usage[model+"[1m][1m]"] = map[string]any{}
				case "fallback":
					usage["claude-opus-4-6"] = map[string]any{}
				}
				result, err := json.Marshal(map[string]any{"type": "result", "is_error": false, "result": "verified output", "session_id": "proof-session", "modelUsage": usage})
				require.NoError(t, err)
				dir := t.TempDir()
				argvLog := filepath.Join(dir, "argv.log")
				keyGuard := "[ \"$tools\" = '' ] && [ \"$skip\" = '' ] || exit 49"
				if agent {
					keyGuard = "[ \"$ANTHROPIC_API_KEY\" = 'claude-root-secret' ] || exit 46\n[ \"$ANTHROPIC_BASE_URL\" = 'https://api.anthropic.com' ] || exit 47\n[ \"$tools\" = 'Read' ] && [ \"$skip\" = 'yes' ] || exit 49"
				}
				script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = '--version' ]; then echo '2.1.301'; exit 0; fi
printf '%%s\n' "$*" >> %q
case " $* " in *' --model claude-sonnet-4-6 '*) ;; *) exit 42 ;; esac
case " $* " in *' --output-format stream-json '*) ;; *) exit 43 ;; esac
case " $* " in *' --fallback-model '*) exit 44 ;; esac
case "$PWD" in *workspaces/instances/*) ;; *) exit 45 ;; esac
tools=missing
skip=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --allowedTools) shift; tools="$1" ;;
    --dangerously-skip-permissions) skip=yes ;;
  esac
  shift
done
%s
prompt=$(cat)
[ "$prompt" = 'hello' ] || exit 48
printf '%%s\n' '%s'
`, argvLog, keyGuard, result)
				bin := filepath.Join(dir, "claude")
				require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
				kind := "llm_call"
				profile := provider.Profile{Alias: "proof", Kind: provider.ProfileKindChat, Model: model}
				if agent {
					kind = "agent_call"
					profile.Kind = provider.ProfileKindAgent
					profile.Backend = "claude-code"
					profile.ConnectionRef = "claude-api"
					profile.AllowedTools = []string{"Read"}
				}
				ds := bootSinkRoot(t, "llm:\n  binarypath: "+bin+"\n", func(workspace string) {
					writeWorkflow(t, workspace, "usability-claude", fmt.Sprintf(`{"name":"usability-claude","nodes":[{"id":"work","type":%q,"entrypoint":true,"config":{"model_profile":"proof","templateFile":"templates/p.md"}}],"edges":[]}`, kind))
					writePinTemplate(t, workspace, "usability-claude")
				})
				ctx := adminCtxForLocalUser(t, ds)
				if agent {
					require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "claude-api", Provider: "anthropic", BaseURL: "https://api.anthropic.com"}, "claude-root-secret"))
				}
				_, _, err = ds.profileSvc.Create(ctx, profile)
				require.NoError(t, err)
				id, _, err := ds.eng.Trigger(ctx, "usability-claude", nil, "normal")
				require.NoError(t, err)
				saved := waitTerminal(t, ds, id)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"proof": model}, saved.ModelPins)
				if actual == "exact" || actual == "helper" || actual == "context-window" {
					require.Equal(t, instance.StateCompleted, saved.State, "trace: %+v", trace)
					assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
					assert.Equal(t, map[string]any{"response": "verified output"}, trace[0].Output)
					require.NotNil(t, trace[0].LLMModel)
					assert.Equal(t, model, *trace[0].LLMModel)
				} else {
					require.Equal(t, instance.StateFailed, saved.State, "unverified output reached durable sink: %+v", trace)
					assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
					assert.Contains(t, fmt.Sprint(trace[0].Error), "model execution evidence")
					assert.Empty(t, trace[0].Output)
					assert.Empty(t, trace[0].LLMConversation)
					assert.NotContains(t, fmt.Sprint(trace[0].Error), "claude-root-secret")
				}
				require.Len(t, readArgvLog(t, argvLog), 1)
			})
		}
	}
}
