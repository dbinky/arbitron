package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired seam Area 7 admin following-profile discovery ↔ Area 10 announcement
// delivery (paired:14). On a serving stack whose catalog was unavailable at
// startup, an MCP profile_create with no stored proof runs the prospective
// discovery that freezes each model's one lifetime event, before (and
// independently of) the profile write. A later refresh with no new usable delta
// (the source is down) after a reopen still drains those events to the
// receiver; the card carries the original "no profiles changed", never the
// later create; an event frozen with an empty webhook is never replayed once
// the webhook is saved; and a settings fault at discovery awaits the setting.
func TestFollowingCreateDiscoveryEventDeliveredByLaterRefreshRoot(t *testing.T) {
	const webhook = "https://teams.example.test/incoming?token=private-setting"
	for _, tc := range []struct {
		name        string
		atDiscovery string
		fault       bool
		refuseWrite bool
		frozen      string
		cards       int
		final       string
	}{
		{name: "configured at discovery", atDiscovery: webhook, frozen: "pending", cards: 1, final: "announced"},
		{name: "profile write refused after discovery", atDiscovery: webhook, refuseWrite: true, frozen: "pending", cards: 1, final: "announced"},
		{name: "empty at discovery is never replayed", frozen: "suppressed", final: "suppressed"},
		{name: "settings fault at discovery awaits the setting", atDiscovery: webhook, fault: true, frozen: "awaiting_setting", cards: 1, final: "announced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := newAnnouncementReceiver(t)
			leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}, down: true}
			var ds *domainStack
			var fault atomic.Bool
			fetch := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
				// The fault lands after collect's first settings read, so only the
				// fresh post-collection eligibility read fails.
				if fault.Load() {
					_, err := ds.db.Exec(`ALTER TABLE system_settings RENAME TO system_settings_down`)
					require.NoError(t, err)
				}
				return leaf.fetch(r)
			})
			ds, _, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\n", leaf, ModelDependencies{FetchTransport: fetch, AnnouncementTransport: receiver.transport})
			ctx := lifecycleConnection(t, ds)
			if tc.atDiscovery != "" {
				setAnnouncementDestination(t, ctx, ds, tc.atDiscovery)
			}
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			rootReports(t, ds, 1)
			var events int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
			require.Zero(t, events, "the unavailable startup source discovers nothing")

			leaf.mu.Lock()
			leaf.down = false
			leaf.mu.Unlock()
			fault.Store(tc.fault)
			if tc.refuseWrite {
				_, err := ds.db.Exec(`CREATE TRIGGER profile_write_refused BEFORE INSERT ON profiles BEGIN SELECT RAISE(ABORT,'profile write refused'); END`)
				require.NoError(t, err)
			}
			call := mcpToolCaller(t, ctx, ds)
			res, text := call("profile_create", map[string]any{
				"alias": "fresh", "kind": "chat", "connection_ref": "root",
				"selection_mode": "following",
				"follow_target":  map[string]any{"provider": "openrouter", "tier": "flash"},
			})
			if tc.fault {
				fault.Store(false)
				_, err := ds.db.Exec(`ALTER TABLE system_settings_down RENAME TO system_settings`)
				require.NoError(t, err)
			}
			if tc.refuseWrite {
				require.True(t, res.IsError, text)
				_, err := ds.profileSvc.Get(ctx, "fresh")
				require.Error(t, err, "the refused write persisted nothing")
			} else {
				require.False(t, res.IsError, text)
				stored, err := ds.profileSvc.Get(ctx, "fresh")
				require.NoError(t, err)
				require.Equal(t, rootNew, stored.Model)
			}
			for _, model := range []string{rootOld, rootNew} {
				assertAnnouncementEvent(t, ds, model, tc.frozen, 0)
			}
			view, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			reports := len(view.Reports)
			if tc.atDiscovery == "" {
				setAnnouncementDestination(t, ctx, ds, webhook)
			}

			// Reopen with the source down: the startup refresh and a manual
			// refresh discover nothing new, yet must drain the frozen events.
			leaf.mu.Lock()
			leaf.down = true
			leaf.mu.Unlock()
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			ds.Close(context.Background())
			now := func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) }
			reopened, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{FetchTransport: catalogHTTPLeaf(leaf.fetch), ProviderTransport: catalogHTTPLeaf(leaf.provider), AnnouncementTransport: receiver.transport, Now: now})
			require.NoError(t, err)
			defer reopened.Close(context.Background())
			require.NoError(t, reopened.Start(ctx))
			rootReports(t, reopened, reports+1)
			result, _ := mcpToolCaller(t, ctx, reopened)("model_catalog_refresh", nil)
			require.False(t, result.IsError)
			rootReports(t, reopened, reports+2)

			for _, model := range []string{rootOld, rootNew} {
				cards := receiver.cards(model)
				require.Len(t, cards, tc.cards, model)
				for _, card := range cards {
					assert.Contains(t, card, "No profiles changed", "the card keeps the frozen discovery-time changes")
					assert.NotContains(t, card, "fresh", "the later create is not invented as an advancement")
					assert.NotContains(t, card, "→")
				}
				assertAnnouncementEvent(t, reopened, model, tc.final, 0)
			}
			for _, body := range receiver.bodies {
				assert.False(t, strings.Contains(body, "private-setting"))
			}
		})
	}
}
