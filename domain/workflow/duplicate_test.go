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

// --- Error: non-admin is forbidden ---

// --- Error: no registry configured → ErrUnauthorized ---

// TestService_Duplicate_NoRegistry_Unauthorized verifies that without a wired
// registry (local mode), Duplicate returns ErrUnauthorized. The admin gate passes;
// the "not configured" check fires with a distinct sentinel so callers can surface
// a helpful message.
func TestService_Duplicate_NoRegistry_Unauthorized(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil) // no registry
	err := svc.Duplicate(ctxWith(uuid.New(), true), "alpha", "alpha-copy")
	require.ErrorIs(t, err, workflow.ErrUnauthorized,
		"Duplicate without a registry must return ErrUnauthorized")
}

// --- Error: CopyTree fails — no registry row written ---

// TestService_Duplicate_CopyTreeFails_NothingRegistered verifies that when
// CopyTree fails (e.g. destination folder already exists), Duplicate propagates
// the error without creating a registry row. The uniqueness check passes (the name
// is not yet in the registry), but the filesystem-level collision aborts the op
// before Register is called.
func TestService_Duplicate_CopyTreeFails_NothingRegistered(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, _ := renameSvc(t, owner)

	// Pre-seed a folder at the destination (independent of the registry check).
	repo.bucket(workflow.Private(owner))["alpha-copy"] = wf("alpha-copy")
	before := reg.count()

	err := svc.Duplicate(adminCtx, "alpha", "alpha-copy")
	require.Error(t, err, "CopyTree failure must propagate from Duplicate")
	assert.Equal(t, before, reg.count(), "no registry row on CopyTree failure")
}

// --- Error: Register fails → copied folder rolled back ---

// TestService_Duplicate_RegisterFails_RollsBackFolder verifies the cross-store
// ordering: when registry.Register fails (step 5), Duplicate rolls back the
// already-copied folder so the two stores do not diverge. The caller sees the
// Register error; no orphan folder is left behind.
func TestService_Duplicate_RegisterFails_RollsBackFolder(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, _ := renameSvc(t, owner)

	regErr := errors.New("registry unavailable")
	reg.failReg = regErr

	err := svc.Duplicate(adminCtx, "alpha", "alpha-copy")
	require.ErrorIs(t, err, regErr, "Register error must propagate from Duplicate")

	_, folderStill := repo.bucket(workflow.Private(owner))["alpha-copy"]
	assert.False(t, folderStill, "copied folder must be rolled back when Register fails")
	assert.Equal(t, 1, reg.count(), "registry unchanged (no orphan row)")
}

// --- Error: stamp Update fails after Register — error surfaced, inconsistent state ---

// TestService_Duplicate_StampUpdateFails_ErrorSurfaced verifies that a
// repo.Update failure during the id-stamp step (step 6) is surfaced as an error.
// At that point the registry row and the folder ALREADY exist (the stamp is
// best-effort within the success path); the folder carries the source id rather
// than the new id. The caller sees the error and must reconcile the inconsistency.
func TestService_Duplicate_StampUpdateFails_ErrorSurfaced(t *testing.T) {
	owner := uuid.New()
	inner := newFakeRepo()
	reg := newFakeRegistry()
	setupSvc := workflow.NewWorkflowService(inner, nil)
	setupSvc.SetRegistry(reg)
	adminCtx := ctxWith(owner, true)
	require.NoError(t, setupSvc.Create(adminCtx, "alpha", wf("alpha")))

	updateErr := errors.New("disk write failed")
	repo := &errOnUpdateRepo{fakeRepo: inner, err: updateErr}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)

	err := svc.Duplicate(adminCtx, "alpha", "alpha-copy")
	require.ErrorIs(t, err, updateErr,
		"stamp Update failure must propagate from Duplicate")

	// Registry row was committed before the stamp step (partial success state).
	srcID, _ := reg.ResolveID(adminCtx, "alpha")
	copyID, regErr := reg.ResolveID(adminCtx, "alpha-copy")
	require.NoError(t, regErr, "registry row was committed before the stamp failure")
	assert.NotEqual(t, uuid.Nil, copyID, "copy has a non-zero registry id")
	assert.NotEqual(t, srcID, copyID, "copy id is distinct from the source id")

	// Copied folder exists in the repo.
	_, ok := inner.bucket(workflow.Private(owner))["alpha-copy"]
	assert.True(t, ok, "copied folder exists despite stamp failure (partial success)")
}

// --- Error: non-admin is forbidden ---

