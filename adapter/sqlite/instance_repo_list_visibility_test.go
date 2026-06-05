package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mkInst(t *testing.T, repo *InstanceRepo, owner uuid.UUID, vis instance.Visibility, dept string) uuid.UUID {
	t.Helper()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "wf",
		WorkflowSnapshot: map[string]any{}, State: instance.StateRunning,
		Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: vis, Department: dept,
	}
	require.NoError(t, repo.Create(context.Background(), inst))
	return inst.ID
}

func listIDs(t *testing.T, repo *InstanceRepo, f instance.ListFilters) map[uuid.UUID]bool {
	t.Helper()
	got, err := repo.List(context.Background(), f)
	require.NoError(t, err)
	out := map[uuid.UUID]bool{}
	for _, s := range got {
		out[s.ID] = true
	}
	return out
}

// The disjunctive filter: owner + public + my-departments, excluding others.
func TestInstanceRepoList_DisjunctiveVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	me := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	other := uuid.New()
	// other must FK-exist; insert directly.
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, 'other', 'user')`, other.String())
	require.NoError(t, err)

	myPrivate := mkInst(t, repo, me, instance.VisibilityPrivate, "")
	othersPrivate := mkInst(t, repo, other, instance.VisibilityPrivate, "engineering")
	engDept := mkInst(t, repo, other, instance.VisibilityDepartment, "engineering")
	finDept := mkInst(t, repo, other, instance.VisibilityDepartment, "finance")
	anyPublic := mkInst(t, repo, other, instance.VisibilityPublic, "platform")

	got := listIDs(t, repo, instance.ListFilters{
		OwnerID:       &me,
		Departments:   []string{"engineering"},
		IncludePublic: true,
		Limit:         100,
	})

	assert.True(t, got[myPrivate], "own private visible")
	assert.True(t, got[engDept], "engineering department run visible")
	assert.True(t, got[anyPublic], "public run visible")
	assert.False(t, got[othersPrivate], "another user's private run hidden")
	assert.False(t, got[finDept], "a finance department run hidden from an engineering member")
}

func TestInstanceRepoList_SurfacesDepartmentVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	id := mkInst(t, repo, owner, instance.VisibilityDepartment, "engineering")

	got, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	var found bool
	for _, s := range got {
		if s.ID == id {
			found = true
			assert.Equal(t, "engineering", s.Department)
			assert.Equal(t, instance.VisibilityDepartment, s.Visibility)
		}
	}
	require.True(t, found)
}

// Admin (no narrowing) sees everything.
func TestInstanceRepoList_AdminUnconstrained(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	a := mkInst(t, repo, owner, instance.VisibilityPrivate, "")
	b := mkInst(t, repo, owner, instance.VisibilityDepartment, "finance")

	got := listIDs(t, repo, instance.ListFilters{Limit: 100}) // no owner/dept/public => all
	assert.True(t, got[a] && got[b], "unconstrained list returns all rows")
}
