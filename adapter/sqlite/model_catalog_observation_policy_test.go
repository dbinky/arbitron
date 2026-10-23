package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogObservationPolicyFaultDefersObservationAndKeepsDiagnostics(t *testing.T) {
	for _, fault := range []string{"missing_row", "storage_error"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := catalogCycle(t, db)
			c.Advances = nil
			c.DeferDiscovery = []string{"vendor/y"}
			_, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			before, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, before.Evidence)
			require.Empty(t, catalogEvents(t, db))
			_, err = db.DB.Exec(`CREATE TABLE saved_policy AS SELECT * FROM model_identity_policy`)
			require.NoError(t, err)
			if fault == "missing_row" {
				_, err = db.DB.Exec(`DELETE FROM model_identity_policy`)
			} else {
				_, err = db.DB.Exec(`ALTER TABLE model_identity_policy RENAME TO unavailable_policy`)
			}
			require.NoError(t, err)
			c.Report.ID = "kept-cycle"
			c.Report.PathStatus = map[string]string{"catalog": "parse_error"}
			c.PolicyRevision, c.DeferDiscovery, c.Evidence = 0, nil, nil
			report, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err, "unreadable policy must defer observations without discarding diagnostics")
			assert.Empty(t, report.NewlyUsable)
			assert.Empty(t, report.Changes)
			assert.Equal(t, "parse_error", report.PathStatus["catalog"])
			after, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, before.Catalog, after.Catalog)
			assert.Equal(t, before.Evidence, after.Evidence)
			assert.Equal(t, before.HistoricalEntries, after.HistoricalEntries)
			assert.Equal(t, before.UnmatchedTargets, after.UnmatchedTargets)
			assert.Equal(t, before.ProbeCursor, after.ProbeCursor)
			assert.Equal(t, append(before.Reports, report), after.Reports)
			assert.Empty(t, catalogEvents(t, db))
			var observations int
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_first_observations`).Scan(&observations))
			assert.Zero(t, observations)
			if fault == "missing_row" {
				_, err = db.DB.Exec(`INSERT INTO model_identity_policy SELECT * FROM saved_policy`)
			} else {
				_, err = db.DB.Exec(`ALTER TABLE unavailable_policy RENAME TO model_identity_policy`)
			}
			require.NoError(t, err)
			replayed, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			assert.Equal(t, report, replayed, "replay preserves the already committed diagnostic report")
			assert.Empty(t, catalogEvents(t, db))
			c.Report.ID = "recovered-cycle"
			c.Report.PathStatus = nil
			c.PolicyRevision = 1
			report, err = repo.CommitCycle(ctx, c)
			require.NoError(t, err, "the next healthy cycle reuses durable proof")
			require.Len(t, report.NewlyUsable, 1)
			assert.Equal(t, "premium", report.NewlyUsable[0].Entry.Tier)
			assert.Len(t, catalogEvents(t, db), 1)
			c.Report.ID = "dedup-cycle"
			report, err = repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			assert.Empty(t, report.NewlyUsable)
			assert.Len(t, catalogEvents(t, db), 1)
			stored, err := NewProfileRepo(db.DB).Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, "vendor/version-x", stored.Model)
			assert.Zero(t, journalCount(t, db, "worker"))
			assert.Equal(t, modelcatalog.SliceStatusOK, report.SliceStatus["vendor"])
		})
	}
}
