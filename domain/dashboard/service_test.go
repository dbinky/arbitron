package dashboard_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/dashboard"
	"github.com/arbitron/arbitron/domain/identity"
)

// fakeRepo records the AggregateParams it was called with and returns canned data.
type fakeRepo struct {
	gotParams dashboard.AggregateParams
	stats     dashboard.Stats
	err       error
}

func (f *fakeRepo) Aggregate(_ context.Context, p dashboard.AggregateParams) (dashboard.Stats, error) {
	f.gotParams = p
	return f.stats, f.err
}

func ctxWith(id identity.Identity) context.Context {
	return identity.WithIdentity(context.Background(), id)
}

func member(depts ...string) identity.Identity {
	return identity.Identity{
		UserID:      uuid.New(),
		Username:    "m",
		Role:        identity.RoleUser,
		Departments: depts,
	}
}

func admin() identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "a", Role: identity.RoleAdmin}
}

func TestGetStats_ClampsRequestedToCallerDepartments(t *testing.T) {
	repo := &fakeRepo{stats: dashboard.Stats{Departments: []string{"engineering"}}}
	svc := dashboard.NewService(repo)

	// Caller is in engineering only but requests engineering + sales.
	out, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{
		Departments: []string{"engineering", "sales"},
		Window:      24 * time.Hour,
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"engineering"}, repo.gotParams.Departments,
		"foreign department must be dropped before querying")
	assert.Equal(t, []string{"engineering"}, out.Departments)
}

func TestGetStats_UnionDefaultWhenNoDepartmentsRequested(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(member("engineering", "sales")), dashboard.Request{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"engineering", "sales"}, repo.gotParams.Departments,
		"omitted departments ⇒ caller's full effective set")
}

// PR #21 Must Fix (admin zero-state): an admin who specifies NO departments
// (first load) must get the WHOLE-ORG aggregate — signaled by AllDepartments —
// not an all-zeros dashboard. The "all" and "none" cases are distinct signals.
func TestGetStats_AdminEmptyRequest_SignalsAllDepartments(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(admin()), dashboard.Request{})
	require.NoError(t, err)
	assert.True(t, repo.gotParams.AllDepartments,
		"an admin with an empty request must signal all departments (whole-org aggregate)")
	assert.Empty(t, repo.gotParams.Departments,
		"the department list is unused when AllDepartments is set")
}

// An admin with ≤1 of their own departments still gets data on first load,
// because the empty request signals AllDepartments (the selector-less zero-state
// can no longer trap the flagship admin).
func TestGetStats_AdminWithOneDept_EmptyRequest_StillAllDepartments(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	adminOneDept := identity.Identity{
		UserID: uuid.New(), Username: "a", Role: identity.RoleAdmin,
		Departments: []string{"engineering"},
	}
	_, err := svc.GetStats(ctxWith(adminOneDept), dashboard.Request{})
	require.NoError(t, err)
	assert.True(t, repo.gotParams.AllDepartments,
		"admin first-load is whole-org regardless of how many departments they belong to")
}

// A non-admin NEVER gets the all-departments signal.
func TestGetStats_NonAdminEmptyRequest_NotAllDepartments(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{})
	require.NoError(t, err)
	assert.False(t, repo.gotParams.AllDepartments, "a non-admin must never get the whole-org signal")
}

// An admin who DID request specific departments is scoped to those (no all signal).
func TestGetStats_AdminExplicitRequest_NotAllDepartments(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(admin()), dashboard.Request{Departments: []string{"finance"}})
	require.NoError(t, err)
	assert.False(t, repo.gotParams.AllDepartments,
		"an admin who named departments is scoped to them, not the whole org")
	assert.Equal(t, []string{"finance"}, repo.gotParams.Departments)
}

func TestGetStats_AdminMayRequestAnyDepartment(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(admin()), dashboard.Request{
		Departments: []string{"finance", "legal"},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"finance", "legal"}, repo.gotParams.Departments,
		"admin requests are not clamped")
}

func TestGetStats_NonMemberRequestReducesToEmpty(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)

	_, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{
		Departments: []string{"sales"},
	})
	require.NoError(t, err)
	assert.Empty(t, repo.gotParams.Departments,
		"requesting only foreign departments yields an empty allowed set")
}

func TestGetStats_WindowClamping(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero defaults", 0, dashboard.DefaultWindow},
		{"negative defaults", -5 * time.Hour, dashboard.DefaultWindow},
		{"below min clamps up", 30 * time.Minute, dashboard.MinWindow},
		{"above max clamps down", 90 * 24 * time.Hour, dashboard.MaxWindow},
		{"in range preserved", 6 * time.Hour, 6 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := dashboard.NewService(repo)
			_, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{Window: tc.in})
			require.NoError(t, err)
			assert.Equal(t, tc.want, repo.gotParams.Window)
		})
	}
}

func TestGetStats_PropagatesRepoError(t *testing.T) {
	repo := &fakeRepo{err: context.DeadlineExceeded}
	svc := dashboard.NewService(repo)
	_, err := svc.GetStats(ctxWith(member("engineering")), dashboard.Request{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestGetStats_PanicsWithoutIdentity(t *testing.T) {
	repo := &fakeRepo{}
	svc := dashboard.NewService(repo)
	assert.Panics(t, func() {
		_, _ = svc.GetStats(context.Background(), dashboard.Request{})
	}, "missing identity is a programming error (route bypassed auth middleware)")
}
