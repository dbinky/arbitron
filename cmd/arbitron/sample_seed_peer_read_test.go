package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A surviving process serves settling reads of a sample another process is
// seeding. While the seeder lives it holds the destination lock, so neither the
// workflow load nor the raw read may decide its prepared entry: the claim, the
// journal input and any half-written tree stay exactly as the seeder left them.
// Once it dies the blocked raw read settles that entry itself: an unproven seed
// releases exactly its own claim and serves nothing; a proven one rolls forward.
func TestSeedSampleWorkflows_PeerSettlingReadWaitsForLiveSeedClaim(t *testing.T) {
	pinLayoutE2EEnv(t)
	for _, stage := range []string{"identity", "partial-tree", "before-proof", "after-proof"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			e := durableSeedEnv(t)
			ds, _ := e.boot(t)
			ds.layout.worker.Stop()
			ctx := context.Background()
			registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
			bystander := workflow.RegistryEntry{ID: uuid.New(), Name: "bystander-sample", Scope: workflow.ScopeShared}
			require.NoError(t, registry.Register(ctx, bystander))

			peer := startSeedAt(t, e, stage)
			req := capturedSeedRequest(t, e)
			claim := workflow.RegistryEntry{ID: req.WorkflowID, Name: req.Name, Scope: workflow.ScopeShared}
			livePrepared := []workflow.JournalEntry{{Request: req, State: workflow.JournalPrepared}}
			manifestPath := e.sharedPath(durableSeedName)
			var liveTree []byte
			if stage != "identity" {
				var err error
				liveTree, err = os.ReadFile(manifestPath)
				require.NoError(t, err)
			}

			// Live seeder: the settling load must time out on the held lock, never
			// treat the live prepare as abandoned or adopt its unproven tree.
			waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			_, err := ds.workflowRepo.Load(waitCtx, workflow.Shared(), durableSeedName)
			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded)
			type rawRead struct {
				data []byte
				err  error
			}
			done := make(chan rawRead, 1)
			go func() {
				data, err := ds.fileReader.ReadFile(uuid.Nil, durableSeedName, "workflow.json")
				done <- rawRead{data, err}
			}()
			select {
			case got := <-done:
				t.Fatalf("raw read returned while the seeder held its lock: %q, %v", got.data, got.err)
			case <-time.After(300 * time.Millisecond):
			}
			got, err := registry.Resolve(ctx, durableSeedName)
			require.NoError(t, err)
			require.Equal(t, claim, got, "a live seed keeps its claim")
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Equal(t, livePrepared, entries, "a live seed keeps its prepared input")
			if liveTree != nil {
				after, err := os.ReadFile(manifestPath)
				require.NoError(t, err)
				require.Equal(t, liveTree, after, "a live seed's tree is not settled under it")
			}

			// Killed seeder: the flock is released and the waiting read decides.
			require.NoError(t, peer.Process.Kill())
			require.Error(t, peer.Wait())
			var read rawRead
			select {
			case read = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("raw read did not settle after the seeder died")
			}
			bystanderAfter, err := registry.Resolve(ctx, bystander.Name)
			require.NoError(t, err)
			require.Equal(t, bystander, bystanderAfter, "settlement releases nothing but the abandoned claim")
			entries, err = ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			if stage == "after-proof" {
				require.NoError(t, read.err)
				var wf workflow.Workflow
				require.NoError(t, json.Unmarshal(read.data, &wf))
				require.Equal(t, req.Revision, wf.SavedRevision())
				require.Equal(t, req.WorkflowID, wf.ID)
				got, err = registry.Resolve(ctx, durableSeedName)
				require.NoError(t, err)
				require.Equal(t, claim, got, "a proven seed keeps its claim")
				require.Equal(t, []workflow.JournalEntry{{Request: req, State: workflow.JournalCommitted}}, entries)
				return
			}
			require.Error(t, read.err)
			require.True(t, errors.Is(read.err, os.ErrNotExist), "unproven seed bytes are never served: %v", read.err)
			require.Nil(t, read.data)
			_, err = registry.Resolve(ctx, durableSeedName)
			require.ErrorIs(t, err, workflow.ErrNotFound, "the abandoned claim is released")
			require.Empty(t, entries, "the abandoned input is quarantined")
			require.NoDirExists(t, filepath.Dir(manifestPath))
			_, present, err := ds.publishedRepo.PublicationOf(ctx, durableSeedName)
			require.NoError(t, err)
			require.False(t, present)
		})
	}
}
