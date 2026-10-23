package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"

	adaptmodelcatalog "github.com/arbitron/arbitron/adapter/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seamFetcher serves the fixture for the two public test hosts and hands every
// other URL to the production guarded fetcher (the same constructor
// newModelRefresh defaults to), so internal targets meet the real fetch-time guard.
type seamFetcher struct {
	mu    sync.Mutex
	urls  []string
	real  modelcatalog.CatalogFetcher
	hosts map[string]string
}

func (f *seamFetcher) Fetch(ctx context.Context, raw string) ([]byte, error) {
	f.mu.Lock()
	f.urls = append(f.urls, raw)
	f.mu.Unlock()
	if u, err := url.Parse(raw); err == nil {
		if body, ok := f.hosts[u.Hostname()]; ok {
			return []byte(body), nil
		}
	}
	return f.real.Fetch(ctx, raw)
}

func (f *seamFetcher) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.urls[len(f.urls)-1]
}

// Paired row 11 (Area 7 settings ↔ Area 2 ingestion): one running refresh
// follows each admin edit on its next cycle, and an internal URL that write-time
// validation accepts is still refused at fetch time without echoing its token.
func TestModelRefreshRootFollowsCatalogURLEditsAndGuardsInternalTargets(t *testing.T) {
	const urlA = "https://catalog-a.example.test/api.json"
	const urlB = "https://catalog-b.example.test/api.json?token=BSECRET"
	ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)

	fetcher := &seamFetcher{real: adaptmodelcatalog.NewFetcher(), hosts: map[string]string{
		"catalog-a.example.test": refreshCatalogFixture,
		"catalog-b.example.test": refreshCatalogFixture,
	}}
	var logs bytes.Buffer
	deps := ds.refreshDeps
	deps.Fetcher = fetcher
	deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()

	setURL := func(raw string) {
		t.Helper()
		_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &raw})
		require.NoError(t, err, "write-time validation accepts any absolute HTTPS URL")
	}
	storedRefs := func() []string {
		t.Helper()
		view, err := ds.catalogStore.Snapshot(ctx)
		require.NoError(t, err)
		var refs []string
		for _, sl := range view.Catalog.Slices {
			for _, m := range sl.Models {
				refs = append(refs, m.SourceRef)
			}
		}
		require.NotEmpty(t, refs)
		return refs
	}
	assertRefsFrom := func(source string) {
		t.Helper()
		for _, ref := range storedRefs() {
			assert.True(t, strings.HasPrefix(ref, source+"#/"), "stored SourceRef %q comes from %q", ref, source)
		}
	}

	setURL(urlA)
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, urlA, fetcher.last())
	assert.NotEqual(t, "fetch_failed", report.PathStatus["catalog"])
	assertRefsFrom(urlA)

	setURL(urlB)
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, urlB, fetcher.last(), "the next cycle fetches the edited URL, not the one read at construction")
	assert.NotEqual(t, "fetch_failed", report.PathStatus["catalog"])
	assertRefsFrom(urlB)

	for _, internal := range []string{
		"https://127.0.0.1:8080/api.json?token=LOOPSECRET",
		"https://10.20.30.40/api.json?token=PRIVSECRET",
		"https://169.254.169.254/latest/meta-data?token=METASECRET",
		"https://[::1]/api.json?token=V6SECRET",
		"https://localhost/api.json?token=NAMESECRET",
	} {
		t.Run(internal, func(t *testing.T) {
			logs.Reset()
			setURL(internal)
			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.Equal(t, internal, fetcher.last(), "the persisted internal URL reaches the guarded fetcher")
			assert.Equal(t, "fetch_failed", report.PathStatus["catalog"])
			assert.Contains(t, logs.String(), "outbound: blocked dial to internal address")
			token := internal[strings.Index(internal, "token=")+len("token="):]
			assert.NotContains(t, logs.String(), token)
			assertRefsFrom(urlB) // the stored catalog is kept, never replaced by the internal source

			public, err := refresh.admin.List(ctx)
			require.NoError(t, err)
			raw, err := json.Marshal(public)
			require.NoError(t, err)
			assert.NotContains(t, string(raw), token)
			assert.NotContains(t, string(raw), "BSECRET", "SourceRef query tokens stay internal")
		})
	}
}
