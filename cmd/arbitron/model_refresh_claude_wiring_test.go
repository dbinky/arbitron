package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const claudeCatalogFixture = `{"anthropic":{"id":"anthropic","models":{
 "claude-sonnet-4-5":{"id":"claude-sonnet-4-5","release_date":"2025-09-29"},
 "claude-sonnet-4-6":{"id":"claude-sonnet-4-6","release_date":"2026-02-17"}}}}`

// A following profile on the Claude Code subscription (no connection) is
// advanced only on the CLI's own modelUsage proof: the root graph hands the
// usability lane the workspace probe directory, the real chat adapter launches
// the pinned candidate there, and a silent fallback keeps the working model.
func TestModelRefreshRootClaudeSubscriptionRouteProvesCandidateFromModelUsage(t *testing.T) {
	const oldModel, newModel = "claude-sonnet-4-5", "claude-sonnet-4-6"
	for _, tc := range []struct {
		name     string
		fallback string
		want     string
		catalog  string
	}{
		{name: "executed model matches the candidate", want: newModel},
		{name: "silent fallback keeps the working model", fallback: "claude-haiku-4-5", want: oldModel},
		{name: "dated snapshot keeps the working model", want: oldModel, catalog: `{"anthropic":{"id":"anthropic","models":{
"claude-sonnet-4-5":{"id":"claude-sonnet-4-5"},
"claude-sonnet-4-20250514":{"id":"claude-sonnet-4-20250514"}}}}`},
		{name: "dated snapshot cannot outrank a valid replacement", want: newModel, catalog: `{"anthropic":{"id":"anthropic","models":{
"claude-sonnet-4-5":{"id":"claude-sonnet-4-5"},
"claude-sonnet-4-6":{"id":"claude-sonnet-4-6"},
"claude-sonnet-4-20250514":{"id":"claude-sonnet-4-20250514"}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argvLog := filepath.Join(dir, "argv")
			bin := filepath.Join(dir, "claude")
			if tc.fallback != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "executed-model"), []byte(tc.fallback), 0o600))
			}
			script := "#!/bin/sh\n" +
				"echo \"$PWD $*\" >> '" + argvLog + "'\n" +
				"cat > /dev/null\n" +
				"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
				"if [ -f '" + filepath.Join(dir, "executed-model") + "' ]; then m=$(cat '" + filepath.Join(dir, "executed-model") + "'); fi\n" +
				"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
			require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))

			ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\nprofiles:\n"+
				"  - alias: worker\n    kind: chat\n    model: "+oldModel+"\n    selection_mode: following\n"+
				"    follow_target:\n      provider: anthropic\n      tier: sonnet\n", func(workspace string) {
				writeWorkflow(t, workspace, "snapshot-exec", `{"name":"snapshot-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "snapshot-exec")
			})
			ctx := adminCtxForLocalUser(t, ds)
			storeConfiguredFollowing(t, ctx, ds, "worker")
			catalogURL := "https://catalog.example.test/api.json"
			_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
			require.NoError(t, err)

			deps := ds.refreshDeps
			catalog := tc.catalog
			if catalog == "" {
				catalog = claudeCatalogFixture
			}
			deps.Fetcher = &fixtureFetcher{body: catalog}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)

			stored, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, tc.want, stored.Model)
			assert.Equal(t, tc.want == newModel, len(report.Changes) == 1)
			if tc.catalog != "" {
				view, err := ds.catalogStore.Snapshot(ctx)
				require.NoError(t, err)
				snapshot := entryOf(t, view.Catalog.Slices, "anthropic", "claude-sonnet-4-20250514")
				assert.Empty(t, snapshot.Tier, "the dated model stays visible but is ineligible")
				assert.Empty(t, snapshot.OrderKey)
				var journals int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal WHERE alias='worker'`).Scan(&journals))
				assert.Equal(t, len(report.Changes), journals)

				run, _, err := ds.eng.Trigger(ctx, "snapshot-exec", nil, "normal")
				require.NoError(t, err)
				done := waitTerminal(t, ds, run)
				assert.Equal(t, instance.StateCompleted, done.State)
				assert.Equal(t, tc.want, done.ModelPins["worker"])
			}

			argv := readArgvLog(t, argvLog)
			require.NotEmpty(t, argv, "the subscription route must be probed through the CLI")
			var probes, runs []string
			for _, line := range argv {
				if isProbeTurn(line) {
					probes = append(probes, line)
				} else {
					runs = append(runs, line)
				}
			}
			if tc.catalog == "" {
				assert.Contains(t, strings.Join(probes, "\n"), "--model "+newModel)
			} else {
				// Discovery probes every catalog row on the path, snapshots
				// included, but a probe is never eligibility: no run may launch on
				// the ineligible snapshot, and the new run takes the safe selection.
				require.Len(t, runs, 1, "only the new run launches outside the probe dir")
				assert.NotContains(t, strings.Join(runs, "\n"), "--model claude-sonnet-4-20250514")
				assert.Contains(t, runs[0], "--model "+tc.want, "the new run executes the safe selection")
			}
			if tc.catalog == "" || tc.want == newModel {
				require.NotEmpty(t, probes)
				for _, line := range probes {
					assertPrivateProbeDir(t, line, deps.ProbeDir)
				}
			}
		})
	}
}
