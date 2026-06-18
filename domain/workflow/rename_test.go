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

// --- Error: no registry configured → ErrUnauthorized ---

// TestService_Rename_NoRegistry_Unauthorized verifies that Rename returns
// ErrUnauthorized (not ErrForbidden) when the registry is not configured. An
// admin-authenticated context can still be rejected with a distinct "not
// configured" sentinel so callers can surface a helpful error message.
func TestService_Rename_NoRegistry_Unauthorized(t *testing.T) {
	owner := uuid.New()
	svc := workflow.NewWorkflowService(newFakeRepo(), nil) // no registry
	adminCtx := ctxWith(owner, true)

	err := svc.Rename(adminCtx, "alpha", "beta")
	require.ErrorIs(t, err, workflow.ErrUnauthorized,
		"Rename without a registry must return ErrUnauthorized (operation not configured)")
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

// --- Error: registry.Rename fails → new folder rolled back ---

// TestService_Rename_RegistryRenameFails_RollsBackNewFolder verifies the
// cross-store half-rename prevention: when the folder CopyTree succeeds but
// registry.Rename then fails, the just-created new folder is deleted (the
// compensating rollback in step 5 of rename.go). The caller sees the registry
// error, and the state is left clean: old folder present, new folder absent,
// registry still resolves the original name.
func TestService_Rename_RegistryRenameFails_RollsBackNewFolder(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, idA := renameSvc(t, owner)

	renameErr := errors.New("registry unavailable")
	reg.failRename = renameErr

	err := svc.Rename(adminCtx, "alpha", "beta")
	require.ErrorIs(t, err, renameErr, "registry.Rename error must propagate")

	// Old folder still present — no move happened.
	_, hasAlpha := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, hasAlpha, "source folder must still exist after rollback")

	// New folder was rolled back — must not exist.
	_, hasBeta := repo.bucket(workflow.Private(owner))["beta"]
	assert.False(t, hasBeta, "new folder must be deleted by the rollback")

	// Registry is unchanged: old name still resolves to the same id.
	gotID, err2 := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err2)
	assert.Equal(t, idA, gotID, "registry must still resolve old name after rollback")
	assert.Equal(t, 1, reg.count(), "exactly one registry row (no orphan)")
}

// TestService_Rename_OldFolderDeleteFails_ErrorSurfaced verifies that a failure
// to delete the old folder (step 7) is surfaced as an error. At that point the
// registry + new folder are already authoritative; the stale old folder is
// cosmetic (Load resolves via the new name). The error is returned so operators
// can clean up the orphan.
func TestService_Rename_OldFolderDeleteFails_ErrorSurfaced(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	// Seed "alpha" and manually mark its bucket entry so repo.Delete will fail by
	// removing it before the Rename call (simulating the pre-delete state then
	// injecting the failure via a wrapping repo). Instead, use a thin wrapper that
	// fails Delete for the specific name "alpha" after the first call.
	require.NoError(t, svc.Create(adminCtx, "alpha", wf("alpha")))

	// Inject a Delete failure for the OLD folder by wrapping the repo. We use the
	// errOnDeleteNameRepo wrapper already known in this test package.
	failRepo := &errOnDeleteNameRepo{inner: repo, failName: "alpha"}
	svcFail := workflow.NewWorkflowService(failRepo, nil)
	svcFail.SetRegistry(reg)

	err := svcFail.Rename(adminCtx, "alpha", "beta")
	require.Error(t, err, "old-folder delete failure must surface as an error")

	// New folder is present and registry resolves the new name — the rename's
	// identity side completed successfully.
	_, hasBeta := repo.bucket(workflow.Private(owner))["beta"]
	assert.True(t, hasBeta, "new folder is present (rename identity side succeeded)")
	gotID, regErr := reg.ResolveID(adminCtx, "beta")
	require.NoError(t, regErr)
	assert.NotEqual(t, uuid.Nil, gotID, "registry resolves the new name")

	// Old folder is also still present (the delete failed).
	_, hasAlpha := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, hasAlpha, "old folder is still present (the delete failed)")
}

// errOnDeleteNameRepo wraps a WorkflowRepository and returns a fixed error from
// Delete when the workflow name matches failName.
type errOnDeleteNameRepo struct {
	inner    *fakeRepo
	failName string
}

