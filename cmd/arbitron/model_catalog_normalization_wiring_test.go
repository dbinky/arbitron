package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type catalogHTTPLeaf func(*http.Request) (*http.Response, error)

func (f catalogHTTPLeaf) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Only HTTP I/O is replaced: the root constructs the guarded bounded fetcher,
// normalizer, mapper, inventory, probe router, transaction and live cache.
func TestFetchNormalizeAllProvidersRoot(t *testing.T) {
	const oldModel = "deepseek/deepseek-v4-flash"
	const newModel = "deepseek/deepseek-v4.1-flash"
	const unknown = "vendor/unclassified-model:tag"
	const source = "https://catalog.example.test/custom.json?revision=17"
	var mu sync.Mutex
	var executed []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) == 0 || r.Header.Get("Authorization") != "Bearer normalization-key" || (body.Model != oldModel && body.Model != newModel) {
			http.Error(w, "missing execution input", http.StatusBadRequest)
			return
		}
		mu.Lock()
		executed = append(executed, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"normalized output"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds, closeStack := bootSinkRootWithClose(t, `profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: normalization-sink
    model: `+oldModel+`
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(workspace string) {
		writeWorkflow(t, workspace, "normalization-exec", `{"name":"normalization-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "normalization-exec")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: "normalization-sink", Provider: "openrouter", BaseURL: upstream.URL}, "normalization-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")
	catalogURL := source
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)
	raw, err := os.ReadFile("../../adapter/modelcatalog/testdata/models.dev.json")
	require.NoError(t, err)
	var catalog map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &catalog))
	rows := catalog["openrouter"]["models"].(map[string]any)
	// Explicit schema-shaped test mutation; captured provider rows are retained.
	rows[unknown] = map[string]any{"id": unknown}
	body, err := json.Marshal(catalog)
	require.NoError(t, err)
	status := http.StatusOK
	var fetched []string
	deps := ds.refreshDeps
	deps.FetchTransport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != source || r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" || r.Body != nil {
			return nil, fmt.Errorf("missing configured catalog request")
		}
		fetched = append(fetched, r.URL.String())
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	})
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Changes)
	initial, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), entryOf(t, initial.Catalog.Slices, "openrouter", oldModel).PolicyRevision)
	// Change policy after storing X. Capture just the major: release_date,
	// not 4.1 > 4, must now advance. Historical X must also be remapped.
	_, err = ds.IdentityPolicy.Update(ctx, 1, []modelidentity.PatternRule{{Providers: []string{"openrouter"}, Tier: "flash", Pattern: `deepseek-v(\d+)(?:\.\d+)?-flash`, VersionGroups: []int{1}}})
	require.NoError(t, err)
	// Remove X from the latest response: its original source metadata must be
	// retained historically and remapped under the same policy as Y.
	delete(rows, oldModel)
	rows[newModel] = map[string]any{"id": newModel, "canonical_model_id": newModel, "release_date": "2026-06-01"}
	body, err = json.Marshal(catalog)
	require.NoError(t, err)
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, newModel, report.Changes[0].ToModel)
	assert.Equal(t, []string{source, source}, fetched)

	assertStored := func(stack *domainStack) modelcatalog.CatalogView {
		t.Helper()
		view, err := stack.catalogStore.Snapshot(ctx)
		require.NoError(t, err)
		require.Len(t, view.Catalog.Slices, 2)
		for _, sl := range view.Catalog.Slices {
			require.NotEmpty(t, sl.Raw)
			wantRaw, err := json.Marshal(catalog[sl.Provider])
			require.NoError(t, err)
			assert.JSONEq(t, string(wantRaw), string(sl.Raw))
			sourceRows := catalog[sl.Provider]["models"].(map[string]any)
			require.Len(t, sl.Models, len(sourceRows))
			require.Len(t, sl.Entries, len(sourceRows))
			for _, model := range sl.Models {
				row, ok := sourceRows[model.NativeID].(map[string]any)
				require.True(t, ok, "stored model must come from the fetched provider slice")
				canonical, _ := row["canonical_model_id"].(string)
				date, _ := row["release_date"].(string)
				assert.Equal(t, canonical, model.CanonicalModelID)
				assert.Equal(t, date, model.ReleaseDate)
				assert.True(t, strings.HasPrefix(model.SourceRef, source+"#/"))
				entry := entryOf(t, view.Catalog.Slices, sl.Provider, model.NativeID)
				assert.Equal(t, model.SourceRef, entry.SourceRef)
				assert.Equal(t, date, entry.ReleaseDate)
			}
		}
		y := entryOf(t, view.Catalog.Slices, "openrouter", newModel)
		assert.Equal(t, int64(2), y.PolicyRevision)
		assert.Equal(t, "4", y.OrderKey)
		assert.Equal(t, "2026-06-01", y.ReleaseDate)
		assert.Equal(t, newModel, y.CanonicalID)
		assert.Equal(t, source+"#/openrouter/models/deepseek~1deepseek-v4.1-flash", y.SourceRef)
		u := entryOf(t, view.Catalog.Slices, "openrouter", unknown)
		assert.Empty(t, u.Tier)
		assert.Empty(t, u.OrderKey)
		assert.False(t, u.Immutable)
		unknownRef, err := url.Parse(u.SourceRef)
		require.NoError(t, err)
		assert.Equal(t, "/openrouter/models/vendor~1unclassified-model:tag", unknownRef.Fragment)
		unknownRef.Fragment = ""
		assert.Equal(t, source, unknownRef.String())
		var historicalX, sourceY bool
		for _, model := range view.HistoricalModels {
			if model.CatalogProvider == "openrouter" && model.NativeID == oldModel {
				historicalX = true
				assert.Equal(t, oldModel, model.CanonicalModelID)
				assert.Equal(t, "2026-04-24", model.ReleaseDate)
			}
			if model.CatalogProvider == "openrouter" && model.NativeID == newModel {
				sourceY = true
				assert.Equal(t, newModel, model.CanonicalModelID)
				assert.Equal(t, "2026-06-01", model.ReleaseDate)
			}
		}
		assert.True(t, historicalX)
		assert.True(t, sourceY)
		p, err := stack.profileSvc.Get(ctx, "worker")
		require.NoError(t, err)
		assert.Equal(t, newModel, p.Model)
		return view
	}
	before := assertStored(ds)
	// Bad responses never replace previously stored catalog data or working Y.
	for _, failure := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"HTTP error", http.StatusServiceUnavailable, `{}`, "fetch_failed"},
		{"foreign schema", http.StatusOK, `{"data":[]}`, "parse_error"},
		{"empty body", http.StatusOK, ``, "fetch_failed"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			status, body = failure.status, []byte(failure.body)
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.Equal(t, failure.want, report.PathStatus["catalog"])
			assert.Empty(t, report.Changes)
			after := assertStored(ds)
			assert.Equal(t, before.Catalog, after.Catalog)
			assert.Equal(t, before.HistoricalModels, after.HistoricalModels)
		})
	}
	mu.Lock()
	executed = nil
	mu.Unlock()
	run, _, err := ds.eng.Trigger(ctx, "normalization-exec", nil, "normal")
	require.NoError(t, err)
	done := waitTerminal(t, ds, run)
	assert.Equal(t, instance.StateCompleted, done.State)
	assert.Equal(t, newModel, done.ModelPins["worker"])
	trace, err := ds.nodeExecRepo.ListByInstance(ctx, run)
	require.NoError(t, err)
	require.Len(t, trace, 1)
	assert.Contains(t, fmt.Sprint(trace[0].Output), "normalized output")
	mu.Lock()
	assert.Equal(t, []string{newModel}, executed)
	mu.Unlock()
	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	refresh.stop()
	closeStack()
	reopened, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close(context.Background()) })
	assertStored(reopened)
}
