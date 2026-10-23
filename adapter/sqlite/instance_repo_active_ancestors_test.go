package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
)

var adaMe = uuid.MustParse("00000000-0000-0000-0000-000000000001") // seeded by migration 001

func adaUser(t *testing.T, db testDB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`, id.String(), "u-"+id.String()[:8])
	require.NoError(t, err)
	return id
}

// adaInst creates a run with an optional parent link and moves it to state.
func adaInst(t *testing.T, repo *InstanceRepo, owner uuid.UUID, vis instance.Visibility, state instance.State, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	inst := &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: map[string]any{},
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: vis,
	}
	if parent != nil {
		p := *parent
		n := "spawn-1"
		inst.ParentInstanceID = &p
		inst.ParentNodeID = &n
		inst.SpawnMode = instance.SpawnModeFireAndForget
	}
	require.NoError(t, repo.Create(ctx, inst))
	if state != instance.StateQueued {
		require.NoError(t, repo.UpdateState(ctx, inst.ID, state))
	}
	return inst.ID
}

func adaList(t *testing.T, repo *InstanceRepo, f instance.ListFilters) map[uuid.UUID]bool {
	t.Helper()
	f.ActiveDescendantAncestors = true
	if f.Limit == 0 {
		f.Limit = 100
	}
	got, err := repo.List(context.Background(), f)
	require.NoError(t, err)
	out := map[uuid.UUID]bool{}
	for _, s := range got {
		out[s.ID] = true
	}
	return out
}

func TestList_ActiveDescendantAncestors_ReturnsTerminalAncestorsOfActiveWork(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	r := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	c := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, &r)
	g := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &c)
	unrelated := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	activeRoot := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateWaiting, &activeRoot)
	doneRoot := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateFailed, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCancelled, &doneRoot)

	got := adaList(t, repo, instance.ListFilters{Unconstrained: true})

	assert.Equal(t, map[uuid.UUID]bool{r: true, c: true}, got,
		"every terminal ancestor of active work, at any depth, and nothing else")
	assert.False(t, got[g], "the active descendant itself is not an ancestor row")
	assert.False(t, got[unrelated])
	assert.False(t, got[activeRoot], "an active parent is already in the active sections")
	assert.False(t, got[doneRoot], "a terminal parent whose descendants are all terminal is not discovered")
}

func TestList_ActiveDescendantAncestors_EveryActiveStateCounts(t *testing.T) {
	for _, st := range []instance.State{instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused} {
		t.Run(string(st), func(t *testing.T) {
			db := setupTestDB(t)
			repo := NewInstanceRepo(db.DB)
			p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
			adaInst(t, repo, adaMe, instance.VisibilityPrivate, st, &p)
			assert.True(t, adaList(t, repo, instance.ListFilters{Unconstrained: true})[p])
		})
	}
}

func TestList_ActiveDescendantAncestors_ComposesWithRootsScope(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	r := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	c := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, &r)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &c)

	got := adaList(t, repo, instance.ListFilters{Unconstrained: true, ParentScope: instance.ParentScopeRoots})

	assert.Equal(t, map[uuid.UUID]bool{r: true}, got)
}

func TestList_ActiveDescendantAncestors_DescendantMustBeVisible(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	other := adaUser(t, db)
	mine := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, other, instance.VisibilityPrivate, instance.StateRunning, &mine)

	got := adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, IncludePublic: true})
	assert.False(t, got[mine], "active work the caller cannot see must not reveal itself through its parent")

	pub := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, other, instance.VisibilityPublic, instance.StateRunning, &pub)
	got = adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, IncludePublic: true})
	assert.True(t, got[pub], "a visible (public) active descendant discovers the caller's parent")
}

func TestList_ActiveDescendantAncestors_AncestorMustBeVisible(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	other := adaUser(t, db)
	theirs := adaInst(t, repo, other, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &theirs)

	got := adaList(t, repo, instance.ListFilters{OwnerID: &adaMe})
	assert.False(t, got[theirs], "the ancestor row itself is still subject to the visibility predicate")
}

func TestList_ActiveDescendantAncestors_DepartmentScopeAppliesToDescendant(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	other := adaUser(t, db)
	p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	child := &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(), OwnerID: other, WorkflowName: "wf", WorkflowSnapshot: map[string]any{},
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: instance.VisibilityDepartment, Department: "engineering",
	}
	pid, node := p, "spawn-1"
	child.ParentInstanceID, child.ParentNodeID, child.SpawnMode = &pid, &node, instance.SpawnModeFireAndForget
	require.NoError(t, repo.Create(context.Background(), child))
	require.NoError(t, repo.UpdateState(context.Background(), child.ID, instance.StateRunning))

	assert.True(t, adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, Departments: []string{"engineering"}})[p])
	assert.False(t, adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, Departments: []string{"finance"}})[p])
}

func TestList_ActiveDescendantAncestors_FailsClosedWithoutScope(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &p)

	assert.Empty(t, adaList(t, repo, instance.ListFilters{}), "no service-set scope admits nothing")
}

func TestList_ActiveDescendantAncestors_TerminatesOnCorruptParentCycle(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	x := uuid.New()
	y := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, &x)
	xInst := &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               x, OwnerID: adaMe, WorkflowName: "wf", WorkflowSnapshot: map[string]any{},
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: instance.VisibilityPrivate,
	}
	ypid, node := y, "spawn-1"
	xInst.ParentInstanceID, xInst.ParentNodeID, xInst.SpawnMode = &ypid, &node, instance.SpawnModeFireAndForget
	require.NoError(t, repo.Create(context.Background(), xInst))
	require.NoError(t, repo.UpdateState(context.Background(), x, instance.StateCompleted))
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &x)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := repo.List(ctx, instance.ListFilters{Unconstrained: true, ActiveDescendantAncestors: true, Limit: 100})
	require.NoError(t, err)
	ids := map[uuid.UUID]bool{}
	for _, s := range got {
		ids[s.ID] = true
	}
	assert.Equal(t, map[uuid.UUID]bool{x: true, y: true}, ids)
}

func TestList_ActiveDescendantAncestors_OffByDefault(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	lonely := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	st := instance.StateCompleted

	got, err := repo.List(context.Background(), instance.ListFilters{Unconstrained: true, State: &st, Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, lonely, got[0].ID, "without the flag the list is unchanged")
}

func TestList_ActiveDescendantAncestors_HonoursLimit(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	for i := 0; i < 3; i++ {
		p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
		adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &p)
	}
	got, err := repo.List(context.Background(), instance.ListFilters{Unconstrained: true, ActiveDescendantAncestors: true, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}
