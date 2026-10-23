package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/require"
)

// A shorter configured timeout bounds a hung turn; failed probes retry on the
// next cycle. A long ordinary timeout does not waive the independent 30s probe
// ceiling (asserted without sleeping by TestUsabilityProbeIndependentHarnessBoundsRoot).
// Both modes must supply their own rollout before the following profile moves.
func TestModelProbeConfiguredTimeoutRetriesCodexAtRoot(t *testing.T) {
	const oldModel, newModel = "gpt-5-sol", "gpt-5.1-sol"
	for _, tc := range []struct {
		name, first, retry string
		timeout            int
	}{
		{name: "configured short deadline bounds a hung turn", timeout: 1, first: "2", retry: "0"},
		{name: "long ordinary budget permits a later quick retry", timeout: 60, first: "refuse", retry: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "account")
			control := filepath.Join(dir, "delay")
			require.NoError(t, os.WriteFile(control, []byte(tc.first), 0600))
			writeCodexAccountModels(t, home, oldModel, newModel)
			script := strings.NewReplacer("@ARGV@", filepath.Join(dir, "argv"), "@FALLBACK@", filepath.Join(dir, "fallback")).Replace(codexProbeScript)
			script = strings.Replace(script, `d="$CODEX_HOME/sessions/2026/10/16"`, fmt.Sprintf(`
if [ "$m" = '%s' ]; then
 delay=$(cat '%s')
 [ "$delay" != refuse ] || exit 49
 sleep "$delay"
fi
d="$CODEX_HOME/sessions/2026/10/16"`, newModel, control), 1)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CODEX_HOME", home)
			t.Setenv(codexInteractiveVerifiedEnv, "true")
			ds := bootSinkRoot(t, fmt.Sprintf("llm:\n  defaultagentbackend: codex\n  timeoutseconds: %d\n", tc.timeout)+
				"profiles:\n  - alias: builder\n    kind: agent\n    backend: codex\n    model: "+oldModel+"\n    selection_mode: following\n"+
				"    follow_target:\n      provider: openai\n      tier: sol\n", func(ws string) {
				writeWorkflow(t, ws, "slow-probe", `{"name":"slow-probe","nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"builder","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`)
			})
			ctx := adminCtxForLocalUser(t, ds)
			storeConfiguredFollowing(t, ctx, ds, "builder")
			url := "https://catalog.example.test/api.json"
			_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &url})
			require.NoError(t, err)
			// Only the catalog I/O is replaced. Keep the root-produced timeout,
			// factory, stream selector, account reader and rollout verifier intact.
			deps := ds.refreshDeps
			deps.Fetcher = &fixtureFetcher{body: codexCatalogFixture}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			first, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Empty(t, first.Changes)
			stored, err := ds.profileSvc.Get(ctx, "builder")
			require.NoError(t, err)
			require.Equal(t, oldModel, stored.Model)
			before, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			unknownModes := map[string]bool{}
			paths, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			for _, ev := range before.Evidence {
				if ev.NativeModel == newModel {
					require.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
					require.Empty(t, ev.ProofRef)
					for _, p := range paths.Paths {
						if p.Key == ev.PathKey && p.Backend == "codex" {
							unknownModes[p.Mode] = true
						}
					}
				}
			}
			require.Equal(t, map[string]bool{"batch": true, "interactive": true}, unknownModes)

			require.NoError(t, os.WriteFile(control, []byte(tc.retry), 0600))
			expireProbeBackoff(t, ds.db)
			second, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Len(t, second.Changes, 1, "a later exact rollout must permit advancement")
			stored, err = ds.profileSvc.Get(ctx, "builder")
			require.NoError(t, err)
			require.Equal(t, newModel, stored.Model)
			after, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			confirmedModes := map[string]bool{}
			for _, ev := range after.Evidence {
				if ev.NativeModel == newModel && ev.Confirmed() {
					for _, p := range paths.Paths {
						if p.Key == ev.PathKey && p.ConfigRevision == ev.ConfigRevision && p.Backend == "codex" {
							confirmedModes[p.Mode] = true
						}
					}
				}
			}
			require.Equal(t, map[string]bool{"batch": true, "interactive": true}, confirmedModes)
		})
	}
}
