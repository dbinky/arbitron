package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryAllSourceCandidatesOnEveryPathRoot(t *testing.T) {
	const oldModel = "deepseek/deepseek-v4-flash"
	models := []string{oldModel, "deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v3-flash:0731", "deepseek/deepseek-v4-pro", "opaque-native"}
	for _, exact := range []bool{true, false} {
		t.Run(fmt.Sprint(exact), func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Tools    []any  `json:"tools"`
					Messages []any  `json:"messages"`
				}
				if r.Header.Get("Authorization") != "Bearer all-key" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Tools) != 0 || len(body.Messages) == 0 {
					http.Error(w, "invalid request", 400)
					return
				}
				mu.Lock()
				calls[body.Model]++
				mu.Unlock()
				actual := body.Model
				if !exact {
					actual = "fallback"
				}
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, actual)
			}))
			defer upstream.Close()
			ds, closeStack := bootSinkRootWithClose(t, `profiles:
  - alias: worker
    kind: chat
    connectionref: all
    model: `+oldModel+`
    selection_mode: following
    follow_target: {provider: openrouter, tier: flash}
`, func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "all", Provider: "openrouter", BaseURL: upstream.URL}, "all-key"))
			storeConfiguredFollowing(t, ctx, ds, "worker")
			source := map[string]any{}
			for _, model := range models {
				source[model] = map[string]any{"id": model}
			}
			raw, err := json.Marshal(map[string]any{"openrouter": map[string]any{"id": "openrouter", "models": source}})
			require.NoError(t, err)
			deps := ds.refreshDeps
			deps.FetchTransport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://models.dev/api.json" {
					return nil, fmt.Errorf("wrong source")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
			})
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			if exact {
				require.Len(t, report.Changes, 1)
			} else {
				assert.Empty(t, report.Changes)
			}
			assertState := func(stack *domainStack) {
				view, err := stack.catalogStore.Snapshot(ctx)
				require.NoError(t, err)
				require.NotEmpty(t, view.ProbeCursor)
				assert.Len(t, view.Evidence, len(models))
				public := modelcatalog.ProjectCatalog(view)
				assert.Len(t, public.Entries, len(models))
				var events int
				require.NoError(t, stack.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
				if exact {
					assert.Equal(t, len(models), events)
				} else {
					assert.Zero(t, events)
				}
				for _, e := range public.Entries {
					if exact {
						assert.Equal(t, modelcatalog.UsabilityConfirmed, e.Usability)
					} else {
						assert.Equal(t, modelcatalog.UsabilityUnknown, e.Usability)
					}
				}
				for _, e := range view.HistoricalEntries {
					if strings.Contains(e.NativeID, "opaque") {
						assert.Empty(t, e.Tier)
						assert.Empty(t, e.OrderKey)
					}
				}
			}
			assertState(ds)
			mu.Lock()
			for _, model := range models {
				assert.Equal(t, 1, calls[model], model)
			}
			mu.Unlock()
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			refresh.stop()
			closeStack()
			reopened, err := initDomain(context.Background(), configPath)
			require.NoError(t, err)
			defer reopened.Close(context.Background())
			assertState(reopened)
		})
	}
}
