package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	catalog "github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

var normalizationTime = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func normalizerFor(t *testing.T, source modelidentity.PolicySource) *Normalizer {
	t.Helper()
	mapper, err := modelidentity.NewIdentityMapper(source)
	require.NoError(t, err)
	n, err := NewNormalizer(mapper, func() time.Time { return normalizationTime })
	require.NoError(t, err)
	return n
}

func capturedCatalog(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/models.dev.json")
	require.NoError(t, err)
	return raw
}

func decodeCatalog(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()
	var value map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}

func encodeCatalog(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func TestFetchNormalizeAllProviders(t *testing.T) {
	raw := capturedCatalog(t)
	const source = "https://catalog.example/custom/api.json?revision=17"
	// Substitute only HTTP I/O. Reject a missing or changed configured destination.
	fetcher := NewFetcher()
	fetcher.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, source, req.URL.String())
		require.Equal(t, http.MethodGet, req.Method)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})
	downloaded, err := fetcher.Fetch(context.Background(), source)
	require.NoError(t, err)
	n := normalizerFor(t, shippedPolicy{})
	var port catalog.CatalogNormalizer = n
	snapshot, err := port.Normalize(context.Background(), source, downloaded)
	require.NoError(t, err)
	assert.Equal(t, normalizationTime, snapshot.FetchedAt)
	require.Len(t, snapshot.Slices, 2)
	original := decodeCatalog(t, raw)
	for i, provider := range []string{"anthropic", "openrouter"} {
		slice := snapshot.Slices[i]
		assert.Equal(t, provider, slice.Provider)
		assert.Equal(t, catalog.SliceStatusOK, slice.Status)
		assert.JSONEq(t, string(encodeCatalog(t, original[provider])), string(slice.Raw))
		rows := original[provider]["models"].(map[string]any)
		require.Len(t, slice.Models, len(rows))
		require.Len(t, slice.Entries, len(rows))
		for j, model := range slice.Models {
			row := rows[model.NativeID].(map[string]any)
			assert.Equal(t, provider, model.CatalogProvider)
			assert.Equal(t, row["id"], model.NativeID)
			assert.Equal(t, row["release_date"], model.ReleaseDate)
			canonical, _ := row["canonical_model_id"].(string)
			assert.Equal(t, canonical, model.CanonicalModelID)
			entry := slice.Entries[j]
			assert.Equal(t, model.NativeID, entry.NativeID)
			assert.Equal(t, model.ReleaseDate, entry.ReleaseDate)
			assert.Equal(t, model.SourceRef, entry.SourceRef)
			assert.Equal(t, int64(1), entry.PolicyRevision)
			u, err := url.Parse(model.SourceRef)
			require.NoError(t, err)
			assert.Equal(t, "/"+provider+"/models/"+strings.ReplaceAll(model.NativeID, "/", "~1"), u.Fragment)
			u.Fragment = ""
			assert.Equal(t, source, u.String())
			if canonical != "" {
				assert.Equal(t, canonical, entry.CanonicalID)
			}
		}
	}
	// Output ordering and all fields are deterministic, independent of map iteration.
	again, err := n.Normalize(context.Background(), source, raw)
	require.NoError(t, err)
	assert.Equal(t, snapshot, again)
	// Raw evidence is owned by the snapshot, not the caller's mutable download.
	before := string(snapshot.Slices[0].Raw)
	for i := range downloaded {
		downloaded[i] = ' '
	}
	assert.Equal(t, before, string(snapshot.Slices[0].Raw))
}

func TestNormalizeOptionalMetadataAndUnknownIDs(t *testing.T) {
	value := decodeCatalog(t, capturedCatalog(t))
	models := value["openrouter"]["models"].(map[string]any)
	// Schema-shaped variants, deliberately not claimed as captured upstream rows.
	models["vendor/unclassified-model:tag"] = map[string]any{"id": "vendor/unclassified-model:tag", "name": "Unknown"}
	snapshot, err := normalizerFor(t, shippedPolicy{}).Normalize(context.Background(), "https://catalog.example/api.json", encodeCatalog(t, value))
	require.NoError(t, err)
	slice := snapshot.Slices[1]
	require.Equal(t, catalog.SliceStatusOK, slice.Status)
	require.Len(t, slice.Models, 3)
	unknown := slice.Models[2]
	assert.Equal(t, "vendor/unclassified-model:tag", unknown.NativeID)
	assert.Empty(t, unknown.CanonicalModelID)
	assert.Empty(t, unknown.ReleaseDate)
	u, err := url.Parse(unknown.SourceRef)
	require.NoError(t, err)
	assert.Equal(t, "/openrouter/models/vendor~1unclassified-model:tag", u.Fragment)
	entry := slice.Entries[2]
	assert.Equal(t, "openrouter/vendor/unclassified-model:tag", entry.CanonicalID)
	assert.Empty(t, entry.Tier)
	assert.Empty(t, entry.OrderKey)
	assert.Empty(t, entry.SpecificModel)
	assert.False(t, entry.Immutable)
}

