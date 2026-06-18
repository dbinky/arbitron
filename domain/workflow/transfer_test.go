package workflow_test

import (
	"context"
	"errors"
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

// --- Error: no registry configured → ErrUnauthorized ---

// TestService_Transfer_NoRegistry_Unauthorized verifies that without a wired
// registry, TransferOwnership returns ErrUnauthorized (the admin gate passes;
// the "not configured" check fires with a distinct sentinel from ErrForbidden).
func TestService_Transfer_NoRegistry_Unauthorized(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil) // no registry
	err := svc.TransferOwnership(ctxWith(uuid.New(), true), "alpha", uuid.New())
	require.ErrorIs(t, err, workflow.ErrUnauthorized,
		"TransferOwnership without a registry must return ErrUnauthorized")
}

// --- Error: SetOwner fails → new folder rolled back ---

// TestService_Transfer_SetOwnerFails_RollsBackNewFolder verifies that when
// registry.SetOwner fails (step 4), TransferOwnership deletes the just-copied
// new folder (rollback). The old owner's folder is unchanged and the registry
// owner remains the original.
func TestService_Transfer_SetOwnerFails_RollsBackNewFolder(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, _ := renameSvc(t, owner)
	newOwner := uuid.New()

	ownerErr := errors.New("registry write failed")
	reg.failSetOwner = ownerErr

	err := svc.TransferOwnership(adminCtx, "alpha", newOwner)
	require.ErrorIs(t, err, ownerErr, "SetOwner error must propagate from TransferOwnership")

	_, atOld := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, atOld, "old owner's folder must remain when SetOwner fails")
	_, atNew := repo.bucket(workflow.Private(newOwner))["alpha"]
	assert.False(t, atNew, "new owner's folder must be rolled back when SetOwner fails")
	assert.Equal(t, owner, ownerOf(t, reg, adminCtx, "alpha"), "registry owner unchanged")
}

// --- Error: old-folder delete fails → error surfaced, new folder authoritative ---

// TestService_Transfer_OldFolderDeleteFails_Surfaced verifies that a failure to
// remove the old owner's folder (step 5) is surfaced. The registry owner and the
// new folder are already authoritative at that point; the stale old folder is
// cosmetic but the error is returned so the operator can reconcile.
func TestService_Transfer_OldFolderDeleteFails_Surfaced(t *testing.T) {
	owner := uuid.New()
	inner := newFakeRepo()
	reg := newFakeRegistry()
	adminCtx := ctxWith(owner, true)

	idA := uuid.New()
	w := wf("alpha")
	w.ID = idA
	inner.bucket(workflow.Private(owner))["alpha"] = w
	require.NoError(t, reg.Register(adminCtx, workflow.RegistryEntry{
		ID: idA, Name: "alpha", Scope: workflow.ScopePrivate, OwnerID: owner,
	}))

	// Wrap the repo so Delete for "alpha" fails (simulates disk error on old folder).
	repo := &errOnDeleteNameRepo{inner: inner, failName: "alpha"}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)

	newOwner := uuid.New()
	err := svc.TransferOwnership(adminCtx, "alpha", newOwner)
	require.Error(t, err, "old folder delete failure must surface from TransferOwnership")

	// Registry reflects the new owner (SetOwner succeeded before the delete).
	assert.Equal(t, newOwner, ownerOf(t, reg, adminCtx, "alpha"), "registry owner updated to newOwner")

	// New folder is present in the inner repo.
	_, atNew := inner.bucket(workflow.Private(newOwner))["alpha"]
	assert.True(t, atNew, "new owner's folder exists")

	// Old folder remains (delete failed).
	_, atOld := inner.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, atOld, "old owner's stale folder remains after delete failure")
}

// --- Edge: self-transfer (same owner) is a no-op ---

// TestService_Transfer_SelfTransfer_NoOp verifies that transferring a workflow to
// its current owner is a no-op: no folder is moved, no registry mutation happens,
// and no error is returned.
func TestService_Transfer_SelfTransfer_NoOp(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, idA := renameSvc(t, owner)

	require.NoError(t, svc.TransferOwnership(adminCtx, "alpha", owner),
		"transferring to the same owner must succeed (no-op)")

	gotID, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, idA, gotID, "id unchanged after self-transfer")
	assert.Equal(t, owner, ownerOf(t, reg, adminCtx, "alpha"), "owner unchanged")

	_, ok := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, ok, "folder still present at original location")
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

// --- Error: Shared workflow SetOwner fails → error propagates ---

// TestService_Transfer_Shared_SetOwnerFails_Propagates verifies that when
// registry.SetOwner fails on a shared-scope transfer, the error is surfaced.
// Unlike the private-scope path there is no folder to roll back — the registry
// mutation is the only side-effect, so the error propagates without compensation.
func TestService_Transfer_Shared_SetOwnerFails_Propagates(t *testing.T) {
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

	ownerErr := errors.New("registry write failed")
	reg.failSetOwner = ownerErr

	err := svc.TransferOwnership(adminCtx, "shared-wf", uuid.New())
	require.ErrorIs(t, err, ownerErr,
		"SetOwner failure on shared transfer must propagate")

	// Shared folder untouched (no folder move in the shared path).
	_, stillShared := repo.bucket(workflow.Shared())["shared-wf"]
	assert.True(t, stillShared, "shared folder unaffected by SetOwner failure")
}

// --- Error: scopeOf List fails → propagates before folder move ---

// TestService_Transfer_ScopeListFails_Propagates verifies that when
// registry.List fails inside scopeOf, TransferOwnership propagates the error
// without moving any folder.
func TestService_Transfer_ScopeListFails_Propagates(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, _ := renameSvc(t, owner)

	listErr := errors.New("registry list unavailable")
	reg.failList = listErr

	err := svc.TransferOwnership(adminCtx, "alpha", uuid.New())
	require.ErrorIs(t, err, listErr,
		"registry.List failure in scopeOf must propagate from TransferOwnership")

	_, folderStill := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, folderStill, "folder must remain when scopeOf List fails")
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
