package workflow_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Happy path: unpublish removes publication + shared folder ---

// TestService_Unpublish_RemovesPublicationAndSharedFolder verifies an admin
// Unpublish removes both the published_workflows record and the shared/<name>
// folder.
func TestService_Unpublish_RemovesPublicationAndSharedFolder(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	admin := uuid.New()
	publisher := uuid.New()

	repo.bucket(workflow.Shared())["alpha"] = wf("alpha")
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", publisher, "src", workflow.VisibilityPublic, ""))

	require.NoError(t, svc.Unpublish(ctxWith(admin, true), "alpha"))

	_, folderStill := repo.bucket(workflow.Shared())["alpha"]
	assert.False(t, folderStill, "shared folder removed")
	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.False(t, published, "publication record cleared")
}

// --- Success/edge: a private copy survives unpublish ---

// TestService_Unpublish_PrivateCopyRemains verifies that unpublishing leaves any
// private copy of the same name intact (only the shared scope is touched).
func TestService_Unpublish_PrivateCopyRemains(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	admin := uuid.New()
	owner := uuid.New()

	repo.bucket(workflow.Shared())["alpha"] = wf("alpha")
	repo.bucket(workflow.Private(owner))["alpha"] = wf("alpha")
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", owner, "src", workflow.VisibilityPublic, ""))

	require.NoError(t, svc.Unpublish(ctxWith(admin, true), "alpha"))

	_, sharedGone := repo.bucket(workflow.Shared())["alpha"]
	assert.False(t, sharedGone, "shared copy removed")
	_, privateStill := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, privateStill, "private copy must remain after unpublish")
}

// --- Error: non-admin rejected ---

// TestService_Unpublish_NonAdmin_Forbidden verifies a non-admin caller is
// rejected with ErrForbidden and nothing is removed.
func TestService_Unpublish_NonAdmin_Forbidden(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()

	repo.bucket(workflow.Shared())["alpha"] = wf("alpha")
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", owner, "src", workflow.VisibilityPublic, ""))

	err := svc.Unpublish(ctxWith(owner, false), "alpha")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrForbidden)

	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.True(t, published, "publication must remain after a rejected unpublish")
}

// --- Failure: unpublishing a non-published workflow ---

// TestService_Unpublish_NotPublished_NotFound verifies that unpublishing a name
// with no publication record returns ErrNotFound.
func TestService_Unpublish_NotPublished_NotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), newFakePublishers())
	err := svc.Unpublish(ctxWith(uuid.New(), true), "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Error: no publishers store wired → ErrUnauthorized ---

// TestService_Unpublish_NoPublishers_Unauthorized verifies that Unpublish
// returns ErrUnauthorized when the published-workflows store is not wired
// (local/unconfigured mode). The admin gate passes; the "not configured" check
// follows, using a distinct sentinel from ErrForbidden so callers can surface a
// helpful message.
func TestService_Unpublish_NoPublishers_Unauthorized(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil) // nil publishers
	err := svc.Unpublish(ctxWith(uuid.New(), true), "alpha")
	require.ErrorIs(t, err, workflow.ErrUnauthorized,
		"Unpublish without a publishers store must return ErrUnauthorized")
}

// --- Edge: shared folder already absent — Unpublish still succeeds ---

// TestService_Unpublish_SharedFolderMissing_StillSucceeds verifies that
// Unpublish succeeds even when the shared folder was already removed (e.g. by a
// prior partial delete). The publication record is the authoritative state;
// ErrNotFound from the shared folder delete is swallowed as idempotent.
func TestService_Unpublish_SharedFolderMissing_StillSucceeds(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	admin := uuid.New()
	owner := uuid.New()

	// Publication record exists but the shared folder is absent.
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", owner, "src", workflow.VisibilityPublic, ""))
	// Deliberately do NOT seed repo.bucket(workflow.Shared())["alpha"].

	err := svc.Unpublish(ctxWith(admin, true), "alpha")
	require.NoError(t, err,
		"Unpublish must succeed when the shared folder is already absent (ErrNotFound is idempotent)")

	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.False(t, published, "publication record removed even when the folder was already missing")
}
