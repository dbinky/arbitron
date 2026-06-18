package workflow_test

import (
	"context"
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

func TestService_Unarchive_NonAdmin_Forbidden(t *testing.T) {
	owner := uuid.New()
	svc, _, reg, adminCtx, idA := renameSvc(t, owner)
	require.NoError(t, reg.SetArchived(adminCtx, idA, true))

	err := svc.Unarchive(ctxWith(owner, false), "alpha")
	require.ErrorIs(t, err, workflow.ErrForbidden)
	assert.True(t, archivedOf(t, reg, adminCtx, "alpha"), "flag unchanged")
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
