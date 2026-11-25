package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const refreshCatalogFixture = `{"openrouter":{"id":"openrouter","models":{
 "deepseek/deepseek-v4-flash":{"id":"deepseek/deepseek-v4-flash","release_date":"2026-01-01"},
 "deepseek/deepseek-v4.1-flash":{"id":"deepseek/deepseek-v4.1-flash","release_date":"2026-06-01"}}}}`

type fixtureFetcher struct {
	mu   sync.Mutex
	urls []string
	body string
	err  error
}

func (f *fixtureFetcher) Fetch(_ context.Context, url string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.body), nil
}

// A refresh driven by the real root graph (settings, persisted policy,
// normalizer, path inventory, usability router over the production chat
// factory, gate, SQLite cycle, cache publication) ends at the model a NEW run
// sends upstream: old run X stays on X, new run Y runs on Y, and any failure
// keeps X.
func TestModelRefreshRootAdvancesNewRunsAndKeepsWorkingModelOnFailure(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	for _, tc := range []struct {
		name              string
		fetchErr          error
		guardedFetch      bool
		rejectNew         bool
		wantAdvance       bool
		wantStatus        string
		corruptPinVersion any
	}{
		{name: "newer model confirmed on the only path", wantAdvance: true},
		{name: "corrupt TEXT pin version does not block discovery", corruptPinVersion: "broken", wantAdvance: true},
		{name: "corrupt REAL pin version does not block discovery", corruptPinVersion: 0.5, wantAdvance: true},
		{name: "corrupt BLOB pin version cannot masquerade as legacy", corruptPinVersion: []byte("0"), wantAdvance: true},
		{name: "fetch failure keeps the working model", fetchErr: errors.New("catalog down"), wantStatus: "fetch_failed"},
		{name: "guarded fetch keeps the working model and redacts query tokens", guardedFetch: true, wantStatus: "fetch_failed"},
		{name: "candidate not executable keeps the working model", rejectNew: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var models []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				models = append(models, body.Model)
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer refresh-key" || (tc.rejectNew && body.Model == newModel) {
					http.Error(w, "refused", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, `profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: refresh-sink
    model: `+oldModel+`
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(workspace string) {
				writeWorkflow(t, workspace, "refresh-exec", `{"name":"refresh-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "refresh-exec")
			})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
			require.NoError(t, conns.Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
			storeConfiguredFollowing(t, ctx, ds, "worker")
			catalogURL := "https://catalog.example.test/api.json"
			if tc.guardedFetch {
				catalogURL = "https://localhost/catalog?token=FETCHSECRET"
			}
			_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
			require.NoError(t, err)

			old, _, err := ds.eng.Trigger(ctx, "refresh-exec", nil, "normal")
			require.NoError(t, err)
			assert.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)

			var corruptID uuid.UUID
			if tc.corruptPinVersion != nil {
				// A paused corrupt run and a healthy legacy run coexist with
				// the completed pinned run. Only the corrupt run is isolated.
				completed, err := ds.instanceRepo.Get(ctx, old)
				require.NoError(t, err)
				for _, corrupt := range []bool{false, true} {
					run := *completed
					run.ID, run.State, run.CompletedAt = uuid.New(), instance.StatePaused, nil
					require.NoError(t, ds.instanceRepo.Create(ctx, &run))
					var version any = 0
					if corrupt {
						version, corruptID = tc.corruptPinVersion, run.ID
					}
					_, err = ds.db.Exec(`UPDATE instances SET model_pins = NULL, model_pins_version = ? WHERE id = ?`, version, run.ID.String())
					require.NoError(t, err)
				}
			}

			fetcher := &fixtureFetcher{body: refreshCatalogFixture, err: tc.fetchErr}
			deps := ds.refreshDeps
			deps.Fetcher = fetcher
			var logs bytes.Buffer
			if tc.guardedFetch {
				deps.Fetcher = nil // use the real guarded fetcher constructed by the root
				deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			if tc.guardedFetch {
				assert.Contains(t, logs.String(), "model catalog fetch failed", "the persisted URL reaches the real guarded fetcher")
				assert.Contains(t, logs.String(), "outbound: blocked dial to internal address")
				assert.NotContains(t, logs.String(), "FETCHSECRET")
				assert.NotContains(t, logs.String(), catalogURL)
				public, err := refresh.admin.List(ctx)
				require.NoError(t, err)
				raw, err := json.Marshal(public)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), "FETCHSECRET")
				require.NotEmpty(t, public.Reports)
				assert.Equal(t, "fetch_failed", public.Reports[len(public.Reports)-1].PathStatus["catalog"])
			} else {
				assert.Equal(t, []string{catalogURL}, fetcher.urls, "the fetch must use the persisted settings URL")
			}

			if tc.corruptPinVersion != nil {
				var state string
				var version any
				require.NoError(t, ds.db.QueryRow(`SELECT state, model_pins_version FROM instances WHERE id = ?`, corruptID.String()).Scan(&state, &version))
				assert.Equal(t, "paused", state, "the refresh's inventory read never transitions a run (the engine fails it on load)")
				assert.Equal(t, tc.corruptPinVersion, version)
			}
			want := oldModel
			if tc.wantAdvance {
				want = newModel
				require.Len(t, report.Changes, 1)
				assert.Equal(t, newModel, report.Changes[0].ToModel)
				assert.False(t, report.PublicationPending)
			} else {
				assert.Empty(t, report.Changes)
			}
			if tc.wantStatus != "" {
				assert.Equal(t, tc.wantStatus, report.PathStatus["catalog"])
			}
			stored, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, want, stored.Model)
			pending, err := ds.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			assert.Empty(t, pending)
			var pendingReports int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_refresh_reports WHERE publication_pending=1`).Scan(&pendingReports))
			assert.Zero(t, pendingReports)

			mu.Lock()
			models = nil
			mu.Unlock()
			fresh, _, err := ds.eng.Trigger(ctx, "refresh-exec", nil, "normal")
			require.NoError(t, err)
			freshRun := waitTerminal(t, ds, fresh)
			assert.Equal(t, instance.StateCompleted, freshRun.State)
			assert.Equal(t, want, freshRun.ModelPins["worker"])
			mu.Lock()
			assert.Equal(t, []string{want}, models, "the new run's request body names the executed model")
			mu.Unlock()
			oldRun, err := ds.instanceRepo.Get(ctx, old)
			require.NoError(t, err)
			assert.Equal(t, oldModel, oldRun.ModelPins["worker"])
		})
	}
}

// The root builds the orchestration but starts nothing; start runs the startup
// pass through the coordinator, and stop (via domainStack.Close) joins it.
func TestModelRefreshStartRunsStartupPassAndCloseJoinsLoop(t *testing.T) {
	var mu sync.Mutex
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds, closeStack := bootSinkRootWithClose(t, `profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: refresh-sink
    model: deepseek/deepseek-v4-flash
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(string) {})
	require.NotNil(t, ds.modelRefresh, "the root assembles the refresh")
	assert.Nil(t, ds.modelRefresh.cancel, "building the stack must not start the loop")
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")

	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	ds.modelRefresh = refresh // Close stops whichever refresh the stack holds.
	require.NoError(t, refresh.startupPass(context.Background()))
	refresh.startScheduled(context.Background())
	refresh.startScheduled(context.Background()) // idempotent
	require.Eventually(t, func() bool {
		p, err := ds.profileSvc.Get(ctx, "worker")
		return err == nil && p.Model == "deepseek/deepseek-v4.1-flash"
	}, 10*time.Second, 20*time.Millisecond, "the startup pass advances the following profile")
	closeStack()
	_, err = refresh.coordinator.Refresh(context.Background())
	assert.ErrorIs(t, err, modelcatalog.ErrRefreshClosed, "Close joined and closed the coordinator")
}

