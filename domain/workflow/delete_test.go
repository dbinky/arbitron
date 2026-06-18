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

// fakeActiveCounter is an in-memory workflow.ActiveInstanceCounter. It models the
// instance store: active records the count of non-terminal instances per workflow
// id; history records instances regardless of state, so a test can assert that a
// completed (terminal) instance row survives a Delete (history preservation).
type fakeActiveCounter struct {
	active  map[uuid.UUID]int      // non-terminal instance count by workflow id
	history map[uuid.UUID][]string // all instance states ever recorded by workflow id
	err     error                  // when set, CountActiveByWorkflowID returns it
}

func newFakeActiveCounter() *fakeActiveCounter {
	return &fakeActiveCounter{active: map[uuid.UUID]int{}, history: map[uuid.UUID][]string{}}
}

// seed records an instance in state for workflowID. Non-terminal states increment
// the active count; every state is appended to history (the row is never deleted).
func (c *fakeActiveCounter) seed(workflowID uuid.UUID, state string) {
	c.history[workflowID] = append(c.history[workflowID], state)
	switch state {
	case "completed", "failed", "cancelled":
		// terminal — not active
	default:
		c.active[workflowID]++
	}
}

func (c *fakeActiveCounter) CountActiveByWorkflowID(_ context.Context, workflowID uuid.UUID) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.active[workflowID], nil
}

// deleteSvc wires a service with a registry + active-instance counter, the
// server-mode composition the admin Delete guard requires.
func deleteSvc(t *testing.T) (*workflow.WorkflowService, *fakeRepo, *fakeRegistry, *fakePublishers, *fakeActiveCounter) {
	t.Helper()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	pubs := newFakePublishers()
	counter := newFakeActiveCounter()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)
	svc.SetActiveInstanceCounter(counter)
	return svc, repo, reg, pubs, counter
}

// seedPrivateRegistered creates a private workflow with a stable id and registers
// it, returning the id.
func seedPrivateRegistered(t *testing.T, repo *fakeRepo, reg *fakeRegistry, owner uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	w := wf(name)
	w.ID = id
	repo.bucket(workflow.Private(owner))[name] = w
	require.NoError(t, reg.Register(context.Background(), workflow.RegistryEntry{
		ID: id, Name: name, Scope: workflow.ScopePrivate, OwnerID: owner,
	}))
	return id
}

// --- Failure: delete refused while running ---

// TestService_Delete_RunningInstance_Refused verifies an admin Delete is refused
// with ErrHasRunningInstances when a non-terminal instance carries the workflow
// id, and that nothing (folder or registry row) is removed.
func TestService_Delete_RunningInstance_Refused(t *testing.T) {
	svc, repo, reg, _, counter := deleteSvc(t)
	admin := uuid.New()
	idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
	counter.seed(idA, "running")

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrHasRunningInstances)

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.True(t, folderStill, "folder must remain when delete is refused")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.NoError(t, regErr, "registry row must remain when delete is refused")
}

// --- Happy path: delete removes everything; history preserved ---

// TestService_Delete_RemovesFolderAndRegistry verifies that with no active
// instances an admin Delete removes the folder and the registry row, while a
// previously-completed instance row remains queryable (history preserved).
func TestService_Delete_RemovesFolderAndRegistry(t *testing.T) {
	svc, repo, reg, _, counter := deleteSvc(t)
	admin := uuid.New()
	idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
	counter.seed(idA, "completed") // a past, terminal run

	require.NoError(t, svc.Delete(ctxWith(admin, true), "alpha"))

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.False(t, folderStill, "folder removed after delete")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.ErrorIs(t, regErr, workflow.ErrNotFound, "registry row removed after delete")

	// History preserved: the completed instance row is untouched by Delete.
	assert.Contains(t, counter.history[idA], "completed",
		"past instance/audit rows must be preserved (history)")
}

// --- Error: guard counter returns an error ---