// SECURITY — every discovered native id becomes a probe's --model argument, so
// ids outside the allow-list (floating '~' aliases, non-ASCII, a leading '-')
// are dropped row by row. The rest of the slice stays observed.
func TestNormalizeSkipsNativeIDsOutsideAllowList(t *testing.T) {
	value := decodeCatalog(t, capturedCatalog(t))
	models := value["openrouter"]["models"].(map[string]any)
	for _, id := range []string{"实验室/模型~1", "~deepseek/deepseek-flash-latest", "-oProxyCommand=x", "@cf/vendor/model"} {
		models[id] = map[string]any{"id": id, "name": "Rejected"}
	}
	snapshot, err := normalizerFor(t, shippedPolicy{}).Normalize(context.Background(), "https://catalog.example/api.json", encodeCatalog(t, value))
	require.NoError(t, err)
	slice := snapshot.Slices[1]
	require.Equal(t, catalog.SliceStatusOK, slice.Status)
	require.Len(t, slice.Models, 2)
	require.Len(t, slice.Entries, 2)
	for _, e := range slice.Entries {
		assert.True(t, strings.HasPrefix(e.NativeID, "deepseek/"), e.NativeID)
	}
}

type normalizationPolicy struct {
	rules []modelidentity.PatternRule
	err   error
}

func (p normalizationPolicy) Snapshot(context.Context) (modelidentity.IdentityPolicy, error) {
	return modelidentity.IdentityPolicy{Revision: 19, Rules: p.rules}, p.err
}