// A provider path that no following profile consumes (here: a pinned profile on
// its own connection) is still probed by the root graph, and the executed-model
// proof lands in durable evidence; the pinned profile never moves.
func TestModelRefreshRootRecordsUsabilityForPathsWithoutFollowers(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	var mu sync.Mutex
	hits := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hits[r.Header.Get("Authorization")+"|"+body.Model]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds := bootSinkRoot(t, `profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: refresh-sink
    model: `+oldModel+`
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "solo-sink", Provider: "openrouter", BaseURL: upstream.URL}, "solo-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")
	// The standalone pinned profile names a connection that only exists now, so it
	// is created through the admin path (the YAML seed runs before any connection
	// does and would skip it).
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "solo", Kind: provider.ProfileKindChat, Backend: "claude-code", ConnectionRef: "solo-sink", Model: oldModel, SelectionMode: provider.SelectionPinned})
	require.NoError(t, err)
	catalogURL := "https://catalog.example.test/api.json"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)

	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1, "only the follower moves")
	assert.Equal(t, "worker", report.Changes[0].Alias)

	mu.Lock()
	assert.Positive(t, hits["Bearer solo-key|"+newModel], "the standalone path's own credential proved the newest model")
	mu.Unlock()
	solo, err := ds.profileSvc.Get(ctx, "solo")
	require.NoError(t, err)
	assert.Equal(t, oldModel, solo.Model, "a pinned profile never moves")

	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	paths := map[string]bool{}
	for _, ev := range view.Evidence {
		if ev.Confirmed() && ev.NativeModel == newModel {
			paths[ev.PathKey] = true
		}
	}
	assert.Len(t, paths, 2, "durable confirmed evidence on the follower's path and on the path without followers")
}