// TestService_Delete_CounterError_Propagates verifies that an error from
// CountActiveByWorkflowID (e.g. the instance store is unreachable) propagates
// from Delete and leaves everything in place. The guard must never silently
// allow a delete when it cannot confirm there are no active runs.
func TestService_Delete_CounterError_Propagates(t *testing.T) {
	svc, repo, reg, _, counter := deleteSvc(t)
	admin := uuid.New()
	idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
	_ = idA

	counterErr := errors.New("instance store unreachable")
	counter.err = counterErr

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.ErrorIs(t, err, counterErr, "counter error must propagate from Delete guard")

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.True(t, folderStill, "folder must remain when the guard returns an error")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.NoError(t, regErr, "registry row must remain when the guard returns an error")
}

// --- Error: registry.Delete fails (step 6) — dangling id ---

// TestService_Delete_RegistryDeleteFails_Surfaced verifies that a failure of
// registry.Delete (step 6, the last step) is surfaced as an error. By that
// point the folder is already gone (step 5 completed), so the registry row
// remains as a dangling id that the operator must reconcile. The error is
// surfaced loudly rather than swallowed.
func TestService_Delete_RegistryDeleteFails_Surfaced(t *testing.T) {
	svc, repo, reg, _, _ := deleteSvc(t)
	admin := uuid.New()
	seedPrivateRegistered(t, repo, reg, admin, "alpha")

	delErr := errors.New("registry write failed")
	reg.failDel = delErr

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.ErrorIs(t, err, delErr, "registry.Delete error must be surfaced from Delete (step 6)")

	// Folder is already gone (step 5 completed before step 6 failed).
	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.False(t, folderStill, "folder is removed before the registry.Delete failure (step 5 ran)")

	// Registry row is still present — dangling id; operator must reconcile.
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.NoError(t, regErr, "registry row remains after step-6 failure (dangling id)")
}

// --- Error: no identity ---

// TestService_Delete_NoIdentity_Forbidden verifies a context with no identity
// is rejected by requireAdmin with ErrForbidden.
func TestService_Delete_NoIdentity_Forbidden(t *testing.T) {
	svc, _, _, _, _ := deleteSvc(t)
	err := svc.Delete(context.Background(), "alpha")
	require.ErrorIs(t, err, workflow.ErrForbidden)
}

// --- Edge: no active-instance counter wired — guard is skipped ---

// TestService_Delete_NoCounter_GuardSkipped verifies that when no
// ActiveInstanceCounter is wired (server mode misconfiguration), the guard is
// skipped and Delete proceeds. The guard is a defence-in-depth check; the
// absence of the counter is treated as "no guard" rather than "block all
// deletes".
func TestService_Delete_NoCounter_GuardSkipped(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)
	// Deliberately omit SetActiveInstanceCounter.

	admin := uuid.New()
	seedPrivateRegistered(t, repo, reg, admin, "alpha")

	require.NoError(t, svc.Delete(ctxWith(admin, true), "alpha"),
		"a nil counter must not block Delete — the guard is skipped")

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.False(t, folderStill, "folder removed when guard is skipped")
}

// --- Error: publication lookup (PublicationOf) returns an error ---

// TestService_Delete_PublicationLookupError_Propagates verifies that when
// published.PublicationOf returns an error (e.g. the publication store is
// temporarily unavailable), Delete propagates the error and leaves both the
// folder and the registry row intact. The guard is fail-closed: a lookup error
// must never be silently treated as "not published."
func TestService_Delete_PublicationLookupError_Propagates(t *testing.T) {
	svc, repo, reg, pubs, _ := deleteSvc(t)
	admin := uuid.New()
	seedPrivateRegistered(t, repo, reg, admin, "alpha")

	lookupErr := errors.New("publication store unreachable")
	pubs.pubLookupErr = lookupErr

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.ErrorIs(t, err, lookupErr, "PublicationOf error must propagate from Delete")

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.True(t, folderStill, "folder must remain when publication lookup fails")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.NoError(t, regErr, "registry row must remain when publication lookup fails")
}

// --- Error: publication delete (published.Delete) fails at step 4 ---

