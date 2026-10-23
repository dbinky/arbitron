package modelcatalog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type announcementClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *announcementClock) Now() time.Time   { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *announcementClock) set(at time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.at = at }

type announcementFixture struct {
	t        *testing.T
	path     string
	db       *sql.DB
	settings *settings.Service
	clock    *announcementClock
	payload  modelcatalog.AnnouncementPayload
}

func announcementContext() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
}
func newAnnouncementFixture(t *testing.T, followers int, configured *bool) *announcementFixture {
	t.Helper()
	f := &announcementFixture{t: t, path: filepath.Join(t.TempDir(), "events.db"), clock: &announcementClock{at: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}}
	f.open()
	t.Cleanup(func() { _ = f.db.Close() })
	ctx := context.Background()
	at := f.clock.Now()
	c := modelcatalog.CycleCommit{Report: modelcatalog.RefreshReport{ID: "first", At: at}, Catalog: modelcatalog.CatalogSnapshot{FetchedAt: at, Slices: []modelcatalog.CatalogSlice{{Provider: "vendor", Status: modelcatalog.SliceStatusOK, Raw: json.RawMessage(`{"models":{}}`), Models: []modelidentity.CatalogModel{{CatalogProvider: "vendor", NativeID: "new-specific", CanonicalModelID: "vendor/new-specific"}}, Entries: []modelidentity.Entry{{CanonicalID: "vendor/new-specific", Provider: "vendor", NativeID: "new-specific", SpecificModel: "new-specific", Tier: "premium", Immutable: true, OrderKey: "2", PolicyRevision: 1}}}}}, Evidence: []modelcatalog.UsabilityEvidence{{CanonicalID: "vendor/new-specific", PathKey: "opaque-path", ConfigRevision: "rev-1", NativeModel: "new-specific", ProofRef: "/private/account/SECRET", CheckedAt: at, State: modelcatalog.UsabilityConfirmed}}, WebhookConfigured: configured}
	profiles := sqlite.NewProfileRepo(f.db)
	for i := 0; i < followers; i++ {
		alias := fmt.Sprintf("alias-%03d-%s", i, strings.Repeat("a", 150))
		p := provider.Profile{Alias: alias, Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "old-specific", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: "premium"}}
		require.NoError(t, profiles.Create(ctx, p))
		p, err := profiles.Get(ctx, alias)
		require.NoError(t, err)
		c.Advances = append(c.Advances, provider.AdvanceRequest{ID: fmt.Sprintf("advance-%d", i), RefreshID: "first", Expected: p, Model: "new-specific", CanonicalID: "vendor/new-specific", PathRevision: "inventory-1", ExecutionRevision: provider.ExecutionRevision{ID: "inventory-1", PolicyRevision: 1}, Evidence: []provider.PathEvidenceRef{{PathKey: "opaque-path", ConfigRevision: "rev-1"}}, At: at})
	}
	_, err := sqlite.NewModelCatalogRepo(f.db).CommitCycle(ctx, c)
	require.NoError(t, err)
	var raw string
	require.NoError(t, f.db.QueryRow(`SELECT payload FROM model_announcement_events`).Scan(&raw))
	require.NoError(t, json.Unmarshal([]byte(raw), &f.payload))
	return f
}
func (f *announcementFixture) open() {
	var err error
	f.db, err = sqlite.Open(f.path)
	require.NoError(f.t, err)
	require.NoError(f.t, sqlite.RunMigrations(f.db))
	f.settings = settings.NewService(sqlite.NewSettingsRepo(f.db))
}
func (f *announcementFixture) reopen() { require.NoError(f.t, f.db.Close()); f.open() }
func (f *announcementFixture) destination(raw string) {
	_, err := f.settings.Update(announcementContext(), settings.Patch{TeamsWebhookURL: &raw})
	require.NoError(f.t, err)
}
func (f *announcementFixture) event() (state string, attempts int, next time.Time, confirmed []int) {
	var raw, deadline string
	require.NoError(f.t, f.db.QueryRow(`SELECT state,attempts,next_attempt_at,confirmed_parts FROM model_announcement_events`).Scan(&state, &attempts, &deadline, &raw))
	require.NoError(f.t, json.Unmarshal([]byte(raw), &confirmed))
	if deadline != "" {
		var err error
		next, err = time.Parse(time.RFC3339Nano, deadline)
		require.NoError(f.t, err)
	}
	return
}

