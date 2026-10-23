package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/require"
)

func probeClaudeAuthState(generation int) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"access-%d","refreshToken":"refresh-%d","expiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max","rateLimitTier":"default"}}`, generation, generation, generation)
}

func probeClaudeGlobalState(account string, generation int) string {
	return fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":"org-a","profileFetchedAt":%d},"numStartups":%d,"tipsHistory":{"tip":%d},"skillUsage":{"s":%d},"mcpServers":{"s":{"command":"original"}},"projects":{"/work/other":{"mcpServers":{"p":{"command":"original"}},"lastCost":%d,"lastSessionId":"session-%d","lastDuration":%d}}}`, account, generation, generation, generation, generation, generation, generation, generation)
}

// The production inventory admits the turn, the actual launcher runs the fake
// binary, and confirmCurrentBinding consumes the rewritten state after I/O.
// Raw global-state hashing fails the counter rewrite; omitting state files fails the edit cases;
// omitting modelUsage verification fails the wrong/missing record cases.
func TestModelProbeClaudeSelfRefreshAtRoot(t *testing.T) {
	for _, relocated := range []bool{false, true} {
		for _, mode := range []string{modelcatalog.ExecutionModeBatch, modelcatalog.ExecutionModeInteractive} {
			for _, outcome := range []string{"global rewrite", "opaque token replacement", "account switch", "settings edit", "MCP edit", "project MCP edit", "launch cwd MCP edit", "run workspace entries", "partial auth", "partial global", "wrong", "missing", "account before launch", "settings before launch", "project settings edit", "project settings before launch"} {
				t.Run(fmt.Sprintf("relocated=%t/%s/%s", relocated, mode, outcome), func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv("HOME", dir)
					configDir, global := filepath.Join(dir, ".claude"), filepath.Join(dir, ".claude.json")
					t.Setenv("CLAUDE_CONFIG_DIR", "")
					if relocated {
						configDir = filepath.Join(dir, "relocated")
						global = filepath.Join(configDir, ".claude.json")
						t.Setenv("CLAUDE_CONFIG_DIR", configDir)
					}
					require.NoError(t, os.MkdirAll(configDir, 0700))
					write := func(path, body string) { t.Helper(); require.NoError(t, os.WriteFile(path, []byte(body), 0600)) }
					auth := filepath.Join(configDir, ".credentials.json")
					write(auth, probeClaudeAuthState(1))
					write(global, probeClaudeGlobalState("account-a", 1))
					const model = "claude-native-preview-20261018"
					bin, log := filepath.Join(dir, "claude"), filepath.Join(dir, "argv")
					require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0700))
					ds := bootSinkRoot(t, "profiles: []\nllm:\n  binarypath: "+bin+"\n", func(ws string) {
						writeWorkflow(t, ws, "self-refresh", `{"name":"self-refresh","nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"builder"}}],"edges":[]}`)
					})
					ctx := adminCtxForLocalUser(t, ds)
					_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "builder", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-sonnet-4-6"})
					require.NoError(t, err)
					renewedAuth, renewedGlobal := probeClaudeAuthState(1), probeClaudeGlobalState("account-a", 2)
					switch outcome {
					case "opaque token replacement":
						renewedAuth = probeClaudeAuthState(2)
					case "account switch", "account before launch":
						renewedGlobal = probeClaudeGlobalState("account-b", 2)
					case "MCP edit":
						renewedGlobal = strings.Replace(renewedGlobal, "original", "changed", 1)
					case "project MCP edit":
						// The probe reads only its own launch directory's entry.
						renewedGlobal = strings.Replace(renewedGlobal, `"projects":{`, fmt.Sprintf(`"projects":{%q:{"mcpServers":{"p":{"command":"changed"}}},`, ds.refreshDeps.ProbeDir), 1)
					case "launch cwd MCP edit":
						// Claude actually launches in the private <ProbeDir>/check-* child
						// and reads that cwd's entry; @CWD@ becomes its real $PWD (G63).
						renewedGlobal = strings.Replace(renewedGlobal, `"projects":{`, `"projects":{"@CWD@":{"mcpServers":{"p":{"command":"changed"}}},`, 1)
					case "run workspace entries":
						// Real runs elsewhere create default entries and operators edit
						// other workspaces; the probe's own first entry is default too.
						renewedGlobal = strings.Replace(renewedGlobal, `"p":{"command":"original"}`, `"p":{"command":"changed"}`, 1)
						renewedGlobal = strings.Replace(renewedGlobal, `"projects":{`, fmt.Sprintf(`"projects":{"/work/run-2":{"allowedTools":[],"mcpServers":{},"hasTrustDialogAccepted":false,"lastCost":1},%q:{"allowedTools":[],"mcpContextUris":[],"lastSessionId":"probe"},"@CWD@":{"allowedTools":[],"mcpServers":{},"lastSessionId":"probe","lastCost":1},`, ds.refreshDeps.ProbeDir), 1)
					case "partial auth":
						renewedAuth = `{"secret":"sentinel-secret"`
					case "partial global":
						renewedGlobal = `{"secret":"sentinel-secret"`
					}
					write(filepath.Join(dir, "renewed-auth"), renewedAuth)
					write(filepath.Join(dir, "renewed-global"), renewedGlobal)
					mutation := fmt.Sprintf("cat '%s/renewed-auth' > '%s.tmp'\nmv '%s.tmp' '%s'\nsed \"s|@CWD@|$(pwd -P)|g\" '%s/renewed-global' > '%s.tmp'\nmv '%s.tmp' '%s'\n", dir, auth, auth, auth, dir, global, global, global)
					if outcome == "settings edit" {
						mutation += fmt.Sprintf("echo '{\"model\":\"other\"}' > '%s/settings.json'\n", configDir)
					}
					// Claude reads project settings from its working directory; the
					// probe's must bind the revision like the user settings do.
					projectSettings := filepath.Join(ds.refreshDeps.ProbeDir, ".claude", "settings.json")
					const otherEndpoint = `{"env":{"ANTHROPIC_BASE_URL":"https://different.invalid"}}`
					if outcome == "project settings edit" {
						mutation += fmt.Sprintf("mkdir -p '%s'\necho '%s' > '%s'\n", filepath.Dir(projectSettings), otherEndpoint, projectSettings)
					}
					// Assert the real launch's working directory, model, format and
					// tool restriction before mutating the harness-owned files.
					script := strings.NewReplacer("@BACKEND@", "claude-code", "@VERSION@", "2.1.303", "@MODEL@", model, "@OUTCOME@", outcome, "@ARGV@", log, "@WORKDIR@", ds.refreshDeps.ProbeDir, "@AUTH@", "").Replace(nativeHarnessScript)
					script = strings.Replace(script, ` echo '{"type":"result"`, mutation+` echo '{"type":"result"`, 1)
					require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
					snap, err := ds.pathInventory.Snapshot(ctx)
					require.NoError(t, err)
					var path modelcatalog.ExecutionPath
					for _, p := range snap.Paths {
						if p.Backend == "claude-code" && p.Mode == mode {
							for _, alias := range p.Aliases {
								if alias == "builder" {
									path = p
								}
							}
						}
					}
					require.NotEmpty(t, path.Key)
					beforeLaunch := strings.HasSuffix(outcome, "before launch")
					if outcome == "account before launch" {
						write(global, renewedGlobal)
					}
					if outcome == "settings before launch" {
						write(filepath.Join(configDir, "settings.json"), `{"model":"other"}`)
					}
					if outcome == "project settings before launch" {
						require.NoError(t, os.MkdirAll(filepath.Dir(projectSettings), 0o700))
						write(projectSettings, otherEndpoint)
					}
					ev, err := ds.modelRefresh.probe.Check(ctx, path, modelidentity.Entry{Provider: "anthropic", CanonicalID: "anthropic/" + model, NativeID: model})
					require.NoError(t, err)
					confirms := outcome == "global rewrite" || outcome == "run workspace entries"
					require.Equal(t, confirms, ev.Confirmed(), "post-launch %s must yield the right evidence state", outcome)
					if confirms {
						require.NotEmpty(t, ev.ProofRef)
						current, err := ds.pathInventory.Snapshot(ctx)
						require.NoError(t, err)
						require.Equal(t, snap.Revision, current.Revision, "normal CLI writes cannot invalidate publication's gate revision")
					} else {
						require.Empty(t, ev.ProofRef)
					}
					if beforeLaunch {
						_, err := os.Stat(log)
						require.True(t, os.IsNotExist(err), "stale account/config cannot launch")
					} else {
						require.NotEmpty(t, readArgvLog(t, log), "post-launch mutation must actually run")
						got, err := os.ReadFile(auth)
						require.NoError(t, err)
						require.Equal(t, renewedAuth, string(got))
					}
				})
			}
		}
	}
}
