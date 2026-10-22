package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No renderer is substituted: the configured missing executable produces an
// eligible failed row. Stop the real worker after that failure to inspect the
// exact accepted generations independently of asynchronous review completion.
func TestLayoutQueryE2E_HistoricalRetryAfterRenameTransferDelete(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const original, renamed = "historical-retry", "renamed-retry"
	require.NoError(t, ds.workflowSvc.Create(ctx, original, queryE2EWorkflow(original, "First saved graph")))
	first := layoutE2EManifest(t, e.privatePath(original))
	firstRow := layoutE2EWaitTerminal(t, ds, first.SavedRevision())
	require.Equal(t, layout.StateFailed, firstRow.State)
	ds.layout.worker.Stop()
	id1, _, err := ds.eng.Trigger(ctx, original, map[string]any{}, "normal")
	require.NoError(t, err)
	firstBytes, err := os.ReadFile(e.privatePath(original))
	require.NoError(t, err)

	require.NoError(t, ds.workflowSvc.Rename(ctx, original, renamed))
	second := layoutE2EManifest(t, e.privatePath(renamed))
	require.NotEqual(t, first.SavedRevision(), second.SavedRevision())
	id2, _, err := ds.eng.Trigger(ctx, renamed, map[string]any{}, "normal")
	require.NoError(t, err)
	secondRow, err := ds.layout.store.Get(ctx, second.SavedRevision())
	require.NoError(t, err)
	movedFirst, err := ds.layout.store.Get(ctx, first.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, renamed, movedFirst.Destination.Name)
	ack, err := ds.layout.query.RetryInstance(ctx, id1, first.SavedRevision())
	require.NoError(t, err, "the old snapshot remains authorized after actual Rename")
	afterRename, err := ds.layout.store.Get(ctx, first.SavedRevision())
	require.NoError(t, err)
	assert.Equal(t, movedFirst.JobGeneration+1, ack.JobGeneration)
	assert.Equal(t, ack.JobGeneration, afterRename.JobGeneration)
	assert.Equal(t, movedFirst.Manifest, afterRename.Manifest)
	assert.JSONEq(t, string(firstBytes), string(afterRename.Manifest))
	assert.Equal(t, movedFirst.Destination, afterRename.Destination)
	current, err := ds.layout.store.Get(ctx, second.SavedRevision())
	require.NoError(t, err)
	assert.Equal(t, secondRow, current, "historical retry cannot touch the renamed current request")

	owner := uuid.New()
	require.NoError(t, ds.workflowSvc.TransferOwnership(ctx, renamed, owner))
	movedCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleAdmin})
	movedPath := filepath.Join(e.workspaceDir, "users", owner.String(), renamed, "workflow.json")
	_, err = ds.layout.query.WorkflowLayout(ctx, renamed)
	require.ErrorIs(t, err, workflow.ErrNotFound, "instance access does not require current workflow visibility")
	require.NoError(t, ds.workflowSvc.Delete(movedCtx, renamed))

	// Lose only the second row and its journal input. Its immutable snapshot
	// reconstructs the request without cancelling the already active first retry.
	require.NoError(t, ds.layout.journal.Remove(ctx, second.SavedRevision()))
	_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision = ?", second.SavedRevision())
	require.NoError(t, err)
	_, err = ds.db.Exec("DELETE FROM layout_revisions WHERE revision = ?", second.SavedRevision())
	require.NoError(t, err)
	activeFirst, err := ds.layout.store.Get(ctx, first.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, layout.StatePending, activeFirst.State)
	ack2, err := ds.layout.query.RetryInstance(ctx, id2, second.SavedRevision())
	require.NoError(t, err)
	rebuilt, err := ds.layout.store.Get(ctx, second.SavedRevision())
	require.NoError(t, err)
	assert.Equal(t, ack2.JobGeneration, rebuilt.JobGeneration)
	assert.Equal(t, layout.StatePending, rebuilt.State)
	assert.Equal(t, secondRow.Manifest, rebuilt.Manifest)
	assert.Equal(t, second.SavedRevision(), snapshotRevision(t, ds, id2))
	unchanged, err := ds.layout.store.Get(ctx, first.SavedRevision())
	require.NoError(t, err)
	assert.Equal(t, activeFirst, unchanged, "a separate historical retry must not be superseded or rewritten")
	for _, id := range []uuid.UUID{id1, id2} {
		view, err := ds.layout.query.InstanceLayout(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, view.Retained)
		assert.Len(t, view.Retained.Positions, 2, "the real snapshot fallback still reaches the query consumer")
		assert.Equal(t, layout.DisplayPending, view.DisplayStatus)
	}
	assert.Equal(t, first.SavedRevision(), snapshotRevision(t, ds, id1))
	require.NoDirExists(t, filepath.Dir(e.privatePath(original)))
	require.NoDirExists(t, filepath.Dir(e.privatePath(renamed)))
	require.NoDirExists(t, filepath.Dir(movedPath))
}

