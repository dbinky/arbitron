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

const codexCatalogFixture = `{"openai":{"id":"openai","models":{
 "gpt-5-sol":{"id":"gpt-5-sol","release_date":"2025-08-07"},
 "gpt-5.1-sol":{"id":"gpt-5.1-sol","release_date":"2025-11-12"}}}}`

// codexProbeScript is a fake codex: `exec` records its cwd and argv, refuses a
// bypass posture or an API key, and stores the account rollout the real CLI
// writes — the requested model, or the fallback file's contents (a silent
// harness fallback).
const codexProbeScript = `#!/bin/sh
if [ "$1" = '--version' ]; then echo 'codex-cli 0.163.0'; exit 0; fi
case " $* " in *' --strict-config '*' mcp '*) echo 'Error: --strict-config is not supported for codex mcp' >&2; exit 1 ;; esac
case " $* " in *' mcp list --json '*) echo '[]'; exit 0 ;; esac
echo "$PWD $*" >> '@ARGV@'
[ -z "$OPENAI_API_KEY" ] || exit 40
case "$*" in *dangerously-bypass*) exit 41 ;; esac
m=$(echo "$*" | sed -n 's/.* -m \([^ ]*\).*/\1/p')
if [ -f '@FALLBACK@' ]; then m=$(cat '@FALLBACK@'); fi
d="$CODEX_HOME/sessions/2026/10/16"
mkdir -p "$d"
sid="probe-thread-$$"
printf '%s\n%s\n' '{"type":"session_meta","payload":{"id":"'"$sid"'"}}' '{"type":"turn_context","payload":{"model":"'"$m"'"}}' > "$d/rollout-t-$sid.jsonl"
echo '{"type":"thread.started","thread_id":"'"$sid"'"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`

// writeCodexAccountModels stores the account model list codex keeps in its home
// (D4: ~/.codex/models_cache.json, .models[].slug).
func writeCodexAccountModels(t *testing.T, home string, slugs ...string) {
	t.Helper()
	models := make([]string, 0, len(slugs))
	for _, s := range slugs {
		models = append(models, `{"slug":"`+s+`"}`)
	}
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(`{"models":[`+strings.Join(models, ",")+`]}`), 0o600))
}

// A following agent profile on the Codex subscription is advanced only on the
// account's stored rollout of a read-only probe turn: the root graph hands the
// agent usability lane the production factory and the workspace probe
// directory, the real codex adapter launches the pinned candidate there without
// the bypass posture, and a silent fallback keeps the working model. A
// candidate absent from the account model list is never launched (D4).
func TestModelRefreshRootCodexAgentRouteProvesCandidateFromRollout(t *testing.T) {
	const oldModel, newModel = "gpt-5-sol", "gpt-5.1-sol"
	for _, tc := range []struct {
		name     string
		fallback string
		unlisted bool
		want     string
	}{
		{name: "executed model matches the candidate", want: newModel},
		{name: "silent fallback keeps the working model", fallback: "gpt-4-sol", want: oldModel},
		{name: "candidate outside the account list is never probed", unlisted: true, want: oldModel},
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

			ds := bootSinkRoot(t, "llm:\n  defaultagentbackend: codex\n"+"profiles:\n"+
				"  - alias: builder\n    kind: agent\n    backend: codex\n    model: "+oldModel+"\n    selection_mode: following\n"+
				"    follow_target:\n      provider: openai\n      tier: sol\n", func(string) {})
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

			argv := readArgvLog(t, argvLog)
			if tc.unlisted {
				for _, line := range argv {
					assert.NotContains(t, line, " -m "+newModel, "an unlisted candidate never reaches codex")
				}
				return
			}
			require.NotEmpty(t, argv, "the codex agent route must be probed through the codex binary")
			assert.Contains(t, strings.Join(argv, "\n"), "exec --sandbox read-only -m "+newModel)
			for _, line := range argv {
				assertPrivateProbeDir(t, line, deps.ProbeDir)
			}
		})
	}
}
