package schedule_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// svcRepo is an in-memory ScheduleRepository for service tests. It records the
// filters passed to List so authz-derived filtering can be asserted.
type svcRepo struct {
	*evalRepo
	lastListFilter schedule.ListFilters
}

func newSvcRepo(ss ...*schedule.Schedule) *svcRepo {
	return &svcRepo{evalRepo: newEvalRepo(ss...)}
}

func (r *svcRepo) List(ctx context.Context, f schedule.ListFilters) ([]schedule.Schedule, error) {
	r.lastListFilter = f
	var out []schedule.Schedule
	for _, s := range r.schedules {
		if f.CreatorID != nil && s.CreatorID != *f.CreatorID {
			continue
		}
		out = append(out, *s)
	}
	return out, nil
}

type fakeCanTrigger struct {
	allow bool
	err   error
}

func (f *fakeCanTrigger) CanTrigger(context.Context, identity.Identity, string) (bool, error) {
	return f.allow, f.err
}

func ctxAs(id identity.Identity) context.Context {
	return identity.WithIdentity(context.Background(), id)
}

func user(dept ...string) identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser, Departments: dept}
}

func admin() identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "a", Role: identity.RoleAdmin}
}

func newSvc(repo schedule.ScheduleRepository, ct schedule.CanTrigger) *schedule.Service {
	return schedule.NewService(repo, ct)
}

// --- Happy-path ---

func TestService_Create_AttributesToCaller(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	caller := user()

	bogus := uuid.New()
	got, err := svc.Create(ctxAs(caller), schedule.CreateInput{
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
		CreatorID:    bogus, // must be ignored
	})
	require.NoError(t, err)
	assert.Equal(t, caller.UserID, got.CreatorID, "creator is the caller, never the input")
	assert.NotEqual(t, uuid.Nil, got.ID)
	assert.False(t, got.CreatedAt.IsZero())
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestService_List_NonAdmin_OwnOnly(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	caller := user()
	_, err := svc.List(ctxAs(caller))
	require.NoError(t, err)
	require.NotNil(t, repo.lastListFilter.CreatorID)
	assert.Equal(t, caller.UserID, *repo.lastListFilter.CreatorID)
}

func TestService_List_Admin_All(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	_, err := svc.List(ctxAs(admin()))
	require.NoError(t, err)
	assert.Nil(t, repo.lastListFilter.CreatorID, "admin list is unrestricted")
}

func TestService_SetEnabled_Toggles(t *testing.T) {
	caller := user()
	s := &schedule.Schedule{ID: uuid.New(), WorkflowName: "report", CronExpr: "0 9 * * *", CreatorID: caller.UserID, Enabled: true}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})

	off, err := svc.SetEnabled(ctxAs(caller), s.ID, false)
	require.NoError(t, err)
	assert.False(t, off.Enabled)

	on, err := svc.SetEnabled(ctxAs(caller), s.ID, true)
	require.NoError(t, err)
	assert.True(t, on.Enabled)
}

// --- Input-validation ---

func TestService_Create_InvalidCron_400Sentinel(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	_, err := svc.Create(ctxAs(user()), schedule.CreateInput{WorkflowName: "report", CronExpr: "nope"})
	assert.ErrorIs(t, err, schedule.ErrInvalidCron)
}

func TestService_Create_DepartmentNotMember_Rejected(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	caller := user("sales") // not in "eng"
	_, err := svc.Create(ctxAs(caller), schedule.CreateInput{
		WorkflowName: "report", CronExpr: "0 9 * * *", Department: "eng",
	})
	require.Error(t, err)
	assert.Len(t, repo.schedules, 0, "no write on rejected department")
}

// --- Error-handling / authz ---

func TestService_Create_CannotTriggerWorkflow_Forbidden(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: false})
	_, err := svc.Create(ctxAs(user()), schedule.CreateInput{WorkflowName: "report", CronExpr: "0 9 * * *"})
	assert.ErrorIs(t, err, schedule.ErrNotAuthorizedWorkflow)
	assert.Len(t, repo.schedules, 0)
}

func TestService_Get_NonOwnerNonAdmin_Forbidden(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	_, err := svc.Get(ctxAs(user()), s.ID) // different user
	assert.ErrorIs(t, err, schedule.ErrForbidden)
}

func TestService_Update_NonOwner_Forbidden(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	_, err := svc.Update(ctxAs(user()), s.ID, schedule.UpdateInput{})
	assert.ErrorIs(t, err, schedule.ErrForbidden)
}

func TestService_Delete_NonOwner_Forbidden(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	err := svc.Delete(ctxAs(user()), s.ID)
	assert.ErrorIs(t, err, schedule.ErrForbidden)
}

func TestService_Admin_ManagesAnyones(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	a := admin()

	_, err := svc.Get(ctxAs(a), s.ID)
	require.NoError(t, err)
	_, err = svc.Update(ctxAs(a), s.ID, schedule.UpdateInput{})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctxAs(a), s.ID))
}

func TestService_AnyMethod_NoIdentity_Forbidden(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	ctx := context.Background()
	_, err := svc.Create(ctx, schedule.CreateInput{WorkflowName: "report", CronExpr: "0 9 * * *"})
	assert.ErrorIs(t, err, schedule.ErrForbidden)
	_, err = svc.List(ctx)
	assert.ErrorIs(t, err, schedule.ErrForbidden)
	_, err = svc.Get(ctx, uuid.New())
	assert.ErrorIs(t, err, schedule.ErrForbidden)
}

func TestService_Get_NotFound(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	_, err := svc.Get(ctxAs(admin()), uuid.New())
	assert.ErrorIs(t, err, schedule.ErrNotFound)
}

// --- Edge-cases / boundary ---

func TestService_Update_CannotChangeCreatorOrID(t *testing.T) {
	caller := user()
	origID := uuid.New()
	s := &schedule.Schedule{ID: origID, CreatorID: caller.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})

	newCron := "0 10 * * *"
	got, err := svc.Update(ctxAs(caller), origID, schedule.UpdateInput{CronExpr: &newCron})
	require.NoError(t, err)
	assert.Equal(t, origID, got.ID)
	assert.Equal(t, caller.UserID, got.CreatorID)
	assert.Equal(t, "0 10 * * *", got.CronExpr)
}

func TestService_Update_WorkflowChange_RechecksCanTrigger(t *testing.T) {
	caller := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: caller.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(s)
	// Deny canTrigger; changing the workflow name must be re-gated.
	svc := newSvc(repo, &fakeCanTrigger{allow: false})

	newWF := "secret"
	_, err := svc.Update(ctxAs(caller), s.ID, schedule.UpdateInput{WorkflowName: &newWF})
	assert.ErrorIs(t, err, schedule.ErrNotAuthorizedWorkflow)
}

func TestService_Create_CanTriggerError_Propagates(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{err: errors.New("lookup failed")})
	_, err := svc.Create(ctxAs(user()), schedule.CreateInput{WorkflowName: "report", CronExpr: "0 9 * * *"})
	require.Error(t, err)
	assert.Len(t, repo.schedules, 0)
}
