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
	"sync/atomic"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The current full refresh can discover classified models without followers.
// Unknown-tier enumeration and prospective bootstrap remain P3T6 obligations;
// the SQLite native-only tests exercise their persistence contract separately.
func TestCatalogDiscoveryWithoutAdvancesRoot(t *testing.T) {
	const model = "deepseek/deepseek-v4.1-flash"
	for _, outcome := range []string{"exact", "wrong", "missing"} {
		t.Run(outcome, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string                           `json:"model"`
					Stream   bool                             `json:"stream"`
					Messages []struct{ Role, Content string } `json:"messages"`
					Tools    []any                            `json:"tools"`
				}
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") || r.Header.Get("Authorization") != "Bearer discovery-secret" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != model || body.Stream || len(body.Messages) != 1 || body.Messages[0].Content == "" || len(body.Tools) != 0 {
					http.Error(w, "invalid execution input", http.StatusBadRequest)
					return
				}
				calls.Add(1)
				actual := model
				if outcome == "wrong" {
					actual = "fallback"
				} else if outcome == "missing" {
					actual = ""
				}
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, actual)
			}))
			defer upstream.Close()
			ds, closeStack := bootSinkRootWithClose(t, "profiles: []\n", func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
			require.NoError(t, conns.Create(ctx, provider.Connection{Name: "discovery", Provider: "openrouter", BaseURL: upstream.URL}, "discovery-secret"))
			url := "https://catalog.example.test/discovery.json"
			_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &url})
			require.NoError(t, err)
			deps := ds.refreshDeps
			deps.FetchTransport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != url {
					return nil, fmt.Errorf("unexpected catalog request")
				}
				body := `{"openrouter":{"id":"openrouter","models":{"` + model + `":{"id":"` + model + `","release_date":"2026-10-18"}}}}`
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.Empty(t, report.Changes)
			assert.EqualValues(t, 1, calls.Load())
			assertStored := func(stack *domainStack) string {
				t.Helper()
				view, err := stack.catalogStore.Snapshot(ctx)
				require.NoError(t, err)
				require.NotEmpty(t, view.ProbeCursor)
				assert.Len(t, view.Evidence, 1)
				if len(view.Evidence) == 1 {
					assert.Equal(t, model, view.Evidence[0].NativeModel)
					assert.NotEmpty(t, view.Evidence[0].PathKey)
					assert.NotEmpty(t, view.Evidence[0].ConfigRevision)
				}
				public := modelcatalog.ProjectCatalog(view)
				require.Len(t, public.Entries, 1)
				var count int
				require.NoError(t, stack.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&count))
				if outcome != "exact" {
					assert.Zero(t, count)
					assert.Equal(t, modelcatalog.UsabilityUnknown, public.Entries[0].Usability)
					return ""
				}
				assert.Equal(t, modelcatalog.UsabilityConfirmed, public.Entries[0].Usability)
				require.Equal(t, 1, count)
				var raw, state string
				require.NoError(t, stack.db.QueryRow(`SELECT payload,state FROM model_announcement_events`).Scan(&raw, &state))
				var payload modelcatalog.AnnouncementPayload
				require.NoError(t, json.Unmarshal([]byte(raw), &payload))
				assert.Equal(t, modelcatalog.EventSuppressed, state, "the real default webhook is empty")
				assert.Equal(t, public.Entries[0].CanonicalID, payload.CanonicalID)
				assert.Equal(t, view.Evidence, payload.Paths)
				assert.Empty(t, payload.Changes)
				return raw
			}
			frozen := assertStored(ds)
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			refresh.stop()
			closeStack()
			reopened, err := initDomain(context.Background(), configPath)
			require.NoError(t, err)
			t.Cleanup(func() { reopened.Close(context.Background()) })
			assert.Equal(t, frozen, assertStored(reopened))
		})
	}
}
