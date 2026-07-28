package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compile-time proof that the concrete repo satisfies the narrow link port.
var _ instance.SpawnLinkRepository = (*InstanceRepo)(nil)

// childOf creates a child of parentID in the given state and mode.
func childOf(t *testing.T, repo *InstanceRepo, parentID uuid.UUID, node string,
	mode instance.SpawnMode, state instance.State, owner uuid.UUID, vis instance.Visibility, dept string) uuid.UUID {
	t.Helper()
	c := newLinkedInstance(instance.SpawnLink{ParentInstanceID: parentID, ParentNodeID: node, Mode: mode})
	c.OwnerID = owner
	c.State = state
	c.Visibility = vis
	c.Department = dept
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

// ---------- ChildCountsByParents ----------

func TestChildCounts_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p1 := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p1))
	p2 := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p2))

	childOf(t, repo, p1.ID, "n1", instance.SpawnModeWaitForChild, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, p1.ID, "n2", instance.SpawnModeFireAndForget, instance.StateCompleted, owner, instance.VisibilityPrivate, "")
	childOf(t, repo, p2.ID, "n1", instance.SpawnModeFireAndForget, instance.StateFailed, owner, instance.VisibilityPrivate, "")

	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p1.ID, p2.ID},
		instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err)
	assert.Equal(t, 2, got[p1.ID])
	assert.Equal(t, 1, got[p2.ID])
}

// Absence means zero, not an error — the contract AggregateByInstances sets.
func TestChildCounts_ParentWithNoChildrenIsAbsent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err)
	_, present := got[p.ID]
	assert.False(t, present, "a parent with zero children must be ABSENT from the map, not mapped to 0")
}

// An empty ids slice returns an empty map without touching storage.
func TestChildCounts_EmptyInputTouchesNoStorage(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	got, err := repo.ChildCountsByParents(context.Background(), nil, instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err)
	assert.Empty(t, got)

	// Closing the DB proves no query was issued for the empty input.
	require.NoError(t, db.DB.Close())
	got, err = repo.ChildCountsByParents(context.Background(), []uuid.UUID{},
		instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err, "an empty id slice must short-circuit before any query")
	assert.Empty(t, got)
}

// Counts are POST-AUTHORIZATION: children the caller cannot see are not counted,
// so no expander appears for a run whose children are invisible.
func TestChildCounts_ExcludesInvisibleChildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	viewer := uuid.MustParse(spawnTestOwner)

	otherOwner := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		otherOwner.String(), "other")
	require.NoError(t, err)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	// One child the viewer owns, one private child owned by someone else.
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, viewer, instance.VisibilityPrivate, "")
	childOf(t, repo, p.ID, "n2", instance.SpawnModeFireAndForget, instance.StateRunning, otherOwner, instance.VisibilityPrivate, "")

	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{
		OwnerID:       &viewer,
		IncludePublic: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got[p.ID], "only children the caller may see are counted")
}

func TestChildCounts_AllChildrenInvisibleIsAbsent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	otherOwner := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		otherOwner.String(), "other")
	require.NoError(t, err)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, otherOwner, instance.VisibilityPrivate, "")

	stranger := uuid.New()
	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{OwnerID: &stranger})
	require.NoError(t, err)
	_, present := got[p.ID]
	assert.False(t, present, "a parent whose children are all invisible must report no count (no existence leak)")
}

// A totally zero-scope ChildVisibility (not Unconstrained, no OwnerID, no
// Departments, no IncludePublic) must fail CLOSED — count nothing — rather
// than falling through to an unfiltered count. This is childVisibilityClause's
// "0=1" branch, distinct from the OwnerID-scoped branch the other invisible-
// children tests exercise: a caller with OwnerID set takes the "c.owner_id = ?"
// arm, never the empty-ors arm this test pins down.
func TestChildCounts_ZeroScopeVisibilityFailsClosed(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	// A public child would be visible under IncludePublic, or under an OwnerID
	// match — neither is set here, so a fail-open regression would surface it.
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPublic, "")

	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{})
	require.NoError(t, err)
	_, present := got[p.ID]
	assert.False(t, present, "zero-scope visibility must count nothing, not fall through to unfiltered")
}

