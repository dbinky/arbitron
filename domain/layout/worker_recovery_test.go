package layout_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWorkerRecoveryOnlyProvenAndPartialErrors(t *testing.T) {
	r := newWorkerRig(t, 5)
	prepared := r.input(t)
	committed := r.input(t)
	abandoned := r.input(t)
	for _, req := range []workflow.ArrangementRequest{prepared, committed, abandoned} {
		require.NoError(t, r.journal.Record(context.Background(), req))
		r.committed.Prepare(req)
	}
	r.committed.Commit(committed.Revision)
	r.committed.Abandon(abandoned.Revision)
	r.committed.SetError(errors.New("one destination offline"))
	require.ErrorContains(t, r.worker.Recover(context.Background()), "one destination offline")
	row := r.row(t, committed.Revision)
	require.Equal(t, 5, row.AttemptLimit)
	require.Equal(t, committed.Manifest, row.Manifest)
	require.False(t, r.journal.Has(committed.Revision))
	require.True(t, r.journal.Has(prepared.Revision))
	for _, req := range []workflow.ArrangementRequest{prepared, abandoned} {
		_, err := r.store.Get(context.Background(), req.Revision)
		require.ErrorIs(t, err, layout.ErrRecordNotFound)
	}
	r.committed.SetError(nil)
	require.NoError(t, r.worker.Recover(context.Background()))
	require.Equal(t, int64(1), r.row(t, committed.Revision).JobGeneration)
	// Neither a newer mutable workflow nor deletion cancels committed history.
	require.NoError(t, r.worker.Drain(context.Background()))
	row = r.row(t, committed.Revision)
	require.Equal(t, layout.StatePassed, row.State)
	require.Empty(t, r.cache.Published(row.Destination))
	require.True(t, r.journal.Has(prepared.Revision))
}
func TestWorkerRecoveryLiveStoreOutage(t *testing.T) {
	r := newWorkerRig(t, 1, func(c *layout.WorkerConfig) { c.PollInterval = 5 * time.Millisecond })
	req := r.input(t)
	r.committed.Prepare(req)
	r.committed.Commit(req.Revision)
	require.NoError(t, r.journal.Record(context.Background(), req))
	require.NoError(t, r.journal.MarkCommitted(context.Background(), req.Revision))
	r.store.SetFailure(layout.ErrStoreUnavailable)
	require.ErrorIs(t, r.worker.Recover(context.Background()), layout.ErrStoreUnavailable)
	require.True(t, r.journal.Has(req.Revision))
	r.worker.Start(context.Background())
	defer r.worker.Stop()
	time.Sleep(15 * time.Millisecond)
	r.store.SetFailure(nil)
	eventually(t, func() bool {
		row, err := r.store.Get(context.Background(), req.Revision)
		return err == nil && row.State == layout.StatePassed
	})
	require.False(t, r.journal.Has(req.Revision))
	result, err := r.store.Result(context.Background(), req.Revision, 1)
	require.NoError(t, err)
	require.Len(t, result.Geometry.Nodes, 4)
	require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
}

type removeFailureJournal struct {
	workflow.RevisionJournal
	err error
}

func (j removeFailureJournal) Remove(ctx context.Context, rev string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return j.err
}
func TestWorkerRecoveryRemoveFailureKeepsEvidence(t *testing.T) {
	r := newWorkerRig(t, 1)
	req := r.input(t)
	r.committed.Prepare(req)
	r.committed.Commit(req.Revision)
	require.NoError(t, r.journal.Record(context.Background(), req))
	cfg := r.cfg
	cfg.Journal = removeFailureJournal{r.journal, errors.New("journal disk error")}
	w, err := layout.NewWorker(cfg)
	require.NoError(t, err)
	require.ErrorContains(t, w.Recover(context.Background()), "journal disk error")
	require.True(t, r.journal.Has(req.Revision))
	require.Equal(t, req.Manifest, r.row(t, req.Revision).Manifest)
	require.NoError(t, r.worker.Recover(context.Background()))
	require.False(t, r.journal.Has(req.Revision))
	require.Equal(t, int64(1), r.row(t, req.Revision).JobGeneration)
}