type announcementTransport func(*http.Request) (*http.Response, error)

func (f announcementTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type receivedAnnouncement struct {
	body []byte
	uri  string
}
type announcementReceiver struct {
	mu     sync.Mutex
	cards  []receivedAnnouncement
	server *httptest.Server
	status int
}

func newAnnouncementReceiver(t *testing.T) *announcementReceiver {
	r := &announcementReceiver{status: 202}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, "POST", req.Method)
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.LessOrEqual(t, len(body), modelcatalog.MaxAnnouncementBytes)
		r.mu.Lock()
		r.cards = append(r.cards, receivedAnnouncement{body: body, uri: req.URL.RequestURI()})
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.server.Close)
	return r
}
func (r *announcementReceiver) snapshot() []receivedAnnouncement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedAnnouncement(nil), r.cards...)
}
func (r *announcementReceiver) sender(t *testing.T, after func(*http.Request, *http.Response) (*http.Response, error)) modelcatalog.ModelAnnouncementSender {
	target, err := url.Parse(r.server.URL)
	require.NoError(t, err)
	return notification.NewTeamsModelSender(&http.Client{Transport: announcementTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "receiver.example" || !strings.Contains(req.URL.Path, "RAW_SECRET") {
			return nil, errors.New("missing current raw destination")
		}
		if deadline, ok := req.Context().Deadline(); !ok || time.Until(deadline) > 15*time.Second {
			return nil, errors.New("missing bounded send")
		}
		forwarded := req.Clone(req.Context())
		u := *req.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		forwarded.URL = &u
		resp, err := r.server.Client().Transport.RoundTrip(forwarded)
		if err != nil {
			return nil, err
		}
		if after != nil {
			return after(req, resp)
		}
		return resp, nil
	})})
}
func assertAnnouncementCards(t *testing.T, cards []receivedAnnouncement, p modelcatalog.AnnouncementPayload) {
	t.Helper()
	parts, err := notification.PartitionAnnouncement(p, modelcatalog.MaxAnnouncementBytes)
	require.NoError(t, err)
	require.Len(t, cards, len(parts))
	var aliases []string
	for index, card := range cards {
		var msg struct {
			Attachments []struct {
				Content struct {
					Body []struct {
						Facts []struct{ Title, Value string }
					}
				}
			}
		}
		require.NoError(t, json.Unmarshal(card.body, &msg))
		facts := map[string]string{}
		for _, a := range msg.Attachments {
			for _, b := range a.Content.Body {
				for _, f := range b.Facts {
					facts[f.Title] = f.Value
				}
			}
		}
		assert.Equal(t, p.EventID, facts["Event"])
		assert.Equal(t, p.CanonicalID, facts["Model"])
		assert.Equal(t, p.Provider, facts["Provider"])
		if len(parts) > 1 {
			assert.Equal(t, fmt.Sprintf("%d/%d", index+1, len(parts)), facts["Part"])
		} else {
			assert.Empty(t, facts["Part"])
		}
		for _, path := range parts[index].Payload.Paths {
			assert.Equal(t, path.PathKey+" ("+path.ConfigRevision+")", facts["Path"])
		}
		assert.NotContains(t, string(card.body), "SECRET")
		assert.NotContains(t, string(card.body), "later-specific")
		for _, change := range p.Changes {
			if value, ok := facts[change.Alias]; ok {
				assert.Equal(t, change.FromModel+" → "+change.ToModel, value)
				aliases = append(aliases, change.Alias)
			}
		}
		if len(p.Changes) == 0 {
			assert.Equal(t, "No profiles changed", facts["Profiles"])
		}
	}
	var expected []string
	for _, change := range p.Changes {
		expected = append(expected, change.Alias)
	}
	assert.ElementsMatch(t, expected, aliases)
}