func (r *errOnDeleteNameRepo) Load(ctx context.Context, s workflow.Scope, name string) (*workflow.Workflow, error) {
	return r.inner.Load(ctx, s, name)
}
func (r *errOnDeleteNameRepo) List(ctx context.Context, s workflow.Scope) ([]workflow.WorkflowSummary, error) {
	return r.inner.List(ctx, s)
}
func (r *errOnDeleteNameRepo) SaveNodePositions(ctx context.Context, s workflow.Scope, name string, positions map[string]workflow.NodePosition) error {
	return r.inner.SaveNodePositions(ctx, s, name, positions)
}
func (r *errOnDeleteNameRepo) Create(ctx context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	return r.inner.Create(ctx, s, name, wf)
}
func (r *errOnDeleteNameRepo) Update(ctx context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	return r.inner.Update(ctx, s, name, wf)
}
func (r *errOnDeleteNameRepo) Delete(ctx context.Context, s workflow.Scope, name string) error {
	if name == r.failName {
		return errors.New("disk full")
	}
	return r.inner.Delete(ctx, s, name)
}
func (r *errOnDeleteNameRepo) WriteFile(ctx context.Context, s workflow.Scope, name, path string, data []byte) error {
	return r.inner.WriteFile(ctx, s, name, path, data)
}
func (r *errOnDeleteNameRepo) DeleteFile(ctx context.Context, s workflow.Scope, name, path string) error {
	return r.inner.DeleteFile(ctx, s, name, path)
}
func (r *errOnDeleteNameRepo) ListFiles(ctx context.Context, s workflow.Scope, name string) ([]string, error) {
	return r.inner.ListFiles(ctx, s, name)
}
func (r *errOnDeleteNameRepo) Exists(ctx context.Context, s workflow.Scope, name string) (bool, error) {
	return r.inner.Exists(ctx, s, name)
}
func (r *errOnDeleteNameRepo) CopyTree(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	return r.inner.CopyTree(ctx, src, srcName, dst, dstName, overwrite)
}

// --- Robustness: CopyTree failure leaves everything untouched ---

// TestService_Rename_CopyTreeFails_NothingMoved verifies that a CopyTree
// failure (step 4) aborts Rename before any registry mutation happens. Both the
// old folder and the registry row are unchanged, and no partial state is left.
func TestService_Rename_CopyTreeFails_NothingMoved(t *testing.T) {
	owner := uuid.New()
	inner := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(inner, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	require.NoError(t, svc.Create(adminCtx, "alpha", wf("alpha")))
	idA, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)

	copyErr := errors.New("filesystem full")
	failRepo := &errOnCopyTreeRepo{fakeRepo: inner, err: copyErr}
	svcFail := workflow.NewWorkflowService(failRepo, nil)
	svcFail.SetRegistry(reg)

	err = svcFail.Rename(adminCtx, "alpha", "beta")
	require.ErrorIs(t, err, copyErr, "CopyTree error must propagate")

	// Registry unchanged: old name still resolves to the original id.
	gotID, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, idA, gotID, "registry must be unchanged after CopyTree failure")

	// New folder must not exist.
	_, hasBeta := inner.bucket(workflow.Private(owner))["beta"]
	assert.False(t, hasBeta, "no new folder when CopyTree fails")
}

// errOnCopyTreeRepo wraps fakeRepo and returns a fixed error from CopyTree.
type errOnCopyTreeRepo struct {
	*fakeRepo
	err error
}

func (r *errOnCopyTreeRepo) CopyTree(_ context.Context, _ workflow.Scope, _ string, _ workflow.Scope, _ string, _ bool) error {
	return r.err
}

// --- Robustness: label update failure (step 6) is surfaced ---