func TestNormalizeCapturedReleaseDateOrdersEqualVersions(t *testing.T) {
	// An edited policy captures the major version only. Both captured 4.x rows
	// consequently have the same numeric key, so their actual dates must decide.
	n := normalizerFor(t, normalizationPolicy{rules: []modelidentity.PatternRule{{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-opus-(\d+)-\d+`, VersionGroups: []int{1}}}})
	snapshot, err := n.Normalize(context.Background(), "https://catalog.example/api.json", capturedCatalog(t))
	require.NoError(t, err)
	entries := snapshot.Slices[0].Entries
	require.Equal(t, "claude-opus-4-6", entries[1].NativeID)
	require.Equal(t, "claude-opus-4-7", entries[2].NativeID)
	assert.Equal(t, "4", entries[1].OrderKey)
	assert.Equal(t, entries[1].OrderKey, entries[2].OrderKey)
	assert.Equal(t, "2026-02-04", entries[1].ReleaseDate)
	assert.Equal(t, "2026-04-14", entries[2].ReleaseDate)
	cmp, err := modelidentity.Compare(entries[2], entries[1])
	require.NoError(t, err)
	assert.Equal(t, 1, cmp)
	_, err = modelidentity.Compare(entries[1], entries[1])
	assert.ErrorIs(t, err, modelidentity.ErrUnorderable)
}

func TestNormalizeSourceProvenance(t *testing.T) {
	n := normalizerFor(t, shippedPolicy{})
	raw := capturedCatalog(t)
	first, err := n.Normalize(context.Background(), "https://first.example/custom.json", raw)
	require.NoError(t, err)
	second, err := n.Normalize(context.Background(), "https://second.example/other.json?rev=2", raw)
	require.NoError(t, err)
	for i := range first.Slices {
		for j, row := range first.Slices[i].Models {
			other := second.Slices[i].Models[j]
			assert.NotEqual(t, row.SourceRef, other.SourceRef)
			assert.NotEqual(t, first.Slices[i].Entries[j].SourceRef, second.Slices[i].Entries[j].SourceRef)
			row.SourceRef = other.SourceRef
			assert.Equal(t, row, other)
		}
	}
}

func TestNormalizeRejectsUnsupportedTopLevel(t *testing.T) {
	n := normalizerFor(t, shippedPolicy{})
	for _, raw := range []string{"", " ", "null", "[]", "42", `{"data":[]}`, `{"object":"list","data":[]}`, `{`, `{} {}`} {
		t.Run(raw, func(t *testing.T) {
			snapshot, err := n.Normalize(context.Background(), "https://catalog.example/api.json", []byte(raw))
			require.Error(t, err)
			assert.Equal(t, catalog.CatalogSnapshot{}, snapshot)
		})
	}
	for _, source := range []string{"", "relative.json", "file:///tmp/catalog.json", "https://"} {
		snapshot, err := n.Normalize(context.Background(), source, capturedCatalog(t))
		require.Error(t, err)
		assert.Equal(t, catalog.CatalogSnapshot{}, snapshot)
	}
}

func TestNormalizeMalformedSliceIsIsolated(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"null":             func(p map[string]any) { p["models"] = nil },
		"array":            func(p map[string]any) { p["models"] = []any{} },
		"missing models":   func(p map[string]any) { delete(p, "models") },
		"wrong provider":   func(p map[string]any) { p["id"] = "other" },
		"model not object": func(p map[string]any) { p["models"].(map[string]any)["claude-opus-5-5"] = nil },
		"wrong native id": func(p map[string]any) {
			p["models"].(map[string]any)["claude-opus-5-5"].(map[string]any)["id"] = "another"
		},
		"wrong date type": func(p map[string]any) {
			p["models"].(map[string]any)["claude-opus-5-5"].(map[string]any)["release_date"] = 2026
		},
		"invalid date": func(p map[string]any) {
			p["models"].(map[string]any)["claude-opus-5-5"].(map[string]any)["release_date"] = "2026-02-30"
		},
		"canonical contradiction": func(p map[string]any) {
			p["models"].(map[string]any)["claude-opus-5-5"].(map[string]any)["canonical_model_id"] = "openai/other"
		},
		"wrong canonical type": func(p map[string]any) {
			p["models"].(map[string]any)["claude-opus-5-5"].(map[string]any)["canonical_model_id"] = []any{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := decodeCatalog(t, capturedCatalog(t))
			mutate(value["anthropic"])
			raw := encodeCatalog(t, value)
			snapshot, err := normalizerFor(t, shippedPolicy{}).Normalize(context.Background(), "https://catalog.example/api.json", raw)
			require.NoError(t, err)
			require.Len(t, snapshot.Slices, 2)
			bad, good := snapshot.Slices[0], snapshot.Slices[1]
			assert.Equal(t, "anthropic", bad.Provider)
			assert.Equal(t, catalog.SliceStatusInvalid, bad.Status)
			assert.Empty(t, bad.Models, "a failed slice must not authorize partial replacement")
			assert.Empty(t, bad.Entries)
			assert.JSONEq(t, string(encodeCatalog(t, value["anthropic"])), string(bad.Raw))
			assert.Equal(t, catalog.SliceStatusOK, good.Status)
			require.Len(t, good.Entries, 2)
			assert.Equal(t, "deepseek/deepseek-v4-flash", good.Entries[0].NativeID)
		})
	}
}

func TestNormalizeEmptyCatalog(t *testing.T) {
	n := normalizerFor(t, shippedPolicy{})
	snapshot, err := n.Normalize(context.Background(), "https://catalog.example/api.json", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, normalizationTime, snapshot.FetchedAt)
	assert.Empty(t, snapshot.Slices)
	snapshot, err = n.Normalize(context.Background(), "https://catalog.example/api.json", []byte(`{"openai":{"id":"openai","models":{}}}`))
	require.NoError(t, err)
	require.Len(t, snapshot.Slices, 1)
	assert.Equal(t, catalog.SliceStatusOK, snapshot.Slices[0].Status)
	assert.Empty(t, snapshot.Slices[0].Models)
	assert.Empty(t, snapshot.Slices[0].Entries)
}

func TestNormalizeDependencyAndCancellationErrors(t *testing.T) {
	_, err := NewNormalizer(nil, time.Now)
	require.Error(t, err)
	mapper, err := modelidentity.NewIdentityMapper(shippedPolicy{})
	require.NoError(t, err)
	_, err = NewNormalizer(mapper, nil)
	require.Error(t, err)
	fault := errors.New("policy storage unavailable")
	n := normalizerFor(t, normalizationPolicy{err: fault})
	snapshot, err := n.Normalize(context.Background(), "https://catalog.example/api.json", capturedCatalog(t))
	require.ErrorIs(t, err, fault)
	assert.Equal(t, catalog.CatalogSnapshot{}, snapshot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err = normalizerFor(t, shippedPolicy{}).Normalize(ctx, "https://catalog.example/api.json", capturedCatalog(t))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, catalog.CatalogSnapshot{}, snapshot)
}
