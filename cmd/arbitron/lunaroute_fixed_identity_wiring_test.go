package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/opencode"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteFixedIdentitySaveRefusals(t *testing.T) {
	ds, call := lunaRouteMCP(t)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": lrRootKey})
	require.False(t, bad, text)
	for _, backend := range []string{"", "claude-code", "opencode", "codex", "pi"} {
		for i, model := range []string{"glm-999.9", "claude-sonnet-5-lr999", "claude-sonnet-5-lr1", "deepseek-4.1-flash-1017", "lunaroute/glm-5.3", "glm-5.3-background"} {
			t.Run(backend+"/"+model, func(t *testing.T) {
				kind := "agent"
				if backend == "" {
					kind = "chat"
				}
				alias := fmt.Sprintf("refused-%s-%d", backend, i)
				bad, text := call("profile_create", map[string]any{"alias": alias, "kind": kind, "connection_ref": "lr", "backend": backend, "model": model})
				require.True(t, bad)
				assert.Contains(t, text, "reviewed fixed LunaRoute id")
				assert.NotContains(t, text, lrRootKey)
				var rows int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias=?`, alias).Scan(&rows))
				assert.Zero(t, rows, "a refused selection cannot enter the pin/launch path")
			})
		}
	}
}

// Exercise the real MCP save/trigger, pin snapshotter, factory, subprocess and
// SQLite evidence sink. The binaries are protocol fixtures, not live evidence.
func TestLunaRouteFixedIdentityAgentConsumers(t *testing.T) {
	for _, backend := range []string{"claude-code", "opencode"} {
		for _, selected := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
			t.Run(backend+"/"+selected, func(t *testing.T) {
				sameFamily := "glm-5.3-flash"
				if selected == sameFamily {
					sameFamily = "glm-5.3"
				} else if selected == "deepseek-4.1-flash" {
					sameFamily = "deepseek-4.1-flash-changed"
				}
				for _, reported := range []string{selected, sameFamily, "claude-sonnet-5-lr1", ""} {
					name := reported
					if name == "" {
						name = "missing"
					}
					t.Run(name, func(t *testing.T) {
						dir := t.TempDir()
						var bin, argv, answer, source, reportedProvider string
						if backend == "claude-code" {
							bin, argv, _ = fakeLunaRouteClaudeModel(t, dir, selected, reported)
							answer, source = "lunaroute ok", "claude_model_usage"
						} else {
							argv, _, _ = fakeLunaRouteOpencodeModel(t, dir, selected, reported)
							t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
							answer, source, reportedProvider = "verified output", "opencode_export", "lunaroute"
						}
						gw := &agentListingGateway{models: []string{selected}}
						ds := bootLunaRouteAgentModel(t, backend, bin, selected, gw)
						call := lunaRouteToolCaller(t, ds)
						id := triggerViaMCP(t, call, "lr-agent")
						in := waitTerminal(t, ds, id)
						assert.Equal(t, instance.ModelPinsVersionSnapshot, in.ModelPinsVersion)
						assert.Equal(t, map[string]string{"lr-agent": selected}, in.ModelPins)
						calls := readArgvLog(t, argv)
						if backend == "claude-code" {
							require.Len(t, calls, 1)
						} else {
							require.Len(t, calls, 2, "actual run and correlated export, no fallback")
						}
						cwd, err := os.ReadFile(argv + ".cwd")
						require.NoError(t, err)
						dirs := strings.Split(strings.TrimSpace(string(cwd)), "\n")
						require.Len(t, dirs, len(calls))
						for _, dir := range dirs {
							assert.Equal(t, in.Variables["workspace"], dir)
						}
						require.NoError(t, checkAcceptanceLog(ds.cfg.Logging.File, lrRootKey))
						if reported != selected {
							require.Equal(t, instance.StateFailed, in.State)
							wantError := "claudecli: model execution evidence missing or inconsistent"
							if backend == "opencode" {
								wantError = opencode.ErrModelExecutionEvidence.Error()
							}
							assert.Contains(t, nodeError(t, ds, id), wantError)
							var succeeded int
							require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM node_executions WHERE instance_id=? AND status='succeeded'`, id.String()).Scan(&succeeded))
							assert.Zero(t, succeeded, "wrong or absent evidence never persists success")
							assert.Equal(t, []string{"GET https://gw.lunaroute.com/v1/models auth=ok"}, gw.seen(), "diagnosis never substitutes a model or retries inference")
							return
						}
						if in.State != instance.StateCompleted {
							t.Fatalf("run ended %s: %s", in.State, nodeError(t, ds, id))
						}
						assert.Equal(t, map[string]any{"response": answer}, successfulNodeOutput(t, ds, id, "work"))
						node, err := ds.nodeExecRepo.GetByInstanceAndNode(adminCtxForLocalUser(t, ds), id, "work")
						require.NoError(t, err)
						require.NotNil(t, node)
						raw, err := json.Marshal(node.LLMConversation)
						require.NoError(t, err)
						var trace struct {
							Evidence []engine.ModelEvidence `json:"model_evidence"`
						}
						require.NoError(t, json.Unmarshal(raw, &trace))
						assert.Equal(t, []engine.ModelEvidence{{Model: selected, Provider: reportedProvider, Source: source}}, trace.Evidence)
						assert.Empty(t, gw.seen(), "successful execution does not need a live catalog")
					})
				}
			})
		}
	}
}
