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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No deduplication: every accepted POST is recorded by an actual HTTP receiver.
// Only dialing is substituted; the production fetcher/sender still guard URLs.
type announcementReceiver struct {
	mu                   sync.Mutex
	bodies, destinations []string
	transport            http.RoundTripper
}

func newAnnouncementReceiver(t *testing.T) *announcementReceiver {
	t.Helper()
	receiver := &announcementReceiver{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || !json.Valid(raw) {
			http.Error(w, "invalid card", http.StatusBadRequest)
			return
		}
		receiver.mu.Lock()
		receiver.bodies = append(receiver.bodies, string(raw))
		receiver.destinations = append(receiver.destinations, r.Header.Get("X-Test-Destination"))
		receiver.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	receiver.transport = catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Host != "teams.example.test" || r.URL.Scheme != "https" {
			return nil, fmt.Errorf("wrong announcement destination")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.Header.Set("X-Test-Destination", r.URL.String())
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})
	return receiver
}

func (r *announcementReceiver) cards(model string) []string {
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

func setAnnouncementDestination(t *testing.T, ctx context.Context, ds *domainStack, destination string) {
	t.Helper()
	result, _ := mcpToolCaller(t, ctx, ds)("system_settings_update", map[string]any{"teams_webhook_url": destination})
	require.False(t, result.IsError, "%+v", result.Content)
	for _, content := range result.Content {
		assert.NotContains(t, fmt.Sprint(content), "private-setting")
	}
}

func assertAnnouncementEvent(t *testing.T, ds *domainStack, canonical, state string, changes int) modelcatalog.AnnouncementPayload {
	t.Helper()
	var raw, got string
	require.NoError(t, ds.db.QueryRow(`SELECT payload,state FROM model_announcement_events WHERE canonical_id=?`, canonical).Scan(&raw, &got))
	require.Equal(t, state, got)
	var payload modelcatalog.AnnouncementPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	require.Len(t, payload.Changes, changes)
	require.NotEmpty(t, payload.Paths)
	return payload
}

func TestModelAnnouncementAllDiscoveredCandidatesRoot(t *testing.T) {
	const older = "deepseek/deepseek-v3-flash"
	const other = "deepseek/deepseek-v4-pro"
	const unknown = "vendor/native-preview-20261018"
	const missing = "vendor/missing-proof"
	const wrong = "vendor/wrong-proof"
	const wrongProvider = "vendor/wrong-provider"
	const unconfirmed = "offering-v4.1"
	receiver := newAnnouncementReceiver(t)
	leaf := &lifecycleHTTP{source: []string{rootOld}, arrived: make(chan struct{}), release: make(chan struct{}), actual: map[string]string{missing: "", wrong: "fallback", wrongProvider: "ollama-cloud/" + wrongProvider}}
	// This earlier historical offering is never executable here. Canonical
	// identity alone must not make it the provider/tier named by the card.
	fetch := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		response, err := leaf.fetch(r)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		var catalog map[string]any
		if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
			return nil, err
		}
		catalog["ollama-cloud"] = map[string]any{"id": "ollama-cloud", "models": map[string]any{
			unconfirmed: map[string]any{"id": unconfirmed, "canonical_model_id": rootNew},
		}}
		raw, err := json.Marshal(catalog)
		if err != nil {
			return nil, err
		}
		return lifecycleResponse(r, string(raw)), nil
	})
	var hookMu sync.Mutex
	var beforeProof func() error
	var hookErr error
	providerHTTP := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct{ Model string }
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		if body.Model == rootNew {
			hookMu.Lock()
			f := beforeProof
			beforeProof = nil
			hookMu.Unlock()
			if f != nil {
				// Return exact evidence from the old authenticated request, then
				// rotate its real connection before the adapter accepts proof.
				response, err := leaf.provider(r)
				hookMu.Lock()
				hookErr = f()
				hookMu.Unlock()
				return response, err
			}
		}
		return leaf.provider(r)
	})
	ds, ticks, intervals := buildLifecycleRootWithDependencies(t, strings.Split(lifecycleFollowing, "  - alias: other")[0], leaf, ModelDependencies{FetchTransport: fetch, ProviderTransport: providerHTTP, AnnouncementTransport: receiver.transport})
	ctx := lifecycleConnection(t, ds)
	policy, err := ds.IdentityPolicy.Get(ctx)
	require.NoError(t, err)
	policy.Rules = append(policy.Rules, modelidentity.PatternRule{
		Providers: []string{"ollama-cloud"}, Tier: "pro", Pattern: `offering-v(\d+(?:\.\d+)?)`, VersionGroups: []int{1},
	})
	_, err = ds.IdentityPolicy.Update(ctx, policy.Revision, policy.Rules)
	require.NoError(t, err)
	setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/incoming?token=private-setting")
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	require.Len(t, receiver.cards(rootOld), 1, "first-boot discovery reaches the receiver")
	assertAnnouncementEvent(t, ds, rootOld, "announced", 0)
	old, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
	require.NoError(t, err)
	select {
	case <-leaf.arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("old run did not start")
	}
	defer func() {
		select {
		case <-leaf.release:
		default:
			close(leaf.release)
		}
	}()
	leaf.mu.Lock()
	leaf.source = []string{rootOld, rootNew, older, other, unknown, missing, wrong, wrongProvider}
	leaf.actual[rootNew] = "fallback"
	leaf.mu.Unlock()
	call := mcpToolCaller(t, ctx, ds)
	assertUnannounced := func() {
		t.Helper()
		assert.Empty(t, receiver.cards(rootNew), "unverified identity/configuration must not reach Teams")
		assert.NotContains(t, announcedPayloads(t, ds), rootNew)
		profile, err := ds.profileSvc.Get(ctx, "worker")
		require.NoError(t, err)
		assert.Equal(t, rootOld, profile.Model)
	}
	result, _ := call("model_catalog_refresh", nil)
	require.False(t, result.IsError, "%+v", result.Content)
	assertUnannounced()
	leaf.mu.Lock()
	delete(leaf.actual, rootNew)
	leaf.mu.Unlock()
	const rotatedKey = "rotated-lifecycle-key"
	hookMu.Lock()
	beforeProof = func() error {
		key := rotatedKey
		if err := ds.connSvc.Update(ctx, provider.Connection{Name: "root", Provider: "openrouter", BaseURL: "https://provider.example.test"}, &key); err != nil {
			return err
		}
		leaf.mu.Lock()
		leaf.key = key
		leaf.mu.Unlock()
		return nil
	}
	hookMu.Unlock()
	expireProbeBackoff(t, ds.db)
	call("model_catalog_refresh", nil)
	hookMu.Lock()
	pendingHook, rotationErr := beforeProof, hookErr
	hookMu.Unlock()
	require.Nil(t, pendingHook, "the real credential edit must occur during proof I/O")
	require.NoError(t, rotationErr)
	assertUnannounced()
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	expireProbeBackoff(t, ds.db)
	rootTick(t, ticks)
	require.Eventually(t, func() bool { return len(receiver.cards(rootNew)) == 1 }, 2*time.Second, time.Millisecond, "the four-hour tick must reach the receiver")
	rootReports(t, ds, len(view.Reports)+1)
	close(leaf.release)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
	lifecycleExecute(t, ds, ctx, rootNew)
	leaf.mu.Lock()
	assert.Equal(t, []string{rootOld, rootOld, rootNew, rootNew}, leaf.executed)
	leaf.mu.Unlock()
	for _, model := range []string{rootNew, older, other, unknown} {
		cards := receiver.cards(model)
		require.Len(t, cards, 1, model)
		canonical := model
		changes := 0
		if model == unknown {
			canonical = "openrouter/" + unknown
		}
		if model == rootNew {
			changes = 1
		}
		facts := announcementCardFacts(t, cards[0])
		assert.Equal(t, []string{canonical}, facts["Model"])
		assert.Equal(t, []string{"openrouter"}, facts["Provider"])
		if model == rootNew {
			assert.Equal(t, []string{"flash"}, facts["Tier"], "never use the earlier unconfirmed pro offering")
			assert.Equal(t, []string{rootOld + " → " + rootNew}, facts["worker"])
		}
		payload := assertAnnouncementEvent(t, ds, canonical, "announced", changes)
		assert.Equal(t, []string{payload.EventID}, facts["Event"])
		assert.Equal(t, "openrouter", payload.Provider)
		assert.Equal(t, []string{payload.FirstConfirmedAt.UTC().Format(time.RFC3339Nano)}, facts["First confirmed"])
		var paths []string
		for _, ev := range payload.Paths {
			paths = append(paths, ev.PathKey+" ("+ev.ConfigRevision+")")
			assert.NotContains(t, cards[0], ev.ProofRef)
		}
		assert.Equal(t, paths, facts["Path"])
		if model == rootNew {
			assert.Equal(t, "flash", payload.Tier)
			for _, ev := range payload.Paths {
				assert.Equal(t, "openrouter", ev.Provider)
				assert.Equal(t, rootNew, ev.NativeModel)
				assert.True(t, ev.Confirmed())
			}
		}
		if changes == 1 {
			assert.Equal(t, "worker", payload.Changes[0].Alias)
			assert.Equal(t, rootOld, payload.Changes[0].FromModel)
			assert.Equal(t, rootNew, payload.Changes[0].ToModel)
			assert.Contains(t, cards[0], rootOld+" → "+rootNew)
		} else {
			assert.Contains(t, cards[0], "No profiles changed")
		}
		for _, secret := range []string{"private-setting", "lifecycle-key", rotatedKey, ds.cfg.Workspace.Path, unconfirmed} {
			assert.NotContains(t, cards[0], secret)
		}
	}
	for _, model := range []string{missing, wrong, wrongProvider} {
		assert.Empty(t, receiver.cards(model))
		var count int
		require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events WHERE canonical_id=?`, "openrouter/"+model).Scan(&count))
		assert.Zero(t, count)
	}
	frozen := announcedPayloads(t, ds)
	originalCards := receiver.cards(rootNew)
	// A genuinely new configured path confirms the same model, without a new
	// lifetime event or a rewrite of the first path/change payload.
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "later", Provider: "openrouter", BaseURL: "https://provider.example.test"}, rotatedKey))
	result, _ = call("model_catalog_refresh", nil)
	require.False(t, result.IsError, "%+v", result.Content)
	later, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	var original modelcatalog.AnnouncementPayload
	require.NoError(t, json.Unmarshal([]byte(frozen[rootNew]), &original))
	confirmedPaths := 0
	for _, ev := range later.Evidence {
		if ev.CanonicalID == rootNew && ev.Confirmed() {
			confirmedPaths++
			assert.Equal(t, "openrouter", ev.Provider)
		}
	}
	assert.Greater(t, confirmedPaths, len(original.Paths), "the new path must actually acquire durable proof")
	policy, err = ds.IdentityPolicy.Get(ctx)
	require.NoError(t, err)
	for i := range policy.Rules {
		if policy.Rules[i].Tier == "flash" {
			policy.Rules[i].Tier = "reclassified"
		}
	}
	_, err = ds.IdentityPolicy.Update(ctx, policy.Revision, policy.Rules)
	require.NoError(t, err)
	leaf.mu.Lock()
	leaf.source = []string{rootOld, older, other, unknown, missing, wrong, wrongProvider}
	leaf.mu.Unlock()
	rootTick(t, ticks)
	rootReports(t, ds, len(later.Reports)+1)
	assert.Equal(t, frozen, announcedPayloads(t, ds), "policy edits and disappearance cannot rewrite original payloads")
	assert.Equal(t, originalCards, receiver.cards(rootNew))
	leaf.mu.Lock()
	leaf.source = append(leaf.source, rootNew)
	leaf.mu.Unlock()
	result, _ = call("model_catalog_refresh", nil)
	require.False(t, result.IsError, "%+v", result.Content)
	assert.Equal(t, frozen, announcedPayloads(t, ds))
	assert.Equal(t, originalCards, receiver.cards(rootNew), "reappearance never creates another lifetime card")
	prior, err := ds.instanceRepo.Get(ctx, old)
	require.NoError(t, err)
	assert.Equal(t, rootOld, prior.ModelPins["worker"])
	before, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, before.HistoricalModels, 9)
	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	ds.Close(context.Background())
	reopened, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{FetchTransport: fetch, ProviderTransport: providerHTTP, AnnouncementTransport: receiver.transport})
	require.NoError(t, err)
	defer reopened.Close(context.Background())
	after, err := reopened.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	foundUnknown := false
	for _, entry := range after.HistoricalEntries {
		if entry.NativeID == unknown {
			foundUnknown = true
			assert.Empty(t, entry.Tier)
			assert.Empty(t, entry.OrderKey)
			assert.Empty(t, entry.SpecificModel)
		}
	}
	require.True(t, foundUnknown, "the unknown native ID survives reopen")
	offerings := map[string]string{}
	for _, entry := range after.HistoricalEntries {
		if entry.CanonicalID == rootNew {
			offerings[entry.Provider] = entry.Tier
		}
	}
	assert.Equal(t, map[string]string{"ollama-cloud": "pro", "openrouter": "reclassified"}, offerings)
	for _, ev := range after.Evidence {
		if ev.CanonicalID == rootNew && ev.Confirmed() {
			assert.Equal(t, "openrouter", ev.Provider, "reopen must retain the actual confirming offering")
		}
	}
	require.NoError(t, reopened.Start(ctx))
	lifecycleExecute(t, reopened, ctx, rootNew)
	assert.Equal(t, frozen, announcedPayloads(t, reopened))
	assert.Equal(t, original, assertAnnouncementEvent(t, reopened, rootNew, "announced", 1))
	assert.Equal(t, originalCards, receiver.cards(rootNew), "restart cannot send or rewrite the original card")
}

func announcedPayloads(t *testing.T, ds *domainStack) map[string]string {
	t.Helper()
	rows, err := ds.db.Query(`SELECT canonical_id,payload,state FROM model_announcement_events ORDER BY canonical_id`)
	require.NoError(t, err)
	defer rows.Close()
	payloads := map[string]string{}
	for rows.Next() {
		var canonical, payload, state string
		require.NoError(t, rows.Scan(&canonical, &payload, &state))
		assert.Equal(t, modelcatalog.EventAnnounced, state)
		payloads[canonical] = payload
	}
	require.NoError(t, rows.Err())
	return payloads
}

func announcementCardFacts(t *testing.T, card string) map[string][]string {
	t.Helper()
	var message struct {
		Attachments []struct {
			Content struct {
				Body []struct {
					Facts []struct{ Title, Value string }
				}
			}
		}
	}
	require.NoError(t, json.Unmarshal([]byte(card), &message))
	require.Len(t, message.Attachments, 1)
	facts := map[string][]string{}
	for _, block := range message.Attachments[0].Content.Body {
		for _, fact := range block.Facts {
			facts[fact.Title] = append(facts[fact.Title], fact.Value)
		}
	}
	return facts
}

type announcementTestClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *announcementTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *announcementTestClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func TestModelAnnouncementMultipartPauseReopenRoot(t *testing.T) {
	// Enough real profile changes to exceed the production serialized card cap.
	const aliasWidth = 128
	profileCount := modelcatalog.MaxAnnouncementBytes/aliasWidth + 1
	aliases := []string{"worker"}
	var profiles strings.Builder
	profiles.WriteString("profiles:\n")
	for i := 0; i < profileCount; i++ {
		alias := "worker"
		if i > 0 {
			alias = fmt.Sprintf("follower%s%04d", strings.Repeat("a", aliasWidth), i)
			aliases = append(aliases, alias)
		}
		fmt.Fprintf(&profiles, "  - alias: %s\n    kind: chat\n    connectionref: root\n    selection_mode: following\n    follow_target: {provider: openrouter, tier: flash}\n", alias)
	}
	receiver := newAnnouncementReceiver(t)
	clock := &announcementTestClock{at: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)}
	var ds *domainStack
	var adminContext context.Context
	var once sync.Once
	transport := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		response, err := receiver.transport.RoundTrip(r)
		if err == nil && strings.Contains(string(raw), rootNew) {
			once.Do(func() {
				var state string
				if err = ds.db.QueryRow(`SELECT state FROM model_announcement_events WHERE canonical_id=?`, rootNew).Scan(&state); err != nil {
					return
				}
				if state != "in_flight" {
					err = fmt.Errorf("multipart plan must be in flight before I/O")
					return
				}
				clock.advance(7 * time.Second)
				empty := ""
				_, err = ds.SystemSettings.Update(adminContext, settingsdomain.Patch{TeamsWebhookURL: &empty})
			})
			if err != nil {
				response.Body.Close()
				return nil, err
			}
		}
		return response, err
	})
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	var ticks chan time.Time
	var intervals chan time.Duration
	ds, ticks, intervals = buildLifecycleRootWithDependencies(t, profiles.String(), leaf, ModelDependencies{AnnouncementTransport: transport, Now: clock.now})
	ctx := lifecycleConnection(t, ds)
	adminContext = ctx
	setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/parts?token=private-setting")
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	lifecycleExecute(t, ds, ctx, rootOld)
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	leaf.mu.Lock()
	leaf.source = []string{rootOld, rootNew}
	leaf.mu.Unlock()
	rootTick(t, ticks)
	// The real capability/route validator processes hundreds of aliases; wait
	// for its receiver sink rather than the small-fixture report deadline.
	require.Eventually(t, func() bool { return len(receiver.cards(rootNew)) == 1 }, 30*time.Second, 10*time.Millisecond)
	rootReports(t, ds, len(view.Reports)+1)
	original := assertAnnouncementEvent(t, ds, rootNew, "pending", profileCount)
	var count, attempts int
	var confirmed, rawParts, next string
	require.NoError(t, ds.db.QueryRow(`SELECT part_count,attempts,confirmed_parts,parts,next_attempt_at FROM model_announcement_events WHERE canonical_id=?`, rootNew).Scan(&count, &attempts, &confirmed, &rawParts, &next))
	require.Greater(t, count, 1)
	assert.Zero(t, attempts, "clearing the destination must not consume a retry attempt")
	assert.JSONEq(t, `[1]`, confirmed)
	require.Len(t, receiver.cards(rootNew), 1, "clearing destination pauses between parts")
	deadline, err := time.Parse(time.RFC3339Nano, next)
	require.NoError(t, err)
	assert.Equal(t, clock.now(), deadline, "reconfiguration must not wait for send-failure backoff")
	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	ds.Close(context.Background())
	clock.advance(time.Second)
	leaf.mu.Lock()
	leaf.down = true
	leaf.mu.Unlock()
	reopened, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{FetchTransport: catalogHTTPLeaf(leaf.fetch), ProviderTransport: catalogHTTPLeaf(leaf.provider), AnnouncementTransport: receiver.transport, Now: clock.now})
	require.NoError(t, err)
	defer reopened.Close(context.Background())
	setAnnouncementDestination(t, ctx, reopened, "https://teams.example.test/remaining-parts?token=private-setting")
	require.NoError(t, reopened.Start(ctx))
	assert.Equal(t, original, assertAnnouncementEvent(t, reopened, rootNew, "announced", profileCount))
	var restoredParts string
	require.NoError(t, reopened.db.QueryRow(`SELECT parts,confirmed_parts FROM model_announcement_events WHERE canonical_id=?`, rootNew).Scan(&restoredParts, &confirmed))
	assert.Equal(t, rawParts, restoredParts, "the frozen partition plan survives reopen")
	var indexes []int
	require.NoError(t, json.Unmarshal([]byte(confirmed), &indexes))
	assert.Len(t, indexes, count)
	cards := receiver.cards(rootNew)
	require.Len(t, cards, count, "confirmed part one is not resent")
	seen := map[string]int{}
	for i, card := range cards {
		assert.LessOrEqual(t, len(card), modelcatalog.MaxAnnouncementBytes)
		assert.Contains(t, card, fmt.Sprintf("%d/%d", i+1, count))
		var message struct {
			Attachments []struct {
				Content struct {
					Body []struct {
						Facts []struct{ Title, Value string }
					}
				}
			}
		}
		require.NoError(t, json.Unmarshal([]byte(card), &message))
		for _, attachment := range message.Attachments {
			for _, body := range attachment.Content.Body {
				for _, fact := range body.Facts {
					if fact.Value == rootOld+" → "+rootNew {
						seen[fact.Title]++
					}
				}
			}
		}
		assert.NotContains(t, card, "private-setting")
		assert.NotContains(t, card, "lifecycle-key")
	}
	require.Len(t, seen, profileCount)
	for _, alias := range aliases {
		assert.Equal(t, 1, seen[alias], alias)
	}
	receiver.mu.Lock()
	part := 0
	for i, body := range receiver.bodies {
		if strings.Contains(body, rootNew) {
			path := "/remaining-parts?"
			if part == 0 {
				path = "/parts?"
			}
			assert.Contains(t, receiver.destinations[i], path, "each part uses the current raw destination")
			part++
		}
	}
	receiver.mu.Unlock()
	lifecycleExecute(t, reopened, ctx, rootNew)
}

// Restart drains frozen events even when discovery fails. The receiver does not
// deduplicate an accepted-but-lost response or an unsaved delivery marker (D7).
func TestModelAnnouncementRetryReopenDespiteDiscoveryFailureRoot(t *testing.T) {
	for _, failure := range []string{"rejected", "accepted response lost", "marker write", "oversized 2xx"} {
		t.Run(failure, func(t *testing.T) {
			receiver := newAnnouncementReceiver(t)
			clock := &announcementTestClock{at: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)}
			var once sync.Once
			transport := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				r.Body = io.NopCloser(strings.NewReader(string(raw)))
				first := false
				if strings.Contains(string(raw), rootNew) {
					once.Do(func() { first = true })
				}
				if first && failure == "rejected" {
					clock.advance(7 * time.Second)
					return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret-response")), Request: r}, nil
				}
				response, err := receiver.transport.RoundTrip(r)
				if first && err == nil {
					clock.advance(7 * time.Second)
					if failure == "accepted response lost" {
						response.Body.Close()
						return nil, fmt.Errorf("private-setting: response lost")
					}
					if failure == "oversized 2xx" {
						response.Body.Close()
						response.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", modelcatalog.MaxAnnouncementBytes+1)))
					}
				}
				return response, err
			})
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			deps := ModelDependencies{AnnouncementTransport: transport, Now: clock.now}
			ds, ticks, intervals := buildLifecycleRootWithDependencies(t, strings.Split(lifecycleFollowing, "  - alias: other")[0], leaf, deps)
			ctx := lifecycleConnection(t, ds)
			setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/first?token=private-setting")
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			lifecycleExecute(t, ds, ctx, rootOld)
			if failure == "marker write" {
				_, err := ds.db.Exec(`CREATE TRIGGER announcement_marker_fault BEFORE UPDATE OF confirmed_parts ON model_announcement_events WHEN NEW.canonical_id='` + rootNew + `' BEGIN SELECT RAISE(ABORT,'marker unavailable'); END`)
				require.NoError(t, err)
			}
			leaf.mu.Lock()
			leaf.source = []string{rootOld, rootNew}
			leaf.mu.Unlock()
			view, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			rootTick(t, ticks)
			rootReports(t, ds, len(view.Reports)+1)
			state := "pending"
			switch failure {
			case "accepted response lost":
				state = "delivery_unknown"
			case "marker write":
				state = "in_flight"
			case "oversized 2xx":
				state = "announced"
			}
			original := assertAnnouncementEvent(t, ds, rootNew, state, 1)
			var next, code string
			require.NoError(t, ds.db.QueryRow(`SELECT next_attempt_at,last_code FROM model_announcement_events WHERE canonical_id=?`, rootNew).Scan(&next, &code))
			assert.NotContains(t, code, "private-setting")
			if failure != "oversized 2xx" {
				deadline, err := time.Parse(time.RFC3339Nano, next)
				require.NoError(t, err)
				if failure == "marker write" {
					assert.Equal(t, clock.now().Add(4*time.Hour-7*time.Second), deadline)
				} else {
					assert.Equal(t, clock.now().Add(4*time.Hour), deadline)
				}
			}
			setAnnouncementDestination(t, ctx, ds, "")
			clock.advance(4 * time.Hour)
			leaf.mu.Lock()
			leaf.down = true
			leaf.mu.Unlock()
			result, _ := mcpToolCaller(t, ctx, ds)("model_catalog_refresh", nil)
			require.False(t, result.IsError)
			before := len(receiver.cards(rootNew))
			setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/reconfigured?token=private-setting")
			if failure == "marker write" {
				_, err := ds.db.Exec(`DROP TRIGGER announcement_marker_fault`)
				require.NoError(t, err)
			}
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			ds.Close(context.Background())
			reopened, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{FetchTransport: catalogHTTPLeaf(leaf.fetch), ProviderTransport: catalogHTTPLeaf(leaf.provider), AnnouncementTransport: transport, Now: clock.now})
			require.NoError(t, err)
			defer reopened.Close(context.Background())
			require.NoError(t, reopened.Start(ctx))
			assert.Equal(t, original, assertAnnouncementEvent(t, reopened, rootNew, "announced", 1), "retry retains original changes and proof")
			want := 1
			if failure == "accepted response lost" || failure == "marker write" {
				want = 2
			}
			cards := receiver.cards(rootNew)
			require.Len(t, cards, want)
			if want == 2 {
				assert.Equal(t, cards[0], cards[1], "D7 permits the identical frozen card twice")
			}
			if failure != "oversized 2xx" {
				assert.Equal(t, before+1, len(cards))
				receiver.mu.Lock()
				assert.Contains(t, receiver.destinations[len(receiver.destinations)-1], "/reconfigured?")
				receiver.mu.Unlock()
			}
			lifecycleExecute(t, reopened, ctx, rootNew)
			for _, card := range cards {
				assert.Contains(t, card, rootOld+" → "+rootNew)
				assert.NotContains(t, card, "private-setting")
				assert.NotContains(t, card, "lifecycle-key")
			}
		})
	}
}
