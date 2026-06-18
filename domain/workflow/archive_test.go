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

// archivedOf reads the archived flag for name from the registry via List (the
// same field the engine consults), failing the test if the name is unknown.
func archivedOf(t *testing.T, reg *fakeRegistry, ctx context.Context, name string) bool {
	t.Helper()
	entries, err := reg.List(ctx)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name == name {
			return e.Archived
		}
	}
	t.Fatalf("registry has no entry for %q", name)
	return false
}

// --- Error: no registry configured → ErrUnauthorized ---

// TestService_Archive_NoRegistry_Unauthorized verifies that without a wired
// registry, Archive returns ErrUnauthorized. The admin gate passes; the "not
// configured" check fires with a distinct sentinel from ErrForbidden.
func TestService_Archive_NoRegistry_Unauthorized(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil) // no registry
	err := svc.Archive(ctxWith(uuid.New(), true), "alpha")
	require.ErrorIs(t, err, workflow.ErrUnauthorized,
		"Archive without a registry must return ErrUnauthorized")
}

// --- Error: SetArchived fails → error propagates, flag unchanged ---

// TestService_Archive_SetArchivedFails_Propagates verifies that when
// registry.SetArchived returns an error, Archive surfaces it and the archived
// flag remains unchanged.
func TestService_Archive_SetArchivedFails_Propagates(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, _ := renameSvc(t, owner)

	archErr := errors.New("registry write failed")
	reg.failSetArchived = archErr

	err := svc.Archive(adminCtx, "alpha")
	require.ErrorIs(t, err, archErr, "SetArchived error must propagate from Archive")

	reg.failSetArchived = nil // clear to allow List for the assertion
	assert.False(t, archivedOf(t, reg, adminCtx, "alpha"), "archived flag unchanged after SetArchived error")
}

// --- Error: non-admin is forbidden ---

// TestService_Archive_NonAdmin_Forbidden verifies a non-admin caller cannot
// archive and the flag is unchanged.
func TestService_Archive_NonAdmin_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, _ := renameSvc(t, owner)

	err := svc.Archive(ctxWith(owner, false), "alpha")
	require.ErrorIs(t, err, workflow.ErrForbidden)
	assert.False(t, archivedOf(t, reg, adminCtx, "alpha"), "flag unchanged")
}

// TestService_Unarchive_NoRegistry_Unauthorized verifies that Unarchive, like
// Archive, returns ErrUnauthorized when the registry is not wired.
func TestService_Unarchive_NoRegistry_Unauthorized(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)
	err := svc.Unarchive(ctxWith(uuid.New(), true), "alpha")
	require.ErrorIs(t, err, workflow.ErrUnauthorized,
		"Unarchive without a registry must return ErrUnauthorized")
}

// TestService_Unarchive_SetArchivedFails_Propagates verifies that when
// registry.SetArchived returns an error during Unarchive, the error propagates
// and the flag remains archived.
func TestService_Unarchive_SetArchivedFails_Propagates(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, idA := renameSvc(t, owner)
	require.NoError(t, reg.SetArchived(adminCtx, idA, true))

	archErr := errors.New("registry write failed")
	reg.failSetArchived = archErr

	err := svc.Unarchive(adminCtx, "alpha")
	require.ErrorIs(t, err, archErr, "SetArchived error must propagate from Unarchive")

	reg.failSetArchived = nil // clear to allow List for the assertion
	assert.True(t, archivedOf(t, reg, adminCtx, "alpha"), "flag must remain archived after Unarchive error")
}

func TestService_Unarchive_NonAdmin_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, idA := renameSvc(t, owner)
	require.NoError(t, reg.SetArchived(adminCtx, idA, true))

	err := svc.Unarchive(ctxWith(owner, false), "alpha")
	require.ErrorIs(t, err, workflow.ErrForbidden)
	assert.True(t, archivedOf(t, reg, adminCtx, "alpha"), "flag unchanged")
}

