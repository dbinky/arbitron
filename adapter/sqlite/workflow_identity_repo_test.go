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

// TestWorkflowIdentity_Resolve_ReturnsFullEntry verifies Resolve(name) returns
// the full entry (id, scope, owner, archived) in a single lookup.
func TestWorkflowIdentity_Resolve_ReturnsFullEntry(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	owner := uuid.New()
	e := workflow.RegistryEntry{
		ID:      uuid.New(),
		Name:    "triage",
		Scope:   workflow.ScopePrivate,
		OwnerID: owner,
	}
	require.NoError(t, repo.Register(ctx, e))

	got, err := repo.Resolve(ctx, "triage")
	require.NoError(t, err)
	assert.Equal(t, e.ID, got.ID)
	assert.Equal(t, "triage", got.Name)
	assert.Equal(t, workflow.ScopePrivate, got.Scope)
	assert.Equal(t, owner, got.OwnerID)
	assert.False(t, got.Archived)
}

// TestWorkflowIdentity_Resolve_SharedZeroOwner verifies Resolve maps a shared
// entry's NULL owner column back to the zero UUID.
func TestWorkflowIdentity_Resolve_SharedZeroOwner(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	e := workflow.RegistryEntry{ID: uuid.New(), Name: "shared-wf", Scope: workflow.ScopeShared}
	require.NoError(t, repo.Register(ctx, e))

	got, err := repo.Resolve(ctx, "shared-wf")
	require.NoError(t, err)
	assert.Equal(t, workflow.ScopeShared, got.Scope)
	assert.Equal(t, uuid.Nil, got.OwnerID, "shared entry resolves to a zero owner")
}

// TestWorkflowIdentity_Resolve_UnknownNotFound verifies an unknown name maps to
// workflow.ErrNotFound.
func TestWorkflowIdentity_Resolve_UnknownNotFound(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	_, err := repo.Resolve(ctx, "ghost")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
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
// held by another row returns ErrAlreadyExists (duplicate-rename edge). Both the
// source (b) and the target (a) must be left completely intact — the failed rename
// must not mutate either row.
func TestWorkflowIdentity_RenameToExistingName(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	a := workflow.RegistryEntry{ID: uuid.New(), Name: "a", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	b := workflow.RegistryEntry{ID: uuid.New(), Name: "b", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, a))
	require.NoError(t, repo.Register(ctx, b))

	err := repo.Rename(ctx, b.ID, "a")
	assert.ErrorIs(t, err, workflow.ErrAlreadyExists)

	// b keeps its original name.
	gotBName, err := repo.ResolveName(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, "b", gotBName, "source entry (b) is intact after failed rename")

	// a is also intact — its id is unchanged.
	gotAID, err := repo.ResolveID(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, a.ID, gotAID, "target entry (a) is intact after failed rename")
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

// TestWorkflowIdentity_IsArchived verifies the engine archive-check seam reflects
// the SetArchived state and maps an unknown name to ErrNotFound.
func TestWorkflowIdentity_IsArchived(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	e := workflow.RegistryEntry{ID: uuid.New(), Name: "wf", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, e))

	got, err := repo.IsArchived(ctx, "wf")
	require.NoError(t, err)
	assert.False(t, got, "freshly registered workflow is not archived")

	require.NoError(t, repo.SetArchived(ctx, e.ID, true))
	got, err = repo.IsArchived(ctx, "wf")
	require.NoError(t, err)
	assert.True(t, got, "archived flag reflected")

	_, err = repo.IsArchived(ctx, "missing")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// TestWorkflowIdentity_SetArchivedToggle verifies the archive flag can be
// toggled: true → false → true, with IsArchived reflecting each transition.
// The unarchive direction (SetArchived false) is distinct from the archive
// direction and must be explicitly exercised.
func TestWorkflowIdentity_SetArchivedToggle(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)
	e := workflow.RegistryEntry{ID: uuid.New(), Name: "wf", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, e))

	// Archive.
	require.NoError(t, repo.SetArchived(ctx, e.ID, true))
	got, err := repo.IsArchived(ctx, "wf")
	require.NoError(t, err)
	assert.True(t, got, "archived after SetArchived(true)")

	// Unarchive.
	require.NoError(t, repo.SetArchived(ctx, e.ID, false))
	got, err = repo.IsArchived(ctx, "wf")
	require.NoError(t, err)
	assert.False(t, got, "unarchived after SetArchived(false)")

	// Re-archive (idempotent second archive).
	require.NoError(t, repo.SetArchived(ctx, e.ID, true))
	got, err = repo.IsArchived(ctx, "wf")
	require.NoError(t, err)
	assert.True(t, got, "re-archived after second SetArchived(true)")
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

// TestWorkflowIdentity_ConcurrentRenameSameTarget verifies that when two goroutines
// race to rename different workflows to the same target name, exactly one wins and
// the other gets ErrAlreadyExists. After the race, no entries are lost or
// duplicated: the total count is still 2, and "gamma" resolves to exactly one of
// the original IDs.
func TestWorkflowIdentity_ConcurrentRenameSameTarget(t *testing.T) {
	repo, ctx := newWorkflowIdentityRepo(t)

	alpha := workflow.RegistryEntry{ID: uuid.New(), Name: "alpha", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	beta := workflow.RegistryEntry{ID: uuid.New(), Name: "beta", Scope: workflow.ScopePrivate, OwnerID: uuid.New()}
	require.NoError(t, repo.Register(ctx, alpha))
	require.NoError(t, repo.Register(ctx, beta))

	const goroutines = 2
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	ids := []uuid.UUID{alpha.ID, beta.ID}
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = repo.Rename(ctx, ids[i], "gamma")
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
	assert.Equal(t, 1, ok, "exactly one rename must win")
	assert.Equal(t, goroutines-1, dup, "the loser must get ErrAlreadyExists")

	// "gamma" exists and maps to one of the original IDs.
	gammaID, err := repo.ResolveID(ctx, "gamma")
	require.NoError(t, err)
	assert.True(t, gammaID == alpha.ID || gammaID == beta.ID,
		"gamma must resolve to one of the original workflow IDs")

	// No entries were lost or duplicated.
	all, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2, "total entry count unchanged after concurrent rename")
}