// This is the real saved-input/commit-proof/store/cache seam. Production root
// construction is separately owned by P2T9; render envelopes here are unit-only.
func TestWorkerRecoveryRealCommitSourceRejectsPhantom(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	journal := filesystem.NewRevisionJournal(base)
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "layout.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, adaptsqlite.RunMigrations(db))
	store := adaptsqlite.NewLayoutRepo(db)
	pubs := adaptsqlite.NewPublishedWorkflowRepo(db)
	registry := adaptsqlite.NewWorkflowIdentityRepo(db)
	requester := layout.NewRequester(store, 5, nil)
	source, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Publications: pubs, Registry: registry, Relocator: requester})
	require.NoError(t, err)
	repo.SetReadSettler(source)
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: source})
	require.NoError(t, err)
	var saved, phantom workflow.ArrangementRequest
	for _, name := range []string{"committed", "phantom"} {
		require.NoError(t, repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: workflow.Shared(), Name: name}}, func(owned context.Context) error {
			wf := layouttest.SampleWorkflow()
			wf.Name = name
			wf.ID = uuid.New()
			req, err := stamper.Stamp(owned, workflow.Shared(), name, wf, nil, workflow.SaveReasonCreate)
			if err != nil {
				return err
			}
			_, err = repo.PrepareTreeCommit(owned, workflow.MutationTarget{Scope: workflow.Shared(), Name: name}, req.Revision)
			if err != nil {
				return err
			}
			if err = repo.Create(owned, workflow.Shared(), name, wf); err != nil {
				return err
			}
			if name == "phantom" {
				phantom = req
				return nil
			}
			saved = req
			return pubs.CommitPublication(owned, name, workflow.Publication{WorkflowID: wf.ID, Revision: req.Revision, Publisher: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Visibility: workflow.VisibilityPublic})
		}))
	}
	renderer := layouttest.NewFakeRenderer()
	co, err := layout.NewCoordinator(renderer, layouttest.NewFakeReviewer())
	require.NoError(t, err)
	cache := filesystem.NewLayoutCache(repo)
	worker, err := layout.NewWorker(layout.WorkerConfig{Store: store, Coordinator: co, Journal: journal, Committed: source, Manifests: layout.NewRepositoryManifests(repo), Cache: cache, Requester: requester})
	require.NoError(t, err)
	defer worker.Stop()
	require.NoError(t, worker.Recover(ctx))
	require.NoError(t, worker.Drain(ctx))
	row, err := store.Get(ctx, saved.Revision)
	require.NoError(t, err)
	require.Equal(t, layout.StatePassed, row.State)
	require.Equal(t, 5, row.AttemptLimit)
	require.Equal(t, saved.Manifest, row.Manifest)
	require.Equal(t, int64(1), row.CacheGeneration)
	result, err := store.Result(ctx, saved.Revision, row.ResultGeneration)
	require.NoError(t, err)
	graph, err := layout.BuildGraph(layouttest.SampleWorkflow())
	require.NoError(t, err)
	require.NoError(t, result.Geometry.Validate(graph))
	require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
	entry, ok, err := cache.ReadCache(ctx, saved.Scope, saved.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(entry.Geometry))
	require.Equal(t, row.JobGeneration, entry.JobGeneration)
	_, err = store.Get(ctx, phantom.Revision)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)
	exists, err := repo.Exists(ctx, phantom.Scope, phantom.Name)
	require.NoError(t, err)
	require.False(t, exists, "unrecorded publish must be rolled back, not arranged")
}

// Recovery removes journal input only after the store acknowledges those exact
// bytes; a conflicting capture for an existing revision keeps its evidence.
func TestWorkerRecoveryConflictingInputKeepsJournal(t *testing.T) {
	r := newWorkerRig(t, 5)
	req := r.request(t)
	wf := layouttest.SampleWorkflow()
	wf.Presentation = &workflow.Presentation{Revision: req.Revision}
	wf.Nodes[0].Label = "different capture"
	conflicting := req
	conflicting.Manifest = layouttest.ManifestJSON(t, wf)
	require.NoError(t, r.journal.Record(context.Background(), conflicting))
	r.committed.Prepare(conflicting)
	r.committed.Commit(conflicting.Revision)
	require.ErrorIs(t, r.worker.Recover(context.Background()), layout.ErrConflictingArrangementInput)
	require.True(t, r.journal.Has(req.Revision))
	require.Equal(t, req.Manifest, r.row(t, req.Revision).Manifest)
}

func TestWorkerRecoveryConflictingLargeIntegerKeepsJournal(t *testing.T) {
	r := newWorkerRig(t, 5)
	req := r.input(t)
	req.Manifest = withMaxTokens(t, req.Manifest, 9007199254740992)
	require.NoError(t, r.requester.RequestArrangement(context.Background(), req))
	conflicting := req
	conflicting.Manifest = withMaxTokens(t, req.Manifest, 9007199254740993)
	require.NoError(t, r.journal.Record(context.Background(), conflicting))
	r.committed.Prepare(conflicting)
	r.committed.Commit(conflicting.Revision)
	require.ErrorIs(t, r.worker.Recover(context.Background()), layout.ErrConflictingArrangementInput)
	require.True(t, r.journal.Has(req.Revision))
	require.Equal(t, req.Manifest, r.row(t, req.Revision).Manifest)
}
