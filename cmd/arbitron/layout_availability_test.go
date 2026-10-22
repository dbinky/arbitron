package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/adapter/renderreview"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLayoutE2E_LiveEnqueueOutageRecoversByPoll(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "7"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "initial", layoutE2EWorkflow("initial", "Initial sweep finished")))
	layoutE2EWaitTerminal(t, ds, layoutE2ESavedRevision(t, e.privatePath("initial")))
	_, err := ds.db.Exec(`CREATE TRIGGER enqueue_outage BEFORE INSERT ON layout_revisions BEGIN SELECT RAISE(ABORT,'live enqueue outage'); END`)
	require.NoError(t, err)
	require.NoError(t, ds.workflowSvc.Create(ctx, "outage", layoutE2EWorkflow("outage", "Durably committed during outage")))
	rev := layoutE2ESavedRevision(t, e.privatePath("outage"))
	_, err = ds.layout.store.Get(ctx, rev)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)
	entries, err := ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, rev, entries[0].Request.Revision)
	require.Equal(t, workflow.JournalCommitted, entries[0].State)
	first := entries[0].Request
	// A later authoring save must not replace the receipt's captured graph or
	// supersede its historical work while the durable queue is unavailable.
	require.NoError(t, ds.workflowSvc.Update(ctx, "outage", layoutE2EWorkflow("outage", "Newer graph during outage")))
	newRev := layoutE2ESavedRevision(t, e.privatePath("outage"))
	require.NotEqual(t, rev, newRev)
	entries, err = ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	_, err = ds.db.Exec("DROP TRIGGER enqueue_outage")
	require.NoError(t, err)
	// No Recover call, notification or restart: only the production periodic sweep can replay this receipt.
	var row layout.Record
	require.Eventually(t, func() bool {
		var err error
		row, err = ds.layout.store.Get(ctx, rev)
		return err == nil && row.State.Terminal()
	}, 22*time.Second, 20*time.Millisecond)
	for _, entry := range entries {
		row = layoutE2EWaitTerminal(t, ds, entry.Request.Revision)
		layoutE2EAssertOutcome(t, row)
		require.Equal(t, 7, row.AttemptLimit, "the configured budget must survive failed enqueue and live replay")
		require.Equal(t, entry.Request.Manifest, row.Manifest)
		require.Equal(t, entry.Request.Retained, row.Retained)
		require.Equal(t, entry.Request.Reason, row.Reason)
		require.True(t, entry.Request.RequestedAt.Equal(row.RequestedAt))
		require.Equal(t, layout.Destination{Scope: entry.Request.Scope, Name: entry.Request.Name, WorkflowID: entry.Request.WorkflowID}, row.Destination)
	}
	// Replaying the original receipt after the newer save remains idempotent.
	require.NoError(t, ds.layout.requester.RequestArrangement(ctx, first))
	replayed, err := ds.layout.store.Get(ctx, first.Revision)
	require.NoError(t, err)
	require.Equal(t, first.Manifest, replayed.Manifest)
	require.NotEqual(t, layout.StateSuperseded, replayed.State)
	require.Equal(t, int64(1), replayed.JobGeneration)
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "outage"))
	entries, err = ds.layout.journal.Entries(ctx)
	require.NoError(t, err)
	require.Empty(t, entries)
}

type layoutCacheGate struct {
	layout.Renderer
	entered chan struct{}
	release chan struct{}
}

