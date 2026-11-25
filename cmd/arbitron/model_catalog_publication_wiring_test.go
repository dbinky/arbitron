package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This covers the store's existing production producer: direct advancement
// journal rows, including the no-report case. Refresh/root recovery remains
// the discovery runtime's separate acceptance obligation.
func TestCatalogPublicationRootAcknowledgesOnlyPublishedChanges(t *testing.T) {
	for _, ackFault := range []bool{false, true} {
		t.Run(fmt.Sprintf("ack_fault_%t", ackFault), func(t *testing.T) {
			var mu sync.Mutex
			expected := "deepseek/deepseek-v4-flash"
			var models []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
					Stream bool `json:"stream"`
				}
				err := json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				want := expected
				models = append(models, body.Model)
				mu.Unlock()
				if err != nil || r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer catalog-key" || body.Model != want || len(body.Messages) == 0 || body.Messages[len(body.Messages)-1].Content == "" || body.Stream {
					http.Error(w, "invalid model/auth/route/prompt", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"catalog-ok"},"finish_reason":"stop"}]}`, want)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, `profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: catalog-sink
    model: deepseek/deepseek-v4-flash
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(workspace string) {
				writeWorkflow(t, workspace, "catalog-exec", `{"name":"catalog-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "catalog-exec")
			})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
			require.NoError(t, conns.Create(ctx, provider.Connection{Name: "catalog-sink", Provider: "openrouter", BaseURL: upstream.URL}, "catalog-key"))
			storeConfiguredFollowing(t, ctx, ds, "worker")
			old, _, err := ds.eng.Trigger(ctx, "catalog-exec", nil, "normal")
			require.NoError(t, err)
			assert.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
			prior, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			snap, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			if ackFault {
				_, err = ds.db.Exec(`CREATE TRIGGER fail_catalog_ack BEFORE INSERT ON model_publication_acks BEGIN SELECT RAISE(ABORT,'ack fault'); END`)
				require.NoError(t, err)
			}
			adv, err := ds.profileSvc.AdvanceModel(ctx, provider.AdvanceRequest{ID: "catalog-direct", Expected: prior, Model: "deepseek/deepseek-v4.1-flash", CanonicalID: "deepseek/deepseek-v4.1-flash", PathRevision: snap.Revision, ExecutionRevision: snap.ExecutionRevision})
			if ackFault {
				require.ErrorIs(t, err, provider.ErrPublicationPending)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, "deepseek/deepseek-v4.1-flash", adv.ToModel)
			var acks, journal, reports int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_publication_acks WHERE advancement_id='catalog-direct'`).Scan(&acks))
			if ackFault {
				assert.Zero(t, acks)
			} else {
				assert.Equal(t, 1, acks)
			}
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journal))
			assert.Equal(t, 1, journal)
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_refresh_reports`).Scan(&reports))
			assert.Zero(t, reports)
			mu.Lock()
			expected = adv.ToModel
			mu.Unlock()
			fresh, _, err := ds.eng.Trigger(ctx, "catalog-exec", nil, "normal")
			require.NoError(t, err)
			freshRun := waitTerminal(t, ds, fresh)
			assert.Equal(t, instance.StateCompleted, freshRun.State)
			assert.Equal(t, adv.ToModel, freshRun.ModelPins["worker"])
			oldRun, err := ds.instanceRepo.Get(ctx, old)
			require.NoError(t, err)
			assert.Equal(t, prior.Model, oldRun.ModelPins["worker"])
			var output string
			require.NoError(t, ds.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id='ask'`, fresh.String()).Scan(&output))
			assert.Contains(t, output, "catalog-ok")
			mu.Lock()
			assert.Equal(t, []string{prior.Model, adv.ToModel}, models)
			mu.Unlock()
			// The store the actual root constructs must see these exact persisted rows.
			pending, err := ds.catalogStore.Unpublished(context.Background())
			require.NoError(t, err)
			if ackFault {
				assert.Equal(t, []provider.Advancement{adv}, pending)
			} else {
				assert.Empty(t, pending)
			}
		})
	}
}
