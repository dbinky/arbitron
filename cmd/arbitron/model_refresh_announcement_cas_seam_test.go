package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired seam Area 4 refresh advancement <-> Area 10 announcement delivery
// (paired:12). The event's Changes are frozen inside CommitCycle from the
// Advancement records commitAdvanceTx returned, and the drain that
// RefreshService.announce runs after every cycle delivers that frozen payload.

// bootTwoFollowersRoot boots the real root with two configured following
// profiles on lifecycleModelX behind one sink connection.
func bootTwoFollowersRoot(t *testing.T, sink *modelSink, aliases ...string) (*domainStack, context.Context) {
	t.Helper()
	cfg := "profiles:\n"
	for _, alias := range aliases {
		cfg += `  - alias: ` + alias + `
    kind: chat
    backend: claude-code
    connectionref: sink
    model: ` + lifecycleModelX + `
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`
	}
	ds, _ := bootSinkRootWithClose(t, cfg, func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	for _, alias := range aliases {
		storeConfiguredFollowing(t, ctx, ds, alias)
	}
	return ds, ctx
}

func configureAnnouncementRoot(t *testing.T, ctx context.Context, ds *domainStack) {
	t.Helper()
	webhook := "https://hooks.example.test/teams/PAIRED10"
	catalogURL := "https://catalog.example.test/api.json"
	_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL, TeamsWebhookURL: &webhook})
	require.NoError(t, err)
}

func frozenPayload(t *testing.T, ds *domainStack, canonical string) modelcatalog.AnnouncementPayload {
	t.Helper()
	var raw string
	require.NoError(t, ds.db.QueryRow(`SELECT payload FROM model_announcement_events WHERE canonical_id=?`, canonical).Scan(&raw))
	var p modelcatalog.AnnouncementPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	return p
}

func receivedCards(r *receiverTransport, model string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var cards []string
	for _, body := range r.bodies {
		if strings.Contains(body, model) {
			cards = append(cards, body)
		}
	}
	return cards
}

// Two eligible followers of one tier; an edit from another process lands on
// pin-alias after the gate rescan, so only its SQL CAS loses. The one lifetime
// card for Y must name the committed peer-alias change and not the conflicted
// profile, and a later cycle that advances pin-alias neither re-announces Y nor
// rewrites the frozen payload.
func TestRefreshPartialCASConflictCardNamesOnlyCommittedChangeRoot(t *testing.T) {
	sink, _ := hookSink(t)
	ds, ctx := bootTwoFollowersRoot(t, sink, "pin-alias", "peer-alias")
	configureAnnouncementRoot(t, ctx, ds)
	receiver := &receiverTransport{}
	deps := ds.refreshDeps
	fetcher := &fixtureFetcher{body: refreshCatalogOnlyX}
	deps.Fetcher = fetcher
	serveRootCatalog(t, ds, fetcher)
	deps.AnnouncementClient = &http.Client{Transport: receiver}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()

	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Empty(t, report.Changes)
	read, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)

	var seq int
	var name, path string
	require.NoError(t, ds.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	other, err := adaptsqlite.Open(path)
	require.NoError(t, err)
	defer other.Close()
	var once sync.Once
	edited := make(chan error, 1)
	ds.executionGate.SetCurrentRevision(func(ctx context.Context) (string, error) {
		rev, err := ds.pathInventory.Revision(ctx)
		once.Do(func() {
			next := read.Clone()
			next.Effort = provider.Effort("high")
			_, editErr := adaptsqlite.NewProfileRepo(other).UpdateExpected(ctx, read, next)
			edited <- editErr
		})
		return rev, err
	})
	defer ds.executionGate.SetCurrentRevision(ds.pathInventory.Revision)

	fetcher.mu.Lock()
	fetcher.body = refreshCatalogFixture
	fetcher.mu.Unlock()
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.NoError(t, <-edited)
	require.Equal(t, "advance_conflict", report.PathStatus["profile:pin-alias"], "%v", report.PathStatus)
	require.Len(t, report.Changes, 1)
	require.Equal(t, "peer-alias", report.Changes[0].Alias)

	payload := frozenPayload(t, ds, lifecycleModelY)
	assert.Equal(t, report.Changes, payload.Changes, "the event freezes exactly the committed advancement")
	cards := receivedCards(receiver, lifecycleModelY)
	require.Len(t, cards, 1, "Y is announced once, by the cycle that committed it")
	assert.Contains(t, cards[0], "peer-alias")
	assert.Contains(t, cards[0], lifecycleModelX+" → "+lifecycleModelY)
	assert.NotContains(t, cards[0], "pin-alias", "the conflicted profile did not change")
	assert.NotContains(t, cards[0], "No profiles changed")

	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	require.Equal(t, "pin-alias", report.Changes[0].Alias)
	assert.Len(t, receivedCards(receiver, lifecycleModelY), 1, "the later advancement never re-announces Y")
	assert.Equal(t, payload, frozenPayload(t, ds, lifecycleModelY), "the frozen payload is never rebuilt from current rows")
}

