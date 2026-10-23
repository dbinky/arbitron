package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cold prospective discovery uses the same SQLite transaction as a scheduled
// cycle. No memory-only proof may survive a failed write, and later profile
// creation/advancement must not rewrite its original no-change events.
func TestCatalogProspectiveBootstrapAtomicFrozenEventsRoot(t *testing.T) {
	const oldModel, newModel, unknownModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash", "vendor/native-preview-20261018"
	const unknownCanonical = "vendor/native-canonical-20261018"
	models := []string{oldModel, newModel, unknownModel}
	for _, faultTable := range []string{"model_announcement_events", "model_refresh_reports", "model_probe_cursors"} {
		t.Run(faultTable, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var mu sync.Mutex
			calls := map[string]int{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string                           `json:"model"`
					Stream   bool                             `json:"stream"`
					Messages []struct{ Role, Content string } `json:"messages"`
					Tools    []any                            `json:"tools"`
				}
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer durable-bootstrap-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Stream || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content == "" || len(body.Tools) != 0 || (body.Model != oldModel && body.Model != newModel && body.Model != unknownModel) {
					http.Error(w, "invalid execution inputs", http.StatusBadRequest)
					return
				}
				mu.Lock()
				calls[body.Model]++
				mu.Unlock()
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"durable-bootstrap-ok"},"finish_reason":"stop"}]}`, body.Model)
			}))
			defer upstream.Close()
			ds, closeStack := bootSinkRootWithClose(t, "profiles: []\n", func(ws string) {
				writeWorkflow(t, ws, "durable-bootstrap", `{"name":"durable-bootstrap","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"later","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, ws, "durable-bootstrap")
			})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: "durable", Provider: "openrouter", BaseURL: upstream.URL}, "durable-bootstrap-key"))
			deps := ds.refreshDeps
			deps.FetchTransport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
					return nil, fmt.Errorf("unexpected catalog source")
				}
				source := map[string]any{}
				for _, model := range models {
					row := map[string]any{"id": model, "release_date": "2026-10-18"}
					if model == unknownModel {
						row["canonical_model_id"] = unknownCanonical
					}
					source[model] = row
				}
				raw, err := json.Marshal(map[string]any{"openrouter": map[string]any{"id": "openrouter", "models": source}})
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
			})
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
			profile := provider.Profile{Alias: "later", Kind: provider.ProfileKindChat, ConnectionRef: "durable", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}}

			_, err = ds.db.Exec(`CREATE TRIGGER bootstrap_atomic_fault BEFORE INSERT ON ` + faultTable + ` BEGIN SELECT RAISE(ABORT,'bootstrap atomic fault'); END`)
			require.NoError(t, err)
			_, err = refresh.candidates.Newest(ctx, profile)
			require.ErrorContains(t, err, "bootstrap atomic fault")
			view, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, view.Catalog.Slices)
			assert.Empty(t, view.HistoricalModels)
			assert.Empty(t, view.HistoricalEntries)
			assert.Empty(t, view.Evidence)
			assert.Empty(t, view.ProbeCursor)
			assert.Empty(t, view.Reports)
			for _, table := range []string{"model_first_observations", "model_announcement_events", "profiles", "profile_advancement_journal"} {
				var count int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
				assert.Zero(t, count, table)
			}
			_, err = ds.db.Exec(`DROP TRIGGER bootstrap_atomic_fault`)
			require.NoError(t, err)
			candidate, err := refresh.candidates.Newest(ctx, profile)
			require.NoError(t, err)
			assert.Equal(t, newModel, candidate.Model)
			again, err := refresh.candidates.Newest(ctx, profile)
			require.NoError(t, err)
			assert.Equal(t, candidate, again)
			mu.Lock()
			for _, model := range models {
				assert.Equal(t, 2, calls[model], "rolled-back proof must be collected again, committed proof reused: %s", model)
			}
			mu.Unlock()
			view, err = ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.Len(t, view.Reports, 1)
			assert.Empty(t, view.Reports[0].Changes)
			assert.False(t, view.Reports[0].PublicationPending)
			require.Len(t, view.Evidence, len(models))
			require.NotEmpty(t, view.ProbeCursor)
			for _, ev := range view.Evidence {
				require.True(t, ev.Confirmed(), "%+v", ev)
			}
			frozen := catalogFrozenPayloads(t, ds)
			require.Len(t, frozen, len(models))
			for _, raw := range frozen {
				var payload modelcatalog.AnnouncementPayload
				require.NoError(t, json.Unmarshal([]byte(raw), &payload))
				assert.Empty(t, payload.Changes)
				assert.NotEmpty(t, payload.Paths)
			}
			public := modelcatalog.ProjectCatalog(view)
			var unknownID string
			for _, entry := range view.HistoricalEntries {
				if entry.NativeID == unknownModel {
					unknownID = entry.CanonicalID
					assert.Empty(t, entry.Tier)
					assert.Empty(t, entry.OrderKey)
					assert.Equal(t, "2026-10-18", entry.ReleaseDate)
				}
			}
			require.Equal(t, unknownCanonical, unknownID)
			require.Contains(t, frozen, unknownID)
			var unknownSource modelidentity.CatalogModel
			for _, cm := range view.HistoricalModels {
				if cm.NativeID == unknownModel {
					unknownSource = cm
				}
			}
			assert.Equal(t, modelidentity.CatalogModel{CatalogProvider: "openrouter", NativeID: unknownModel, CanonicalModelID: unknownCanonical, ReleaseDate: "2026-10-18", SourceRef: "https://models.dev/api.json#/openrouter/models/vendor~1native-preview-20261018"}, unknownSource)
			profile.Model = candidate.Model
			require.NoError(t, refresh.candidates.RevalidateStart(ctx, profile, candidate))
			var selectedProof modelcatalog.UsabilityEvidence
			for _, ev := range view.Evidence {
				if ev.CanonicalID == candidate.CanonicalID {
					selectedProof = ev
				}
			}
			require.True(t, selectedProof.Confirmed())
			for _, column := range []string{"path_key", "config_revision", "proof_ref"} {
				original := map[string]string{"path_key": selectedProof.PathKey, "config_revision": selectedProof.ConfigRevision, "proof_ref": selectedProof.ProofRef}[column]
				bad := "wrong-route-proof"
				if column == "proof_ref" {
					bad = ""
				}
				_, err = ds.db.Exec(`UPDATE model_usability_evidence SET `+column+`=? WHERE canonical_id=?`, bad, candidate.CanonicalID)
				require.NoError(t, err)
				assert.ErrorIs(t, refresh.candidates.RevalidateStart(ctx, profile, candidate), provider.ErrExecutionRevisionUnavailable, column)
				_, err = ds.db.Exec(`UPDATE model_usability_evidence SET `+column+`=? WHERE canonical_id=?`, original, candidate.CanonicalID)
				require.NoError(t, err)
			}
			require.NoError(t, refresh.candidates.RevalidateStart(ctx, profile, candidate))
			for _, entry := range public.Entries {
				assert.Equal(t, modelcatalog.UsabilityConfirmed, entry.Usability)
				if entry.CanonicalID == unknownID {
					assert.Empty(t, entry.Tier)
				}
			}

			// Create an older eligible follower after the original observation,
			// then let the actual refresh commit an ordinary replacement.
			profile.Model = oldModel
			_, _, err = ds.profileSvc.Create(ctx, profile)
			require.NoError(t, err)
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Len(t, report.Changes, 1)
			assert.Equal(t, oldModel, report.Changes[0].FromModel)
			assert.Equal(t, newModel, report.Changes[0].ToModel)
			assert.Empty(t, report.NewlyUsable)
			assert.Equal(t, frozen, catalogFrozenPayloads(t, ds))
			pending, err := ds.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			assert.Empty(t, pending)
			fresh, _, err := ds.eng.Trigger(ctx, "durable-bootstrap", nil, "normal")
			require.NoError(t, err)
			run := waitTerminal(t, ds, fresh)
			assert.Equal(t, instance.StateCompleted, run.State)
			assert.Equal(t, newModel, run.ModelPins[profile.Alias])
			var output string
			require.NoError(t, ds.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id='ask'`, fresh.String()).Scan(&output))
			assert.Contains(t, output, "durable-bootstrap-ok")
			view, err = ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.Len(t, view.Reports, 2)
			assert.Equal(t, report.Changes, view.Reports[1].Changes)

			mu.Lock()
			beforeReopen := maps.Clone(calls)
			mu.Unlock()
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			refresh.stop()
			closeStack()
			reopened, err := initDomain(context.Background(), configPath)
			require.NoError(t, err)
			defer reopened.Close(context.Background())
			restored, err := reopened.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, view, restored, "metadata, exact proof, cursors and original reports must survive reopen")
			assert.Equal(t, frozen, catalogFrozenPayloads(t, reopened))
			profile.Model = newModel
			_, err = reopened.modelRefresh.candidates.ValidateStart(ctx, profile)
			require.NoError(t, err)
			mu.Lock()
			assert.Equal(t, beforeReopen, calls, "unchanged persisted routes must not be re-probed after restart")
			mu.Unlock()

			call := mcpToolCaller(t, ctx, reopened)
			assertList := func(want string) {
				t.Helper()
				res, _ := call("model_catalog_list", nil)
				require.False(t, res.IsError, "%+v", res.Content)
				text := res.Content[0].(gomcp.TextContent).Text
				var listed modelcatalog.PublicCatalogView
				require.NoError(t, json.Unmarshal([]byte(text), &listed))
				require.Len(t, listed.Entries, len(models))
				found := false
				for _, entry := range listed.Entries {
					if entry.CanonicalID != unknownID {
						continue
					}
					found = true
					assert.Empty(t, entry.Tier)
					assert.Equal(t, want, entry.Usability)
					require.NotEmpty(t, entry.Paths)
					for _, path := range entry.Paths {
						assert.Equal(t, want, path.State)
					}
				}
				require.True(t, found, "the unclassified native model must survive the registered list projection")
				assert.NotContains(t, text, "durable-bootstrap-key")
				assert.NotContains(t, text, "ProofRef")
				assert.NotContains(t, text, "proof_ref")
				assert.NotContains(t, text, unknownSource.SourceRef)
			}
			assertList(modelcatalog.UsabilityConfirmed)
			// A storage fault cannot make the public per-path label claim more
			// than the persisted execution proof, nor alter the original event.
			_, err = reopened.db.Exec(`UPDATE model_usability_evidence SET proof_ref='' WHERE canonical_id=?`, unknownID)
			require.NoError(t, err)
			assertList(modelcatalog.UsabilityUnknown)
			assert.Equal(t, frozen, catalogFrozenPayloads(t, reopened))
		})
	}
}

func catalogFrozenPayloads(t *testing.T, ds *domainStack) map[string]string {
	t.Helper()
	rows, err := ds.db.Query(`SELECT canonical_id,payload,state FROM model_announcement_events ORDER BY canonical_id`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, payload, state string
		require.NoError(t, rows.Scan(&id, &payload, &state))
		assert.Equal(t, modelcatalog.EventSuppressed, state, "the actual webhook setting remains empty")
		assert.NotContains(t, payload, "durable-bootstrap-key")
		out[id] = payload
	}
	require.NoError(t, rows.Err())
	return out
}

func TestCatalogOpaquePathsNeverPersistConfirmationRoot(t *testing.T) {
	for _, route := range []string{"azure", "bedrock", "unmapped-provider"} {
		t.Run(route, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				http.Error(w, "unsupported routes must not be probed", http.StatusBadRequest)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: route, Provider: route, BaseURL: upstream.URL, Extras: map[string]string{"api-version": "2025-01-01", "region": "us-east-1", "access-key-id": "test-account"}}, "must-not-be-used"))
			receiver := newAnnouncementReceiver(t)
			setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/opaque?token=private-setting")
			deps := ds.refreshDeps
			deps.AnnouncementClient = &http.Client{Transport: receiver.transport}
			deps.FetchTransport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
					return nil, fmt.Errorf("wrong catalog source")
				}
				raw := fmt.Sprintf(`{%q:{"id":%q,"models":{"native-id":{"id":"native-id"}}}}`, route, route)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw)), Request: r}, nil
			})
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.Empty(t, report.Changes)
			assert.Empty(t, report.NewlyUsable)
			view, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.Len(t, view.Evidence, 1, "unsupported paths still persist an explicit unknown check")
			assert.Equal(t, "native-id", view.Evidence[0].NativeModel, "the durable failure identifies the attempted native model")
			assert.Equal(t, modelcatalog.UsabilityUnknown, view.Evidence[0].State)
			assert.Empty(t, view.Evidence[0].ProofRef)
			public := modelcatalog.ProjectCatalog(view)
			require.Len(t, public.Entries, 1)
			assert.Equal(t, "native-id", public.Entries[0].NativeID)
			assert.Equal(t, modelcatalog.UsabilityUnknown, public.Entries[0].Usability)
			assert.Empty(t, catalogFrozenPayloads(t, ds))
			assert.Zero(t, requests.Load())
			assert.Empty(t, receiver.cards("native-id"), "D8 never reaches a receiver even when delivery is configured")
		})
	}
}

// A rotated route is not confirmation of its old path/config, even when the
// provider returns the requested native model. The real router rejects it
// before the shared cycle store can create an observation or an event.
func TestCatalogDiscoveryRejectsChangedConfigRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const model = "vendor/native-preview-20261018"
	ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	var conn provider.Connection
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []any  `json:"messages"`
			Tools    []any  `json:"tools"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer original-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != model || len(body.Messages) != 1 || len(body.Tools) != 0 {
			http.Error(w, "invalid probe inputs", http.StatusBadRequest)
			return
		}
		key := "rotated-key"
		if err := conns.Update(ctx, conn, &key); err != nil {
			http.Error(w, "rotation failed", 500)
			return
		}
		requests.Add(1)
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, model)
	}))
	defer upstream.Close()
	conn = provider.Connection{Name: "rotating", Provider: "openrouter", BaseURL: upstream.URL}
	require.NoError(t, conns.Create(ctx, conn, "original-key"))
	deps := ds.refreshDeps
	deps.FetchTransport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.String() != "https://models.dev/api.json" {
			return nil, fmt.Errorf("wrong catalog source")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"openrouter":{"id":"openrouter","models":{"` + model + `":{"id":"` + model + `"}}}}`)), Request: r}, nil
	})
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, requests.Load())
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, view.Evidence, 1)
	assert.False(t, view.Evidence[0].Confirmed())
	assert.Empty(t, view.Evidence[0].ProofRef)
	public := modelcatalog.ProjectCatalog(view)
	require.Len(t, public.Entries, 1)
	assert.Equal(t, modelcatalog.UsabilityUnknown, public.Entries[0].Usability)
	assert.Empty(t, catalogFrozenPayloads(t, ds))
}
