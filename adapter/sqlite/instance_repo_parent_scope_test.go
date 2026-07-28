package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parentScopeFixture creates one root with two children and one unrelated root.
// It returns (rootA, childA1, childA2, rootB).
func parentScopeFixture(t *testing.T, repo *InstanceRepo) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	rootA := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, rootA))
	rootB := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, rootB))

	c1 := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: rootA.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeWaitForChild,
	})
	require.NoError(t, repo.Create(ctx, c1))
	c2 := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: rootA.ID, ParentNodeID: "spawn-2", Mode: instance.SpawnModeFireAndForget,
	})
	require.NoError(t, repo.Create(ctx, c2))

	return rootA.ID, c1.ID, c2.ID, rootB.ID
}

func idsOf(summaries []instance.InstanceSummary) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, s := range summaries {
		out[s.ID] = true
	}
	return out
}

// ---------- happy path ----------

func TestList_ParentScopeRoots_ReturnsOnlyRoots(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, c2, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeRoots, Limit: 100,
	})
	require.NoError(t, err)
	ids := idsOf(got)
	assert.True(t, ids[rootA], "rootA must be listed")
	assert.True(t, ids[rootB], "rootB must be listed")
	assert.False(t, ids[c1], "a child must not appear in the roots-only list")
	assert.False(t, ids[c2], "a child must not appear in the roots-only list")
}

func TestList_ParentScopeChildren_ReturnsOnlyThatParentsChildren(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, c2, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, ParentInstanceID: &rootA, Limit: 100,
	})
	require.NoError(t, err)
	ids := idsOf(got)
	assert.True(t, ids[c1])
	assert.True(t, ids[c2])
	assert.False(t, ids[rootA], "the parent itself must not be in its own child list")
	assert.False(t, ids[rootB])
	assert.Len(t, got, 2)
}

func TestList_ParentScopeAny_ReturnsEverything(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, c2, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	ids := idsOf(got)
	for _, id := range []uuid.UUID{rootA, c1, c2, rootB} {
		assert.True(t, ids[id], "unset parent scope must do no narrowing (missing %s)", id)
	}
}

// ---------- success: summary carries the link ----------

func TestList_SummaryCarriesParentInstanceID(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	rootA, c1, _, _ := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	for _, s := range got {
		switch s.ID {
		case c1:
			require.NotNil(t, s.ParentInstanceID, "a child summary must carry its parent id")
			assert.Equal(t, rootA, *s.ParentInstanceID)
		case rootA:
			assert.Nil(t, s.ParentInstanceID, "a root summary must carry a nil parent id")
		}
	}
}

// ---------- edge cases ----------

// The parent selector is a NARROWING and must compose with — never override —
// the service-set owner constraint. A caller scoped to owner X asking for the
// children of a run owned by Y sees nothing.
func TestList_ParentScopeCannotOverrideOwnerConstraint(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	rootA, _, _, _ := parentScopeFixture(t, repo)

	otherOwner := uuid.New()
	got, err := repo.List(ctx, instance.ListFilters{
		OwnerID:          &otherOwner, // service-set: a different user
		ParentScope:      instance.ParentScopeChildren,
		ParentInstanceID: &rootA,
		Limit:            100,
	})
	require.NoError(t, err)
	assert.Empty(t, got, "a parent selector must not widen past the service-set owner constraint")
}

// A parent scope of "children" with no parent id is a caller bug; return an
// empty page rather than silently degrading to an unfiltered list.
func TestList_ParentScopeChildrenWithNoParentID_ReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, Limit: 100,
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestList_ParentWithNoChildren_ReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	_, _, _, rootB := parentScopeFixture(t, repo)

	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, ParentInstanceID: &rootB, Limit: 100,
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestList_UnknownParentID_ReturnsEmptyNotError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parentScopeFixture(t, repo)

	unknown := uuid.New()
	got, err := repo.List(context.Background(), instance.ListFilters{
		ParentScope: instance.ParentScopeChildren, ParentInstanceID: &unknown, Limit: 100,
	})
	require.NoError(t, err, "naming a run the caller cannot see must not leak via an error")
	assert.Empty(t, got)
}

// Existing behaviour must be preserved by the rewrite: state, workflowName,
// since, limit/offset, and the department disjunction.
func TestList_ExistingFiltersStillApplyAfterRewrite(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	a := newLinkedInstance(instance.SpawnLink{})
	a.WorkflowName = "alpha"
	require.NoError(t, repo.Create(ctx, a))

	b := newLinkedInstance(instance.SpawnLink{})
	b.WorkflowName = "beta"
	b.State = instance.StateCompleted
	require.NoError(t, repo.Create(ctx, b))

	name := "alpha"
	got, err := repo.List(ctx, instance.ListFilters{WorkflowName: &name, Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, a.ID, got[0].ID)

	st := instance.StateCompleted
	got, err = repo.List(ctx, instance.ListFilters{State: &st, Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, b.ID, got[0].ID)

	got, err = repo.List(ctx, instance.ListFilters{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, got, 1, "limit must still apply")
}

func TestList_DepartmentDisjunctionStillApplies(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	dept := newLinkedInstance(instance.SpawnLink{})
	dept.Visibility = instance.VisibilityDepartment
	dept.Department = "engineering"
	require.NoError(t, repo.Create(ctx, dept))

	priv := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, priv))

	stranger := uuid.New()
	got, err := repo.List(ctx, instance.ListFilters{
		OwnerID: &stranger, Departments: []string{"engineering"}, Limit: 100,
	})
	require.NoError(t, err)
	ids := idsOf(got)
	assert.True(t, ids[dept.ID], "a department-visible run must be listed for a member")
	assert.False(t, ids[priv.ID], "a private run of another owner must not be listed")
}