func TestService_Archive_NoIdentity_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, _, _, _ := renameSvc(t, owner)
	err := svc.Archive(context.Background(), "alpha")
	require.ErrorIs(t, err, workflow.ErrForbidden)
}

func TestService_Unarchive_NoIdentity_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, _, _, _ := renameSvc(t, owner)
	err := svc.Unarchive(context.Background(), "alpha")
	require.ErrorIs(t, err, workflow.ErrForbidden)
}

// --- Happy path ---

// TestService_Archive_SetsFlag verifies an admin archive sets the registry flag,
// and unarchive clears it.
func TestService_Archive_SetsFlag(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, _ := renameSvc(t, owner)

	require.NoError(t, svc.Archive(adminCtx, "alpha"))
	assert.True(t, archivedOf(t, reg, adminCtx, "alpha"), "archived set")

	require.NoError(t, svc.Unarchive(adminCtx, "alpha"))
	assert.False(t, archivedOf(t, reg, adminCtx, "alpha"), "archived cleared")
}

// --- Failure: unknown name ---

func TestService_Archive_UnknownName_NotFound(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)

	err := svc.Archive(adminCtx, "ghost")
	require.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Edge: archive is idempotent ---

// TestService_Archive_Idempotent verifies archiving an already-archived workflow
// (and unarchiving an already-active one) succeeds without error.
func TestService_Archive_Idempotent(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, _ := renameSvc(t, owner)

	require.NoError(t, svc.Archive(adminCtx, "alpha"))
	require.NoError(t, svc.Archive(adminCtx, "alpha"), "archiving twice is a no-op, not an error")
	assert.True(t, archivedOf(t, reg, adminCtx, "alpha"))

	require.NoError(t, svc.Unarchive(adminCtx, "alpha"))
	require.NoError(t, svc.Unarchive(adminCtx, "alpha"), "unarchiving twice is a no-op")
	assert.False(t, archivedOf(t, reg, adminCtx, "alpha"))
}

// --- Edge: an archived workflow remains loadable/editable ---

// TestService_Archive_StillLoadableAndEditable verifies archiving does not block
// Load or Update — only triggering (asserted in the engine test).
func TestService_Archive_StillLoadableAndEditable(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)

	require.NoError(t, svc.Archive(adminCtx, "alpha"))

	loaded, err := svc.Load(adminCtx, "alpha")
	require.NoError(t, err, "archived workflow remains loadable")

	loaded.Description = "edited while archived"
	require.NoError(t, svc.Update(adminCtx, "alpha", loaded), "archived workflow remains editable")
}

// TestService_Archive_StillListable verifies archiving does not remove the
// workflow from List results — archived workflows must remain discoverable so
// admins can find and manage them. The trigger path is the only blocked action.
func TestService_Archive_StillListable(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)

	require.NoError(t, svc.Archive(adminCtx, "alpha"))

	summaries, err := svc.List(adminCtx)
	require.NoError(t, err, "List must not fail after archiving")

	names := make([]string, 0, len(summaries))
	for _, s := range summaries {
		names = append(names, s.Name)
	}
	assert.Contains(t, names, "alpha", "archived workflow must still appear in List")
}

// TestService_Archive_CanStillDuplicate verifies that archiving a workflow does
// not prevent it from being duplicated. The archive flag only gates triggering;
// it does not block admin lifecycle management operations.
func TestService_Archive_CanStillDuplicate(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, adminCtx, _ := renameSvc(t, owner)

	require.NoError(t, svc.Archive(adminCtx, "alpha"))

	err := svc.Duplicate(adminCtx, "alpha", "alpha-copy")
	require.NoError(t, err, "archived workflow must remain duplicatable")

	_, hasCopy := repo.bucket(workflow.Private(owner))["alpha-copy"]
	assert.True(t, hasCopy, "copy folder created")
	_, regErr := reg.ResolveID(adminCtx, "alpha-copy")
	assert.NoError(t, regErr, "copy registered in registry")
}