// Override only the target producer to exercise malformed port data against
// the real repository coordinator, authorization service and SQLite sink.
type queryTargetAccess struct {
	layout.WorkflowAccess
	targets map[string][]workflow.MutationTarget
}

func (a queryTargetAccess) LayoutRetryTargets(ctx context.Context, name string) ([]workflow.MutationTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targets, ok := a.targets[name]
	if !ok {
		return nil, fmt.Errorf("no retry targets for %s", name)
	}
	return targets, nil
}

func TestLayoutQueryE2E_InvalidRetryTargetsPreserveBytesAndGeneration(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "invalid-retry-targets"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Saved input")))
	wf := layoutE2EManifest(t, e.privatePath(name))
	layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	ds.layout.worker.Stop()
	before, err := ds.layout.store.Get(ctx, wf.SavedRevision())
	require.NoError(t, err)
	bytes, err := os.ReadFile(e.privatePath(name))
	require.NoError(t, err)
	resolved := workflow.MutationTarget{Scope: workflow.Private(localDefaultUser), Name: name}
	for label, targets := range map[string][]workflow.MutationTarget{
		"missing":        nil,
		"empty":          {},
		"omits resolved": {{Scope: workflow.Shared(), Name: name}},
		"invalid name":   {resolved, {Scope: workflow.Shared(), Name: "../escape"}},
		"invalid scope":  {resolved, {Scope: workflow.Private(uuid.Nil), Name: name}},
	} {
		t.Run(label, func(t *testing.T) {
			access := queryTargetAccess{WorkflowAccess: ds.workflowSvc, targets: map[string][]workflow.MutationTarget{name: targets}}
			if label == "missing" {
				delete(access.targets, name)
			}
			q := layout.NewQueryService(ds.layout.reader, ds.layout.requester, access, ds.eng, ds.layout.mutations)
			_, err := q.RetryWorkflow(ctx, name, wf.SavedRevision())
			require.Error(t, err)
			after, err := ds.layout.store.Get(ctx, wf.SavedRevision())
			require.NoError(t, err)
			assert.Equal(t, before, after)
			afterBytes, err := os.ReadFile(e.privatePath(name))
			require.NoError(t, err)
			assert.Equal(t, bytes, afterBytes)
		})
	}
}

func TestLayoutQueryE2E_UnauthorizedSharedRetryPreservesBytesAndGeneration(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	admin := adminCtxForLocalUser(t, ds)
	const name = "shared-retry-denied"
	require.NoError(t, ds.workflowSvc.Create(admin, "source", queryE2EWorkflow("source", "Shared input")))
	require.NoError(t, ds.workflowSvc.Publish(admin, "source", name, workflow.VisibilityPublic, ""))
	wf := layoutE2EManifest(t, e.sharedPath(name))
	layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	ds.layout.worker.Stop()
	before, err := ds.layout.store.Get(admin, wf.SavedRevision())
	require.NoError(t, err)
	bytes, err := os.ReadFile(e.sharedPath(name))
	require.NoError(t, err)
	user := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleUser})
	_, err = ds.layout.query.RetryWorkflow(user, name, wf.SavedRevision())
	require.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	after, err := ds.layout.store.Get(admin, wf.SavedRevision())
	require.NoError(t, err)
	assert.Equal(t, before, after)
	afterBytes, err := os.ReadFile(e.sharedPath(name))
	require.NoError(t, err)
	assert.Equal(t, bytes, afterBytes)
}