func TestAnnouncementDurableOriginalPayload(t *testing.T) {
	configured := true
	for _, followers := range []int{0, 2} {
		t.Run(fmt.Sprint(followers), func(t *testing.T) {
			f := newAnnouncementFixture(t, followers, &configured)
			f.destination("https://receiver.example/RAW_SECRET?sig=original")
			r := newAnnouncementReceiver(t)
			// Subsequent profile edits cannot reconstruct the first-discovery card.
			profiles := sqlite.NewProfileRepo(f.db)
			for _, change := range f.payload.Changes {
				p, err := profiles.Get(context.Background(), change.Alias)
				require.NoError(t, err)
				p.Model = "later-specific"
				require.NoError(t, profiles.Update(context.Background(), p))
			}
			f.reopen()
			svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
			assert.NoError(t, svc.Announce(context.Background(), modelcatalog.RefreshReport{}))
			assert.NoError(t, svc.Drain(context.Background()))
			state, attempts, _, confirmed := f.event()
			assert.Equal(t, modelcatalog.EventAnnounced, state)
			assert.Equal(t, 1, attempts)
			assert.Equal(t, []int{1}, confirmed)
			cards := r.snapshot()
			require.Len(t, cards, 1)
			assertAnnouncementCards(t, cards, f.payload)
			assert.Equal(t, "/RAW_SECRET?sig=original", cards[0].uri)
			f.reopen()
			state, _, _, _ = f.event()
			assert.Equal(t, modelcatalog.EventAnnounced, state)
		})
	}
}

func TestAnnouncementAmbiguousAndMarkerFailureRecovery(t *testing.T) {
	for _, markerFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(markerFailure), func(t *testing.T) {
			yes := true
			f := newAnnouncementFixture(t, 2, &yes)
			f.destination("https://receiver.example/RAW_SECRET?sig=old")
			r := newAnnouncementReceiver(t)
			sender := r.sender(t, func(_ *http.Request, resp *http.Response) (*http.Response, error) {
				if markerFailure {
					return resp, nil
				}
				_ = resp.Body.Close()
				return nil, errors.New("RAW_SECRET accepted response lost")
			})
			var store modelcatalog.AnnouncementStore = sqlite.NewModelAnnouncementRepo(f.db)
			if markerFailure {
				store = &failAnnouncementMarker{AnnouncementStore: store}
			}
			svc := modelcatalog.NewAnnouncementService(store, f.settings, sender, f.clock)
			require.Error(t, svc.Drain(context.Background()))
			state, attempts, next, _ := f.event()
			if markerFailure {
				assert.Equal(t, modelcatalog.EventInFlight, state)
			} else {
				assert.Equal(t, modelcatalog.EventDeliveryUnknown, state)
			}
			assert.Equal(t, 1, attempts)
			require.Len(t, r.snapshot(), 1)
			f.reopen()
			f.destination("https://receiver.example/RAW_SECRET?sig=changed")
			f.clock.set(f.clock.Now().Add(time.Minute))
			svc = modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
			require.NoError(t, svc.Drain(context.Background()))
			require.Len(t, r.snapshot(), 1)
			f.clock.set(next)
			require.NoError(t, svc.Announce(context.Background(), modelcatalog.RefreshReport{AnnouncementError: "discovery failed"}))
			cards := r.snapshot()
			require.Len(t, cards, 2)
			assert.Equal(t, cards[0].body, cards[1].body)
			assert.Equal(t, "/RAW_SECRET?sig=old", cards[0].uri)
			assert.Equal(t, "/RAW_SECRET?sig=changed", cards[1].uri)
			assertAnnouncementCards(t, cards[:1], f.payload)
			state, attempts, _, _ = f.event()
			assert.Equal(t, modelcatalog.EventAnnounced, state)
			assert.Equal(t, 2, attempts)
			require.NoError(t, svc.Drain(context.Background()))
			require.Len(t, r.snapshot(), 2)
		})
	}
}

type failAnnouncementMarker struct{ modelcatalog.AnnouncementStore }

func (s *failAnnouncementMarker) CompletePart(context.Context, string, string, int, modelcatalog.SendOutcome, time.Time) error {
	return errors.New("marker write failed")
}

