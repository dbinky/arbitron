package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxUserDepts(u uuid.UUID, depts ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Username: "u", Role: identity.RoleUser, Departments: depts,
	})
}

// seedVisInstance puts an instance with explicit owner/visibility/department.
func seedVisInstance(repo *mockInstanceRepo, owner uuid.UUID, vis instance.Visibility, dept string) uuid.UUID {
	id := uuid.New()
	repo.mu.Lock()
	repo.instances[id] = &instance.Instance{
		ID: id, OwnerID: owner, WorkflowName: "wf",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		Visibility: vis, Department: dept,
	}
	repo.mu.Unlock()
	return id
}

// Owner sees their own private run.
func TestList_OwnerSeesOwnPrivate(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	own := seedVisInstance(repo, owner, instance.VisibilityPrivate, "")

	got, err := eng.ListInstances(ctxUserDepts(owner, "engineering"), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, own), "owner sees their own private run")
}

// A department peer (member, not owner) sees a department run in their dept.
func TestList_PeerSeesDepartmentRun(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	peer := uuid.New()
	deptRun := seedVisInstance(repo, owner, instance.VisibilityDepartment, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(peer, "engineering"), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, deptRun), "a member of engineering sees an engineering department run they did not trigger")
}

// A non-member does NOT see another department's department run.
func TestList_NonMemberDoesNotSeeDepartmentRun(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	outsider := uuid.New()
	deptRun := seedVisInstance(repo, owner, instance.VisibilityDepartment, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(outsider, "finance"), instance.ListFilters{})
	require.NoError(t, err)
	assert.False(t, containsID(got, deptRun), "a finance member must not see an engineering department run")
}

// Everyone sees a public run.
func TestList_EveryoneSeesPublic(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	stranger := uuid.New()
	pub := seedVisInstance(repo, owner, instance.VisibilityPublic, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(stranger, "finance"), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, pub), "a public run is visible to everyone")
}

// Admin sees all (no constraint).
func TestList_AdminSeesAll(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	a := seedVisInstance(repo, uuid.New(), instance.VisibilityPrivate, "")
	b := seedVisInstance(repo, uuid.New(), instance.VisibilityDepartment, "engineering")

	got, err := eng.ListInstances(adminCtx(uuid.New()), instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(got, a) && containsID(got, b), "admin sees every run regardless of visibility")
}

// A non-admin does NOT see another user's private run.
func TestList_PeerDoesNotSeeOthersPrivate(t *testing.T) {
	repo := newMockInstanceRepo()
	eng := newListEngine(repo)
	owner := uuid.New()
	other := uuid.New()
	priv := seedVisInstance(repo, owner, instance.VisibilityPrivate, "engineering")

	got, err := eng.ListInstances(ctxUserDepts(other, "engineering"), instance.ListFilters{})
	require.NoError(t, err)
	assert.False(t, containsID(got, priv), "a private run is owner-only even within the same department")
}

func containsID(s []instance.InstanceSummary, id uuid.UUID) bool {
	for _, x := range s {
		if x.ID == id {
			return true
		}
	}
	return false
}
