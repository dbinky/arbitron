package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Persist the publication first, then cancel at the last pre-acknowledgement
// boundary. All publication arguments and side effects remain real.
type seedCancelPublication struct {
	workflow.RevisionPublicationStore
	cancel context.CancelFunc
}

func (p seedCancelPublication) CommitPublication(ctx context.Context, name string, pub workflow.Publication) error {
	if err := p.RevisionPublicationStore.CommitPublication(ctx, name, pub); err != nil {
		return err
	}
	p.cancel()
	return nil
}

type seedCompletionJournal struct {
	workflow.RevisionJournal
	afterReceipt func(context.Context, string)
}

func (j seedCompletionJournal) MarkCommitted(ctx context.Context, revision string) error {
	if err := j.RevisionJournal.MarkCommitted(ctx, revision); err != nil {
		return err
	}
	j.afterReceipt(ctx, revision)
	return nil
}

type seedCompletionLedger struct {
	samples.Ledger
	beforeRecord func(context.Context)
}

func (l seedCompletionLedger) Record(ctx context.Context, name string) error {
	l.beforeRecord(ctx)
	return l.Ledger.Record(ctx, name)
}

// Dropping WithoutCancel in Seeder.recordSeed must lose the receipt/row, while
// replacing the owned context with Background must lose reentrant lock access.
func TestSeedSampleWorkflows_PostPublicationCancellationCompletes(t *testing.T) {
	e := durableSeedEnv(t)
	ds, closeRoot := e.boot(t)
	ds.layout.worker.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	targets := []workflow.MutationTarget{{Scope: workflow.Shared(), Name: durableSeedName}}
	checkOwned := func(completion context.Context) {
		t.Helper()
		require.NoError(t, completion.Err())
		deadline, ok := completion.Deadline()
		require.True(t, ok, "detached completion must still have a timeout")
		require.WithinDuration(t, time.Now().Add(30*time.Second), deadline, time.Second)
		require.NoError(t, ds.layout.mutations.WithMutations(completion, targets, func(context.Context) error { return nil }), "detachment must preserve reentrant lock ownership")
		peerCtx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer stop()
		err := filesystem.NewWorkflowRepo(e.workspaceDir).WithMutations(peerCtx, targets, func(context.Context) error {
			t.Error("peer entered the destination before seed acknowledgement finished")
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	var receipt workflow.JournalEntry
	journal := seedCompletionJournal{ds.layout.journal, func(completion context.Context, revision string) {
		checkOwned(completion)
		entries, err := ds.layout.journal.Entries(completion)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		receipt = entries[0]
		require.Equal(t, workflow.JournalCommitted, receipt.State)
		require.Equal(t, revision, receipt.Request.Revision)
	}}
	ledger := seedCompletionLedger{filesystem.NewSeedLedger(e.workspaceDir), func(completion context.Context) {
		row, err := ds.layout.store.Get(context.Background(), receipt.Request.Revision)
		require.NoError(t, err, "durable enqueue must precede ledger append")
		require.Equal(t, receipt.Request.Manifest, row.Manifest)
		checkOwned(completion)
	}}
	stamper, err := workflow.NewRevisionStamper(ds.workflowRepo, workflow.StamperDeps{Journal: journal, Requester: ds.layout.requester, Displayed: ds.layout.store, Settler: ds.layout.commits})
	require.NoError(t, err)
	seeder := samples.NewSeeder(filesystem.NewSampleSource(e.samplesDir), ds.workflowRepo, seedCancelPublication{ds.publishedRepo, cancel}, ledger, localDefaultUser, layoutE2EDiscardLogger())
	seeder.SetRevisionStamper(stamper)
	seeder.SetMutationCoordinator(ds.layout.mutations)
	seeder.SetRegistry(adaptsqlite.NewWorkflowIdentityRepo(ds.db))
	require.NoError(t, seeder.Seed(ctx))
	require.ErrorIs(t, ctx.Err(), context.Canceled)

	req := receipt.Request
	wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
	var captured workflow.Workflow
	require.NoError(t, json.Unmarshal(req.Manifest, &captured))
	require.Equal(t, *wf, captured)
	row, err := ds.layout.store.Get(context.Background(), req.Revision)
	require.NoError(t, err)
	require.Equal(t, layout.Destination{Scope: workflow.Shared(), Name: durableSeedName, WorkflowID: wf.ID}, row.Destination)
	require.Equal(t, workflow.SaveReasonSeed, row.Reason)
	require.Equal(t, wf.Presentation.SavedAt, row.RequestedAt)
	require.Equal(t, wf.Presentation.Retained, row.Retained)
	require.Equal(t, layout.StatePending, row.State)
	proof, present, err := ds.publishedRepo.PublicationOf(context.Background(), durableSeedName)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, req.Revision, proof.Revision)
	require.Equal(t, wf.ID, proof.WorkflowID)
	claim, err := adaptsqlite.NewWorkflowIdentityRepo(ds.db).Resolve(context.Background(), durableSeedName)
	require.NoError(t, err)
	require.Equal(t, wf.ID, claim.ID)
	entries, err := ds.layout.journal.Entries(context.Background())
	require.NoError(t, err)
	require.Empty(t, entries, "only the durable enqueue acknowledgement cleans the receipt")
	closeRoot()
	// The next boot must see the once-only ledger, preserving the exact saved
	// revision even when the bundled source has become unusable.
	require.NoError(t, os.WriteFile(filepath.Join(e.samplesDir, durableSeedName, "workflow.json"), []byte("malformed"), 0600))
	ds, _ = e.boot(t)
	require.NoError(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo))
	ledgerBytes, err := os.ReadFile(filepath.Join(e.workspaceDir, ".samples_seeded"))
	require.NoError(t, err)
	require.Equal(t, durableSeedName+"\n", string(ledgerBytes))
	require.Equal(t, req.Revision, layoutE2ESavedRevision(t, e.sharedPath(durableSeedName)))
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
}

type seedPeerRelocationJournal struct {
	workflow.RevisionJournal
	barrier layoutCrashBarrier
}

func (j seedPeerRelocationJournal) BeginRelocation(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string, id uuid.UUID) (workflow.RelocationIntent, error) {
	intent, err := j.RevisionJournal.BeginRelocation(ctx, from, fromName, to, toName, id)
	if err == nil {
		j.barrier.pause()
	}
	return intent, err
}

// A real service rename holds both OS locks and leaves its real durable intent
// when killed. The parent has already completed its own boot settlement.
func TestSeedSampleWorkflows_RelocationProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_SEED_RELOCATION_CONFIG")
	if configPath == "" {
		return
	}
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	ds.layout.worker.Stop()
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "seed-peer-source", layoutE2EWorkflow("seed-peer-source", "peer source")))
	require.NoError(t, ds.workflowSvc.Publish(ctx, "seed-peer-source", "seed-peer-published", workflow.VisibilityPublic, ""))
	journal := seedPeerRelocationJournal{ds.layout.journal, layoutCrashBarrier{root: filepath.Dir(configPath)}}
	stamper, err := workflow.NewRevisionStamper(ds.workflowRepo, workflow.StamperDeps{Journal: journal, Requester: ds.layout.requester, Displayed: ds.layout.store, Settler: ds.layout.commits})
	require.NoError(t, err)
	ds.workflowSvc.SetRevisionStamper(stamper)
	require.NoError(t, ds.workflowSvc.Rename(ctx, "seed-peer-published", durableSeedName))
	t.Fatal("rename did not reach its relocation barrier")
}

