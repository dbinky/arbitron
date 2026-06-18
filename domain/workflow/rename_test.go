package workflow_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renameSvc builds a WorkflowService with a wired registry and an admin-seeded
// private workflow "alpha" owned by owner. It returns the service, the repo, the
// registry, the admin context, and the stable id assigned to "alpha".
func renameSvc(t *testing.T, owner uuid.UUID) (*workflow.WorkflowService, *fakeRepo, *fakeRegistry, context.Context, uuid.UUID) {
	t.Helper()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)
	require.NoError(t, svc.Create(adminCtx, "alpha", wf("alpha")))
	id, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)
	return svc, repo, reg, adminCtx, id
}

// --- Error: non-admin is forbidden ---

// TestService_Rename_NonAdmin_Forbidden verifies a non-admin caller is rejected
// with ErrForbidden and that nothing is moved.
func TestService_Rename_NonAdmin_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, idA := renameSvc(t, owner)

	userCtx := ctxWith(owner, false)
	err := svc.Rename(userCtx, "alpha", "beta")
	require.ErrorIs(t, err, workflow.ErrForbidden)

	// Nothing moved.
	_, hasAlpha := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, hasAlpha, "alpha folder untouched")
	_, hasBeta := repo.bucket(workflow.Private(owner))["beta"]
	assert.False(t, hasBeta, "beta folder not created")
	gotID, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, idA, gotID, "registry unchanged")
}

// TestService_Rename_NoIdentity_Forbidden verifies a context with no identity is
// forbidden (the requireAdmin gate also covers the unauthenticated case).
func TestService_Rename_NoIdentity_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, _, _, _ := renameSvc(t, owner)

	err := svc.Rename(context.Background(), "alpha", "beta")
	require.ErrorIs(t, err, workflow.ErrForbidden)
}

// --- Happy path ---

// TestService_Rename_MovesFolderAndRegistry verifies an admin rename moves the
// name-keyed folder, the new name loads, the old name is not-found, and the
// registry resolves the new name to the SAME id.
func TestService_Rename_MovesFolderAndRegistry(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, idA := renameSvc(t, owner)

	require.NoError(t, svc.Rename(adminCtx, "alpha", "beta"))

	// Load(new) works, Load(old) is not-found.
	loaded, err := svc.Load(adminCtx, "beta")
	require.NoError(t, err)
	assert.Equal(t, "beta", loaded.Name, "workflow.json label updated to new name")
	assert.Equal(t, idA, loaded.ID, "stable id preserved")

	_, err = svc.Load(adminCtx, "alpha")
	require.ErrorIs(t, err, workflow.ErrNotFound)

	// Registry resolves new -> same id; old name is gone.
	gotID, err := reg.ResolveID(adminCtx, "beta")
	require.NoError(t, err)
	assert.Equal(t, idA, gotID, "registry resolves new name to the same id")
	_, err = reg.ResolveID(adminCtx, "alpha")
	require.ErrorIs(t, err, workflow.ErrNotFound)

	// Old folder physically gone, new folder present.
	_, hasAlpha := repo.bucket(workflow.Private(owner))["alpha"]
	assert.False(t, hasAlpha, "old folder removed")
	_, hasBeta := repo.bucket(workflow.Private(owner))["beta"]
	assert.True(t, hasBeta, "new folder present")
	assert.Equal(t, 1, reg.count(), "exactly one registry row")
}

// --- Success: a reference carrying the id still resolves after rename ---

// TestService_Rename_ReferencesResolveByID verifies that a reference (e.g. an
// instance or schedule) holding the stable workflow id still resolves to the
// (now renamed) workflow via the id, since references are linked by id, not name.
func TestService_Rename_ReferencesResolveByID(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, idA := renameSvc(t, owner)

	// A reference captured the stable id before the rename.
	refID := idA

	require.NoError(t, svc.Rename(adminCtx, "alpha", "beta"))

	// The reference's id still resolves — now to the new name.
	name, err := reg.ResolveName(adminCtx, refID)
	require.NoError(t, err)
	assert.Equal(t, "beta", name, "id-keyed reference resolves to the new name")
}

// --- Failure: target name taken ---

// TestService_Rename_TargetTaken_AlreadyExists verifies renaming to a name that
// already resolves to an existing workflow returns ErrAlreadyExists and moves
// nothing (both source and target folders/registry rows are unchanged).
func TestService_Rename_TargetTaken_AlreadyExists(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, idA := renameSvc(t, owner)

	// Seed a second workflow "beta" the rename would collide with.
	require.NoError(t, svc.Create(adminCtx, "beta", wf("beta")))
	idB, err := reg.ResolveID(adminCtx, "beta")
	require.NoError(t, err)

	err = svc.Rename(adminCtx, "alpha", "beta")
	require.ErrorIs(t, err, workflow.ErrAlreadyExists)

	// Nothing moved: both folders and both registry rows intact.
	_, hasAlpha := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, hasAlpha, "source folder untouched")
	gotA, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, idA, gotA, "source registry row unchanged")
	gotB, err := reg.ResolveID(adminCtx, "beta")
	require.NoError(t, err)
	assert.Equal(t, idB, gotB, "target registry row unchanged")
	assert.Equal(t, 2, reg.count())
}

// TestService_Rename_UnknownSource_NotFound verifies renaming a name that does
// not resolve returns ErrNotFound.
func TestService_Rename_UnknownSource_NotFound(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)

	err := svc.Rename(adminCtx, "ghost", "beta")
	require.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Edge: rename a SHARED workflow ---

// TestService_Rename_Shared_MovesSharedFolder_KeepsPublication verifies renaming
// a shared workflow moves shared/<old> -> shared/<new> and that the publication,
// linked by workflow_id (not name), survives — the id continues to resolve.
func TestService_Rename_Shared_MovesSharedFolder_KeepsPublication(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	// Seed a shared workflow + its registry row (scope shared).
	sharedID := uuid.New()
	shared := wf("shared-old")
	shared.ID = sharedID
	repo.bucket(workflow.Shared())["shared-old"] = shared
	require.NoError(t, reg.Register(adminCtx, workflow.RegistryEntry{
		ID: sharedID, Name: "shared-old", Scope: workflow.ScopeShared,
	}))

	require.NoError(t, svc.Rename(adminCtx, "shared-old", "shared-new"))

	// shared/<old> -> shared/<new>.
	_, hasOld := repo.bucket(workflow.Shared())["shared-old"]
	assert.False(t, hasOld, "old shared folder removed")
	moved, hasNew := repo.bucket(workflow.Shared())["shared-new"]
	require.True(t, hasNew, "new shared folder present")
	assert.Equal(t, sharedID, moved.ID, "stable id preserved on the shared copy")
	assert.Equal(t, "shared-new", moved.Name, "shared workflow.json label updated")

	// The publication (linked by id) survives: the id still resolves to the new name.
	name, err := reg.ResolveName(adminCtx, sharedID)
	require.NoError(t, err)
	assert.Equal(t, "shared-new", name)
}
