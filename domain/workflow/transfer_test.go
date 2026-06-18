package workflow_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerOf reads the owner id for name from the registry via List.
func ownerOf(t *testing.T, reg *fakeRegistry, ctx context.Context, name string) uuid.UUID {
	t.Helper()
	entries, err := reg.List(ctx)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name == name {
			return e.OwnerID
		}
	}
	t.Fatalf("registry has no entry for %q", name)
	return uuid.Nil
}

// --- Error: non-admin is forbidden ---

// TestService_Transfer_NonAdmin_Forbidden verifies a non-admin caller cannot
// transfer ownership; the registry owner is unchanged.
func TestService_Transfer_NonAdmin_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, _ := renameSvc(t, owner)
	newOwner := uuid.New()

	err := svc.TransferOwnership(ctxWith(owner, false), "alpha", newOwner)
	require.ErrorIs(t, err, workflow.ErrForbidden)
	assert.Equal(t, owner, ownerOf(t, reg, adminCtx, "alpha"), "owner unchanged")
}

func TestService_Transfer_NoIdentity_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, _, _, _ := renameSvc(t, owner)

	err := svc.TransferOwnership(context.Background(), "alpha", uuid.New())
	require.ErrorIs(t, err, workflow.ErrForbidden)
}

// --- Happy path: a PRIVATE workflow moves its folder ---

// TestService_Transfer_Private_MovesFolder verifies transferring a private
// workflow updates the registry owner AND moves the folder from the old owner's
// private space to the new owner's, leaving nothing behind.
func TestService_Transfer_Private_MovesFolder(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, idA := renameSvc(t, owner)
	newOwner := uuid.New()

	require.NoError(t, svc.TransferOwnership(adminCtx, "alpha", newOwner))

	// Registry owner reassigned (id and name unchanged).
	assert.Equal(t, newOwner, ownerOf(t, reg, adminCtx, "alpha"), "owner reassigned")
	gotID, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, idA, gotID, "id unchanged")

	// The folder moved between user trees.
	_, atOld := repo.bucket(workflow.Private(owner))["alpha"]
	assert.False(t, atOld, "old owner's folder removed")
	moved, atNew := repo.bucket(workflow.Private(newOwner))["alpha"]
	require.True(t, atNew, "new owner's folder present")
	assert.Equal(t, idA, moved.ID, "stable id preserved on the moved folder")
}

// --- Failure: unknown name ---

func TestService_Transfer_UnknownName_NotFound(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)

	err := svc.TransferOwnership(adminCtx, "ghost", uuid.New())
	require.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Edge: a SHARED workflow updates owner without a folder move ---

// TestService_Transfer_Shared_NoFolderMove verifies transferring a shared
// workflow reassigns the registry owner but does NOT move any private folder (the
// shared folder stays put).
func TestService_Transfer_Shared_NoFolderMove(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	sharedID := uuid.New()
	shared := wf("shared-wf")
	shared.ID = sharedID
	repo.bucket(workflow.Shared())["shared-wf"] = shared
	require.NoError(t, reg.Register(adminCtx, workflow.RegistryEntry{
		ID: sharedID, Name: "shared-wf", Scope: workflow.ScopeShared,
	}))

	newOwner := uuid.New()
	require.NoError(t, svc.TransferOwnership(adminCtx, "shared-wf", newOwner))

	// Owner reassigned in the registry.
	assert.Equal(t, newOwner, ownerOf(t, reg, adminCtx, "shared-wf"))

	// The shared folder stayed put; no private folder was created for either user.
	_, stillShared := repo.bucket(workflow.Shared())["shared-wf"]
	assert.True(t, stillShared, "shared folder unmoved")
	_, inNewPrivate := repo.bucket(workflow.Private(newOwner))["shared-wf"]
	assert.False(t, inNewPrivate, "no private folder created on a shared transfer")
}
