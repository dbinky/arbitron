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

// A following codex agent profile consumed standalone and by an opted-in
// interactive_agent node advances only when the candidate is proven on BOTH
// routes. The interactive route is probed by streaming through the production
// stream selector's codex interactive provider under the read-only sandbox in
// the workspace probe directory, and is proven from the account's stored
// rollout; a silent fallback keeps the working model. Without the operator
// opt-in the route has no stream to prove, so it is never probed and the
// working model stays. A candidate absent from the account model list is
// launched on neither route (D4).
func TestModelRefreshRootCodexInteractiveAgentRouteProvesCandidateFromRollout(t *testing.T) {
	const oldModel, newModel = "gpt-5-sol", "gpt-5.1-sol"
	for _, tc := range []struct {
		name     string
		fallback string
		optedOut bool
		unlisted bool
		want     string
		probes   int
	}{
		{name: "executed model matches the candidate", want: newModel, probes: 2},
		{name: "silent fallback keeps the working model", fallback: "gpt-4-sol", want: oldModel, probes: 2},
		{name: "route not opted in is never probed and never advances", optedOut: true, want: oldModel, probes: 1},
		{name: "candidate outside the account list is probed on neither route", unlisted: true, want: oldModel, probes: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "account")
			argvLog := filepath.Join(dir, "argv")
			fallback := filepath.Join(dir, "fallback-model")
			if tc.fallback != "" {
				require.NoError(t, os.WriteFile(fallback, []byte(tc.fallback), 0o600))
			}
			script := strings.NewReplacer("@ARGV@", argvLog, "@FALLBACK@", fallback).Replace(codexProbeScript)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CODEX_HOME", home)
			t.Setenv("OPENAI_API_KEY", "must-not-reach-codex")
			if tc.unlisted {
				writeCodexAccountModels(t, home, oldModel)
			} else {
				writeCodexAccountModels(t, home, oldModel, newModel)
			}
			if tc.optedOut {
				t.Setenv(codexInteractiveVerifiedEnv, "")
			} else {
				t.Setenv(codexInteractiveVerifiedEnv, "true")
			}

			ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n"+"profiles:\n"+
				"  - alias: builder\n    kind: agent\n    backend: codex\n    model: "+oldModel+"\n    selection_mode: following\n"+
				"    follow_target:\n      provider: openai\n      tier: sol\n", func(workspace string) {
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
			deps.Fetcher = &fixtureFetcher{body: codexCatalogFixture}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)

			stored, err := ds.profileSvc.Get(ctx, "builder")
			require.NoError(t, err)
			assert.Equal(t, tc.want, stored.Model)
			assert.Equal(t, tc.want == newModel, len(report.Changes) == 1)

			var probes, interactive int
			for _, line := range readArgvLog(t, argvLog) {
				if !strings.Contains(line, " -m "+newModel) {
					continue
				}
				probes++
				assert.Contains(t, line, "read-only", "every probe runs read-only: %s", line)
				assert.NotContains(t, line, "workspace-write")
				assert.NotContains(t, line, "writable_roots")
				assertPrivateProbeDir(t, line, deps.ProbeDir)
				if strings.Contains(line, "--skip-git-repo-check") {
					interactive++
				}
			}
			wantProbes := tc.probes
			if wantProbes > 0 {
				wantProbes++
			} // deployment-default path has its own configuration identity
			assert.Equal(t, wantProbes, probes, "default, profile batch and opted-in interactive paths are each probed")
			if tc.probes == 0 {
				assert.Zero(t, interactive)
				return
			}
			assert.Equal(t, tc.probes-1, interactive, "exactly the interactive route uses the interactive exec argv")
		})
	}
}
