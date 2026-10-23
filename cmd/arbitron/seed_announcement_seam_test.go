package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired seam Area 9 first-boot seed ↔ Area 10 announcement delivery
// (paired:15). Startup never discovers, so on a first boot with a pending YAML
// following seed the background loop's first cycle discovers each model,
// freezes its one lifetime event and drains it; the seed retry after that cycle
// reuses the cycle's proof. The seed retry is interrupted: either the seed
// acknowledgement or the profile write itself faults. A reopen resumes the seed
// (acknowledging the existing alias, or writing the unwritten one) and runs its
// own first cycle with the source down, or with no new usable delta. Across
// both boots there is one durable event and one card per model, carrying the
// frozen "no profiles changed", and an event frozen with an empty webhook is
// never replayed once the webhook is saved.
func TestSeedDiscoveryEventDeliveredAfterInterruptedFirstBootRoot(t *testing.T) {
	const webhook = "https://teams.example.test/incoming?token=private-setting"
	seed := strings.Split(lifecycleFollowing, "  - alias: other")[0]
	for _, tc := range []struct {
		name        string
		atDiscovery string
		interrupt   string // SQL that faults the first boot after prospective discovery
		reopenUp    bool   // the reopened source is reachable, so its refreshes reobserve the seeded models
		frozen      string
		cards       int
	}{
		{name: "acknowledgement interrupted, reopened source down", atDiscovery: webhook, interrupt: `CREATE TRIGGER seed_ack_fault BEFORE DELETE ON profile_seed_pending BEGIN SELECT RAISE(ABORT,'seed ack fault'); END`, frozen: "announced", cards: 1},
		{name: "profile write interrupted, retry reuses the stored proof", atDiscovery: webhook, interrupt: `CREATE TRIGGER seed_write_fault BEFORE INSERT ON profiles BEGIN SELECT RAISE(ABORT,'seed write fault'); END`, reopenUp: true, frozen: "announced", cards: 1},
		{name: "uninterrupted, the same boot's first cycle drains", atDiscovery: webhook, frozen: "announced", cards: 1},
		{name: "empty at discovery is never replayed", interrupt: `CREATE TRIGGER seed_ack_fault BEFORE DELETE ON profile_seed_pending BEGIN SELECT RAISE(ABORT,'seed ack fault'); END`, frozen: "suppressed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := newAnnouncementReceiver(t)
			leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}}
			ds, _, _ := buildLifecycleRootWithDependencies(t, seed, leaf, ModelDependencies{AnnouncementTransport: receiver.transport})
			ctx := lifecycleConnection(t, ds)
			if tc.atDiscovery != "" {
				setAnnouncementDestination(t, ctx, ds, tc.atDiscovery)
			}
			if tc.interrupt != "" {
				_, err := ds.db.Exec(tc.interrupt)
				require.NoError(t, err)
				// Startup never discovers, so the fault surfaces in the background
				// seed retry after the first cycle, not as a failed Start.
				require.NoError(t, ds.Start(ctx), "readiness does not depend on seed discovery")
				view, err := ds.catalogStore.Snapshot(ctx)
				require.NoError(t, err)
				require.Len(t, view.Reports, 1, "the first cycle committed; the faulted seed retry reused its proof")
				require.Empty(t, view.Reports[0].Changes, "the first cycle advances nothing")
				_, err = ds.db.Exec(`DROP TRIGGER ` + strings.Fields(tc.interrupt)[2])
				require.NoError(t, err)
			} else {
				require.NoError(t, ds.Start(ctx))
				// The serving boot's first cycle drains its events; the reopen
				// below must not resend them.
				rootReports(t, ds, 1)
			}
			for _, model := range []string{rootOld, rootNew} {
				assertAnnouncementEvent(t, ds, model, tc.frozen, 0)
			}
			if tc.atDiscovery == "" {
				setAnnouncementDestination(t, ctx, ds, webhook)
			}

			before, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			leaf.mu.Lock()
			leaf.down = !tc.reopenUp
			leaf.mu.Unlock()
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			ds.Close(context.Background())
			now := func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) }
			reopened, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{FetchTransport: catalogHTTPLeaf(leaf.fetch), ProviderTransport: catalogHTTPLeaf(leaf.provider), AnnouncementTransport: receiver.transport, Now: now})
			require.NoError(t, err)
			defer reopened.Close(context.Background())
			require.NoError(t, reopened.Start(ctx), "the reopen resumes the seed and serves")
			rootReports(t, reopened, len(before.Reports)+1)
			result, _ := mcpToolCaller(t, ctx, reopened)("model_catalog_refresh", nil)
			require.False(t, result.IsError)
			rootReports(t, reopened, len(before.Reports)+2)
			after, err := reopened.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.Len(t, after.Reports, len(before.Reports)+2, "the startup and manual refreshes each commit; reobserving a seeded model never faults the cycle")

			stored, err := reopened.profileSvc.Get(ctx, "worker")
			require.NoError(t, err, "the seeded alias exists after resume")
			assert.Equal(t, rootNew, stored.Model)
			pending, err := adaptsqlite.NewProfileRepo(reopened.db).PendingProfileSeed(ctx, nil)
			require.NoError(t, err)
			assert.Empty(t, pending, "the seed acknowledgement resumed")

			var events int
			require.NoError(t, reopened.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
			require.Equal(t, 2, events, "seed retry never creates another canonical event")
			final := "announced"
			if tc.frozen == "suppressed" {
				final = "suppressed"
			}
			for _, model := range []string{rootOld, rootNew} {
				cards := receiver.cards(model)
				require.Len(t, cards, tc.cards, model)
				for _, card := range cards {
					assert.Contains(t, card, "No profiles changed", "the card keeps the frozen discovery-time changes")
					assert.NotContains(t, card, "worker", "the seed write is not invented as an advancement")
				}
				assertAnnouncementEvent(t, reopened, model, final, 0)
			}
			for _, body := range receiver.bodies {
				assert.NotContains(t, body, "private-setting")
			}
		})
	}
}

