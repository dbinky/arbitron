package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWorkflowIdentityRepo builds the repo over a freshly migrated in-memory DB.
func newWorkflowIdentityRepo(t *testing.T) (*WorkflowIdentityRepo, context.Context) {
	t.Helper()
	db := setupTestDB(t)
	return NewWorkflowIdentityRepo(db.DB), context.Background()
}

// --- Happy path ------------------------------------------------------------

// TestWorkflowIdentity_RegisterThenResolve verifies a registered entry round-trips:
// ResolveID(name) returns the id, ResolveName(id) returns the name.
func TestWorkflowIdentity_RegisterThenResolve(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	owner := uuid.New()
	e := workflow.RegistryEntry{
		ID:      uuid.New(),
		Name:    "triage",
		Scope:   workflow.ScopePrivate,
		OwnerID: owner,
	}
	require.NoError(t, repo.Register(ctx, e))

	gotID, err := repo.ResolveID(ctx, "triage")
	require.NoError(t, err)
	assert.Equal(t, e.ID, gotID)

	gotName, err := repo.ResolveName(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, "triage", gotName)
}

// --- Success ---------------------------------------------------------------

// TestWorkflowIdentity_ListReturnsEntries verifies List returns the registered
// rows with their fields intact (including a zero owner for a shared entry).
func TestWorkflowIdentity_ListReturnsEntries(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	priv := workflow.RegistryEntry{ID: uuid.New(), Name: "priv-wf", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	shared := workflow.RegistryEntry{ID: uuid.New(), Name: "shared-wf", Scope: workflow.ScopeShared}
	require.NoError(t, repo.Register(ctx, priv))
	require.NoError(t, repo.Register(ctx, shared))

	got, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)

	byName := map[string]workflow.RegistryEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	assert.Equal(t, priv.ID, byName["priv-wf"].ID)
	assert.Equal(t, priv.OwnerID, byName["priv-wf"].OwnerID)
	assert.Equal(t, workflow.ScopePrivate, byName["priv-wf"].Scope)
	assert.Equal(t, shared.ID, byName["shared-wf"].ID)
	assert.Equal(t, uuid.Nil, byName["shared-wf"].OwnerID, "shared entry has a zero owner")
	assert.Equal(t, workflow.ScopeShared, byName["shared-wf"].Scope)
}

// TestWorkflowIdentity_RenameThenResolve verifies a rename updates the name↔id
// mapping: the old name no longer resolves, the new one resolves to the same id.
func TestWorkflowIdentity_RenameThenResolve(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	e := workflow.RegistryEntry{ID: uuid.New(), Name: "old-name", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, e))

	require.NoError(t, repo.Rename(ctx, e.ID, "new-name"))

	gotID, err := repo.ResolveID(ctx, "new-name")
	require.NoError(t, err)
	assert.Equal(t, e.ID, gotID)

	_, err = repo.ResolveID(ctx, "old-name")
	assert.ErrorIs(t, err, workflow.ErrNotFound)

	gotName, err := repo.ResolveName(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-name", gotName)
}

// TestWorkflowIdentity_SetArchivedAndOwner verifies the archived flag and owner
// reassignment persist and are reflected by List.
func TestWorkflowIdentity_SetArchivedAndOwner(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	e := workflow.RegistryEntry{ID: uuid.New(), Name: "wf", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, e))

	require.NoError(t, repo.SetArchived(ctx, e.ID, true))
	newOwner := uuid.New()
	require.NoError(t, repo.SetOwner(ctx, e.ID, newOwner))

	got, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].Archived)
	assert.Equal(t, newOwner, got[0].OwnerID)
}

// TestWorkflowIdentity_Delete verifies a deleted entry no longer resolves.
func TestWorkflowIdentity_Delete(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	e := workflow.RegistryEntry{ID: uuid.New(), Name: "wf", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, e))

	require.NoError(t, repo.Delete(ctx, e.ID))

	_, err := repo.ResolveID(ctx, "wf")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Failure ---------------------------------------------------------------

// TestWorkflowIdentity_RegisterDuplicateName verifies UNIQUE(name) is the
// authoritative global-uniqueness rule: a second registration of the same name
// (even with a different id) returns ErrAlreadyExists.
func TestWorkflowIdentity_RegisterDuplicateName(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	require.NoError(t, repo.Register(ctx, workflow.RegistryEntry{
		ID: uuid.New(), Name: "dup", Scope: workflow.ScopePrivate, OwnerID: uuid.New(),
	}))

	err := repo.Register(ctx, workflow.RegistryEntry{
		ID: uuid.New(), Name: "dup", Scope: workflow.ScopeShared,
	})
	assert.ErrorIs(t, err, workflow.ErrAlreadyExists)
}

// TestWorkflowIdentity_RenameToExistingName verifies Rename onto a name already
// held by another row returns ErrAlreadyExists (duplicate-rename edge).
func TestWorkflowIdentity_RenameToExistingName(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	a := workflow.RegistryEntry{ID: uuid.New(), Name: "a", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	b := workflow.RegistryEntry{ID: uuid.New(), Name: "b", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, a))
	require.NoError(t, repo.Register(ctx, b))

	err := repo.Rename(ctx, b.ID, "a")
	assert.ErrorIs(t, err, workflow.ErrAlreadyExists)

	// b keeps its original name.
	gotName, err := repo.ResolveName(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, "b", gotName)
}

// --- Error -----------------------------------------------------------------

// TestWorkflowIdentity_ResolveIDMissing verifies an unknown name maps to ErrNotFound.
func TestWorkflowIdentity_ResolveIDMissing(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	_, err := repo.ResolveID(ctx, "missing")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// TestWorkflowIdentity_ResolveNameMissing verifies an unknown id maps to ErrNotFound.
func TestWorkflowIdentity_ResolveNameMissing(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	_, err := repo.ResolveName(ctx, uuid.New())
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// TestWorkflowIdentity_MutateMissing verifies Rename/SetArchived/SetOwner/Delete
// of an unknown id map to ErrNotFound rather than silently succeeding.
func TestWorkflowIdentity_MutateMissing(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	id := uuid.New()
	assert.ErrorIs(t, repo.Rename(ctx, id, "x"), workflow.ErrNotFound)
	assert.ErrorIs(t, repo.SetArchived(ctx, id, true), workflow.ErrNotFound)
	assert.ErrorIs(t, repo.SetOwner(ctx, id, uuid.New()), workflow.ErrNotFound)
	assert.ErrorIs(t, repo.Delete(ctx, id), workflow.ErrNotFound)
}

// --- Edge ------------------------------------------------------------------

// TestWorkflowIdentity_ConcurrentRegisterSameName verifies the UNIQUE(name)
// constraint arbitrates a race: two goroutines registering the same name —
// exactly one succeeds, the other gets ErrAlreadyExists.
func TestWorkflowIdentity_ConcurrentRegisterSameName(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = repo.Register(ctx, workflow.RegistryEntry{
				ID: uuid.New(), Name: "race", Scope: workflow.ScopePrivate, OwnerID: uuid.New(),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, dup int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, workflow.ErrAlreadyExists):
			dup++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, ok, "exactly one registration must win")
	assert.Equal(t, goroutines-1, dup, "all losers must get ErrAlreadyExists")

	// The winner is resolvable.
	_, err := repo.ResolveID(ctx, "race")
	assert.NoError(t, err)
}