func TestChildCounts_DepartmentVisibleChildrenAreCounted(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	otherOwner := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		otherOwner.String(), "other")
	require.NoError(t, err)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning,
		otherOwner, instance.VisibilityDepartment, "engineering")

	stranger := uuid.New()
	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{
		OwnerID:     &stranger,
		Departments: []string{"engineering"},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got[p.ID])
}

// The IncludePublic disjunct's positive arm ("c.visibility = 'public'") was
// never exercised alone: TestChildCounts_ExcludesInvisibleChildren sets
// IncludePublic alongside an OwnerID match that already admits the counted
// child via the "c.owner_id = ?" arm, so a regression that dropped or broke
// the IncludePublic disjunct would still pass every existing test. This
// isolates it: a public child owned by a stranger, with neither OwnerID nor
// Departments matching, is counted purely because IncludePublic is set.
func TestChildCounts_IncludePublicAloneCountsPublicChildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	otherOwner := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		otherOwner.String(), "other")
	require.NoError(t, err)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning,
		otherOwner, instance.VisibilityPublic, "")

	stranger := uuid.New()
	got, err := repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{
		OwnerID:       &stranger,
		IncludePublic: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got[p.ID], "a public child must be counted under IncludePublic even with no owner/department match")
}

// ---------- ListActiveChildren ----------

func TestListActiveChildren_CoversBothModes(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	awaited := childOf(t, repo, p.ID, "n1", instance.SpawnModeWaitForChild, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	fireForget := childOf(t, repo, p.ID, "n2", instance.SpawnModeFireAndForget, instance.StateQueued, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	set := map[uuid.UUID]bool{}
	for _, id := range got {
		set[id] = true
	}
	assert.True(t, set[awaited])
	assert.True(t, set[fireForget],
		"the cascade covers EVERY active child; spawn mode is not a filter on it")
	assert.Len(t, got, 2)
}

func TestListActiveChildren_CoversEveryCancellableState(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	want := map[uuid.UUID]bool{}
	for _, st := range []instance.State{
		instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused,
	} {
		want[childOf(t, repo, p.ID, "n"+string(st), instance.SpawnModeFireAndForget, st, owner, instance.VisibilityPrivate, "")] = true
	}
	for _, st := range []instance.State{
		instance.StateCompleted, instance.StateFailed, instance.StateCancelled,
	} {
		childOf(t, repo, p.ID, "t"+string(st), instance.SpawnModeFireAndForget, st, owner, instance.VisibilityPrivate, "")
	}

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.Len(t, got, 4)
	for _, id := range got {
		assert.True(t, want[id], "terminal children must not be returned as active")
	}
}

func TestListActiveChildren_NoChildrenReturnsEmptySlice(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	assert.NotNil(t, got, "must return an empty slice, not nil")
	assert.Empty(t, got)
}

func TestListActiveChildren_DoesNotReturnGrandchildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	child := childOf(t, repo, p.ID, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")
	grand := childOf(t, repo, child, "n1", instance.SpawnModeFireAndForget, instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListActiveChildren(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, got, 1, "the query is ONE level; the cascade recurses in the domain")
	assert.Equal(t, child, got[0])
	assert.NotEqual(t, grand, got[0])
}

// ---------- error ----------

func TestSpawnQueries_StorageFailureSurfacesAsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	p := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, db.DB.Close())

	_, err := repo.ListActiveChildren(ctx, p.ID)
	require.Error(t, err, "a repository failure must surface as an error, never as an empty result "+
		"indistinguishable from \"no children\"")

	_, err = repo.ChildCountsByParents(ctx, []uuid.UUID{p.ID}, instance.ChildVisibility{Unconstrained: true})
	require.Error(t, err)
}
