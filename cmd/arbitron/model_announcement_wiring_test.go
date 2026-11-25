package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type receiverTransport struct {
	mu     sync.Mutex
	urls   []string
	bodies []string
}

func (r *receiverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.bodies = append(r.bodies, string(body))
	r.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("1")), Header: http.Header{}, Request: req}, nil
}

func (r *receiverTransport) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.urls)
}

// The Teams webhook an admin saves through the settings service reaches the
// announcement sink: a real refresh that advances a following profile posts
// one card per discovered model to the saved destination. Events are durably
// announced, a later refresh sends nothing, and with the webhook empty (the
// default) nothing is ever sent.
func TestModelAnnouncementWiringToReceiverSink(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	for _, tc := range []struct {
		name       string
		webhook    string
		wantSends  int
		wantStates map[string]int
	}{
		{name: "configured webhook receives one card per model", webhook: "https://hooks.example.test/teams/SECRET", wantSends: 2, wantStates: map[string]int{"announced": 2}},
		{name: "empty webhook suppresses the announcement", wantStates: map[string]int{"suppressed": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var buf bytes.Buffer
				_, _ = buf.ReadFrom(r.Body)
				w.Header().Set("Content-Type", "application/json")
				model := newModel
				if strings.Contains(buf.String(), oldModel) && !strings.Contains(buf.String(), newModel) {
					model = oldModel
				}
				_, _ = io.WriteString(w, `{"model":"`+model+`","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
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
			require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
			storeConfiguredFollowing(t, ctx, ds, "worker")
			catalogURL := "https://catalog.example.test/api.json"
			patch := settingsdomain.Patch{CatalogURL: &catalogURL}
			if tc.webhook != "" {
				patch.TeamsWebhookURL = &tc.webhook
			}
			_, err = ds.SystemSettings.Update(ctx, patch)
			require.NoError(t, err)

			receiver := &receiverTransport{}
			deps := ds.refreshDeps
			deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
			deps.AnnouncementClient = &http.Client{Transport: receiver}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			ds.modelRefresh = refresh

			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Len(t, report.Changes, 1)
			assert.Equal(t, newModel, report.Changes[0].ToModel)
			assert.Equal(t, tc.wantSends, receiver.count())
			if tc.webhook != "" {
				assert.Equal(t, tc.webhook, receiver.urls[0], "the card goes to the saved destination")
				assert.Contains(t, strings.Join(receiver.bodies, "\n"), oldModel+" → "+newModel)
			}
			states := map[string]int{}
			rows, err := ds.db.Query(`SELECT state, COUNT(*) FROM model_announcement_events GROUP BY state`)
			require.NoError(t, err)
			defer rows.Close()
			for rows.Next() {
				var state string
				var n int
				require.NoError(t, rows.Scan(&state, &n))
				states[state] = n
			}
			assert.Equal(t, tc.wantStates, states)

			_, err = refresh.coordinator.Refresh(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.wantSends, receiver.count(), "a later cycle never re-sends an announced event")
		})
	}
}

// onFetchFetcher runs a hook while the catalog fetch is in flight, after the
// refresh's initial settings read and before its fresh eligibility read.
type onFetchFetcher struct {
	*fixtureFetcher
	hook func()
}

func (f onFetchFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	f.hook()
	return f.fixtureFetcher.Fetch(ctx, url)
}

// A settings read fault at the discovery commit is not an empty webhook: the
// profile still advances, the event is frozen awaiting_setting with its
// original changes, nothing is sent while the setting is unreadable, and once
// the setting is readable and configured a later cycle delivers exactly the
// original card.
func TestModelAnnouncementAwaitsSettingWhenEligibilityReadFaults(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		w.Header().Set("Content-Type", "application/json")
		model := newModel
		if strings.Contains(buf.String(), oldModel) && !strings.Contains(buf.String(), newModel) {
			model = oldModel
		}
		_, _ = io.WriteString(w, `{"model":"`+model+`","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
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
	require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")
	catalogURL, webhook := "https://catalog.example.test/api.json", "https://hooks.example.test/teams/SECRET"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL, TeamsWebhookURL: &webhook})
	require.NoError(t, err)

	receiver := &receiverTransport{}
	deps := ds.refreshDeps
	deps.Fetcher = onFetchFetcher{fixtureFetcher: &fixtureFetcher{body: refreshCatalogFixture}, hook: func() {
		_, err := ds.db.Exec(`ALTER TABLE system_settings RENAME TO system_settings_down`)
		require.NoError(t, err)
	}}
	deps.AnnouncementClient = &http.Client{Transport: receiver}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	ds.modelRefresh = refresh

	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err, "an unreadable eligibility setting must not discard the discovery")
	require.Len(t, report.Changes, 1)
	assert.Equal(t, newModel, report.Changes[0].ToModel)
	assert.Zero(t, receiver.count())
	var state string
	require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events`).Scan(&state))
	assert.Equal(t, "awaiting_setting", state)

	_, err = ds.db.Exec(`ALTER TABLE system_settings_down RENAME TO system_settings`)
	require.NoError(t, err)
	deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
	refresh, err = newModelRefresh(deps)
	require.NoError(t, err)
	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, receiver.count(), "the recovered setting delivers both frozen discoveries")
	assert.Equal(t, webhook, receiver.urls[0])
	assert.Contains(t, strings.Join(receiver.bodies, "\n"), oldModel+" → "+newModel, "the card carries the original change")
	require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events`).Scan(&state))
	assert.Equal(t, "announced", state)
}