func (r *layoutCacheGate) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	close(r.entered)
	select {
	case <-r.release:
		return r.Renderer.Render(ctx, req)
	case <-ctx.Done():
		return layout.Rendering{}, ctx.Err()
	}
}
func TestLayoutE2E_CacheOutageAndDeletionPreserveAuthoritativeResult(t *testing.T) {
	gate := &layoutCacheGate{Renderer: layouttest.NewFakeRenderer(), entered: make(chan struct{}), release: make(chan struct{})}
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	// Argument-bound unit evidence isolates cache/Store lifecycle, not visual acceptance.
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return gate, layouttest.NewFakeReviewer(), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	// Make the disposable cache path unwritable as a file, on every platform/user.
	cachePath := filepath.Join(filepath.Dir(e.privatePath("cache")), filesystem.LayoutCacheFile)
	require.NoError(t, ds.workflowSvc.Create(ctx, "cache", layoutE2EWorkflow("cache", "Authoritative graph")))
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("renderer not fed")
	}
	require.NoError(t, os.MkdirAll(cachePath, 0700))
	close(gate.release)
	wf := layoutE2EManifest(t, e.privatePath("cache"))
	row := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	require.Equal(t, layout.StatePassed, row.State)
	first := ds.layout.reader.View(ctx, wf)
	require.NotNil(t, first.Geometry)
	require.False(t, first.Unreviewed)
	result, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
	graph, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	require.True(t, layout.Compatible(result.Geometry, graph))
	require.NoError(t, os.RemoveAll(cachePath))
	require.NoError(t, ds.layout.worker.ReconcileCache(ctx))
	require.FileExists(t, cachePath)
	require.NoError(t, os.Remove(cachePath))
	after := ds.layout.reader.View(ctx, wf)
	require.Equal(t, first.Geometry, after.Geometry)
	require.Equal(t, first.SavedRevision, after.SavedRevision)
	require.Equal(t, first.ResultGeneration, after.ResultGeneration)
}

func TestLayoutE2E_NilCopiedIDStillRequests(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	// An imported unregistered legacy shared source may legitimately have no ID.
	input := layoutE2EWorkflow("imported", "Legacy copy input")
	require.NoError(t, ds.workflowRepo.Create(ctx, workflow.Shared(), "imported", input))
	require.Equal(t, uuid.Nil, layoutE2EManifest(t, e.sharedPath("imported")).ID)
	require.NoError(t, ds.workflowSvc.Copy(ctx, "imported", "nil-copy"))
	wf := layoutE2EManifest(t, e.privatePath("nil-copy"))
	require.NotEqual(t, uuid.Nil, wf.ID, "the copy gets an independent identity even when its source has none")
	require.Equal(t, input.Nodes, wf.Nodes)
	require.Equal(t, input.Edges, wf.Edges)
	require.NotEmpty(t, wf.SavedRevision())
	row := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	layoutE2EAssertOutcome(t, row)
	require.Equal(t, wf.ID, row.Destination.WorkflowID)
	captured, err := json.Marshal(wf)
	require.NoError(t, err)
	require.JSONEq(t, string(captured), string(row.Manifest))
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, "nil-copy"))
}

func TestLayoutE2E_PreSeedUndecidedSettlementStaysConfined(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{seedSample: "unrelated-seed"})
	repo := filesystem.NewWorkflowRepo(e.workspaceDir)
	journal := filesystem.NewRevisionJournal(e.workspaceDir)
	// Prepared-only input is not a committed fixture. An unreadable manifest leaves
	// its fate undecided: that destination stays blocked and its input preserved,
	// but boot, seeding and unrelated destinations proceed.
	db, err := adaptsqlite.Open(e.dbPath)
	require.NoError(t, err)
	require.NoError(t, adaptsqlite.RunMigrations(db))
	ls, err := newLayoutStack(layoutE2EConfig(e.workspaceDir, 3), db, repo, adaptsqlite.NewPublishedWorkflowRepo(db), adaptsqlite.NewWorkflowIdentityRepo(db), layoutE2EDiscardLogger())
	require.NoError(t, err)
	var req workflow.ArrangementRequest
	require.NoError(t, repo.WithMutations(context.Background(), []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "undecided"}}, func(ctx context.Context) error {
		var err error
		req, err = ls.stamper.Stamp(ctx, workflow.Shared(), "undecided", layoutE2EWorkflow("undecided", "Prepared input"), nil, workflow.SaveReasonSeed)
		return err
	}))
	path := e.sharedPath("undecided")
	require.NoError(t, os.MkdirAll(path, 0700)) // workflow.json is a directory: real read I/O failure
	ls.stop()
	require.NoError(t, db.Close())
	ds, _ := e.boot(t)
	require.FileExists(t, e.sharedPath("unrelated-seed"))
	require.Error(t, ds.layout.commits.Settle(context.Background(), workflow.Shared(), "undecided"))
	ds.layout.worker.Stop() // no concurrent recovery sweep while inspecting preserved input
	entries, err := journal.Entries(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, req.Manifest, entries[0].Request.Manifest)
	require.Equal(t, workflow.JournalPrepared, entries[0].State)
}