func TestAnnouncementMultipartPauseRestartAndCurrentDestination(t *testing.T) {
	for _, priorFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_send_failure=%t", priorFailure), func(t *testing.T) {
			f := newAnnouncementFixture(t, 180, boolPtr(true))
			f.destination("https://receiver.example/RAW_SECRET?sig=first")
			r := newAnnouncementReceiver(t)
			ctx := context.Background()
			priorAttempts := 0
			if priorFailure {
				r.status = 503
				svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
				require.Error(t, svc.Drain(ctx))
				_, _, next, _ := f.event()
				f.clock.set(next)
				r.mu.Lock()
				r.status, r.cards = 202, nil
				r.mu.Unlock()
				priorAttempts = 1
			}
			svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, func(_ *http.Request, resp *http.Response) (*http.Response, error) {
				f.destination("")
				return resp, nil
			}), f.clock)
			require.NoError(t, svc.Drain(ctx))
			require.Len(t, r.snapshot(), 1)
			state, attempts, next, confirmed := f.event()
			assert.Equal(t, modelcatalog.EventPending, state)
			assert.Equal(t, priorAttempts, attempts, "a clear must retain only prior send failures")
			assert.Equal(t, f.clock.Now(), next, "reconfiguration must not wait for send-failure backoff")
			assert.Equal(t, []int{1}, confirmed)
			var partsRaw string
			require.NoError(t, f.db.QueryRow(`SELECT parts FROM model_announcement_events`).Scan(&partsRaw))
			var parts []modelcatalog.AnnouncementPart
			require.NoError(t, json.Unmarshal([]byte(partsRaw), &parts))
			require.Greater(t, len(parts), 1)
			for range 2 {
				require.NoError(t, svc.Drain(ctx))
				require.Len(t, r.snapshot(), 1)
				_, attempts, next, confirmed = f.event()
				assert.Equal(t, priorAttempts, attempts)
				assert.Equal(t, f.clock.Now(), next)
				assert.Equal(t, []int{1}, confirmed)
			}
			_, err := f.db.Exec(`UPDATE profiles SET model='later-specific'`)
			require.NoError(t, err)
			f.reopen()
			f.destination("https://receiver.example/RAW_SECRET?sig=second")
			f.clock.set(f.clock.Now().Add(time.Second))
			svc = modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
			require.NoError(t, svc.Drain(ctx))
			cards := r.snapshot()
			require.Len(t, cards, len(parts))
			assertAnnouncementCards(t, cards, f.payload)
			assert.Equal(t, "/RAW_SECRET?sig=first", cards[0].uri)
			for _, card := range cards[1:] {
				assert.Equal(t, "/RAW_SECRET?sig=second", card.uri)
			}
			state, attempts, _, confirmed = f.event()
			assert.Equal(t, modelcatalog.EventAnnounced, state)
			assert.Equal(t, priorAttempts+1, attempts)
			assert.Len(t, confirmed, len(parts))
			var restoredParts string
			require.NoError(t, f.db.QueryRow(`SELECT parts FROM model_announcement_events`).Scan(&restoredParts))
			assert.Equal(t, partsRaw, restoredParts)
		})
	}
}