// TestService_Duplicate_NonAdmin_Forbidden verifies a non-admin caller is
// rejected with ErrForbidden and nothing is copied or registered.
func TestService_Duplicate_NonAdmin_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, _, _ := renameSvc(t, owner)

	userCtx := ctxWith(owner, false)
	err := svc.Duplicate(userCtx, "alpha", "alpha-copy")
	require.ErrorIs(t, err, workflow.ErrForbidden)

	_, hasCopy := repo.bucket(workflow.Private(owner))["alpha-copy"]
	assert.False(t, hasCopy, "nothing copied for a non-admin")
	assert.Equal(t, 1, reg.count(), "no new registry row")
}

// TestService_Duplicate_NoIdentity_Forbidden verifies a context with no identity
// is forbidden.
func TestService_Duplicate_NoIdentity_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, _, _, _ := renameSvc(t, owner)

	err := svc.Duplicate(context.Background(), "alpha", "alpha-copy")
	require.ErrorIs(t, err, workflow.ErrForbidden)
}

// --- Happy path ---

// TestService_Duplicate_CopiesToRequesterPrivateWithNewID verifies an admin
// duplicate copies the definition into the REQUESTER's private space under a NEW
// id and registers the new name, leaving the original untouched.
func TestService_Duplicate_CopiesToRequesterPrivateWithNewID(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, idA := renameSvc(t, owner)

	require.NoError(t, svc.Duplicate(adminCtx, "alpha", "alpha-copy"))

	// The copy lives in the admin requester's private space.
	copy, hasCopy := repo.bucket(workflow.Private(owner))["alpha-copy"]
	require.True(t, hasCopy, "copy present in requester's private space")
	assert.Equal(t, "alpha-copy", copy.Name, "copy carries the new name")

	// A NEW registry id was allocated for the copy (distinct from the original).
	copyID, err := reg.ResolveID(adminCtx, "alpha-copy")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, copyID, "copy has a non-zero id")
	assert.NotEqual(t, idA, copyID, "copy id differs from the original")
	assert.Equal(t, copyID, copy.ID, "persisted folder id matches the registry id")

	// The original is untouched and both names are registered.
	gotA, err := reg.ResolveID(adminCtx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, idA, gotA, "original id unchanged")
	assert.Equal(t, 2, reg.count(), "both names registered")
}

// --- Failure: target name already exists ---

// TestService_Duplicate_ExistingName_AlreadyExists verifies duplicating to a name
// already held by another entry returns ErrAlreadyExists and copies nothing.
func TestService_Duplicate_ExistingName_AlreadyExists(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, _ := renameSvc(t, owner)

	require.NoError(t, svc.Create(adminCtx, "beta", wf("beta")))
	before := reg.count()

	err := svc.Duplicate(adminCtx, "alpha", "beta")
	require.ErrorIs(t, err, workflow.ErrAlreadyExists)
	assert.Equal(t, before, reg.count(), "no new row on a duplicate name")
}

// TestService_Duplicate_UnknownSource_NotFound verifies duplicating a name that
// does not resolve returns ErrNotFound.
func TestService_Duplicate_UnknownSource_NotFound(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)

	err := svc.Duplicate(adminCtx, "ghost", "ghost-copy")
	require.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Edge: duplicate of a SHARED workflow lands in the caller's private space ---

// TestService_Duplicate_Shared_LandsInCallerPrivate verifies duplicating a shared
// workflow copies it into the admin caller's PRIVATE space (not shared), with a
// fresh id, leaving the shared original intact.
func TestService_Duplicate_Shared_LandsInCallerPrivate(t *testing.T) {
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

	require.NoError(t, svc.Duplicate(adminCtx, "shared-wf", "my-copy"))

	// The copy is private to the caller, not shared.
	_, inShared := repo.bucket(workflow.Shared())["my-copy"]
	assert.False(t, inShared, "copy is NOT placed in the shared scope")
	cp, inPrivate := repo.bucket(workflow.Private(owner))["my-copy"]
	require.True(t, inPrivate, "copy lives in the caller's private space")

	copyID, err := reg.ResolveID(adminCtx, "my-copy")
	require.NoError(t, err)
	assert.NotEqual(t, sharedID, copyID, "copy gets a fresh id")
	assert.Equal(t, copyID, cp.ID)

	// Shared original untouched.
	_, stillShared := repo.bucket(workflow.Shared())["shared-wf"]
	assert.True(t, stillShared, "shared original intact")
}
