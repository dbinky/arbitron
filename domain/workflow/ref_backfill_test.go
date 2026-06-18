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

// fakeRefBackfiller is an in-memory ReferenceBackfiller used to probe the
// domain BackfillReferences logic without a real database.
type fakeRefBackfiller struct {
	names    []string            // returned by UnresolvedReferenceNames
	namesErr error               // when set, UnresolvedReferenceNames returns this error
	setIDs   map[string]uuid.UUID // records name→id passed to SetReferenceID
	setErr   error               // when set, ALL SetReferenceID calls return this error
}

func newFakeRefBackfiller(names ...string) *fakeRefBackfiller {
	return &fakeRefBackfiller{
		names:  names,
		setIDs: map[string]uuid.UUID{},
	}
}

func (f *fakeRefBackfiller) UnresolvedReferenceNames(_ context.Context) ([]string, error) {
	return f.names, f.namesErr
}

func (f *fakeRefBackfiller) SetReferenceID(_ context.Context, name string, id uuid.UUID) (int64, error) {
	if f.setErr != nil {
		return 0, f.setErr
	}
	f.setIDs[name] = id
	return 1, nil
}

// --- Happy path ---

// TestBackfillReferences_NilRegistry_NoOp verifies that BackfillReferences is
// a safe no-op when no registry is wired (local/unconfigured deployments).
func TestBackfillReferences_NilRegistry_NoOp(t *testing.T) {
	fb := newFakeRefBackfiller("alpha")
	svc := workflow.NewWorkflowService(nil, nil) // no registry
	svc.SetReferenceBackfiller(fb)

	require.NoError(t, svc.BackfillReferences(context.Background()))
	assert.Empty(t, fb.setIDs, "SetReferenceID must not be called when registry is nil")
}

// TestBackfillReferences_NilBackfiller_NoOp verifies that BackfillReferences
// is a safe no-op when no reference backfiller is wired.
func TestBackfillReferences_NilBackfiller_NoOp(t *testing.T) {
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	// No SetReferenceBackfiller call → nil backfiller.

	require.NoError(t, svc.BackfillReferences(context.Background()))
}

// TestBackfillReferences_ResolvesAndStamps verifies the primary happy path: for
// each name that has a NULL workflow_id, the registry is consulted and the
// stable id is stamped via SetReferenceID.
func TestBackfillReferences_ResolvesAndStamps(t *testing.T) {
	reg := newFakeRegistry()
	fb := newFakeRefBackfiller("alpha", "beta")
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(fb)

	owner := uuid.New()
	ctx := ctxWith(owner, false)

	idA := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: idA, Name: "alpha", Scope: workflow.ScopeShared}))
	idB := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: idB, Name: "beta", Scope: workflow.ScopeShared}))

	require.NoError(t, svc.BackfillReferences(ctx))

	assert.Equal(t, idA, fb.setIDs["alpha"], "alpha must be stamped with its registry id")
	assert.Equal(t, idB, fb.setIDs["beta"], "beta must be stamped with its registry id")
}

// --- Success ---

// TestBackfillReferences_EmptyList_NoOp verifies that BackfillReferences is a
// no-op when UnresolvedReferenceNames returns an empty list (all rows already
// have ids).
func TestBackfillReferences_EmptyList_NoOp(t *testing.T) {
	reg := newFakeRegistry()
	fb := newFakeRefBackfiller() // no names — all rows resolved
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(fb)

	require.NoError(t, svc.BackfillReferences(context.Background()))
	assert.Empty(t, fb.setIDs)
}

// TestBackfillReferences_MixedDanglingAndResolvable verifies that a name that
// no longer exists in the registry (ErrNotFound) is silently skipped while the
// next name is still stamped. The dangling historical label is a non-fatal
// condition; caller should see no error.
func TestBackfillReferences_MixedDanglingAndResolvable(t *testing.T) {
	reg := newFakeRegistry()
	fb := newFakeRefBackfiller("ghost", "real") // "ghost" won't be registered
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(fb)

	owner := uuid.New()
	ctx := ctxWith(owner, false)

	idReal := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: idReal, Name: "real", Scope: workflow.ScopeShared}))

	require.NoError(t, svc.BackfillReferences(ctx), "a dangling name must not abort the backfill")

	_, ghostStamped := fb.setIDs["ghost"]
	assert.False(t, ghostStamped, "ghost must not be stamped — it has no registry row")
	assert.Equal(t, idReal, fb.setIDs["real"], "real must still be stamped despite ghost being dangling")
}

