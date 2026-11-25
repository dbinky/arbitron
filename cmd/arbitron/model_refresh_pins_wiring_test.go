package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const refreshPinsWorkflow = "refresh-pins"

// bootRefreshPinsRoot boots the real root with a configured following profile
// on lifecycleModelX behind a sink connection, and a workflow whose single
// llm_call node retries once.
func bootRefreshPinsRoot(t *testing.T, sink *modelSink) (*domainStack, func(), context.Context) {
	t.Helper()
	ds, closeStack := bootSinkRootWithClose(t, `profiles:
  - alias: pin-alias
    kind: chat
    backend: claude-code
    connectionref: sink
    model: `+lifecycleModelX+`
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(dir string) {
		writeWorkflow(t, dir, refreshPinsWorkflow, `{"name":"`+refreshPinsWorkflow+`","inputs":[],"nodes":[
  {"id":"ask","type":"llm_call","entrypoint":true,
   "config":{"model_profile":"pin-alias","templateFile":"templates/p.md"},
   "errorBehavior":{"type":"retry","retries":1}}
],"edges":[]}`)
		writePinTemplate(t, dir, refreshPinsWorkflow)
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	storeConfiguredFollowing(t, ctx, ds, "pin-alias")
	return ds, closeStack, ctx
}

// refreshAdvancesToY runs one real refresh cycle through the root's graph
// (fetch, probe on the sink, CommitCycle CAS, ProfileCache publication) and
// requires that it reported X -> Y as published.
func refreshAdvancesToY(t *testing.T, ctx context.Context, ds *domainStack) {
	t.Helper()
	deps := ds.refreshDeps
	fetcher := &fixtureFetcher{body: refreshCatalogFixture}
	deps.Fetcher = fetcher
	serveRootCatalog(t, ds, fetcher)
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	require.Equal(t, lifecycleModelX, report.Changes[0].FromModel)
	require.Equal(t, lifecycleModelY, report.Changes[0].ToModel)
	require.False(t, report.PublicationPending, "publication must be confirmed before the refresh reports success")
	stored, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	require.Equal(t, lifecycleModelY, stored.Model)
}

// mark returns a reader of the models recorded after this call. The sink's
// first-request gating counts every request, so it is never reset.
func (s *modelSink) mark() func() []string {
	n := len(s.seen())
	return func() []string { return s.seen()[n:] }
}

// Paired seam Area 3 run pins <-> Area 4 refresh advancement. Runs pinned on X
// before a real refresh advances and publishes Y keep sending X on in-process
// retry, on dequeue after the advancement, and on Retry after a restart; the
// first run triggered after the refresh reports success sends Y.
func TestModelRefreshAdvancementKeepsRunPinsAndPublishesNewRuns(t *testing.T) {
	t.Run("in-flight retry and queued run after advancement", func(t *testing.T) {
		sink, release := lifecycleSink(t, true)
		// A failed assertion must not leave R1 blocked in the sink while the
		// stack's cleanup waits for its walker.
		defer release()
		ds, _, ctx := bootRefreshPinsRoot(t, sink)
		ds.queueMgr.UpdateConfig(1, 1)

		r1, started, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.True(t, started)
		awaitLifecycleRequest(t, sink) // R1's first attempt is in flight on X
		queued, started, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.False(t, started, "the second X run must wait in the queue across the advancement")
		assertRefreshPins(t, ds, r1, lifecycleModelX)
		assertRefreshPins(t, ds, queued, lifecycleModelX)

		refreshAdvancesToY(t, ctx, ds)
		after := sink.mark()
		release() // R1's first attempt now fails, so its retry runs after Y is live

		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, r1).State)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, queued).State)
		trace, err := ds.nodeExecRepo.ListByInstance(ctx, r1)
		require.NoError(t, err)
		require.Len(t, trace, 2, "R1 must have failed once and retried")
		assert.Equal(t, []string{lifecycleModelX, lifecycleModelX}, after(),
			"R1's retry and the queued run's first call execute their trigger-time pin")

		after = sink.mark()
		r2, _, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, r2).State)
		assert.Equal(t, []string{lifecycleModelY}, after(), "the first run after published advancement executes Y")
		assertRefreshPins(t, ds, r2, lifecycleModelY)
		assertRefreshPins(t, ds, r1, lifecycleModelX)
	})

	t.Run("restart after advancement", func(t *testing.T) {
		sink, release := lifecycleSink(t, false)
		release()
		ds, closeStack, ctx := bootRefreshPinsRoot(t, sink)
		ds.queueMgr.UpdateConfig(1, 1)
		require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
			ID: uuid.New(), OwnerID: localDefaultUser, WorkflowName: "capacity-holder",
			State: instance.StateRunning, Priority: instance.PriorityNormal,
			WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
			ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		}))
		r1, started, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.False(t, started)
		// Crash boundary: R1 was running its provider node when the process died.
		require.NoError(t, ds.instanceRepo.UpdateCurrentNode(ctx, r1, "ask"))
		require.NoError(t, ds.instanceRepo.UpdateState(ctx, r1, instance.StateRunning))

		refreshAdvancesToY(t, ctx, ds)
		after := sink.mark()
		configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
		closeStack()

		reopened, err := initRecoveryTestDomain(context.Background(), configPath)
		require.NoError(t, err)
		t.Cleanup(func() { reopened.Close(context.Background()) })
		stored, err := reopened.profileSvc.Get(ctx, "pin-alias")
		require.NoError(t, err)
		require.Equal(t, lifecycleModelY, stored.Model, "the advancement is durable across restart")
		recovered, err := reopened.instanceRepo.Get(ctx, r1)
		require.NoError(t, err)
		require.Equal(t, instance.StateFailed, recovered.State)
		require.Empty(t, after())

		require.NoError(t, reopened.eng.Retry(ctx, r1, nil))
		require.Equal(t, instance.StateCompleted, waitTerminal(t, reopened, r1).State)
		assert.Equal(t, []string{lifecycleModelX}, after(), "restart must reload R1's X pin, not the advanced Y")

		after = sink.mark()
		r2, _, err := reopened.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, reopened, r2).State)
		assert.Equal(t, []string{lifecycleModelY}, after())
		assertRefreshPins(t, reopened, r2, lifecycleModelY)
		assertRefreshPins(t, reopened, r1, lifecycleModelX)
	})

	// The report may be acknowledged only after the cache holds Y. A cache
	// reload fault after the durable CAS keeps the report pending and new runs
	// on the working X; the next cycle's Recover republishes, so the next run
	// executes Y. Acknowledging first would strand new runs on X.
	t.Run("cache publication fault keeps the report pending", func(t *testing.T) {
		sink, release := lifecycleSink(t, false)
		release()
		ds, _, ctx := bootRefreshPinsRoot(t, sink)
		_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "publication-guard", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: lifecycleModelX})
		require.NoError(t, err)
		// A database-leaf fault breaks List only AFTER the CAS journal insert.
		_, err = ds.db.Exec(`CREATE TRIGGER fail_publication AFTER INSERT ON profile_advancement_journal BEGIN UPDATE profiles SET allowed_tools='invalid-json' WHERE alias='publication-guard'; END`)
		require.NoError(t, err)

		deps := ds.refreshDeps
		fetcher := &fixtureFetcher{body: refreshCatalogFixture}
		deps.Fetcher = fetcher
		serveRootCatalog(t, ds, fetcher)
		refresh, err := newModelRefresh(deps)
		require.NoError(t, err)
		defer refresh.stop()

		report, err := refresh.coordinator.Refresh(ctx)
		require.ErrorIs(t, err, provider.ErrPublicationPending)
		require.Len(t, report.Changes, 1)
		require.Equal(t, lifecycleModelY, report.Changes[0].ToModel)
		pending := func() bool {
			var p bool
			require.NoError(t, ds.db.QueryRow(`SELECT publication_pending FROM model_refresh_reports WHERE id=?`, report.ID).Scan(&p))
			return p
		}
		require.True(t, pending(), "a report whose cache publication failed must stay pending")

		after := sink.mark()
		r2, _, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, r2).State)
		assert.Equal(t, []string{lifecycleModelX}, after(), "an unpublished advancement must not reach new runs")
		assertRefreshPins(t, ds, r2, lifecycleModelX)

		_, err = ds.db.Exec(`DROP TRIGGER fail_publication; UPDATE profiles SET allowed_tools='[]' WHERE alias='publication-guard'`)
		require.NoError(t, err)
		_, err = refresh.coordinator.Refresh(ctx)
		require.NoError(t, err)
		require.False(t, pending(), "the next cycle republishes and then acknowledges the original report")

		after = sink.mark()
		r3, _, err := ds.eng.Trigger(ctx, refreshPinsWorkflow, map[string]any{}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, r3).State)
		assert.Equal(t, []string{lifecycleModelY}, after(), "the first run after recovered publication executes Y")
		assertRefreshPins(t, ds, r3, lifecycleModelY)
		assertRefreshPins(t, ds, r2, lifecycleModelX)
	})
}

func assertRefreshPins(t *testing.T, ds *domainStack, id uuid.UUID, model string) {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion)
	assert.Equal(t, map[string]string{"pin-alias": model}, inst.ModelPins)
}
