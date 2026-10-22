package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Float64 comparison would adopt an edited tree as the original input because
// 2^53 and 2^53+1 round to the same value. Exercise both durable proof sources.
func TestSeedSampleWorkflows_ResumeRejectsRoundedInput(t *testing.T) {
	for _, proof := range []string{"acknowledged", "receipt"} {
		t.Run(proof, func(t *testing.T) {
			e := durableSeedEnv(t)
			ds, _ := e.boot(t)
			ds.layout.worker.Stop()
			ctx := context.Background()
			sourcePath := filepath.Join(e.samplesDir, durableSeedName, "workflow.json")
			source := layoutE2EManifest(t, sourcePath)
			source.Nodes[0].Config.MaxTokens = 9007199254740992
			raw, err := json.Marshal(source)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(sourcePath, raw, 0600))
			if proof == "receipt" {
				_, err := ds.db.Exec(`CREATE TRIGGER seed_enqueue_outage BEFORE INSERT ON layout_revisions BEGIN SELECT RAISE(ABORT, 'seed queue unavailable'); END`)
				require.NoError(t, err)
			}
			ledger := filesystem.NewSeedLedger(e.workspaceDir)
			boom := errors.New("ledger append unavailable")
			seeder := samples.NewSeeder(filesystem.NewSampleSource(e.samplesDir), ds.workflowRepo, ds.publishedRepo,
				seedLedgerOutage{ledger, boom}, localDefaultUser, layoutE2EDiscardLogger())
			seeder.SetRevisionStamper(ds.layout.stamper)
			seeder.SetMutationCoordinator(ds.layout.mutations)
			seeder.SetRegistry(adaptsqlite.NewWorkflowIdentityRepo(ds.db))
			require.ErrorIs(t, seeder.Seed(ctx), boom)
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			wantRows := 1
			if proof == "receipt" {
				wantRows = 0
				require.Len(t, entries, 1)
				require.Equal(t, workflow.JournalCommitted, entries[0].State)
				_, err = ds.db.Exec("DROP TRIGGER seed_enqueue_outage")
				require.NoError(t, err)
			} else {
				require.Empty(t, entries)
			}
			wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
			require.Equal(t, 9007199254740992, wf.Nodes[0].Config.MaxTokens)
			wf.Nodes[0].Config.MaxTokens = 9007199254740993
			raw, err = json.Marshal(wf)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(e.sharedPath(durableSeedName), raw, 0600))
			before, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)
			require.ErrorIs(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo), workflow.ErrMutationPending)
			after, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)
			require.Equal(t, before, after)
			seeded, err := ledger.Seeded(ctx)
			require.NoError(t, err)
			require.False(t, seeded[durableSeedName], "changed input cannot complete the seed acknowledgement")
			afterEntries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Equal(t, entries, afterEntries, "rejection must preserve durable evidence")
			require.Equal(t, wantRows, layoutE2ECountRows(t, ds.db, durableSeedName))
		})
	}
}

// Even a real acknowledged seed cannot repair its ledger using unrelated or
// missing publication proof. Exercise the production SQLite acknowledgement
// after journal cleanup, not a fake that merely returns the current manifest.
func TestSeedSampleWorkflows_ResumeRequiresExactPublication(t *testing.T) {
	for _, mismatch := range []string{"missing", "revision", "identity", "publisher", "source"} {
		t.Run(mismatch, func(t *testing.T) {
			e := durableSeedEnv(t)
			ds, _ := e.boot(t)
			ds.layout.worker.Stop()
			ctx := context.Background()
			ledger := filesystem.NewSeedLedger(e.workspaceDir)
			boom := errors.New("ledger append unavailable")
			registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
			seeder := samples.NewSeeder(filesystem.NewSampleSource(e.samplesDir), ds.workflowRepo, ds.publishedRepo,
				seedLedgerOutage{ledger, boom}, localDefaultUser, layoutE2EDiscardLogger())
			seeder.SetRevisionStamper(ds.layout.stamper)
			seeder.SetMutationCoordinator(ds.layout.mutations)
			seeder.SetRegistry(registry)
			require.ErrorIs(t, seeder.Seed(ctx), boom)
			_, err := ds.layout.commits.CommittedRequests(ctx)
			require.NoError(t, err)
			before, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)
			wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
			original, err := ds.layout.store.Get(ctx, wf.SavedRevision())
			require.NoError(t, err)
			require.NotEmpty(t, original.Manifest)
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries, "the input must come from the acknowledged row")
			proof, present, err := ds.publishedRepo.PublicationOf(ctx, durableSeedName)
			require.NoError(t, err)
			require.True(t, present)
			owner := localDefaultUser
			switch mismatch {
			case "missing":
				require.NoError(t, ds.publishedRepo.Delete(ctx, durableSeedName))
			case "revision":
				proof.Revision = uuid.Must(uuid.NewV7()).String()
			case "identity":
				proof.WorkflowID = uuid.New()
			case "publisher":
				owner = uuid.New()
			case "source":
				proof.SourceName = "unrelated-source"
			}
			if mismatch != "missing" {
				require.NoError(t, ds.publishedRepo.CommitPublication(ctx, durableSeedName, proof))
			}
			seeder = samples.NewSeeder(filesystem.NewSampleSource(e.samplesDir), ds.workflowRepo, ds.publishedRepo, ledger, owner, layoutE2EDiscardLogger())
			seeder.SetRevisionStamper(ds.layout.stamper)
			seeder.SetMutationCoordinator(ds.layout.mutations)
			seeder.SetRegistry(registry)
			require.ErrorIs(t, seeder.Seed(ctx), workflow.ErrMutationPending)
			after, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)
			require.Equal(t, before, after)
			seeded, err := ledger.Seeded(ctx)
			require.NoError(t, err)
			require.False(t, seeded[durableSeedName])
			recovered, err := ds.layout.store.Get(ctx, wf.SavedRevision())
			require.NoError(t, err)
			require.Equal(t, original, recovered, "a rejected ledger repair must not reset or requeue input")
			require.Equal(t, layout.StatePending, recovered.State)
			require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
		})
	}
}