// Paired seam Area 7 ↔ Area 8 (paired:14), traced from discovery to the
// receiver URL with the webhook saved through the real HTTP route. A discovery
// whose eligibility read faults is delivered once an admin saves the URL (a
// fault is not "empty"); a discovery made while the webhook is empty stays
// suppressed after the URL is saved; the redacted placeholder is refused at
// PUT and never stored, so it can never reach the sender.
func TestModelAnnouncementSettingsSeamThroughHTTP(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	const webhook = "https://hooks.example.test/teams/HTTP-SECRET"
	for _, tc := range []struct {
		name       string
		faultRead  bool
		discovered string
		wantSends  int
		wantFinal  string
	}{
		{name: "read fault awaits and delivers after HTTP save", faultRead: true, discovered: "awaiting_setting", wantSends: 2, wantFinal: "announced"},
		{name: "empty at discovery is never replayed after HTTP save", discovered: "suppressed", wantFinal: "suppressed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var buf bytes.Buffer
				_, _ = buf.ReadFrom(r.Body)
				w.Header().Set("Content-Type", "application/json")
				model := newModel
				if strings.Contains(buf.String(), oldModel) && !strings.Contains(buf.String(), newModel) {
					model = oldModel
				}
				_, _ = io.WriteString(w, `{"model":"`+model+`","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
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
			require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
			storeConfiguredFollowing(t, ctx, ds, "worker")
			router := adapthttp.NewRouterFromDeps(ds.httpDeps())
			put := func(body string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest("PUT", "/api/v1/system/settings", strings.NewReader(body)))
				return w
			}
			// The webhook starts empty; only the catalog URL is saved.
			require.Equal(t, 200, put(`{"catalogUrl":"https://catalog.example.test/api.json"}`).Code)

			receiver := &receiverTransport{}
			deps := ds.refreshDeps
			deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
			if tc.faultRead {
				deps.Fetcher = onFetchFetcher{fixtureFetcher: &fixtureFetcher{body: refreshCatalogFixture}, hook: func() {
					_, err := ds.db.Exec(`ALTER TABLE system_settings RENAME TO system_settings_down`)
					require.NoError(t, err)
				}}
			}
			deps.AnnouncementClient = &http.Client{Transport: receiver}
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			_, err = refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			var state string
			require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events`).Scan(&state))
			assert.Equal(t, tc.discovered, state, "discovery-time webhook state")
			assert.Zero(t, receiver.count())
			if tc.faultRead {
				_, err = ds.db.Exec(`ALTER TABLE system_settings_down RENAME TO system_settings`)
				require.NoError(t, err)
			}

			w := put(`{"teamsWebhookUrl":"` + config.RedactedSecret + `"}`)
			assert.Equal(t, 400, w.Code, "the redacted placeholder is refused, not stored")
			stored, err := ds.SystemSettings.ReadInternal(ctx)
			require.NoError(t, err)
			assert.Empty(t, stored.TeamsWebhookURL)
			w = put(`{"teamsWebhookUrl":"` + webhook + `"}`)
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), webhook)

			deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
			refresh, err = newModelRefresh(deps)
			require.NoError(t, err)
			_, err = refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			_, err = refresh.coordinator.Refresh(ctx)
			require.NoError(t, err, "a later cycle's drain must not resend or replay")
			require.Equal(t, tc.wantSends, receiver.count())
			for _, u := range receiver.urls {
				assert.Equal(t, webhook, u, "the receiver is the raw URL saved through HTTP")
			}
			for _, b := range receiver.bodies {
				assert.NotContains(t, b, config.RedactedSecret)
			}
			if tc.wantSends != 0 {
				assert.Contains(t, strings.Join(receiver.bodies, "\n"), oldModel+" → "+newModel)
			}
			require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events`).Scan(&state))
			assert.Equal(t, tc.wantFinal, state)
		})
	}
}
