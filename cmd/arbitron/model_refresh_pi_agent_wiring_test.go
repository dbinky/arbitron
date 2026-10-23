package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A following agent profile on the pi backend is advanced only on pi's own
// assistant message_end proof from a tool-disabled probe: the root graph hands
// the agent usability lane the production factory, the persisted connection's
// credential and the workspace probe directory; the real pi adapter launches
// the pinned candidate there with --no-tools/--no-mcp, and a silent fallback
// keeps the working model.
func TestModelRefreshRootPiAgentRouteProvesCandidateFromAssistantMessage(t *testing.T) {
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
			executed := filepath.Join(dir, "executed-model")
			if tc.fallback != "" {
				require.NoError(t, os.WriteFile(executed, []byte(tc.fallback), 0o600))
			}
			// The fake emits the requested model only when the credential and the
			// tool-disabling flags arrive; anything else is a wrong-model reply.
			script := "#!/bin/sh\n" +
				"if [ \"$1\" = \"--version\" ]; then echo '1.3.0'; exit 0; fi\n" +
				"echo \"$PWD $*\" >> '" + argvLog + "'\n" +
				"cat > /dev/null\n" +
				"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
				"[ \"$ANTHROPIC_API_KEY\" = 'pi-refresh-secret' ] || m=missing-credential\n" +
				"case \" $* \" in *' --no-tools --no-mcp '*) ;; *) m=tools-enabled ;; esac\n" +
				"if [ -f '" + executed + "' ]; then m=$(cat '" + executed + "'); fi\n" +
				"echo '{\"type\":\"session\",\"version\":3,\"id\":\"pi-refresh-session\"}'\n" +
				"echo '{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"provider\":\"anthropic\",\"model\":\"'\"$m\"'\",\"stopReason\":\"stop\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}'\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

			ds := bootSinkRoot(t, "profiles:\n"+
				"  - alias: builder\n    kind: agent\n    backend: pi\n    connectionref: pi-anthropic\n    model: "+oldModel+"\n    selection_mode: following\n"+
				"    follow_target:\n      provider: anthropic\n      tier: sonnet\n", func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "pi-anthropic", Provider: provider.CanonicalAnthropicAPI, BaseURL: "https://api.anthropic.com"}, "pi-refresh-secret"))
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
			require.NotEmpty(t, argv, "the pi agent route must be probed through the pi binary")
			assert.Contains(t, strings.Join(argv, "\n"), "--model "+newModel)
			for _, line := range argv {
				assert.Contains(t, line, "--no-tools --no-mcp")
				assertPrivateProbeDir(t, line, deps.ProbeDir)
			}
		})
	}
}
