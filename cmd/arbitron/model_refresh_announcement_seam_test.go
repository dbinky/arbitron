package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired seam Area 4 refresh advancement <-> Area 10 announcement delivery
// (paired:12). An admin edit landing during discovery bumps the execution
// revision, so RefreshService.commit drops every proposal of that cycle. The
// newly usable model's one lifetime event must not be frozen by that cycle
// with "no profiles changed": it is frozen by the cycle that commits the
// advancement, so the card names the profile that moved to Y, and nothing is
// announced twice.
func TestRefreshDroppedProposalsDeferAnnouncementToCommittingCycle(t *testing.T) {
	sink, onModel := hookSink(t)
	ds, _, ctx := bootRefreshPinsRoot(t, sink)
	webhook := "https://hooks.example.test/teams/SEAM"
	catalogURL := "https://catalog.example.test/api.json"
	_, err := ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL, TeamsWebhookURL: &webhook})
	require.NoError(t, err)

	receiver := &receiverTransport{}
	deps := ds.refreshDeps
	fetcher := &fixtureFetcher{body: refreshCatalogOnlyX}
	deps.Fetcher = fetcher
	serveRootCatalog(t, ds, fetcher)
	deps.AnnouncementClient = &http.Client{Transport: receiver}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	cardsFor := func(model string) []string {
		receiver.mu.Lock()
		defer receiver.mu.Unlock()
		var cards []string
		for _, body := range receiver.bodies {
			if strings.Contains(body, model) {
				cards = append(cards, body)
			}
		}
		return cards
	}

	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	read, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)

	edited := make(chan error, 1)
	onModel(lifecycleModelY, func() {
		done := make(chan error, 1)
		go func() {
			isErr, text := mcpProfileCaller(t, ctx, ds)("profile_update", map[string]any{"alias": "pin-alias", "effort": "high", "revision": read.Revision})
			if isErr {
				done <- fmt.Errorf("profile_update: %s", text)
				return
			}
			done <- nil
		}()
		select {
		case err := <-done:
			edited <- err
		case <-time.After(10 * time.Second):
			edited <- fmt.Errorf("admin edit blocked behind the refresh probe")
		}
	})
	fetcher.mu.Lock()
	fetcher.body = refreshCatalogFixture
	fetcher.mu.Unlock()
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.NoError(t, <-edited)
	require.Empty(t, report.Changes)
	require.Equal(t, "advance_conflict", report.PathStatus["refresh"])
	// The admin edit re-proves its own route through the stack's prospective
	// discovery, which commits the one lifetime event for Y itself (paired row
	// 12), so Y may already be announced here, frozen without the later
	// advancement (gaps G27). What must hold regardless: one card, ever.
	cards := cardsFor(lifecycleModelY)
	require.LessOrEqual(t, len(cards), 1, "Y is announced at most once")

	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	cards = cardsFor(lifecycleModelY)
	require.Len(t, cards, 1, "Y is announced exactly once across the dropped and committing cycles")

	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.Len(t, cardsFor(lifecycleModelY), 1, "a later cycle never re-announces Y")
}
