package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing durable row isolation or the root Journal feed must leave either
// discovery blocked or the original advancement unacknowledged in this test.
func TestRefreshPoisonReportRetriesPublicationAcrossCyclesAndReopen(t *testing.T) {
	for _, poison := range []struct{ name, sql string }{
		{"invalid JSON", `UPDATE model_refresh_reports SET report='{' WHERE id=?`},
		{"empty advancement", `UPDATE model_refresh_reports SET report=json_set(report,'$.Changes',json('[{}]')) WHERE id=?`},
		{"foreign advancement", `UPDATE model_refresh_reports SET report=json_set(report,'$.Changes[0].ID','foreign') WHERE id=?`},
		{"omitted advancements", `UPDATE model_refresh_reports SET report=json_set(report,'$.Changes',json('[]')) WHERE id=?`},
	} {
		t.Run(poison.name, func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			ds, _, intervals := buildLifecycleRoot(t, lifecycleFollowing, leaf)
			ctx := lifecycleConnection(t, ds)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			lifecycleExecute(t, ds, ctx, rootOld)
			leaf.mu.Lock()
			leaf.source = []string{rootOld, rootNew}
			leaf.mu.Unlock()
			_, err := ds.db.Exec(`CREATE TRIGGER poison_ack_fault BEFORE INSERT ON model_publication_acks BEGIN SELECT RAISE(ABORT,'ack unavailable'); END`)
			require.NoError(t, err)
			first, err := ds.modelRefresh.coordinator.Refresh(ctx)
			require.ErrorIs(t, err, provider.ErrPublicationPending)
			require.Len(t, first.Changes, 2)
			frozen := catalogFrozenPayloads(t, ds)
			_, err = ds.db.Exec(poison.sql, first.ID)
			require.NoError(t, err)
			var poisoned string
			require.NoError(t, ds.db.QueryRow(`SELECT report FROM model_refresh_reports WHERE id=?`, first.ID).Scan(&poisoned))
			_, err = ds.modelRefresh.coordinator.Refresh(ctx)
			require.ErrorIs(t, err, provider.ErrPublicationPending, "a quarantined report cannot conceal a journal ack fault")
			pending, err := ds.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			assert.Equal(t, first.Changes, pending)
			_, err = ds.db.Exec(`DROP TRIGGER poison_ack_fault`)
			require.NoError(t, err)
			for range 2 {
				leaf.mu.Lock()
				before := leaf.fetches
				leaf.mu.Unlock()
				report, err := ds.modelRefresh.coordinator.Refresh(ctx)
				require.NoError(t, err, "discovery must resume despite the damaged old report")
				assert.NotEmpty(t, report.ID)
				assert.Empty(t, report.Changes, "recovery must not repeat model CAS")
				leaf.mu.Lock()
				assert.Greater(t, leaf.fetches, before)
				leaf.mu.Unlock()
			}
			lifecycleExecute(t, ds, ctx, rootNew)
			configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
			deps := ModelDependencies{FetchTransport: ds.refreshDeps.FetchTransport, ProviderTransport: catalogHTTPLeaf(leaf.provider), Now: ds.refreshDeps.Clock}
			ds.Close(context.Background())
			reopened, err := initDomainWithModelDependencies(context.Background(), configPath, deps)
			require.NoError(t, err)
			defer reopened.Close(context.Background())
			require.NoError(t, reopened.Start(ctx))
			report, err := reopened.modelRefresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.Empty(t, report.Changes)
			pending, err = reopened.catalogStore.Unpublished(ctx)
			require.NoError(t, err)
			assert.Empty(t, pending, "root recovery must publish and acknowledge the original journal intent")
			var raw string
			var stillPending bool
			require.NoError(t, reopened.db.QueryRow(`SELECT report,publication_pending FROM model_refresh_reports WHERE id=?`, first.ID).Scan(&raw, &stillPending))
			assert.Equal(t, poisoned, raw, "retain the damaged row for repair")
			assert.True(t, stillPending, "do not pretend the unreadable report was published")
			var quarantines, journals int
			require.NoError(t, reopened.db.QueryRow(`SELECT COUNT(*) FROM model_refresh_report_quarantine WHERE report_id=? AND report=?`, first.ID, poisoned).Scan(&quarantines))
			assert.Equal(t, 1, quarantines)
			require.NoError(t, reopened.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journals))
			assert.Equal(t, 2, journals)
			assert.Equal(t, frozen, catalogFrozenPayloads(t, reopened), "quarantine cannot rewrite frozen discovery events")
			_, err = reopened.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			lifecycleExecute(t, reopened, ctx, rootNew)
			leaf.mu.Lock()
			assert.Equal(t, []string{rootOld, rootOld, rootNew, rootNew, rootNew, rootNew}, leaf.executed)
			leaf.mu.Unlock()

		})
	}
}
