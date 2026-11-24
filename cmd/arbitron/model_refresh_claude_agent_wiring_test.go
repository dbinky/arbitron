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

// A following agent profile on the claude-code backend is advanced only on the
// CLI's own modelUsage proof from a tool-disabled probe: the root graph hands
// the agent usability lane the production factory and the workspace probe
// directory, the real agent adapter launches the pinned candidate there with
// the empty allowlist, and a silent fallback keeps the working model.
func TestModelRefreshRootClaudeCodeAgentRouteProvesCandidateFromModelUsage(t *testing.T) {
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
				"    follow_target:\n      provider: anthropic\n      tier: sonnet\n", func(string) {})
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
			require.NotEmpty(t, argv, "the claude-code agent route must be probed through the CLI")
			// Discovery probes the working model too; every probe must keep the
			// empty allowlist, and the candidate must be among them.
			var candidateProbed bool
			for _, line := range argv {
				candidateProbed = candidateProbed || strings.Contains(line, "--model "+newModel)
				assert.Contains(t, line, "--allowedTools  ", "the probe runs with the empty allowlist")
				assert.NotContains(t, line, "--dangerously-skip-permissions")
				assert.NotContains(t, line, "Bash,Write", "the agent tool scope never reaches a probe")
				assertPrivateProbeDir(t, line, deps.ProbeDir)
			}
			assert.True(t, candidateProbed, "the candidate is probed through the claude-code agent route")
		})
	}
}
