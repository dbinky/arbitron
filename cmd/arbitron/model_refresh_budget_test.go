package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptcatalog "github.com/arbitron/arbitron/adapter/modelcatalog"
	"github.com/arbitron/arbitron/adapter/probe"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One worker-sized batch per tiny-budget cycle reaches the real chat lane;
// remaining checks consume the scan deadline. Rebuilding the wrapper, pipeline
// and SQL repository each cycle simulates a process restart.
const completedChecksPerCycle = 4

type completedProbeBatch struct {
	probe modelcatalog.UsabilityProbe
	calls atomic.Int32
}

func (p *completedProbeBatch) Check(ctx context.Context, path modelcatalog.ExecutionPath, e modelidentity.Entry) (modelcatalog.UsabilityEvidence, error) {
	if p.calls.Add(1) > completedChecksPerCycle {
		<-ctx.Done()
		return modelcatalog.UsabilityEvidence{}, ctx.Err()
	}
	return p.probe.Check(ctx, path, e)
}

// Without persisted scheduling inputs, every tiny-budget restart retries the
// unavailable alphabetical head, so the tail profile never runs the new model.
func TestModelRefreshBudgetRestartsReachTailExecution(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	requests := make(chan string, 64)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") == "Bearer head-key" {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		requests <- body.Model
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds := bootSinkRoot(t, `profiles:
  - alias: a-head
    kind: chat
    backend: claude-code
    connectionref: head
    model: `+oldModel+`
    selection_mode: following
    follow_target: {provider: openrouter, tier: flash}
  - alias: z-worker
    kind: chat
    backend: claude-code
    connectionref: tail
    model: `+oldModel+`
    selection_mode: following
    follow_target: {provider: openrouter, tier: flash}
  - alias: solo
    kind: chat
    backend: claude-code
    connectionref: solo
    model: `+oldModel+`
`, func(workspace string) {
		writeWorkflow(t, workspace, "budget-exec", `{"name":"budget-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"z-worker","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "budget-exec")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	for _, name := range []string{"head", "tail", "solo"} {
		require.NoError(t, conns.Create(ctx, provider.Connection{Name: name, Provider: "openrouter", BaseURL: upstream.URL}, name+"-key"))
	}
	storeConfiguredFollowing(t, ctx, ds, "a-head")
	storeConfiguredFollowing(t, ctx, ds, "z-worker")
	mapper, err := modelidentity.NewIdentityMapper(ds.refreshDeps.Policy)
	require.NoError(t, err)
	normalizer, err := adaptcatalog.NewNormalizer(mapper, time.Now)
	require.NoError(t, err)
	lane, err := probe.NewChatUsabilityLane(probe.ChatLaneDeps{Chats: ds.refreshDeps.Chats, WithBinding: ds.refreshDeps.Inventory.WithBinding})
	require.NoError(t, err)
	router, err := probe.NewUsabilityRouter(probe.UsabilityRouterDeps{Lanes: []probe.UsabilityLane{lane}})
	require.NoError(t, err)
	var databaseSeq int
	var databaseName, databasePath string
	require.NoError(t, ds.db.QueryRow(`PRAGMA database_list`).Scan(&databaseSeq, &databaseName, &databasePath))
	for cycle := 0; cycle < 12; cycle++ {
		// Reopen SQLite as well: no cursor or evidence can survive only in memory.
		cycleDB, err := adaptsqlite.Open(databasePath)
		require.NoError(t, err)
		t.Cleanup(func() { cycleDB.Close() })
		expireProbeBackoff(t, cycleDB)
		store := adaptsqlite.NewModelCatalogRepo(cycleDB)
		pipeline, err := modelcatalog.NewDiscoveryPipeline(modelcatalog.DiscoveryDeps{
			Settings: ds.refreshDeps.Settings, Fetcher: &fixtureFetcher{body: refreshCatalogFixture},
			Normalizer: normalizer, Mapper: mapper, Inventory: ds.refreshDeps.Inventory,
			Profiles: ds.profileSvc, Store: store, Probe: &completedProbeBatch{probe: router}, AdvanceValidator: ds.profileSvc, Budget: 100 * time.Millisecond,
		})
		require.NoError(t, err)
		service := &modelcatalog.RefreshService{Pipeline: pipeline, Store: store, Gate: ds.refreshDeps.Gate, Publisher: ds.refreshDeps.Publisher, Journal: store, Announcer: modelcatalog.DeferredAnnouncer{}}
		report, err := service.Refresh(ctx)
		require.NoError(t, err)
		require.NoError(t, cycleDB.Close())
		if cycle == 0 {
			for _, change := range report.Changes {
				assert.Equal(t, "z-worker", change.Alias, "only the fully confirmed independent tail may advance")
			}
			assert.Equal(t, "budget_exhausted", report.PathStatus["probe"])
		}
	}
	head, err := ds.profileSvc.Get(ctx, "a-head")
	require.NoError(t, err)
	assert.Equal(t, oldModel, head.Model, "an unavailable path keeps its working model")
	tail, err := ds.profileSvc.Get(ctx, "z-worker")
	require.NoError(t, err)
	require.Equal(t, newModel, tail.Model, "the unavailable head must not starve an eligible tail across restarts")
	view, err := adaptsqlite.NewModelCatalogRepo(ds.db).Snapshot(ctx)
	require.NoError(t, err)
	confirmedPaths := map[string]bool{}
	for _, ev := range view.Evidence {
		if ev.NativeModel == newModel && ev.Confirmed() {
			confirmedPaths[ev.PathKey] = true
		}
	}
	assert.Len(t, confirmedPaths, 2, "tail and standalone paths both reach durable confirmation")
	assert.NotEmpty(t, view.ProbeCursor)
	var events int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
	assert.Positive(t, events, "tail confirmation reaches the durable observation/event sink")
	for len(requests) > 0 {
		<-requests
	} // discard probe traffic before the new run
	runID, _, err := ds.eng.Trigger(ctx, "budget-exec", nil, "normal")
	require.NoError(t, err)
	run := waitTerminal(t, ds, runID)
	assert.Equal(t, instance.StateCompleted, run.State)
	assert.Equal(t, newModel, run.ModelPins["z-worker"])
	require.Len(t, requests, 1, "one new-run request reaches the upstream sink")
	assert.Equal(t, newModel, <-requests, "the new run executes the advanced tail model")
}
