package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real initDomain -> launch-home files -> inventory/HMAC -> gate -> SQL CAS /
// journal -> cache -> triggered agent output. Proof is collected by the real
// Codex lane before restart, not manufactured from the requested model.
func TestPathInventoryCodexEffectiveConfigRejectsStaleCASRoot(t *testing.T) {
	const oldModel, newModel = "gpt-5-sol", "gpt-5.1-sol"
	for _, mutation := range []string{"unchanged restart", "config edit", "rollout growth", "unreadable config"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "account")
			writeCodexAccountModels(t, home, oldModel, newModel)
			config := filepath.Join(home, "config.toml")
			const originalConfig = "model_reasoning_effort = 'low'\n"
			require.NoError(t, os.WriteFile(config, []byte(originalConfig), 0o600))
			log := filepath.Join(dir, "argv")
			script := strings.NewReplacer("@ARGV@", log, "@FALLBACK@", filepath.Join(dir, "fallback")).Replace(codexProbeScript)
			script = strings.Replace(script, "case \"$*\" in *dangerously-bypass*) exit 41 ;; esac", `
[ "$CODEX_HOME" = '`+home+`' ] || exit 41
case "$PWD" in *.model-probe) case "$*" in *'--sandbox read-only'*) ;; *) exit 42 ;; esac ;; esac`, 1)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700))
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CODEX_HOME", home)
			ds, closeStack := bootSinkRootWithClose(t, "profiles: []\nllm:\n  defaultagentbackend: codex\n", func(ws string) {
				writeWorkflow(t, ws, "config-proof", `{"name":"config-proof","inputs":[],"nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"builder","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, ws, "config-proof")
			})
			ctx := adminCtxForLocalUser(t, ds)
			// Start pinned, load the catalog, then validate the following transition
			// through the actual candidate service. No cold-bootstrap substitute.
			p, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "builder", Kind: provider.ProfileKindAgent, Backend: workflow.BackendCodex, Model: oldModel})
			require.NoError(t, err)
			deps := ds.refreshDeps
			deps.Fetcher = &fixtureFetcher{body: codexCatalogFixture}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			t.Cleanup(refresh.stop)
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Empty(t, report.Changes)
			p.SelectionMode = provider.SelectionFollowing
			p.FollowTarget = &provider.FollowTarget{Provider: "openai", Tier: "sol"}
			_, _, err = ds.profileSvc.Update(ctx, p)
			require.NoError(t, err)
			report, err = refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Len(t, report.Changes, 1)
			require.Equal(t, newModel, report.Changes[0].ToModel)
			// An explicit admin start on X is valid; retain Y's genuine durable
			// confirmation to exercise another advancement after restart.
			p, err = ds.profileSvc.Get(ctx, "builder")
			require.NoError(t, err)
			p.Model = oldModel
			_, _, err = ds.profileSvc.Update(ctx, p)
			require.NoError(t, err)
			before, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			view, err := deps.Store.Snapshot(ctx)
			require.NoError(t, err)
			path := codexAliasPath(t, before, "builder")
			var evidence provider.PathEvidenceRef
			var canonical string
			for _, ev := range view.Evidence {
				if ev.NativeModel == newModel && ev.PathKey == path.Key && ev.Confirmed() {
					evidence = provider.PathEvidenceRef{PathKey: ev.PathKey, ConfigRevision: ev.ConfigRevision}
					canonical = ev.CanonicalID
				}
			}
			require.NotEmpty(t, canonical, "initial exact confirmation must reach SQLite")
			require.NotEmpty(t, evidence.ConfigRevision)
			require.Equal(t, evidence.PathKey, path.Key)
			require.Equal(t, evidence.ConfigRevision, path.ConfigRevision, "model-only edits retain confirmation")
			probes := len(readArgvLog(t, log))
			refresh.stop()
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			closeStack()
			ds, err = initDomain(context.Background(), configPath)
			require.NoError(t, err)
			t.Cleanup(func() { ds.Close(context.Background()) })
			same, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			require.Equal(t, before.Revision, same.Revision, "restart with unchanged files retains proof identity")
			persisted, err := ds.refreshDeps.Store.Snapshot(ctx)
			require.NoError(t, err)
			require.Equal(t, view.Evidence, persisted.Evidence)
			read, err := ds.profileSvc.Get(ctx, "builder")
			require.NoError(t, err)
			req := provider.AdvanceRequest{ID: uuid.NewString(), Expected: read, Model: newModel, CanonicalID: canonical, PathRevision: before.Revision, ExecutionRevision: before.ExecutionRevision, Evidence: []provider.PathEvidenceRef{evidence}, At: time.Now().UTC()}
			switch mutation {
			case "config edit":
				require.NoError(t, os.WriteFile(config, []byte("model_reasoning_effort = 'high'\n"), 0o600))
			case "rollout growth":
				require.NoError(t, os.WriteFile(filepath.Join(home, "sessions", "unrelated.jsonl"), []byte("unrelated turn"), 0o600))
			case "unreadable config":
				require.NoError(t, os.Remove(config))
				require.NoError(t, os.Mkdir(config, 0o700))
			}
			after, scanErr := ds.pathInventory.Snapshot(ctx)
			_, advanceErr := ds.profileSvc.AdvanceModel(ctx, req)
			want, count := newModel, 1
			switch mutation {
			case "config edit":
				require.NoError(t, scanErr)
				assert.NotEqual(t, before.Revision, after.Revision)
				assert.NotEqual(t, evidence.ConfigRevision, codexAliasPath(t, after, "builder").ConfigRevision)
				assert.ErrorIs(t, advanceErr, provider.ErrExecutionRevisionStale)
				want, count = oldModel, 0
			case "unreadable config":
				assert.Error(t, scanErr)
				assert.ErrorIs(t, advanceErr, provider.ErrExecutionRevisionUnavailable)
				want, count = oldModel, 0
				require.NoError(t, os.Remove(config))
				require.NoError(t, os.WriteFile(config, []byte(originalConfig), 0o600))
			default:
				require.NoError(t, scanErr)
				assert.Equal(t, before.Revision, after.Revision)
				require.NoError(t, advanceErr)
			}
			stored, err := ds.profileSvc.Get(ctx, "builder")
			require.NoError(t, err)
			assert.Equal(t, want, stored.Model)
			assert.Equal(t, read.Revision+int64(count), stored.Revision)
			var journals int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journals))
			assert.Equal(t, 1+count, journals, "stale proof must not append another journal change")
			require.Len(t, readArgvLog(t, log), probes, "unchanged durable evidence is reused without another probe")
			n := len(readArgvLog(t, log))
			id, _, err := ds.eng.Trigger(ctx, "config-proof", map[string]any{}, "normal")
			require.NoError(t, err)
			require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
			calls := readArgvLog(t, log)[n:]
			require.Len(t, calls, 1)
			assert.Contains(t, calls[0], " -m "+want+" ", "new execution must use only the successfully published model")
			run, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"builder": want}, run.ModelPins)
			assert.Equal(t, instance.ModelPinsVersionSnapshot, run.ModelPinsVersion)
		})
	}
}

func codexAliasPath(t *testing.T, snap modelcatalog.PathSnapshot, alias string) modelcatalog.ExecutionPath {
	t.Helper()
	for _, p := range snap.Paths {
		for _, a := range p.Aliases {
			if a == alias && p.Backend == workflow.BackendCodex {
				return p
			}
		}
	}
	t.Fatal("missing configured Codex path")
	return modelcatalog.ExecutionPath{}
}
