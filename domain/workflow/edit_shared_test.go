package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedEditSvc builds a service with a registry holding a shared workflow
// "shared-wf" (no private copy). It returns the service, repo, registry, and the
// shared workflow's stable id.
func sharedEditSvc(t *testing.T) (*workflow.Service, *fakeRepo, *fakeRegistry, uuid.UUID) {
	t.Helper()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	adminCtx := ctxWith(uuid.New(), true)

	sharedID := uuid.New()
	shared := wf("shared-wf")
	shared.ID = sharedID
	repo.bucket(workflow.Shared())["shared-wf"] = shared
	require.NoError(t, reg.Register(adminCtx, workflow.RegistryEntry{
		ID: sharedID, Name: "shared-wf", Scope: workflow.ScopeShared,
	}))
	return svc, repo, reg, sharedID
}

// --- Error: a non-admin still gets ErrSharedReadOnly ---

// TestService_EditShared_NonAdmin_StillReadOnly verifies a non-admin editing a
// shared-only workflow still gets ErrSharedReadOnly and the shared copy is
// unchanged.
func TestService_EditShared_NonAdmin_StillReadOnly(t *testing.T) {
	svc, repo, _, _ := sharedEditSvc(t)
	userCtx := ctxWith(uuid.New(), false)

	edited := wf("shared-wf")
	edited.Description = "tampered"
	err := svc.Update(userCtx, "shared-wf", edited)
	require.ErrorIs(t, err, workflow.ErrSharedReadOnly)

	assert.Equal(t, "d", repo.bucket(workflow.Shared())["shared-wf"].Description,
		"shared copy untouched by a non-admin")
}

// TestService_WriteFileShared_NonAdmin_StillReadOnly mirrors the above for WriteFile.
func TestService_WriteFileShared_NonAdmin_StillReadOnly(t *testing.T) {
	svc, _, _, _ := sharedEditSvc(t)
	userCtx := ctxWith(uuid.New(), false)

	err := svc.WriteFile(userCtx, "shared-wf", "notes.md", []byte("x"))
	require.ErrorIs(t, err, workflow.ErrSharedReadOnly)
}

// --- Happy path: an admin may edit a shared workflow in place ---

// TestService_EditShared_Admin_WritesSharedScope verifies an admin Update against
// a shared-only workflow writes the SHARED copy in place (no private copy made).
func TestService_EditShared_Admin_WritesSharedScope(t *testing.T) {
	svc, repo, _, sharedID := sharedEditSvc(t)
	adminCtx := ctxWith(uuid.New(), true)

	edited := wf("shared-wf")
	edited.ID = sharedID
	edited.Description = "admin edit"
	require.NoError(t, svc.Update(adminCtx, "shared-wf", edited))

	got := repo.bucket(workflow.Shared())["shared-wf"]
	assert.Equal(t, "admin edit", got.Description, "shared copy edited in place")
}

// TestService_WriteFileShared_Admin_Succeeds verifies an admin WriteFile against a
// shared-only workflow succeeds (targets the shared scope).
func TestService_WriteFileShared_Admin_Succeeds(t *testing.T) {
	svc, _, _, _ := sharedEditSvc(t)
	adminCtx := ctxWith(uuid.New(), true)

	require.NoError(t, svc.WriteFile(adminCtx, "shared-wf", "notes.md", []byte("x")))
}

// --- Edge: a private workflow of the admin is unaffected by the shared branch ---

// TestService_EditShared_Admin_PrefersPrivate verifies that when the admin has
// their OWN private workflow of the same name, an Update targets the private copy
// (the shared branch only applies when there is no private copy to write).
func TestService_EditShared_Admin_PrefersPrivate(t *testing.T) {
	svc, repo, reg, _ := sharedEditSvc(t)
	admin := uuid.New()
	adminCtx := ctxWith(admin, true)

	// The admin also owns a private "shared-wf".
	privID := uuid.New()
	priv := wf("shared-wf")
	priv.ID = privID
	repo.bucket(workflow.Private(admin))["shared-wf"] = priv
	require.NoError(t, reg.Register(adminCtx, workflow.RegistryEntry{
		ID: uuid.New(), Name: "another", Scope: workflow.ScopePrivate, OwnerID: admin,
	}))

	edited := wf("shared-wf")
	edited.ID = privID
	edited.Description = "private edit"
	require.NoError(t, svc.Update(adminCtx, "shared-wf", edited))

	assert.Equal(t, "private edit", repo.bucket(workflow.Private(admin))["shared-wf"].Description,
		"admin's private copy edited")
	assert.Equal(t, "d", repo.bucket(workflow.Shared())["shared-wf"].Description,
		"shared copy untouched when a private copy exists")
}
