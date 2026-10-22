package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Paired seam 18: an ownership move rewrites a committed-but-unqueued journal
// envelope; a historical retry from the old instance must queue at the moved
// destination with the captured bytes unchanged, and the worker must publish
// there. The root's real journal, SQLite store, lock and cache are exercised.
func TestLayoutE2E_HistoricalRetryUsesCommittedRelocatedEnvelope(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	// Argument-bound unit evidence isolates the envelope/destination seam, not visual acceptance.
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return layouttest.NewFakeRenderer(), layouttest.NewFakeReviewer(), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	name := "retry-relocated"

	_, err := ds.db.Exec(`CREATE TRIGGER enqueue_outage BEFORE INSERT ON layout_revisions BEGIN SELECT RAISE(ABORT,'relocation retry enqueue outage'); END`)
	require.NoError(t, err)
	require.NoError(t, ds.workflowSvc.Create(ctx, name, layoutE2EWorkflow(name, "Committed during outage")))
	oldPath := e.privatePath(name)
	rev := layoutE2ESavedRevision(t, oldPath)
	captured := committedJournalEntry(t, ds, rev)
	require.Equal(t, workflow.Private(localDefaultUser), captured.Scope)
	id, _, err := ds.eng.Trigger(ctx, name, map[string]any{}, "normal")
	require.NoError(t, err)

	owner := uuid.New()
	require.NoError(t, ds.workflowSvc.TransferOwnership(ctx, name, owner))
	movedPath := filepath.Join(e.workspaceDir, "users", owner.String(), name, "workflow.json")
	require.NoDirExists(t, filepath.Dir(oldPath))
	require.Equal(t, rev, layoutE2ESavedRevision(t, movedPath), "transfer preserves the revision")
	_, err = ds.layout.store.Get(ctx, rev)
	require.ErrorIs(t, err, layout.ErrRecordNotFound, "the outage left no row for Relocate to move")
	relocated := committedJournalEntry(t, ds, rev)
	require.Equal(t, workflow.Private(owner), relocated.Scope, "Journal.Relocate must move the committed envelope")
	require.Equal(t, name, relocated.Name)
	require.Equal(t, captured.Manifest, relocated.Manifest, "relocation never rewrites captured bytes")

	_, err = ds.db.Exec("DROP TRIGGER enqueue_outage")
	require.NoError(t, err)
	// The old caller's authorised instance snapshot still names the old locator.
	ack, err := ds.layout.query.RetryInstance(ctx, id, rev)
	require.NoError(t, err)
	require.Equal(t, rev, ack.SavedRevision)
	row, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	require.Equal(t, layout.Destination{Scope: workflow.Private(owner), Name: name, WorkflowID: captured.WorkflowID}, row.Destination,
		"historical retry must queue at the committed relocated destination, not the snapshot's locator")
	require.Equal(t, captured.Manifest, row.Manifest, "the row keeps the captured manifest bytes")
	snapshot := layoutE2EManifest(t, movedPath)
	require.Equal(t, name, snapshot.Presentation.Name)

	row = layoutE2EWaitTerminal(t, ds, rev)
	require.Equal(t, layout.StatePassed, row.State)
	require.Eventually(t, func() bool {
		published, ok, err := ds.layout.cache.ReadCache(ctx, workflow.Private(owner), name)
		return err == nil && ok && published.Revision == rev
	}, 20*time.Second, 20*time.Millisecond, "the worker must publish at the legitimate moved destination")
	require.NoDirExists(t, filepath.Dir(oldPath), "publication must not recreate the moved-away folder")
	movedCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleAdmin})
	v, err := ds.layout.query.WorkflowLayout(movedCtx, name)
	require.NoError(t, err)
	require.Equal(t, layout.StatePassed, v.State)
	iv, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	require.Equal(t, rev, iv.SavedRevision)
	require.Equal(t, layout.StatePassed, iv.State)
}

