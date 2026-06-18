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
