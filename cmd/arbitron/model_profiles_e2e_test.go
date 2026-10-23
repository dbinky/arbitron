package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertPublicationPending(t *testing.T, result gomcp.CallToolResult) {
	t.Helper()
	var report modelcatalog.PublicRefreshReport
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(gomcp.TextContent).Text), &report))
	require.True(t, report.PublicationPending, "a failed publication must never report success")
}

func rootTicker(t *testing.T, intervals <-chan time.Duration) {
	t.Helper()
	select {
	case d := <-intervals:
		require.Equal(t, 4*time.Hour, d)
	case <-time.After(2 * time.Second):
		t.Fatal("root ticker not constructed")
	}
}

// expireProbeBackoff stands in for the wall-clock time between scheduled
// cycles: it ages every non-confirming usability check past its retry delay so
// the next cycle may re-probe it. Retirement (the failure count) is untouched.
func expireProbeBackoff(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`UPDATE model_usability_evidence SET checked_at=? WHERE state <> 'confirmed'`, time.Unix(0, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func rootTick(t *testing.T, ticks chan time.Time) {
	t.Helper()
	select {
	case ticks <- time.Now():
	case <-time.After(2 * time.Second):
		t.Fatal("root ticker not consumed")
	}
}
func rootReports(t *testing.T, ds *domainStack, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		v, e := ds.catalogStore.Snapshot(context.Background())
		return e == nil && len(v.Reports) >= want
	}, 2*time.Second, time.Millisecond)
	// Wait for the same coordinator's admission slot, not a second refresh.
	require.Eventually(t, func() bool {
		return ds.modelRefresh.coordinator.Do(context.Background(), func(context.Context) error { return nil }) == nil
	}, 2*time.Second, time.Millisecond)
}

func TestModelProfilesE2ESinks(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}, arrived: make(chan struct{}), release: make(chan struct{})}
	ds, ticks, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	call := mcpToolCaller(t, ctx, ds)
	created, _ := call("profile_create", map[string]any{"alias": "worker", "kind": "chat", "connection_ref": "root", "selection_mode": "following", "follow_target": map[string]any{"provider": "openrouter", "tier": "flash"}})
	require.False(t, created.IsError, "%+v", created.Content)
	old, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
	require.NoError(t, err)
	select {
	case <-leaf.arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("old run did not execute")
	}
	defer func() {
		select {
		case <-leaf.release:
		default:
			close(leaf.release)
		}
	}()
	leaf.mu.Lock()
	leaf.source = []string{rootOld, rootNew}
	leaf.mu.Unlock()
	before, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	rootTick(t, ticks)
	require.Eventually(t, func() bool { p, e := ds.profileSvc.Get(ctx, "worker"); return e == nil && p.Model == rootNew }, 2*time.Second, time.Millisecond)
	rootReports(t, ds, len(before.Reports)+1)
	close(leaf.release)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
	lifecycleExecute(t, ds, ctx, rootNew)
	leaf.mu.Lock()
	assert.Equal(t, []string{rootOld, rootOld, rootNew, rootNew}, leaf.executed)
	leaf.down = true
	leaf.mu.Unlock()
	before, err = ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	rootTick(t, ticks)
	rootReports(t, ds, len(before.Reports)+1)
	p, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, rootNew, p.Model)
	res, _ := call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	pending, err := ds.catalogStore.Unpublished(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
	var raw string
	require.NoError(t, ds.db.QueryRow(`SELECT payload FROM model_announcement_events WHERE canonical_id=?`, rootNew).Scan(&raw))
	var event modelcatalog.AnnouncementPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &event))
	require.Len(t, event.Changes, 1)
	assert.Equal(t, rootOld, event.Changes[0].FromModel)
	assert.Equal(t, rootNew, event.Changes[0].ToModel)
	prior, err := ds.instanceRepo.Get(ctx, old)
	require.NoError(t, err)
	assert.Equal(t, rootOld, prior.ModelPins["worker"])
}