// TestService_Rename_LabelUpdateFails_ErrorSurfaced verifies that a repo.Update
// failure during the workflow.json label-update step (step 6) is surfaced as an
// error. By that point the registry + new folder are consistent; the error
// informs the operator that the label is stale and can be corrected by a
// re-publish or manual fix.
//
// NOTE: the standard fakeRepo.CopyTree already stamps cp.Name = dstName (a
// convenience short-cut). In the REAL filesystem adapter CopyTree copies bytes
// verbatim, so workflow.json retains the source name. To exercise step 6 (the
// label update) we use realLabelCopyRepo, which mirrors real-FS behavior by
// preserving the source name in the copy and then injects a hard Update failure.
func TestService_Rename_LabelUpdateFails_ErrorSurfaced(t *testing.T) {
	owner := uuid.New()
	inner := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(inner, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	require.NoError(t, svc.Create(adminCtx, "alpha", wf("alpha")))

	updateErr := errors.New("disk write failed")
	failRepo := &realLabelCopyRepo{fakeRepo: inner, updateErr: updateErr}
	svcFail := workflow.NewWorkflowService(failRepo, nil)
	svcFail.SetRegistry(reg)

	err := svcFail.Rename(adminCtx, "alpha", "beta")
	require.ErrorIs(t, err, updateErr, "label-update failure must propagate from Rename")

	// Registry already resolved the new name (Rename succeeded before step 6).
	_, regErr := reg.ResolveID(adminCtx, "beta")
	require.NoError(t, regErr, "registry must already have the new name at point of Update failure")
}

// realLabelCopyRepo wraps fakeRepo with two behavioural changes that together
// let us exercise Rename's step-6 (label update) failure:
//
//  1. CopyTree preserves the source workflow name in the copied entry (as the
//     real filesystem adapter does — workflow.json is copied verbatim, so the
//     copied folder still carries the old name until step 6 updates it).
//  2. Update always returns updateErr (to inject the step-6 failure).
type realLabelCopyRepo struct {
	*fakeRepo
	updateErr error
}

func (r *realLabelCopyRepo) CopyTree(_ context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	wfSrc, ok := r.bucket(src)[srcName]
	if !ok {
		return workflow.ErrNotFound
	}
	if _, exists := r.bucket(dst)[dstName]; exists && !overwrite {
		return workflow.ErrAlreadyExists
	}
	cp := *wfSrc
	// Deliberately do NOT update cp.Name: the real FS copies bytes verbatim.
	r.bucket(dst)[dstName] = &cp
	return nil
}

func (r *realLabelCopyRepo) Update(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return r.updateErr
}

// --- Edge: same-name rename returns ErrAlreadyExists ---

// TestService_Rename_SameName_AlreadyExists documents the behavior when the
// caller requests renaming a workflow to its current name. The uniqueness check
// (step 3) sees the current name already in the registry and returns
// ErrAlreadyExists. No folder move happens. This is conservative but
// self-consistent: the name IS already "taken" by the workflow itself.
func TestService_Rename_SameName_AlreadyExists(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)

	err := svc.Rename(adminCtx, "alpha", "alpha")
	require.ErrorIs(t, err, workflow.ErrAlreadyExists,
		"renaming to the current name must return ErrAlreadyExists (conservative uniqueness check)")
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

// --- Cross-scope uniqueness: rename target is a SHARED name ---

// TestService_Rename_TargetIsSharedName_AlreadyExists verifies the global
// uniqueness invariant: even if the collision is in a different scope (shared
// vs private), renaming to a name that is already registered anywhere must
// return ErrAlreadyExists and leave both entries untouched.
func TestService_Rename_TargetIsSharedName_AlreadyExists(t *testing.T) {
	owner := uuid.New()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)

	// Seed private "alpha" (the rename source).
	idPriv := uuid.New()
	w := wf("alpha")
	w.ID = idPriv
	repo.bucket(workflow.Private(owner))["alpha"] = w
	require.NoError(t, reg.Register(adminCtx, workflow.RegistryEntry{
		ID: idPriv, Name: "alpha", Scope: workflow.ScopePrivate, OwnerID: owner,
	}))

	// Seed shared "shared-pub" (the rename target) in the registry.
	idShared := uuid.New()
	repo.bucket(workflow.Shared())["shared-pub"] = wf("shared-pub")
	require.NoError(t, reg.Register(adminCtx, workflow.RegistryEntry{
		ID: idShared, Name: "shared-pub", Scope: workflow.ScopeShared,
	}))
	before := reg.count()

	err := svc.Rename(adminCtx, "alpha", "shared-pub")
	require.ErrorIs(t, err, workflow.ErrAlreadyExists,
		"renaming to a shared-scope registered name must return ErrAlreadyExists")

	// Both entries untouched.
	gotPriv, _ := reg.ResolveID(adminCtx, "alpha")
	assert.Equal(t, idPriv, gotPriv, "private entry unchanged")
	gotShared, _ := reg.ResolveID(adminCtx, "shared-pub")
	assert.Equal(t, idShared, gotShared, "shared entry unchanged")
	assert.Equal(t, before, reg.count(), "registry count unchanged")
}
