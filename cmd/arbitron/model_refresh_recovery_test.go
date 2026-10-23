package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshRepairsPublicationWithoutRestart(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	for _, kind := range []string{"direct", "cycle", "superseded", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			requests := make(chan string, 32)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				if r.Header.Get("Authorization") != "Bearer repair-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" {
					http.Error(w, "bad request", 400)
					return
				}
				requests <- body.Model
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"repaired"},"finish_reason":"stop"}]}`, body.Model)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, "profiles: []\n", func(workspace string) {
				writeWorkflow(t, workspace, "repair-exec", `{"name":"repair-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "repair-exec")
			})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: "repair", Provider: "openrouter", BaseURL: upstream.URL}, "repair-key"))
			deps := ds.refreshDeps
			fetch := &fixtureFetcher{body: refreshCatalogFixture}
			deps.Fetcher = fetch
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
			p, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "repair", Model: oldModel, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}})
			require.NoError(t, err)
			old, _, err := ds.eng.Trigger(ctx, "repair-exec", nil, "normal")
			require.NoError(t, err)
			require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
			_, err = ds.db.Exec(`CREATE TRIGGER repair_ack_fault BEFORE INSERT ON model_publication_acks BEGIN SELECT RAISE(ABORT,'ack unavailable'); END`)
			require.NoError(t, err)
			if kind == "direct" {
				snap, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				_, err = ds.profileSvc.AdvanceModel(ctx, provider.AdvanceRequest{ID: "repair-direct", Expected: p, Model: newModel, CanonicalID: "deepseek/deepseek-v4.1-flash", PathRevision: snap.Revision, ExecutionRevision: snap.ExecutionRevision})
				require.ErrorIs(t, err, provider.ErrPublicationPending)
			} else {
				_, err = refresh.coordinator.Refresh(ctx)
				require.ErrorIs(t, err, provider.ErrPublicationPending)
			}
			original, err := ds.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			require.Len(t, original, 1)
			wantModel := newModel
			switch kind {
			case "superseded":
				current, err := ds.profileSvc.Get(ctx, "worker")
				require.NoError(t, err)
				current.Model = "deepseek/deepseek-v4.2-flash"
				current.SelectionMode = provider.SelectionPinned
				current.FollowTarget = nil
				_, _, err = ds.profileSvc.Update(ctx, current)
				require.NoError(t, err)
				wantModel = current.Model
			case "deleted":
				require.NoError(t, ds.profileSvc.Delete(ctx, "worker"))
			}
			fetch.mu.Lock()
			fetch.err = errors.New("discovery down")
			fetch.mu.Unlock()
			_, err = refresh.coordinator.Refresh(ctx)
			require.ErrorIs(t, err, provider.ErrPublicationPending, "a repeated ack fault must not report success")
			_, err = ds.db.Exec(`DROP TRIGGER repair_ack_fault`)
			require.NoError(t, err)
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.False(t, report.PublicationPending)
			pending, err := ds.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			assert.Empty(t, pending)
			var journals, pendingReports int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journals))
			assert.Equal(t, 1, journals)
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_refresh_reports WHERE publication_pending=1`).Scan(&pendingReports))
			assert.Zero(t, pendingReports)
			for len(requests) > 0 {
				<-requests
			}
			prior, err := ds.instanceRepo.Get(ctx, old)
			require.NoError(t, err)
			assert.Equal(t, oldModel, prior.ModelPins["worker"])
			fresh, _, err := ds.eng.Trigger(ctx, "repair-exec", nil, "normal")
			if kind == "deleted" {
				require.Error(t, err, "recovery must not resurrect a deleted alias")
				assert.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			run := waitTerminal(t, ds, fresh)
			assert.Equal(t, instance.StateCompleted, run.State)
			require.Len(t, requests, 1)
			assert.Equal(t, wantModel, <-requests)
		})
	}
}