// Treating the settings fault as a send failure postpones the remaining card
// for hours and consumes attempts even though no HTTP attempt was made.
func TestAnnouncementMultipartSettingsFaultRetriesPromptly(t *testing.T) {
	for _, priorFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_send_failure=%t", priorFailure), func(t *testing.T) {
			f := newAnnouncementFixture(t, 180, boolPtr(true))
			f.destination("https://receiver.example/RAW_SECRET?sig=original")
			r := newAnnouncementReceiver(t)
			ctx := context.Background()
			priorAttempts := 0
			if priorFailure {
				r.status = 503
				svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
				require.Error(t, svc.Drain(ctx))
				_, _, next, _ := f.event()
				f.clock.set(next)
				r.mu.Lock()
				r.status, r.cards = 202, nil
				r.mu.Unlock()
				priorAttempts = 1
			}
			reader := &interruptedAnnouncementSettings{Service: f.settings}
			sender := r.sender(t, func(_ *http.Request, resp *http.Response) (*http.Response, error) {
				reader.unavailable = true // part 1 accepted; part 2 cannot read settings
				return resp, nil
			})
			svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), reader, sender, f.clock)
			err := svc.Drain(ctx)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "RAW_SECRET")
			require.Len(t, r.snapshot(), 1)
			state, attempts, next, confirmed := f.event()
			assert.Equal(t, modelcatalog.EventPending, state)
			assert.Equal(t, priorAttempts, attempts)
			assert.False(t, next.After(f.clock.Now()), "settings recovery must not wait for send-failure backoff")
			assert.Equal(t, []int{1}, confirmed)
			var frozenPlan string
			require.NoError(t, f.db.QueryRow(`SELECT parts FROM model_announcement_events`).Scan(&frozenPlan))
			var parts []modelcatalog.AnnouncementPart
			require.NoError(t, json.Unmarshal([]byte(frozenPlan), &parts))
			require.Len(t, parts, 2)

			// Repeated faults after claiming must not increase the backoff counter.
			f.reopen()
			reader.Service = f.settings
			svc = modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), reader, r.sender(t, nil), f.clock)
			for range 2 {
				reader.unavailable, reader.failAfterRead = false, true
				f.clock.set(f.clock.Now().Add(time.Second))
				require.Error(t, svc.Drain(ctx))
				state, attempts, next, confirmed = f.event()
				assert.Equal(t, modelcatalog.EventPending, state)
				assert.Equal(t, priorAttempts, attempts)
				assert.False(t, next.After(f.clock.Now()))
				assert.Equal(t, []int{1}, confirmed)
				require.Len(t, r.snapshot(), 1)
			}
			// Later profile edits and a process restart cannot replace original changes.
			_, err = f.db.Exec(`UPDATE profiles SET model='later-specific'`)
			require.NoError(t, err)
			f.reopen()
			f.clock.set(f.clock.Now().Add(time.Second))
			svc = modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
			require.NoError(t, svc.Drain(ctx))
			assertAnnouncementCards(t, r.snapshot(), f.payload)
			state, attempts, _, confirmed = f.event()
			assert.Equal(t, modelcatalog.EventAnnounced, state)
			assert.Equal(t, priorAttempts+1, attempts)
			assert.Equal(t, []int{1, 2}, confirmed)
			var savedPlan string
			require.NoError(t, f.db.QueryRow(`SELECT parts FROM model_announcement_events`).Scan(&savedPlan))
			assert.Equal(t, frozenPlan, savedPlan)
			require.NoError(t, svc.Drain(ctx))
			require.Len(t, r.snapshot(), 2)
		})
	}
}

type interruptedAnnouncementSettings struct {
	*settings.Service
	unavailable, failAfterRead bool
}

func (s *interruptedAnnouncementSettings) ReadInternal(ctx context.Context) (settings.Values, error) {
	if s.unavailable {
		return settings.Values{}, errors.New("RAW_SECRET settings fault")
	}
	values, err := s.Service.ReadInternal(ctx)
	s.unavailable = s.failAfterRead
	return values, err
}

func TestAnnouncementSettingsFaultsAndSuppression(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured *bool
	}{{"awaiting_setting", nil}, {"suppressed", boolPtr(false)}, {"pending", boolPtr(true)}} {
		t.Run(test.name, func(t *testing.T) {
			f := newAnnouncementFixture(t, 0, test.configured)
			r := newAnnouncementReceiver(t)
			sender := r.sender(t, nil)
			broken := &announcementSettingsFault{}
			svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), broken, sender, f.clock)
			err := svc.Drain(context.Background())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "RAW_SECRET")
			_, attempts, _, _ := f.event()
			assert.Zero(t, attempts)
			assert.Empty(t, r.snapshot())
			svc = modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, sender, f.clock)
			require.NoError(t, svc.Drain(context.Background()))
			f.destination("https://receiver.example/RAW_SECRET")
			require.NoError(t, svc.Drain(context.Background()))
			state, _, _, _ := f.event()
			if test.configured != nil && *test.configured {
				assert.Equal(t, modelcatalog.EventAnnounced, state)
				require.Len(t, r.snapshot(), 1)
			} else {
				assert.Equal(t, modelcatalog.EventSuppressed, state)
				assert.Empty(t, r.snapshot())
			}
		})
	}
}
func boolPtr(v bool) *bool { return &v }

type announcementSettingsFault struct{}

func (s *announcementSettingsFault) ReadInternal(context.Context) (settings.Values, error) {
	return settings.Values{}, errors.New("RAW_SECRET settings fault")
}