func TestAutomaticCapabilitiesRoot(t *testing.T) {
	const bad = "deepseek/deepseek-v4.1-flash"
	const good = "deepseek/deepseek-v4.2-flash"
	caps := filepath.Join(t.TempDir(), "capabilities.yaml")
	require.NoError(t, os.WriteFile(caps, []byte(`backends:
  claude-code:
    context_tiers: [default, 1m]
    effort_levels: [default, high]
    model_overrides:
      deepseek/deepseek-v4.1-flash:
        context_tiers: [default]
`), 0600))
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, ticks, intervals := buildLifecycleRoot(t, "capabilitiespath: "+caps+"\nprofiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	before, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, Backend: "claude-code", ConnectionRef: "root", Model: rootOld, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}, ContextWindow: provider.Context1M, Effort: provider.EffortHigh, AllowedTools: []string{"Read"}})
	require.NoError(t, err)
	require.Equal(t, provider.Context1M, before.ContextWindow)
	old, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
	v, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	leaf.mu.Lock()
	leaf.source = []string{rootOld, bad}
	leaf.mu.Unlock()
	rootTick(t, ticks)
	rootReports(t, ds, len(v.Reports)+1)
	after, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, before, after)
	leaf.mu.Lock()
	leaf.source = []string{rootOld, bad, good}
	leaf.mu.Unlock()
	call := mcpToolCaller(t, ctx, ds)
	res, _ := call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	after, err = ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	expected := before.Clone()
	expected.Model = good
	expected.Revision++
	assert.Equal(t, expected, after)
	var count int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&count))
	assert.Equal(t, 1, count)
	lifecycleExecute(t, ds, ctx, good)
	prior, err := ds.instanceRepo.Get(ctx, old)
	require.NoError(t, err)
	assert.Equal(t, rootOld, prior.ModelPins["worker"])
}

func TestNextTickPublicationRecoveryRoot(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint(direct), func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			receiver := newAnnouncementReceiver(t)
			ds, ticks, intervals := buildLifecycleRootWithDependencies(t, strings.Split(lifecycleFollowing, "  - alias: other")[0], leaf, ModelDependencies{AnnouncementTransport: receiver.transport})
			ctx := lifecycleConnection(t, ds)
			setAnnouncementDestination(t, ctx, ds, "https://teams.example.test/publication?token=private-setting")
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			old, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
			require.NoError(t, err)
			require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, old).State)
			_, err = ds.db.Exec(`CREATE TRIGGER lifecycle_ack_fault BEFORE INSERT ON model_publication_acks BEGIN SELECT RAISE(ABORT,'ack unavailable'); END`)
			require.NoError(t, err)
			leaf.mu.Lock()
			leaf.source = []string{rootOld, rootNew}
			leaf.mu.Unlock()
			call := mcpToolCaller(t, ctx, ds)
			if direct {
				p, err := ds.profileSvc.Get(ctx, "worker")
				require.NoError(t, err)
				snap, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				_, err = ds.profileSvc.AdvanceModel(ctx, provider.AdvanceRequest{ID: "lifecycle-direct", Expected: p, Model: rootNew, CanonicalID: rootNew, PathRevision: snap.Revision, ExecutionRevision: snap.ExecutionRevision})
				require.ErrorIs(t, err, provider.ErrPublicationPending)
			} else {
				res, _ := call("model_catalog_refresh", nil)
				require.False(t, res.IsError)
				assertPublicationPending(t, res)
			}
			original, err := ds.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			require.Len(t, original, 1)
			leaf.mu.Lock()
			leaf.down = true
			leaf.mu.Unlock()
			res, _ := call("model_catalog_refresh", nil)
			require.False(t, res.IsError)
			assertPublicationPending(t, res)
			pending, err := ds.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			assert.Equal(t, original, pending)
			_, err = ds.db.Exec(`DROP TRIGGER lifecycle_ack_fault`)
			require.NoError(t, err)
			rootTick(t, ticks)
			require.Eventually(t, func() bool { p, e := ds.catalogStore.Unpublished(ctx); return e == nil && len(p) == 0 }, 2*time.Second, time.Millisecond)
			require.Eventually(t, func() bool {
				return ds.modelRefresh.coordinator.Do(ctx, func(context.Context) error { return nil }) == nil
			}, 2*time.Second, time.Millisecond)
			var count int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&count))
			assert.Equal(t, 1, count)
			var id string
			require.NoError(t, ds.db.QueryRow(`SELECT id FROM profile_advancement_journal`).Scan(&id))
			assert.Equal(t, original[0].ID, id)
			if !direct {
				cards := receiver.cards(rootNew)
				require.Len(t, cards, 1, "publication recovery drains the original event despite failed discovery")
				assert.Contains(t, cards[0], rootOld+" → "+rootNew)
				payload := assertAnnouncementEvent(t, ds, rootNew, "announced", 1)
				assert.Equal(t, original[0].ID, payload.Changes[0].ID)
			}
			lifecycleExecute(t, ds, ctx, rootNew)
			prior, err := ds.instanceRepo.Get(ctx, old)
			require.NoError(t, err)
			assert.Equal(t, rootOld, prior.ModelPins["worker"])
			res, _ = call("model_catalog_list", nil)
			require.False(t, res.IsError)
			text := res.Content[0].(gomcp.TextContent).Text
			assert.Contains(t, text, rootOld)
		})
	}
}