// TestService_Delete_PublicationDeleteError_Propagates verifies that when
// published.Delete fails (step 4), Delete surfaces the error and leaves both
// the folder (step 5) and the registry row (step 6) untouched. Aborting at
// step 4 prevents an orphaned folder that no registry row can locate.
func TestService_Delete_PublicationDeleteError_Propagates(t *testing.T) {
	svc, repo, reg, pubs, _ := deleteSvc(t)
	admin := uuid.New()
	publisher := uuid.New()
	seedPrivateRegistered(t, repo, reg, admin, "alpha")
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", publisher, "src", workflow.VisibilityPublic, ""))

	delErr := errors.New("publication delete failed")
	pubs.deleteErr = delErr

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.ErrorIs(t, err, delErr, "published.Delete error must propagate from Delete (step 4)")

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.True(t, folderStill, "folder must remain when step 4 publication delete fails")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.NoError(t, regErr, "registry row must remain when step 4 fails")
}

// --- Error: registry.List fails inside scopeOf ---

// TestService_Delete_ScopeListFails_Propagates verifies that when registry.List
// fails inside scopeOf (the call that determines which folder scope to remove),
// Delete propagates the error and leaves the folder intact. Without the scope
// nothing can be safely removed.
func TestService_Delete_ScopeListFails_Propagates(t *testing.T) {
	svc, repo, reg, _, _ := deleteSvc(t)
	admin := uuid.New()
	seedPrivateRegistered(t, repo, reg, admin, "alpha")

	listErr := errors.New("registry list unavailable")
	reg.failList = listErr

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.ErrorIs(t, err, listErr, "registry.List error in scopeOf must propagate from Delete")

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.True(t, folderStill, "folder must remain when scopeOf List fails")
}

// --- Error: non-admin rejected ---

// TestService_Delete_NonAdmin_Forbidden verifies a non-admin caller is rejected
// with ErrForbidden and nothing is removed.
func TestService_Delete_NonAdmin_Forbidden(t *testing.T) {
	svc, repo, reg, _, _ := deleteSvc(t)
	owner := uuid.New()
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	err := svc.Delete(ctxWith(owner, false), "alpha")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrForbidden)

	_, folderStill := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, folderStill, "non-admin delete must not remove the folder")
}

// --- Failure: delete a non-existent workflow ---

// TestService_Delete_Unknown_NotFound verifies deleting a name with no registry
// row returns ErrNotFound.
func TestService_Delete_Unknown_NotFound(t *testing.T) {
	svc, _, _, _, _ := deleteSvc(t)
	err := svc.Delete(ctxWith(uuid.New(), true), "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Edge: delete a published/shared workflow clears publication + shared folder ---

// TestService_Delete_PublishedShared_ClearsPublication verifies that deleting a
// shared (published) workflow also removes its publication record and the shared
// folder — the case that is impossible under the shared-read-only rule today.
func TestService_Delete_PublishedShared_ClearsPublication(t *testing.T) {
	svc, repo, reg, pubs, _ := deleteSvc(t)
	admin := uuid.New()
	publisher := uuid.New()

	// A shared, published workflow with a registered id.
	id := uuid.New()
	sw := wf("alpha")
	sw.ID = id
	repo.bucket(workflow.Shared())["alpha"] = sw
	require.NoError(t, reg.Register(context.Background(), workflow.RegistryEntry{
		ID: id, Name: "alpha", Scope: workflow.ScopeShared,
	}))
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", publisher, "src", workflow.VisibilityPublic, ""))

	require.NoError(t, svc.Delete(ctxWith(admin, true), "alpha"))

	_, folderStill := repo.bucket(workflow.Shared())["alpha"]
	assert.False(t, folderStill, "shared folder removed")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.ErrorIs(t, regErr, workflow.ErrNotFound, "registry row removed")
	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.False(t, published, "publication record cleared")
}

// --- Edge: no registry wired (local mode) falls back to private folder delete ---

// TestService_Delete_NoRegistry_LocalMode verifies that without a wired registry
// (the MCP/local composition) Delete still removes the caller's private folder.
// Local mode seeds an admin identity, so requireAdmin passes.
func TestService_Delete_NoRegistry_LocalMode(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil) // no registry, no counter
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["mine"] = wf("mine")

	require.NoError(t, svc.Delete(ctxWith(owner, true), "mine"))
	_, ok := repo.bucket(workflow.Private(owner))["mine"]
	assert.False(t, ok, "local-mode delete removes the private folder")
}
