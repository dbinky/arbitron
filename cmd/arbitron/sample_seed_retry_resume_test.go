package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/stretchr/testify/require"
)

// Paired row 6: an authorised retry between a failed seed ledger append and the
// next boot must leave the row an exact acknowledgement of the saved input, or
// exact seed resume rejects it and the ledger is never completed.
func TestSeedSampleWorkflows_RetryBeforeResumeKeepsExactAcknowledgement(t *testing.T) {
	for _, row := range []string{"present", "missing"} {
		t.Run(row, func(t *testing.T) {
			e := durableSeedEnv(t)
			ds, _ := e.boot(t)
			ds.layout.worker.Stop()
			ctx := context.Background()
			ledger := filesystem.NewSeedLedger(e.workspaceDir)
			boom := errors.New("ledger append unavailable")
			seeder := samples.NewSeeder(filesystem.NewSampleSource(e.samplesDir), ds.workflowRepo, ds.publishedRepo,
				seedLedgerOutage{ledger, boom}, localDefaultUser, layoutE2EDiscardLogger())
			seeder.SetRevisionStamper(ds.layout.stamper)
			seeder.SetMutationCoordinator(ds.layout.mutations)
			seeder.SetRegistry(adaptsqlite.NewWorkflowIdentityRepo(ds.db))
			require.ErrorIs(t, seeder.Seed(ctx), boom)
			_, err := ds.layout.commits.CommittedRequests(ctx)
			require.NoError(t, err)
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries, "resume must depend on the acknowledged row")
			wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
			rev := wf.SavedRevision()
			original, err := ds.layout.store.Get(ctx, rev)
			require.NoError(t, err)
			require.True(t, original.RequestedAt.Equal(wf.Presentation.SavedAt))
			before, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)

			switch row {
			case "present":
				_, err = ds.db.Exec(`UPDATE layout_revisions SET state='failed' WHERE revision=?`, rev)
			case "missing":
				_, err = ds.db.Exec(`DELETE FROM layout_revisions WHERE revision=?`, rev)
			}
			require.NoError(t, err)
			admin := identity.WithIdentity(ctx, identity.Identity{UserID: localDefaultUser, Role: identity.RoleAdmin})
			ack, err := ds.layout.query.RetryWorkflow(admin, durableSeedName, rev)
			require.NoError(t, err)
			require.Equal(t, rev, ack.SavedRevision)

			retried, err := ds.layout.store.Get(ctx, rev)
			require.NoError(t, err)
			require.Equal(t, original.Destination, retried.Destination)
			require.Equal(t, original.Reason, retried.Reason)
			require.Equal(t, original.Retained, retried.Retained)
			require.JSONEq(t, string(original.Manifest), string(retried.Manifest))
			require.True(t, original.RequestedAt.Equal(retried.RequestedAt),
				"retry must keep the saved acknowledgement time %s, got %s", original.RequestedAt, retried.RequestedAt)

			require.NoError(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo))
			seeded, err := ledger.Seeded(ctx)
			require.NoError(t, err)
			require.True(t, seeded[durableSeedName], "exact resume must complete the ledger after a retry")
			after, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)
			require.Equal(t, before, after, "resume must not re-stamp or rewrite the saved tree")
			resumed, err := ds.layout.store.Get(ctx, rev)
			require.NoError(t, err)
			require.True(t, original.RequestedAt.Equal(resumed.RequestedAt))
			require.Equal(t, retried.JobGeneration, resumed.JobGeneration, "resume must not bump or reset the retried generation")
			require.Equal(t, layout.StatePending, resumed.State)
			require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
		})
	}
}