func committedJournalEntry(t *testing.T, ds *domainStack, rev string) workflow.ArrangementRequest {
	t.Helper()
	entries, err := ds.layout.journal.Entries(context.Background())
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Request.Revision == rev {
			require.Equal(t, workflow.JournalCommitted, entry.State)
			return entry.Request
		}
	}
	t.Fatalf("no journal entry for %s", rev)
	return workflow.ArrangementRequest{}
}

// pausingRequestStore holds the first insert for one revision until released,
// so a transfer can be started while the historical retry is mid-insert.
type pausingRequestStore struct {
	layout.Store
	rev      string
	reached  chan struct{}
	release  chan struct{}
	inserted sync.Once
}

func (s *pausingRequestStore) Request(ctx context.Context, req layout.NewRequest) (layout.Record, bool, error) {
	if req.Revision == s.rev {
		s.inserted.Do(func() { close(s.reached); <-s.release })
	}
	return s.Store.Request(ctx, req)
}

// G41: a transfer that starts after the historical retry read its envelope must
// wait for the insert (both run under the real filesystem destination lock), then
// relocate the row; the moved folder, not the vacated one, receives the layout.
func TestLayoutE2E_HistoricalRetryInsertSerializesWithTransfer(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return layouttest.NewFakeRenderer(), layouttest.NewFakeReviewer(), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	name := "retry-races-transfer"

	_, err := ds.db.Exec(`CREATE TRIGGER enqueue_outage BEFORE INSERT ON layout_revisions BEGIN SELECT RAISE(ABORT,'relocation retry enqueue outage'); END`)
	require.NoError(t, err)
	require.NoError(t, ds.workflowSvc.Create(ctx, name, layoutE2EWorkflow(name, "Committed during outage")))
	oldPath := e.privatePath(name)
	rev := layoutE2ESavedRevision(t, oldPath)
	captured := committedJournalEntry(t, ds, rev)
	id, _, err := ds.eng.Trigger(ctx, name, map[string]any{}, "normal")
	require.NoError(t, err)
	_, err = ds.db.Exec("DROP TRIGGER enqueue_outage")
	require.NoError(t, err)

	store := &pausingRequestStore{Store: ds.layout.store, rev: rev, reached: make(chan struct{}), release: make(chan struct{})}
	requester := layout.NewRequester(store, ds.layout.requester.AttemptLimit(), nil)
	requester.SetNotifier(ds.layout.worker.Notify)
	q := layout.NewQueryService(ds.layout.reader, requester, ds.workflowSvc, ds.eng, ds.layout.mutations)
	retried := make(chan error, 1)
	go func() { _, err := q.RetryInstance(ctx, id, rev); retried <- err }()
	select {
	case <-store.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("historical retry never reached its insert")
	}

	owner := uuid.New()
	transferred := make(chan error, 1)
	go func() { transferred <- ds.workflowSvc.TransferOwnership(ctx, name, owner) }()
	select {
	case err := <-transferred:
		t.Fatalf("transfer completed while the historical insert was in flight (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	require.Equal(t, workflow.Private(localDefaultUser), committedJournalEntry(t, ds, rev).Scope, "the journal must not move under a held destination lock")
	close(store.release)
	require.NoError(t, <-retried)
	require.NoError(t, <-transferred)

	row, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	require.Equal(t, layout.Destination{Scope: workflow.Private(owner), Name: name, WorkflowID: captured.WorkflowID}, row.Destination,
		"the transfer must relocate the row the historical retry inserted")
	require.Equal(t, captured.Manifest, row.Manifest, "the row keeps the captured manifest bytes")
	require.Equal(t, workflow.Private(owner), committedJournalEntry(t, ds, rev).Scope)
	row = layoutE2EWaitTerminal(t, ds, rev)
	require.Equal(t, layout.StatePassed, row.State)
	require.Eventually(t, func() bool {
		published, ok, err := ds.layout.cache.ReadCache(ctx, workflow.Private(owner), name)
		return err == nil && ok && published.Revision == rev
	}, 20*time.Second, 20*time.Millisecond, "the worker must publish at the legitimate moved destination")
	require.NoDirExists(t, filepath.Dir(oldPath), "publication must not recreate the moved-away folder")
}

// Paired seam 4 (rename direction): a rename mints a new revision at the new
// name but must still relocate the old revision's committed-but-unqueued
// envelope. A historical retry from an instance frozen on the old revision
// queues at the renamed destination with the old revision's captured bytes,
// and the renamed current request is left untouched.
func TestLayoutE2E_HistoricalRetryAfterRenameUsesCommittedRelocatedEnvelope(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return layouttest.NewFakeRenderer(), layouttest.NewFakeReviewer(), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const original, renamed = "retry-rename-source", "retry-rename-target"

	_, err := ds.db.Exec(`CREATE TRIGGER enqueue_outage BEFORE INSERT ON layout_revisions BEGIN SELECT RAISE(ABORT,'rename retry enqueue outage'); END`)
	require.NoError(t, err)
	require.NoError(t, ds.workflowSvc.Create(ctx, original, layoutE2EWorkflow(original, "Committed during outage")))
	oldPath := e.privatePath(original)
	rev := layoutE2ESavedRevision(t, oldPath)
	captured := committedJournalEntry(t, ds, rev)
	require.Equal(t, original, captured.Name)
	id, _, err := ds.eng.Trigger(ctx, original, map[string]any{}, "normal")
	require.NoError(t, err)

	require.NoError(t, ds.workflowSvc.Rename(ctx, original, renamed))
	newPath := e.privatePath(renamed)
	require.NoDirExists(t, filepath.Dir(oldPath))
	current := layoutE2ESavedRevision(t, newPath)
	require.NotEqual(t, rev, current, "rename mints a new revision")
	relocated := committedJournalEntry(t, ds, rev)
	require.Equal(t, workflow.Private(localDefaultUser), relocated.Scope)
	require.Equal(t, renamed, relocated.Name, "rename must relocate the old revision's committed envelope")
	require.Equal(t, captured.Manifest, relocated.Manifest, "relocation never rewrites captured bytes")
	_, err = ds.layout.store.Get(ctx, rev)
	require.ErrorIs(t, err, layout.ErrRecordNotFound, "the outage left no row for RelocateArrangements to move")

	_, err = ds.db.Exec("DROP TRIGGER enqueue_outage")
	require.NoError(t, err)
	_, err = ds.layout.query.WorkflowLayout(ctx, original)
	require.ErrorIs(t, err, workflow.ErrNotFound, "the old name is gone; the instance snapshot alone authorises")
	ack, err := ds.layout.query.RetryInstance(ctx, id, rev)
	require.NoError(t, err)
	require.Equal(t, rev, ack.SavedRevision)
	row, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	require.Equal(t, layout.Destination{Scope: workflow.Private(localDefaultUser), Name: renamed, WorkflowID: captured.WorkflowID}, row.Destination,
		"historical retry must queue at the renamed destination, not the snapshot's locator")
	require.Equal(t, captured.Manifest, row.Manifest, "the row keeps the old revision's captured bytes")
	var frozen workflow.Workflow
	require.NoError(t, json.Unmarshal(row.Manifest, &frozen))
	require.Equal(t, original, frozen.Presentation.Name, "the manifest is never rewritten to the new locator")

	row = layoutE2EWaitTerminal(t, ds, rev)
	require.Equal(t, layout.StatePassed, row.State)
	require.NoDirExists(t, filepath.Dir(oldPath), "publication must not recreate the renamed-away folder")
	iv, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	require.Equal(t, rev, iv.SavedRevision)
	require.Equal(t, layout.StatePassed, iv.State)
	require.Equal(t, current, layoutE2ESavedRevision(t, newPath), "historical retry cannot touch the renamed current revision")
}