func TestLayoutE2E_CorruptReceiptNeverArranges(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, close := e.boot(t)
	var req workflow.ArrangementRequest
	require.NoError(t, ds.layout.mutations.WithMutations(context.Background(), []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "corrupt"}}, func(ctx context.Context) error {
		var err error
		req, err = ds.layout.stamper.Stamp(ctx, workflow.Shared(), "corrupt", layoutE2EWorkflow("corrupt", "Not proven"), nil, workflow.SaveReasonSeed)
		return err
	}))
	close()
	receipt := filepath.Join(e.workspaceDir, ".arbitron-layout-journal", req.Revision+".committed")
	require.NoError(t, os.WriteFile(receipt, []byte(`{"revision":"wrong","digest":"wrong"}`), 0600))
	// A corrupt receipt is confined to its record: boot proceeds, and the input
	// is quarantined as unproven rather than ever arranged.
	ds, _ = e.boot(t)
	committed, err := ds.layout.commits.CommittedRequests(context.Background())
	require.NoError(t, err)
	for _, c := range committed {
		require.NotEqual(t, req.Revision, c.Revision)
	}
	require.Equal(t, 0, layoutE2ECountRows(t, ds.db, "corrupt"))
	require.DirExists(t, filepath.Join(e.workspaceDir, ".arbitron-layout-journal", "quarantine", req.Revision))
}

func TestLayoutE2E_NewSaveDoesNotSupersedeHistoricalWork(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	renderer := &layoutE2EGatedRenderer{entered: make(chan layout.MeasureRequest, 2)}
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return renderer, renderreview.NewReadabilityReviewer(nil), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, close := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "history", layoutE2EWorkflow("history", "First immutable input")))
	first := layoutE2ESavedRevision(t, e.privatePath("history"))
	select {
	case req := <-renderer.entered:
		require.NotEmpty(t, req.Items)
	case <-time.After(3 * time.Second):
		t.Fatal("first job never reached renderer")
	}
	require.NoError(t, ds.workflowSvc.Update(ctx, "history", layoutE2EWorkflow("history", "Second immutable input")))
	second := layoutE2ESavedRevision(t, e.privatePath("history"))
	require.NotEqual(t, first, second)
	select {
	case req := <-renderer.entered:
		require.NotEmpty(t, req.Items)
	case <-time.After(3 * time.Second):
		t.Fatal("second job never reached renderer")
	}
	for _, rev := range []string{first, second} {
		row, err := ds.layout.store.Get(ctx, rev)
		require.NoError(t, err)
		require.Equal(t, layout.StateInProgress, row.State)
	}
	require.Equal(t, 2, layoutE2ECountRows(t, ds.db, "history"))
	close()
}

func TestLayoutE2E_PreparedInputIsQuarantinedNeverArranged(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, close := e.boot(t)
	var req workflow.ArrangementRequest
	require.NoError(t, ds.layout.mutations.WithMutations(context.Background(), []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "prepared"}}, func(ctx context.Context) error {
		var err error
		req, err = ds.layout.stamper.Stamp(ctx, workflow.Shared(), "prepared", layoutE2EWorkflow("prepared", "Never visible"), nil, workflow.SaveReasonSeed)
		return err
	}))
	close()
	ds, _ = e.boot(t)
	_, err := ds.layout.store.Get(context.Background(), req.Revision)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)
	entries, err := ds.layout.journal.Entries(context.Background())
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoFileExists(t, e.sharedPath("prepared"))
	files, err := os.ReadDir(filepath.Join(e.workspaceDir, ".arbitron-layout-journal", "quarantine"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
}