func TestAnnouncementClaimsRenewBeforeEveryPart(t *testing.T) {
	yes := true
	f := newAnnouncementFixture(t, 700, &yes)
	f.destination("https://receiver.example/RAW_SECRET")
	r := newAnnouncementReceiver(t)
	otherDB, err := sqlite.Open(f.path)
	require.NoError(t, err)
	defer otherDB.Close()
	rival := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(otherDB), settings.NewService(sqlite.NewSettingsRepo(otherDB)), r.sender(t, nil), f.clock)
	startedAt := f.clock.Now()
	sender := r.sender(t, func(_ *http.Request, resp *http.Response) (*http.Response, error) {
		// Each logical send is within 15s, but the multipart union exceeds one
		// lease. A rival checks expiry between parts using the same clock.
		f.clock.set(f.clock.Now().Add(12 * time.Second))
		require.NoError(t, rival.Drain(context.Background()))
		return resp, nil
	})
	svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, sender, f.clock)
	require.NoError(t, svc.Drain(context.Background()))
	cards := r.snapshot()
	require.GreaterOrEqual(t, f.clock.Now().Sub(startedAt), time.Minute)
	assertAnnouncementCards(t, cards, f.payload)
	state, attempts, _, confirmed := f.event()
	assert.Equal(t, modelcatalog.EventAnnounced, state)
	assert.Equal(t, 1, attempts)
	assert.Len(t, confirmed, len(cards))
}

func TestAnnouncementStopsWhenRenewalFails(t *testing.T) {
	yes := true
	f := newAnnouncementFixture(t, 180, &yes)
	f.destination("https://receiver.example/RAW_SECRET")
	r := newAnnouncementReceiver(t)
	store := &failSecondAnnouncementRenewal{AnnouncementStore: sqlite.NewModelAnnouncementRepo(f.db)}
	svc := modelcatalog.NewAnnouncementService(store, f.settings, r.sender(t, nil), f.clock)
	require.Error(t, svc.Drain(context.Background()))
	require.Len(t, r.snapshot(), 1)
	state, _, _, confirmed := f.event()
	assert.Equal(t, modelcatalog.EventInFlight, state)
	assert.Equal(t, []int{1}, confirmed)
}

type failSecondAnnouncementRenewal struct {
	modelcatalog.AnnouncementStore
	calls int
}

func (s *failSecondAnnouncementRenewal) RenewClaim(ctx context.Context, id, claim string, at time.Time, lease time.Duration) error {
	s.calls++
	if s.calls == 2 {
		return errors.New("renewal I/O failure")
	}
	return s.AnnouncementStore.RenewClaim(ctx, id, claim, at, lease)
}

func TestAnnouncementDefiniteFailureAndConcurrentDrains(t *testing.T) {
	yes := true
	f := newAnnouncementFixture(t, 0, &yes)
	f.destination("https://receiver.example/RAW_SECRET")
	r := newAnnouncementReceiver(t)
	r.status = 503
	svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
	require.Error(t, svc.Drain(context.Background()))
	state, _, next, _ := f.event()
	assert.Equal(t, modelcatalog.EventPending, state)
	require.NoError(t, svc.Drain(context.Background()))
	require.Len(t, r.snapshot(), 1)
	f.reopen()
	f.clock.set(next)
	r.mu.Lock()
	r.status = 202
	r.mu.Unlock()
	svc = modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.Drain(context.Background()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, r.snapshot(), 2)
	state, _, _, _ = f.event()
	assert.Equal(t, modelcatalog.EventAnnounced, state)
}