// rejectingReceiver answers 503 while reject is set, then accepts like
// receiverTransport. Only accepted bodies are recorded.
type rejectingReceiver struct {
	reject   atomic.Bool
	rejected atomic.Int32
	accepted *receiverTransport
}

func (r *rejectingReceiver) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.reject.Load() {
		r.rejected.Add(1)
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: req}, nil
	}
	return r.accepted.RoundTrip(req)
}

// A cycle whose catalog fetch fails, or whose publication recovery is blocked
// before collection (an acknowledgement fault), still runs the drain: an older event
// whose backoff has expired reaches the receiver in that cycle with its
// original change, rather than waiting for a cycle that commits.
func TestFailedRefreshCycleDeliversDueAnnouncementRoot(t *testing.T) {
	for _, failure := range []string{"catalog fetch fails", "publication recovery blocked"} {
		t.Run(failure, func(t *testing.T) {
			sink, _ := hookSink(t)
			ds, _, ctx := bootRefreshPinsRoot(t, sink)
			configureAnnouncementRoot(t, ctx, ds)
			clock := &announcementTestClock{at: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}
			receiver := &rejectingReceiver{accepted: &receiverTransport{}}
			receiver.reject.Store(true)
			deps := ds.refreshDeps
			fetcher := &fixtureFetcher{body: refreshCatalogFixture}
			deps.Fetcher = fetcher
			serveRootCatalog(t, ds, fetcher)
			deps.AnnouncementClient = &http.Client{Transport: receiver}
			deps.Clock = clock.now
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()

			report, err := refresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Len(t, report.Changes, 1)
			require.NotZero(t, receiver.rejected.Load(), "the committing cycle attempted delivery")
			require.Empty(t, receivedCards(receiver.accepted, lifecycleModelY))
			original := frozenPayload(t, ds, lifecycleModelY)
			var state string
			require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events WHERE canonical_id=?`, lifecycleModelY).Scan(&state))
			require.Equal(t, "pending", state)

			fetcher.mu.Lock()
			fetches := len(fetcher.urls)
			fetcher.mu.Unlock()
			switch failure {
			case "catalog fetch fails":
				fetcher.mu.Lock()
				fetcher.err = errors.New("catalog down")
				fetcher.mu.Unlock()
			case "publication recovery blocked":
				// Replay a real durable publication and fail its acknowledgement.
				// Malformed reports are quarantined and no longer block cycles.
				_, err := ds.db.Exec(`DELETE FROM model_publication_acks WHERE advancement_id=?`, report.Changes[0].ID)
				require.NoError(t, err)
				_, err = ds.db.Exec(`UPDATE model_refresh_reports SET publication_pending=1 WHERE id=?`, report.ID)
				require.NoError(t, err)
				_, err = ds.db.Exec(`CREATE TRIGGER announcement_ack_fault BEFORE INSERT ON model_publication_acks BEGIN SELECT RAISE(ABORT,'ack unavailable'); END`)
				require.NoError(t, err)
			}
			receiver.reject.Store(false)
			clock.advance(4 * time.Hour)

			report, err = refresh.coordinator.Refresh(ctx)
			fetcher.mu.Lock()
			after := len(fetcher.urls)
			fetcher.mu.Unlock()
			switch failure {
			case "catalog fetch fails":
				require.NoError(t, err, "a failed fetch commits a degraded report")
				assert.Greater(t, after, fetches, "the cycle attempted the fetch")
				assert.Empty(t, report.Changes)
			case "publication recovery blocked":
				require.ErrorIs(t, err, provider.ErrPublicationPending)
				assert.Equal(t, fetches, after, "the cycle returned before collection")
			}
			cards := receivedCards(receiver.accepted, lifecycleModelY)
			require.Len(t, cards, 1, "the due event is delivered by the failed cycle's drain")
			assert.Contains(t, cards[0], "pin-alias")
			assert.Contains(t, cards[0], lifecycleModelX+" → "+lifecycleModelY)
			assert.Equal(t, original, frozenPayload(t, ds, lifecycleModelY))
			require.NoError(t, ds.db.QueryRow(`SELECT state FROM model_announcement_events WHERE canonical_id=?`, lifecycleModelY).Scan(&state))
			assert.Equal(t, "announced", state)
		})
	}
}
