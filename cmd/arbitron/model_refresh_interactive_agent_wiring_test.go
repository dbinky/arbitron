package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A following claude-code agent profile consumed by an interactive_agent node
// as well as standalone advances only when the candidate is proven on BOTH
// routes. The interactive route is probed by streaming through the production
// stream selector's claude-code adapter, tool-free, in the workspace probe
// directory; the stored profile (and so a new run) then uses the new model. A
// stream whose modelUsage names another model keeps the working model.
func TestModelRefreshRootClaudeCodeInteractiveAgentRouteProvesCandidateFromStream(t *testing.T) {
	const oldModel, newModel = "claude-sonnet-4-5", "claude-sonnet-4-6"
	for _, tc := range []struct {
		name     string
		fallback string
		want     string
	}{
		{name: "executed model matches the candidate", want: newModel},
		{name: "silent fallback keeps the working model", fallback: "claude-haiku-4-5", want: oldModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argvLog := filepath.Join(dir, "argv")
			bin := filepath.Join(dir, "claude")
			if tc.fallback != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "executed-model"), []byte(tc.fallback), 0o600))
			}
			script := "#!/bin/sh\n" +
				"case \"$1\" in --version) echo '2.1.330 (Claude Code)'; exit 0 ;; auth) echo '{\"loggedIn\":true}'; exit 0 ;; esac\n" +
				"echo \"$PWD $*\" >> '" + argvLog + "'\n" +
				"cat > /dev/null\n" +
				"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
				"if [ -f '" + filepath.Join(dir, "executed-model") + "' ]; then m=$(cat '" + filepath.Join(dir, "executed-model") + "'); fi\n" +
				"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
			require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))

			ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n  skippermissions: true\n  allowedtools: [Bash, Write]\nprofiles:\n"+
				"  - alias: builder\n    kind: agent\n    backend: claude-code\n    model: "+oldModel+"\n    selection_mode: following\n"+
				"    follow_target:\n      provider: anthropic\n      tier: sonnet\n", func(workspace string) {
				writeWorkflow(t, workspace, "refresh-talk", `{"name":"refresh-talk","nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"builder","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "refresh-talk")
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "refresh-talk", "templates", "o.md"), []byte("orchestrate"), 0o600))
			})
			ctx := adminCtxForLocalUser(t, ds)
			storeConfiguredFollowing(t, ctx, ds, "builder")
			catalogURL := "https://catalog.example.test/api.json"
			_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
			require.NoError(t, err)

			deps := ds.refreshDeps
			deps.Fetcher = &fixtureFetcher{body: claudeCatalogFixture}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)

			stored, err := ds.profileSvc.Get(ctx, "builder")
			require.NoError(t, err)
			assert.Equal(t, tc.want, stored.Model)
			assert.Equal(t, tc.want == newModel, len(report.Changes) == 1)

			argv := readArgvLog(t, argvLog)
			var probes int
			for _, line := range argv {
				if !strings.Contains(line, "--model "+newModel) {
					continue
				}
				probes++
				assert.Contains(t, line, "--allowedTools  ", "a probe runs with the empty allowlist")
				assert.NotContains(t, line, "--dangerously-skip-permissions")
				assert.NotContains(t, line, "Bash,Write", "the agent tool scope never reaches a probe")
				assertPrivateProbeDir(t, line, deps.ProbeDir)
			}
			// The standalone scan also probes the candidate on every other path, so
			// this is a floor: the batch and the interactive agent route are each
			// probed through the CLI.
			assert.GreaterOrEqual(t, probes, 2, "the batch and the interactive agent route are each probed through the CLI")
		})
	}
}
