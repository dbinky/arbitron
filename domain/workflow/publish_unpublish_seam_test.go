package workflow_test

// Area 15 (paired): Publish/Unpublish + shared folder & publication record
//
// Verifies the integration seam at the boundary between the private scope,
// the shared scope, and the publication record. Publish is a COPY (not a
// move): both scopes must hold a copy after the operation. Unpublish is the
// inverse: the shared copy is removed, but the private copy is preserved.

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublishSeam_BothCopiesExistAfterPublish verifies that Publish is a COPY
// operation: after publishing, the private source folder still exists AND the
// shared destination folder exists. Neither scope should lose its entry.
func TestPublishSeam_BothCopiesExistAfterPublish(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	pubs := newFakePublishers()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	require.NoError(t, svc.Create(adminCtx, "mine", wf("mine")))

	require.NoError(t, svc.Publish(adminCtx, "mine", "shared-mine", workflow.VisibilityPublic, ""))

	// Private copy must still exist.
	_, privateStill := repo.bucket(workflow.Private(owner))["mine"]
	assert.True(t, privateStill, "private source must remain after Publish (Publish is a copy, not a move)")

	// Shared copy must now exist.
	_, sharedPresent := repo.bucket(workflow.Shared())["shared-mine"]
	assert.True(t, sharedPresent, "shared destination must be created by Publish")

	// Publication record must reference the owner.
	pubID, recorded, err := pubs.PublisherOf(context.Background(), "shared-mine")
	require.NoError(t, err)
	require.True(t, recorded, "publication record must exist")
	assert.Equal(t, owner, pubID, "publication must reference the publishing owner")
}

// TestPublishSeam_UnpublishRemovesSharedPreservesPrivate verifies that after a
// publish-then-unpublish cycle, the private copy is preserved and the shared
// copy is removed. The publication record is also cleared.
func TestPublishSeam_UnpublishRemovesSharedPreservesPrivate(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	pubs := newFakePublishers()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	require.NoError(t, svc.Create(adminCtx, "mine", wf("mine")))
	require.NoError(t, svc.Publish(adminCtx, "mine", "shared-mine", workflow.VisibilityPublic, ""))
	require.NoError(t, svc.Unpublish(adminCtx, "shared-mine"))

	// Private copy must survive unpublish.
	_, privateStill := repo.bucket(workflow.Private(owner))["mine"]
	assert.True(t, privateStill, "private copy must survive Unpublish")

	// Shared copy must be removed.
	_, sharedGone := repo.bucket(workflow.Shared())["shared-mine"]
	assert.False(t, sharedGone, "shared copy must be removed by Unpublish")

	// Publication record must be cleared.
	_, published, _ := pubs.PublicationOf(context.Background(), "shared-mine")
	assert.False(t, published, "publication record must be cleared by Unpublish")

	// Publish again must succeed (the name is now free).
	require.NoError(t, svc.Publish(adminCtx, "mine", "shared-mine", workflow.VisibilityPublic, ""),
		"re-publishing after Unpublish must succeed")
}

// TestPublishSeam_RegistryConsistency verifies that after publishing, two registry
// entries exist: one for the private workflow and one for the shared publication,
// each with their own distinct stable UUID.
func TestPublishSeam_RegistryConsistency(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	pubs := newFakePublishers()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	require.NoError(t, svc.Create(adminCtx, "mine", wf("mine")))
	privateID, err := reg.ResolveID(adminCtx, "mine")
	require.NoError(t, err)

	require.NoError(t, svc.Publish(adminCtx, "mine", "pub", workflow.VisibilityPublic, ""))

	sharedID, err := reg.ResolveID(adminCtx, "pub")
	require.NoError(t, err)

	// Two distinct UUIDs: private and shared are separate identities.
	assert.NotEqual(t, privateID, sharedID,
		"private workflow and shared publication must have distinct stable UUIDs")
	assert.Equal(t, 2, reg.count(), "exactly two registry entries after publish (private + shared)")

	// After unpublish, only the private entry remains.
	require.NoError(t, svc.Unpublish(adminCtx, "pub"))
	assert.Equal(t, 1, reg.count(),
		"shared registry entry must be removed by Unpublish, leaving only the private entry")
	_, err = reg.ResolveID(adminCtx, "mine")
	assert.NoError(t, err, "private registry entry must survive Unpublish")
	_, err = reg.ResolveID(adminCtx, "pub")
	assert.ErrorIs(t, err, workflow.ErrNotFound, "shared registry entry must be gone after Unpublish")
}
