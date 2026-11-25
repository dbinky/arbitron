package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A following profile consumed by an interactive_llm node as well as a batch
// llm_call advances only when the candidate is proven on BOTH routes: the
// interactive route is probed by streaming through the production stream
// selector, and the new model is what the stored profile (and so a new run)
// uses afterwards. A stream that names another model, or errors, keeps the
// working model.
func TestModelRefreshRootInteractiveChatRouteProvesCandidateFromStream(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	for _, tc := range []struct {
		name        string
		streamModel string // model the stream frames name for the new model ("" = the requested one)
		wantAdvance bool
	}{
		{name: "stream names the requested model", wantAdvance: true},
		{name: "stream names a fallback model", streamModel: "fallback-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls atomic.Int32
			streamed := map[string]int{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model  string `json:"model"`
					Stream bool   `json:"stream"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.Header.Get("Authorization") != "Bearer refresh-key" {
					http.Error(w, "refused", http.StatusUnauthorized)
					return
				}
				calls.Add(1)
				executed := body.Model
				if body.Stream {
					mu.Lock()
					streamed[body.Model]++
					mu.Unlock()
					if body.Model == newModel && tc.streamModel != "" {
						executed = tc.streamModel
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", executed)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, executed)
			}))
			defer upstream.Close()
			ds, closeStack := bootSinkRootWithClose(t, `profiles:
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
				writeWorkflow(t, workspace, "refresh-batch", `{"name":"refresh-batch","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "refresh-batch")
				writeWorkflow(t, workspace, "refresh-chat", `{"name":"refresh-chat","nodes":[{"id":"talk","type":"interactive_llm","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "refresh-chat")
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "refresh-chat", "templates", "o.md"), []byte("orchestrate"), 0o600))
			})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
			storeConfiguredFollowing(t, ctx, ds, "worker")
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

			mu.Lock()
			assert.Positive(t, streamed[newModel], "the interactive route was probed by streaming the candidate")
			mu.Unlock()
			stored, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			if tc.wantAdvance {
				require.Len(t, report.Changes, 1)
				assert.Equal(t, newModel, stored.Model)
			} else {
				assert.Empty(t, report.Changes)
				assert.Equal(t, oldModel, stored.Model, "an unverified stream keeps the working model")
				return
			}
			before := calls.Load()
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			refresh.stop()
			closeStack()
			reopened, err := initDomain(context.Background(), configPath)
			require.NoError(t, err)
			defer reopened.Close(context.Background())
			deps = reopened.refreshDeps
			deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
			next, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer next.stop()
			second, err := next.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.Empty(t, second.Changes)
			assert.Equal(t, before, calls.Load(), "both batch and interactive confirmations survive reopen without expiry/re-probe")
			view, err := reopened.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			assert.Len(t, view.Evidence, 4, "two models on distinct batch and interactive path/configurations")
			for _, ev := range view.Evidence {
				assert.True(t, ev.Confirmed())
			}
			stored, err = reopened.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, newModel, stored.Model)
		})
	}
}
