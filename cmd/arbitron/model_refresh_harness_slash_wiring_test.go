package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fake harness below stands in for pi on an OpenRouter connection. @MODE@ picks what the "upstream" reports:
//   - aggregator: OpenRouter serves the slash-bearing id it was asked for;
//   - native: the harness reads the id's first segment as a provider, so the
//     native deepseek provider serves the leaf, on probe and run alike;
//   - switch: the probe (cwd under .model-probe) is served by OpenRouter, the
//     later pinned run by the native provider.
// It records "$PWD $*" so the probe's and the run's spellings can be compared.

const piSlashScript = `#!/bin/sh
if [ "$1" = "--version" ]; then echo '1.3.0'; exit 0; fi
echo "$PWD $*" >> '@ARGV@'
cat > /dev/null
m=$(echo "$*" | sed -n 's/.*--model \([^ ]*\).*/\1/p')
[ "$OPENROUTER_API_KEY" = 'slash-secret' ] || m=missing-credential
p=openrouter
case "@MODE@" in
native) p=${m%%/*}; m=${m#*/} ;;
switch) case "$PWD" in *.model-probe*) ;; *) p=${m%%/*}; m=${m#*/} ;; esac ;;
esac
echo '{"type":"session","version":3,"id":"pi-slash-session"}'
echo '{"type":"message_end","message":{"role":"assistant","provider":"'"$p"'","model":"'"$m"'","stopReason":"stop","content":[{"type":"text","text":"verified output"}]}}'
`

// Paired seam Area 2 refresh discovery <-> Area 5 pi/OpenCode lanes. A
// slash-bearing aggregator id is confirmed by the real refresh probe, advanced,
// and then executed by a pinned run through the same harness: the run's argv
// carries the exact spelling the probe confirmed, and the run's own verifier
// holds it to the same provider. A harness that resolves the id's prefix as a
// native provider never confirms; one that switches provider after the probe
// fails the run instead of returning output from another provider. The OpenCode
// lane's D12 isolation (resolved-config MCP overrides, --pure) is covered by
// TestUsabilityOpenCodeD12IsolationRoot and TestUsabilityConfirmedExactModel.
func TestModelRefreshHarnessSlashSpellingProbeMatchesPinnedRun(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	for _, h := range []struct {
		backend, script, argvModel string
	}{
		{backend: "pi", script: piSlashScript, argvModel: "--model "},
	} {
		for _, tc := range []struct {
			mode      string
			wantModel string
			runOK     bool
		}{
			{mode: "aggregator", wantModel: newModel, runOK: true},
			{mode: "native", wantModel: oldModel},
			{mode: "switch", wantModel: newModel},
		} {
			t.Run(h.backend+"/"+tc.mode, func(t *testing.T) {
				dir := t.TempDir()
				argvLog := filepath.Join(dir, "argv")
				script := strings.NewReplacer("@ARGV@", argvLog, "@MODE@", tc.mode, "@EXECUTED@", filepath.Join(dir, "executed")).Replace(h.script)
				require.NoError(t, os.WriteFile(filepath.Join(dir, h.backend), []byte(script), 0o700))
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

				const wf = "refresh-slash"
				ds := bootSinkRoot(t, "profiles:\n"+
					"  - alias: builder\n    kind: agent\n    backend: "+h.backend+"\n    connectionref: slash-or\n    model: "+oldModel+"\n    selection_mode: following\n"+
					"    follow_target:\n      provider: openrouter\n      tier: flash\n", func(workspace string) {
					writeWorkflow(t, workspace, wf, fmt.Sprintf(`{"name":%q,"nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"backend":%q,"model_profile":"builder","templateFile":"templates/p.md"}}],"edges":[]}`, wf, h.backend))
					writePinTemplate(t, workspace, wf)
				})
				ctx := adminCtxForLocalUser(t, ds)
				require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "slash-or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, "slash-secret"))
				storeConfiguredFollowing(t, ctx, ds, "builder")
				catalogURL := "https://catalog.example.test/api.json"
				_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
				require.NoError(t, err)

				deps := ds.refreshDeps
				deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
				refresh, err := newModelRefresh(deps)
				require.NoError(t, err)
				defer refresh.stop()
				report, err := refresh.coordinator.Refresh(ctx)
				require.NoError(t, err)
				stored, err := ds.profileSvc.Get(ctx, "builder")
				require.NoError(t, err)
				require.Equal(t, tc.wantModel, stored.Model, "advancement must follow the probe's verdict")
				assert.Equal(t, tc.wantModel == newModel, len(report.Changes) == 1)

				probes := readArgvLog(t, argvLog)
				require.NotEmpty(t, probes, "the candidate must be probed through the %s binary", h.backend)
				// Discovery probes every catalog row on the path, the working model
				// included; the candidate must be among them with its spelling intact.
				var candidateProbed bool
				for _, line := range probes {
					assert.Contains(t, line, ".model-probe")
					candidateProbed = candidateProbed || strings.Contains(line+" ", h.argvModel+newModel+" ")
				}
				assert.True(t, candidateProbed, "the probe executes the catalog's slash-bearing spelling verbatim")

				id, _, err := ds.eng.Trigger(ctx, wf, nil, "normal")
				require.NoError(t, err)
				saved := waitTerminal(t, ds, id)
				assert.Equal(t, map[string]string{"builder": tc.wantModel}, saved.ModelPins)
				all := readArgvLog(t, argvLog)
				require.Len(t, all, len(probes)+1, "exactly one pinned run launch after the probes")
				run := all[len(all)-1]
				assert.Contains(t, run, "workspaces/instances/")
				assert.Contains(t, run+" ", h.argvModel+tc.wantModel+" ", "the pinned run executes the spelling the profile holds")
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				if tc.runOK {
					require.Equal(t, instance.StateCompleted, saved.State, "trace: %+v", trace)
					assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
					assert.Contains(t, fmt.Sprint(trace[0].Output), "verified output")
					require.NotNil(t, trace[0].LLMModel)
					assert.Equal(t, newModel, *trace[0].LLMModel)
					return
				}
				require.Equal(t, instance.StateFailed, saved.State, "output from another provider reached the run sink: %+v", trace)
				assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Error), "evidence")
				assert.NotContains(t, fmt.Sprint(trace[0].Output), "verified output")
			})
		}
	}
}