// Returning ErrAnnouncementState without deferring the corrupt row prevents
// Drain from reaching the later event and its real HTTP receiver.
func TestAnnouncementCorruptRowsDoNotBlockReceiver(t *testing.T) {
	for _, corruption := range []struct{ name, set string }{
		{"payload_json", "payload='{'"},
		{"parts_json", "parts='{'"},
		{"confirmed_json", "confirmed_parts='{'"},
		{"missing_plan", "parts='[]'"},
		{"confirmed_out_of_range", "confirmed_parts='[2]'"},
		{"identity_mismatch", "payload=json_set(payload,'$.EventID','wrong')"},
		{"lease_timestamp", "lease_until='broken'"},
		{"retry_timestamp", "next_attempt_at='!'"},
		{"attempts_text", "attempts='broken'"},
		{"attempts_fractional", "attempts=1.5"},
		{"part_count_text", "part_count='broken'"},
		{"part_count_fractional", "part_count=1.5"},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			yes := true
			f := newAnnouncementFixture(t, 2, &yes)
			f.destination("https://receiver.example/RAW_SECRET")
			poison := f.payload
			poison.EventID = "corrupt-event"
			poison.CanonicalID = "vendor/corrupt-specific"
			payload, err := json.Marshal(poison)
			require.NoError(t, err)
			parts, err := notification.PartitionAnnouncement(poison, modelcatalog.MaxAnnouncementBytes)
			require.NoError(t, err)
			plan, err := json.Marshal(parts)
			require.NoError(t, err)
			_, err = f.db.Exec(`INSERT INTO model_first_observations(canonical_id,observation) VALUES(?,'{}')`, poison.CanonicalID)
			require.NoError(t, err)
			_, err = f.db.Exec(`INSERT INTO model_announcement_events(id,canonical_id,payload,state,parts,part_count) VALUES(?,?,?,'pending',?,1)`, poison.EventID, poison.CanonicalID, string(payload), string(plan))
			require.NoError(t, err)
			// Put the corrupt event strictly ahead of the valid event.
			_, err = f.db.Exec(`UPDATE model_announcement_events SET next_attempt_at=? WHERE id=?`, f.clock.Now().Format("2006-01-02T15:04:05.000000000Z"), f.payload.EventID)
			require.NoError(t, err)
			_, err = f.db.Exec(`UPDATE model_announcement_events SET `+corruption.set+` WHERE id=?`, poison.EventID)
			require.NoError(t, err)
			var frozenPayload, frozenParts, frozenConfirmed string
			require.NoError(t, f.db.QueryRow(`SELECT payload,parts,confirmed_parts FROM model_announcement_events WHERE id=?`, poison.EventID).Scan(&frozenPayload, &frozenParts, &frozenConfirmed))
			r := newAnnouncementReceiver(t)
			svc := modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
			require.ErrorIs(t, svc.Drain(context.Background()), modelcatalog.ErrAnnouncementPayload)
			assertAnnouncementCards(t, r.snapshot(), f.payload)
			f.reopen()
			var state, next, code, savedPayload, savedParts, savedConfirmed string
			var attempts int
			require.NoError(t, f.db.QueryRow(`SELECT state,attempts,next_attempt_at,last_code,payload,parts,confirmed_parts FROM model_announcement_events WHERE id=?`, poison.EventID).Scan(&state, &attempts, &next, &code, &savedPayload, &savedParts, &savedConfirmed))
			assert.Equal(t, modelcatalog.EventPending, state)
			assert.Equal(t, 1, attempts)
			assert.Equal(t, "payload_invalid", code)
			due, err := time.Parse(time.RFC3339Nano, next)
			require.NoError(t, err)
			assert.Equal(t, f.clock.Now().Add(4*time.Hour), due)
			assert.Equal(t, frozenPayload, savedPayload)
			assert.Equal(t, frozenParts, savedParts)
			assert.Equal(t, frozenConfirmed, savedConfirmed)
			require.NoError(t, f.db.QueryRow(`SELECT state FROM model_announcement_events WHERE id=?`, f.payload.EventID).Scan(&state))
			assert.Equal(t, modelcatalog.EventAnnounced, state)
			svc = modelcatalog.NewAnnouncementService(sqlite.NewModelAnnouncementRepo(f.db), f.settings, r.sender(t, nil), f.clock)
			require.NoError(t, svc.Drain(context.Background()))
			require.Len(t, r.snapshot(), 1)
			// Repair the original data, retaining its persisted retry deadline.
			_, err = f.db.Exec(`UPDATE model_announcement_events SET payload=?,parts=?,part_count=1,confirmed_parts='[]',lease_until='' WHERE id=?`, string(payload), string(plan), poison.EventID)
			require.NoError(t, err)
			f.clock.set(due)
			require.NoError(t, svc.Drain(context.Background()))
			cards := r.snapshot()
			require.Len(t, cards, 2)
			assertAnnouncementCards(t, cards[1:], poison)
		})
	}
}