// Ignoring covering intents in CommitSource.Settle must create a fresh seed
// after peer death, making the pending/error and unchanged-sink assertions red.
func TestSeedSampleWorkflows_CoveringPeerRelocationRejectsClaim(t *testing.T) {
	e := durableSeedEnv(t)
	ds, _ := e.boot(t)
	ds.layout.worker.Stop()
	processCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	cmd := exec.CommandContext(processCtx, os.Args[0], "-test.run=^TestSeedSampleWorkflows_RelocationProcess$")
	cmd.Env = append(os.Environ(), "ARBITRON_SEED_RELOCATION_CONFIG="+e.configPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 10*time.Second, 10*time.Millisecond)
	ctx := context.Background()
	intents, err := ds.layout.journal.RelocationIntents(ctx)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	require.Equal(t, workflow.Shared(), intents[0].To)
	require.Equal(t, durableSeedName, intents[0].ToName)
	entries, err := ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	trees, err := ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(ctx)
	require.NoError(t, err)
	peerBytes, err := os.ReadFile(e.sharedPath("seed-peer-published"))
	require.NoError(t, err)
	registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
	claim, err := registry.Resolve(ctx, "seed-peer-published")
	require.NoError(t, err)
	proof, present, err := ds.publishedRepo.PublicationOf(ctx, "seed-peer-published")
	require.NoError(t, err)
	require.True(t, present)
	var rows int
	require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rows))
	seed := func(ctx context.Context) error {
		return seedSampleWorkflows(ctx, e.samplesDir, e.workspaceDir, ds.workflowRepo, ds.publishedRepo, ds.layout.stamper, ds.layout.mutations, registry, nil, nil, localDefaultUser, layoutE2EDiscardLogger())
	}
	assertUnchanged := func() {
		t.Helper()
		gotIntents, err := ds.layout.journal.RelocationIntents(ctx)
		require.NoError(t, err)
		require.Equal(t, intents, gotIntents)
		gotEntries, err := ds.layout.journal.Entries(ctx)
		require.NoError(t, err)
		require.Equal(t, entries, gotEntries)
		gotTrees, err := ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(ctx)
		require.NoError(t, err)
		require.Equal(t, trees, gotTrees)
		gotBytes, err := os.ReadFile(e.sharedPath("seed-peer-published"))
		require.NoError(t, err)
		require.Equal(t, peerBytes, gotBytes)
		gotClaim, err := registry.Resolve(ctx, "seed-peer-published")
		require.NoError(t, err)
		require.Equal(t, claim, gotClaim)
		gotProof, present, err := ds.publishedRepo.PublicationOf(ctx, "seed-peer-published")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, proof, gotProof)
		_, err = registry.Resolve(ctx, durableSeedName)
		require.ErrorIs(t, err, workflow.ErrNotFound)
		require.NoDirExists(t, filepath.Dir(e.sharedPath(durableSeedName)))
		_, present, err = ds.publishedRepo.PublicationOf(ctx, durableSeedName)
		require.NoError(t, err)
		require.False(t, present)
		require.NoFileExists(t, filepath.Join(e.workspaceDir, ".samples_seeded"))
		var gotRows int
		require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&gotRows))
		require.Equal(t, rows, gotRows)
		require.Zero(t, layoutE2ECountRows(t, ds.db, durableSeedName))
	}
	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, seed(waitCtx), context.DeadlineExceeded, "a live peer's OS lock bounds seed waiting")
	assertUnchanged()
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	// Use the surviving process, without a global recovery sweep that could
	// erase the covering intent before EnsureStamped encounters it.
	require.ErrorIs(t, seed(ctx), workflow.ErrMutationPending)
	assertUnchanged()
}
