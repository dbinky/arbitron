package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Losing the quarantine write, skipping later rows, or acknowledging the poison
// report before publication must fail this regression.
func TestCatalogRecoveryQuarantinesMalformedReportWithoutLosingIntent(t *testing.T) {
	for _, raw := range []string{"{", "", "null", "{}", `{"id":"another-report","at":"2026-10-01T00:00:00Z"}`} {
		t.Run(raw, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			cycle := catalogCycle(t, db)
			first, err := repo.CommitCycle(ctx, cycle)
			require.NoError(t, err)
			var original string
			require.NoError(t, db.DB.QueryRow(`SELECT report FROM model_refresh_reports WHERE id=?`, first.ID).Scan(&original))
			cycle.Report.ID = "healthy-later-report"
			cycle.Advances = nil
			later, err := repo.CommitCycle(ctx, cycle)
			require.NoError(t, err)
			_, err = db.DB.Exec(`UPDATE model_refresh_reports SET report=? WHERE id=?`, raw, first.ID)
			require.NoError(t, err)
			for range 2 {
				reports, err := repo.Recover(ctx)
				require.NoError(t, err, "one malformed report must not starve later publication")
				require.Equal(t, []modelcatalog.RefreshReport{later}, reports)
				view, err := repo.Snapshot(ctx)
				require.NoError(t, err, "quarantine must also unblock discovery's snapshot")
				require.Len(t, view.Reports, 1)
				pending, err := repo.Unpublished(ctx)
				require.NoError(t, err)
				assert.Equal(t, first.Changes, pending, "journal intent stays pending until cache publication")
			}
			var retained string
			var pending bool
			require.NoError(t, db.DB.QueryRow(`SELECT report,publication_pending FROM model_refresh_reports WHERE id=?`, first.ID).Scan(&retained, &pending))
			assert.Equal(t, raw, retained)
			assert.True(t, pending)
			var quarantines int
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_refresh_report_quarantine WHERE report_id=? AND report=?`, first.ID, raw).Scan(&quarantines))
			assert.Equal(t, 1, quarantines)
			// Retry the original row after repair; quarantine never hides its new bytes.
			_, err = db.DB.Exec(`UPDATE model_refresh_reports SET report=? WHERE id=?`, original, first.ID)
			require.NoError(t, err)
			reports, err := repo.Recover(ctx)
			require.NoError(t, err)
			assert.Equal(t, []modelcatalog.RefreshReport{first, later}, reports)
			require.NoError(t, repo.MarkPublished(ctx, first.ID))
			pendingChanges, err := repo.Unpublished(ctx)
			require.NoError(t, err)
			assert.Empty(t, pendingChanges)
		})
	}
}

func TestCatalogRecoveryQuarantineWriteFaultIsRetried(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	cycle := catalogCycle(t, db)
	first, err := repo.CommitCycle(ctx, cycle)
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE model_refresh_reports SET report='{' WHERE id=?`, first.ID)
	require.NoError(t, err)
	// Missing durable isolation must remain an error, with no publication ack.
	_, err = db.DB.Exec(`CREATE TRIGGER quarantine_fault BEFORE INSERT ON model_refresh_report_quarantine BEGIN SELECT RAISE(ABORT,'quarantine unavailable'); END`)
	require.NoError(t, err)
	_, err = repo.Recover(ctx)
	require.Error(t, err)
	pending, err := repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, first.Changes, pending)
	var count int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_refresh_report_quarantine`).Scan(&count))
	assert.Zero(t, count)
	_, err = db.DB.Exec(`DROP TRIGGER quarantine_fault`)
	require.NoError(t, err)
	reports, err := repo.Recover(ctx)
	require.NoError(t, err)
	assert.Empty(t, reports)
	_, err = repo.Snapshot(ctx)
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = repo.Recover(cancelled)
	require.ErrorIs(t, err, context.Canceled)
}

// Without relational validation Recover publishes fabricated report changes and
// MarkPublished can acknowledge an unrelated journal row or lose an omitted one.
func TestCatalogRecoveryQuarantinesChangesThatDisagreeWithJournal(t *testing.T) {
	for _, fault := range []string{"empty ID", "duplicate ID", "foreign ID", "missing ID", "omitted", "null", "alias", "from model", "to model", "canonical", "revision", "refresh ID", "time"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			cycle := catalogCycle(t, db)
			first, err := repo.CommitCycle(ctx, cycle)
			require.NoError(t, err)
			profiles := NewProfileRepo(db.DB)
			require.NoError(t, profiles.Create(ctx, followingProfile("independent")))
			independent, err := profiles.Get(ctx, "independent")
			require.NoError(t, err)
			foreign, err := profiles.CommitAdvance(ctx, advanceReq("foreign", independent, "vendor/version-y"))
			require.NoError(t, err)
			cycle.Report.ID = "healthy-later-report"
			cycle.Advances = nil
			later, err := repo.CommitCycle(ctx, cycle)
			require.NoError(t, err)
			damaged := first
			damaged.Changes = append([]provider.Advancement(nil), first.Changes...)
			switch fault {
			case "empty ID":
				damaged.Changes = []provider.Advancement{{}}
			case "duplicate ID":
				damaged.Changes = append(damaged.Changes, damaged.Changes[0])
			case "foreign ID":
				damaged.Changes = []provider.Advancement{foreign}
			case "missing ID":
				damaged.Changes[0].ID = "missing"
			case "omitted":
				damaged.Changes = []provider.Advancement{}
			case "null":
				damaged.Changes = nil
			case "alias":
				damaged.Changes[0].Alias = "independent"
			case "from model":
				damaged.Changes[0].FromModel = "wrong"
			case "to model":
				damaged.Changes[0].ToModel = "wrong"
			case "canonical":
				damaged.Changes[0].CanonicalID = "wrong"
			case "revision":
				damaged.Changes[0].Revision++
			case "refresh ID":
				damaged.Changes[0].RefreshID = "wrong"
			case "time":
				damaged.Changes[0].At = damaged.Changes[0].At.Add(time.Second)
			}
			raw, err := json.Marshal(damaged)
			require.NoError(t, err)
			_, err = db.DB.Exec(`UPDATE model_refresh_reports SET report=? WHERE id=?`, string(raw), first.ID)
			require.NoError(t, err)
			require.Error(t, repo.MarkPublished(ctx, first.ID), "acknowledgement must also refuse corrupt intent")
			for range 2 {
				reports, err := repo.Recover(ctx)
				require.NoError(t, err)
				require.Equal(t, []modelcatalog.RefreshReport{later}, reports, "only the damaged report is isolated")
				pending, err := repo.Unpublished(ctx)
				require.NoError(t, err)
				assert.Equal(t, append(append([]provider.Advancement(nil), first.Changes...), foreign), pending)
				view, err := repo.Snapshot(ctx)
				require.NoError(t, err)
				require.Len(t, view.Reports, 1)
			}
			var retained string
			var stillPending bool
			require.NoError(t, db.DB.QueryRow(`SELECT report,publication_pending FROM model_refresh_reports WHERE id=?`, first.ID).Scan(&retained, &stillPending))
			assert.Equal(t, string(raw), retained)
			assert.True(t, stillPending)
			// Repair retries normally without another model mutation or journal insert.
			original, err := json.Marshal(first)
			require.NoError(t, err)
			_, err = db.DB.Exec(`UPDATE model_refresh_reports SET report=? WHERE id=?`, string(original), first.ID)
			require.NoError(t, err)
			reports, err := repo.Recover(ctx)
			require.NoError(t, err)
			assert.Equal(t, []modelcatalog.RefreshReport{first, later}, reports)
			require.NoError(t, repo.MarkPublished(ctx, first.ID))
			pending, err := repo.Unpublished(ctx)
			require.NoError(t, err)
			assert.Equal(t, []provider.Advancement{foreign}, pending, "foreign intent was never acknowledged by this report")
			assert.Equal(t, 1, journalCount(t, db, "worker"))
		})
	}
}