// paired:15, the seedPending direction. The first boot finds the source down,
// so both YAML following seeds stay pending and no event exists. The next
// scheduled cycle reaches the source either in its own Refresh (the seed retry
// then reuses that proof) or, when the Refresh fetch fails, only in the seed
// retry's prospective discovery, which commits after that cycle's drain. Either
// way the following cycle's drain delivers exactly one card per canonical model
// with the frozen "no profiles changed", later retries and cycles never add an
// event, and an event frozen with an empty webhook stays suppressed after the
// webhook is saved.
func TestPendingSeedRetryDiscoveryEventDeliveredOnceRoot(t *testing.T) {
	const webhook = "https://teams.example.test/incoming?token=private-setting"
	for _, tc := range []struct {
		name          string
		atDiscovery   string
		refreshFails  bool // the retry tick's Refresh fetch fails; only the seed retry discovers
		frozen, final string
		cards         int
	}{
		{name: "refresh discovers, seed retry reuses the proof", atDiscovery: webhook, frozen: "announced", final: "announced", cards: 1},
		{name: "refresh fetch fails, seed retry discovers after the drain", atDiscovery: webhook, refreshFails: true, frozen: "pending", final: "announced", cards: 1},
		{name: "empty at seed-retry discovery is never replayed", refreshFails: true, frozen: "suppressed", final: "suppressed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := newAnnouncementReceiver(t)
			leaf := &lifecycleHTTP{source: []string{rootOld}, down: true}
			var fetchMu sync.Mutex
			failNext := 0
			fetch := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				fetchMu.Lock()
				fail := failNext > 0
				if fail {
					failNext--
				}
				fetchMu.Unlock()
				if fail {
					return nil, fmt.Errorf("source unavailable for this refresh")
				}
				return leaf.fetch(r)
			})
			ds, ticks, intervals := buildLifecycleRootWithDependencies(t, lifecycleFollowing, leaf, ModelDependencies{FetchTransport: fetch, AnnouncementTransport: receiver.transport})
			ctx := lifecycleConnection(t, ds)
			if tc.atDiscovery != "" {
				setAnnouncementDestination(t, ctx, ds, tc.atDiscovery)
			}
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			require.Len(t, pendingSeedOriginals(t, ds), 2, "an outage at boot leaves both seeds pending")
			countEvents := func() int {
				var n int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&n))
				return n
			}
			require.Zero(t, countEvents(), "nothing was discovered at boot")

			leaf.mu.Lock()
			leaf.down = false
			leaf.mu.Unlock()
			if tc.refreshFails {
				fetchMu.Lock()
				failNext = 1 // The tick's coordinated Refresh fetches first.
				fetchMu.Unlock()
			}
			rootTick(t, ticks)
			require.Eventually(t, func() bool {
				var n int
				return ds.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&n) == nil && n == 0
			}, 5*time.Second, 20*time.Millisecond, "the scheduled retry seeds both pending aliases")
			// The retry tick's own drain has run: the event is delivered when its
			// Refresh discovered, still pending when only the seed retry did.
			assertAnnouncementEvent(t, ds, rootOld, tc.frozen, 0)
			if tc.frozen == "announced" {
				require.Len(t, receiver.cards(rootOld), 1)
			} else {
				require.Empty(t, receiver.bodies, "the seed retry's prospective cycle never drains")
			}
			if tc.atDiscovery == "" {
				setAnnouncementDestination(t, ctx, ds, webhook)
			}
			// Each tick is consumed only after the previous cycle and its seed
			// retry returned, so the final send joins the third cycle.
			before, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			for i := 0; i < 4; i++ {
				rootTick(t, ticks)
			}
			after, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(after.Reports), len(before.Reports)+3, "reobserving the seeded model never faults a later cycle's commit")

			require.Equal(t, 1, countEvents(), "the seed retry and later cycles never add a canonical event")
			for _, alias := range []string{"worker", "other"} {
				p, err := ds.profileSvc.Get(ctx, alias)
				require.NoError(t, err)
				assert.Equal(t, rootOld, p.Model)
			}
			cards := receiver.cards(rootOld)
			require.Len(t, cards, tc.cards)
			for _, card := range cards {
				assert.Contains(t, card, "No profiles changed", "the card keeps the frozen discovery-time changes")
				assert.NotContains(t, card, "worker", "the seed write is not invented as an advancement")
			}
			assertAnnouncementEvent(t, ds, rootOld, tc.final, 0)
			for _, body := range receiver.bodies {
				assert.NotContains(t, body, "private-setting")
			}
		})
	}
}