// --- Error ---

// TestBackfillReferences_UnresolvedNamesError_Propagates verifies that a
// storage error from UnresolvedReferenceNames surfaces to the caller.
func TestBackfillReferences_UnresolvedNamesError_Propagates(t *testing.T) {
	reg := newFakeRegistry()
	fb := newFakeRefBackfiller()
	fb.namesErr = errors.New("db unreachable")
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(fb)

	err := svc.BackfillReferences(context.Background())
	require.ErrorIs(t, err, fb.namesErr,
		"UnresolvedReferenceNames error must propagate to the caller")
}

// TestBackfillReferences_ResolveIDError_Propagates verifies that a
// non-ErrNotFound error from registry.ResolveID surfaces to the caller and
// aborts further processing.
func TestBackfillReferences_ResolveIDError_Propagates(t *testing.T) {
	reg := newFakeRegistry()
	fb := newFakeRefBackfiller("alpha", "beta") // two names
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(fb)

	// Inject a non-ErrNotFound registry failure so both ResolveID calls return it.
	registryErr := errors.New("registry unavailable")
	reg.failReg = registryErr // makes Register fail; but ResolveID of an absent name returns ErrNotFound
	// To get a non-ErrNotFound error from ResolveID we need to register "alpha" first,
	// then break the registry. Since fakeRegistry only supports failReg (Register) and
	// not failResolve, we use a thin wrapper.
	brokenReg := &errOnResolveRegistry{inner: reg, resolveErr: registryErr}
	svcBroken := workflow.NewWorkflowService(nil, nil)
	svcBroken.SetRegistry(brokenReg)
	svcBroken.SetReferenceBackfiller(fb)

	err := svcBroken.BackfillReferences(context.Background())
	require.ErrorIs(t, err, registryErr,
		"a non-ErrNotFound ResolveID error must propagate and abort")
	assert.Empty(t, fb.setIDs, "no rows must be stamped when resolve fails")
}

// TestBackfillReferences_SetReferenceIDError_Propagates verifies that an error
// from SetReferenceID surfaces to the caller and aborts further processing.
func TestBackfillReferences_SetReferenceIDError_Propagates(t *testing.T) {
	reg := newFakeRegistry()
	fb := newFakeRefBackfiller("alpha", "beta")
	fb.setErr = errors.New("write failed")
	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(fb)

	owner := uuid.New()
	ctx := ctxWith(owner, false)

	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: uuid.New(), Name: "alpha", Scope: workflow.ScopeShared}))
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{ID: uuid.New(), Name: "beta", Scope: workflow.ScopeShared}))

	err := svc.BackfillReferences(ctx)
	require.ErrorIs(t, err, fb.setErr,
		"SetReferenceID error must propagate to the caller")
}

// --- Helpers ---

// errOnResolveRegistry wraps a WorkflowRegistry and returns a fixed error from
// ResolveID, to test the non-ErrNotFound resolve-failure path without needing
// the real SQLite backend.
type errOnResolveRegistry struct {
	inner      *fakeRegistry
	resolveErr error
}

func (r *errOnResolveRegistry) Register(ctx context.Context, e workflow.RegistryEntry) error {
	return r.inner.Register(ctx, e)
}
func (r *errOnResolveRegistry) Resolve(_ context.Context, _ string) (workflow.RegistryEntry, error) {
	return workflow.RegistryEntry{}, r.resolveErr
}
func (r *errOnResolveRegistry) ResolveID(_ context.Context, _ string) (uuid.UUID, error) {
	return uuid.Nil, r.resolveErr
}
func (r *errOnResolveRegistry) ResolveName(ctx context.Context, id uuid.UUID) (string, error) {
	return r.inner.ResolveName(ctx, id)
}
func (r *errOnResolveRegistry) Rename(ctx context.Context, id uuid.UUID, newName string) error {
	return r.inner.Rename(ctx, id, newName)
}
func (r *errOnResolveRegistry) SetArchived(ctx context.Context, id uuid.UUID, archived bool) error {
	return r.inner.SetArchived(ctx, id, archived)
}
func (r *errOnResolveRegistry) SetOwner(ctx context.Context, id, owner uuid.UUID) error {
	return r.inner.SetOwner(ctx, id, owner)
}
func (r *errOnResolveRegistry) Delete(ctx context.Context, id uuid.UUID) error {
	return r.inner.Delete(ctx, id)
}
func (r *errOnResolveRegistry) List(ctx context.Context) ([]workflow.RegistryEntry, error) {
	return r.inner.List(ctx)
}
