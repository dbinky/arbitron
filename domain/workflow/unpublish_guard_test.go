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

// seedSharedRegistered creates a shared (published) workflow with a stable id,
// registers it, and records the publication, returning the id.
func seedSharedRegistered(t *testing.T, repo *fakeRepo, reg *fakeRegistry, pubs *fakePublishers, publisher uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	w := wf(name)
	w.ID = id
	repo.bucket(workflow.Shared())[name] = w
	require.NoError(t, reg.Register(context.Background(), workflow.RegistryEntry{
		ID: id, Name: name, Scope: workflow.ScopeShared,
	}))
	require.NoError(t, pubs.Upsert(context.Background(), name, publisher, "src", workflow.VisibilityPublic, ""))
	return id
}

// --- Failure: unpublish refused while running (Must Fix #5, option a) ---

// TestService_Unpublish_RunningInstance_Refused verifies that, like Delete,
// Unpublish is refused with ErrHasRunningInstances when a non-terminal instance
// carries the shared workflow's stable id — and that nothing (publication, shared
// folder, or registry row) is removed. Unpublish removes the shared registry row
// that Rerun resolves WorkflowID->name through, so allowing it while runs are live
// would silently degrade every historical instance's rerun resolution; the guard
// keeps the asymmetry with Delete from re-opening.
func TestService_Unpublish_RunningInstance_Refused(t *testing.T) {
	svc, repo, reg, pubs, counter := deleteSvc(t)
	admin := uuid.New()
	publisher := uuid.New()
	id := seedSharedRegistered(t, repo, reg, pubs, publisher, "alpha")
	counter.seed(id, "running")

	err := svc.Unpublish(ctxWith(admin, true), "alpha")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrHasRunningInstances)

	// Nothing removed: publication, shared folder, and registry row all remain.
	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.True(t, published, "publication must remain when unpublish is refused")
	_, folderStill := repo.bucket(workflow.Shared())["alpha"]
	assert.True(t, folderStill, "shared folder must remain when unpublish is refused")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.NoError(t, regErr, "registry row must remain when unpublish is refused")
}

// --- Happy path: unpublish proceeds when only terminal instances exist ---

// TestService_Unpublish_TerminalInstances_Proceeds verifies an admin Unpublish
// proceeds (removing publication + shared folder + registry row) when the
// workflow has only terminal instances — history is preserved, the guard only
// blocks on live runs.
func TestService_Unpublish_TerminalInstances_Proceeds(t *testing.T) {
	svc, repo, reg, pubs, counter := deleteSvc(t)
	admin := uuid.New()
	publisher := uuid.New()
	id := seedSharedRegistered(t, repo, reg, pubs, publisher, "alpha")
	counter.seed(id, "completed")

	require.NoError(t, svc.Unpublish(ctxWith(admin, true), "alpha"))

	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.False(t, published, "publication cleared")
	_, folderStill := repo.bucket(workflow.Shared())["alpha"]
	assert.False(t, folderStill, "shared folder removed")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.ErrorIs(t, regErr, workflow.ErrNotFound, "shared registry row removed")
}

// --- Error: the guard counter erroring fails closed ---

// TestService_Unpublish_CounterError_Propagates verifies that an error from the
// active-instance counter propagates from Unpublish and leaves everything in
// place — the guard must never silently unpublish when it cannot confirm there
// are no active runs.
func TestService_Unpublish_CounterError_Propagates(t *testing.T) {
	svc, repo, reg, pubs, counter := deleteSvc(t)
	admin := uuid.New()
	publisher := uuid.New()
	seedSharedRegistered(t, repo, reg, pubs, publisher, "alpha")

	counterErr := errors.New("instance store unreachable")
	counter.err = counterErr

	err := svc.Unpublish(ctxWith(admin, true), "alpha")
	require.ErrorIs(t, err, counterErr, "counter error must propagate from Unpublish guard")

	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.True(t, published, "publication must remain when the guard errors")
}
