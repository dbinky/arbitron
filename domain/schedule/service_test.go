package schedule_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestService_ListForWorkflow covers the optional workflow-name narrowing on
// List. The creator scoping stays service-set from identity; WorkflowName is a
// non-security narrowing filter only.
func TestService_ListForWorkflow(t *testing.T) {
	t.Run("admin with workflow narrows to workflow, no creator scope", func(t *testing.T) {
		repo := newSvcRepo()
		svc := newSvc(repo, &fakeCanTrigger{allow: true})
		_, err := svc.List(ctxAs(admin()), schedule.ListOptions{WorkflowName: "wf-a"})
		require.NoError(t, err)
		assert.Nil(t, repo.lastListFilter.CreatorID, "admin list is unrestricted")
		require.NotNil(t, repo.lastListFilter.WorkflowName)
		assert.Equal(t, "wf-a", *repo.lastListFilter.WorkflowName)
	})

	t.Run("non-admin with workflow keeps creator scope and adds workflow", func(t *testing.T) {
		repo := newSvcRepo()
		svc := newSvc(repo, &fakeCanTrigger{allow: true})
		caller := user()
		_, err := svc.List(ctxAs(caller), schedule.ListOptions{WorkflowName: "wf-a"})
		require.NoError(t, err)
		require.NotNil(t, repo.lastListFilter.CreatorID)
		assert.Equal(t, caller.UserID, *repo.lastListFilter.CreatorID, "creator scope still service-set")
		require.NotNil(t, repo.lastListFilter.WorkflowName)
		assert.Equal(t, "wf-a", *repo.lastListFilter.WorkflowName)
	})

	t.Run("no options is back-compatible: creator scope only, no workflow", func(t *testing.T) {
		repo := newSvcRepo()
		svc := newSvc(repo, &fakeCanTrigger{allow: true})
		caller := user()
		_, err := svc.List(ctxAs(caller))
		require.NoError(t, err)
		require.NotNil(t, repo.lastListFilter.CreatorID)
		assert.Equal(t, caller.UserID, *repo.lastListFilter.CreatorID)
		assert.Nil(t, repo.lastListFilter.WorkflowName)
	})

	t.Run("empty workflow name means no workflow filter", func(t *testing.T) {
		repo := newSvcRepo()
		svc := newSvc(repo, &fakeCanTrigger{allow: true})
		caller := user()
		_, err := svc.List(ctxAs(caller), schedule.ListOptions{WorkflowName: ""})
		require.NoError(t, err)
		require.NotNil(t, repo.lastListFilter.CreatorID)
		assert.Equal(t, caller.UserID, *repo.lastListFilter.CreatorID)
		assert.Nil(t, repo.lastListFilter.WorkflowName, "empty == no filter")
	})

	t.Run("no identity is forbidden and never leaks", func(t *testing.T) {
		repo := newSvcRepo()
		svc := newSvc(repo, &fakeCanTrigger{allow: true})
		_, err := svc.List(context.Background(), schedule.ListOptions{WorkflowName: "wf-a"})
		assert.ErrorIs(t, err, schedule.ErrForbidden)
	})
}

// Edge: List accepts a variadic for 0-or-1 opts; only opts[0] is ever
// consulted. Passing two opts must silently ignore the second — documents the
// 0-or-1 contract so callers know extra opts are not additive.
func TestService_List_ExtraOptsIgnored(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	caller := user()
	_, err := svc.List(ctxAs(caller),
		schedule.ListOptions{WorkflowName: "wf-a"},
		schedule.ListOptions{WorkflowName: "wf-b"}, // must be ignored
	)
	require.NoError(t, err)
	require.NotNil(t, repo.lastListFilter.WorkflowName)
	assert.Equal(t, "wf-a", *repo.lastListFilter.WorkflowName, "only first opts element is used")
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

// error — non-owner non-admin cannot enable or disable another user's schedule.
func TestService_SetEnabled_NonOwner_Forbidden(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	_, err := svc.SetEnabled(ctxAs(user()), s.ID, false) // different user
	assert.ErrorIs(t, err, schedule.ErrForbidden)
	assert.True(t, repo.schedules[s.ID].Enabled, "enabled flag must not change on forbidden")
}

// error — SetEnabled on a non-existent schedule propagates ErrNotFound.
func TestService_SetEnabled_NotFound(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	_, err := svc.SetEnabled(ctxAs(admin()), uuid.New(), true)
	assert.ErrorIs(t, err, schedule.ErrNotFound)
}

// input-validation — updating to an invalid cron expression returns ErrInvalidCron.
func TestService_Update_InvalidCron_Rejected(t *testing.T) {
	caller := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: caller.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	bad := "not-a-cron"
	_, err := svc.Update(ctxAs(caller), s.ID, schedule.UpdateInput{CronExpr: &bad})
	assert.ErrorIs(t, err, schedule.ErrInvalidCron)
}

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

// boundary — updating a schedule's department to one the caller does not belong
// to must be rejected, matching the same check in Create.
func TestService_Update_DepartmentChange_NotMember(t *testing.T) {
	caller := user("sales")
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: caller.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(s)
	svc := newSvc(repo, &fakeCanTrigger{allow: true})
	eng := "eng" // caller is not in "eng"
	_, err := svc.Update(ctxAs(caller), s.ID, schedule.UpdateInput{Department: &eng})
	require.Error(t, err)
	assert.Len(t, repo.schedules, 1, "schedule must not be modified on department rejection")
}

func TestService_Create_CanTriggerError_Propagates(t *testing.T) {
	repo := newSvcRepo()
	svc := newSvc(repo, &fakeCanTrigger{err: errors.New("lookup failed")})
	_, err := svc.Create(ctxAs(user()), schedule.CreateInput{WorkflowName: "report", CronExpr: "0 9 * * *"})
	require.Error(t, err)
	assert.Len(t, repo.schedules, 0)
}

// --- Bounds: per-creator quantity cap + minimum interval floor ---

// boundary — a non-admin creator at the count cap is rejected with the quota
// sentinel and no schedule is persisted.
func TestService_Create_QuotaCap_RejectsAtLimit(t *testing.T) {
	caller := user()
	existing := &schedule.Schedule{ID: uuid.New(), CreatorID: caller.UserID, WorkflowName: "a", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(existing)
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true},
		schedule.WithBounds(schedule.Bounds{MaxSchedulesPerCreator: 1}))

	_, err := svc.Create(ctxAs(caller), schedule.CreateInput{WorkflowName: "b", CronExpr: "0 10 * * *"})
	assert.ErrorIs(t, err, schedule.ErrQuotaExceeded)
	assert.Len(t, repo.schedules, 1, "no schedule persisted when over quota")
}

// success — a creator below the cap may still create.
func TestService_Create_QuotaCap_AllowsBelowLimit(t *testing.T) {
	caller := user()
	repo := newSvcRepo()
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true},
		schedule.WithBounds(schedule.Bounds{MaxSchedulesPerCreator: 2}))

	_, err := svc.Create(ctxAs(caller), schedule.CreateInput{WorkflowName: "a", CronExpr: "0 9 * * *"})
	require.NoError(t, err)
}

// authz — admins are exempt from the count cap.
func TestService_Create_QuotaCap_AdminExempt(t *testing.T) {
	adm := admin()
	existing := &schedule.Schedule{ID: uuid.New(), CreatorID: adm.UserID, WorkflowName: "a", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(existing)
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true},
		schedule.WithBounds(schedule.Bounds{MaxSchedulesPerCreator: 1}))

	_, err := svc.Create(ctxAs(adm), schedule.CreateInput{WorkflowName: "b", CronExpr: "0 10 * * *"})
	require.NoError(t, err, "admins bypass the per-creator quota")
}

// boundary — a cron that fires more often than the configured minimum interval
// is rejected with the interval sentinel.
func TestService_Create_IntervalFloor_RejectsTooFrequent(t *testing.T) {
	caller := user()
	repo := newSvcRepo()
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true},
		schedule.WithBounds(schedule.Bounds{MinInterval: 5 * time.Minute}))

	// "* * * * *" fires every minute, finer than the 5-minute floor.
	_, err := svc.Create(ctxAs(caller), schedule.CreateInput{WorkflowName: "a", CronExpr: "* * * * *"})
	assert.ErrorIs(t, err, schedule.ErrIntervalTooFrequent)
	assert.Len(t, repo.schedules, 0)
}

// success — a cron at or above the floor is allowed.
func TestService_Create_IntervalFloor_AllowsAtOrAboveFloor(t *testing.T) {
	caller := user()
	repo := newSvcRepo()
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true},
		schedule.WithBounds(schedule.Bounds{MinInterval: 5 * time.Minute}))

	// "*/5 * * * *" fires every 5 minutes, exactly at the floor.
	_, err := svc.Create(ctxAs(caller), schedule.CreateInput{WorkflowName: "a", CronExpr: "*/5 * * * *"})
	require.NoError(t, err)
}

// authz — admins are exempt from the interval floor.
func TestService_Create_IntervalFloor_AdminExempt(t *testing.T) {
	adm := admin()
	repo := newSvcRepo()
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true},
		schedule.WithBounds(schedule.Bounds{MinInterval: time.Hour}))

	_, err := svc.Create(ctxAs(adm), schedule.CreateInput{WorkflowName: "a", CronExpr: "* * * * *"})
	require.NoError(t, err, "admins bypass the interval floor")
}

// default — the package default permits every valid standard cron (finest is
// 1/min) without an explicit bound override.
func TestService_Create_DefaultBounds_AllowPerMinute(t *testing.T) {
	caller := user()
	repo := newSvcRepo()
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true})

	_, err := svc.Create(ctxAs(caller), schedule.CreateInput{WorkflowName: "a", CronExpr: "* * * * *"})
	require.NoError(t, err, "default 1-minute floor allows a per-minute cron")
}

// boundary — Update that changes the cron to a too-frequent expression is
// rejected by the interval floor.
func TestService_Update_IntervalFloor_RejectsTooFrequent(t *testing.T) {
	caller := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: caller.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: true}
	repo := newSvcRepo(s)
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true},
		schedule.WithBounds(schedule.Bounds{MinInterval: 5 * time.Minute}))

	tooFrequent := "* * * * *"
	_, err := svc.Update(ctxAs(caller), s.ID, schedule.UpdateInput{CronExpr: &tooFrequent})
	assert.ErrorIs(t, err, schedule.ErrIntervalTooFrequent)
}

// --- Service.RunNow ---

type fakeFirer struct {
	instID uuid.UUID
	fired  bool
	err    error
}

func (f *fakeFirer) Fire(_ context.Context, _ schedule.Schedule, _ bool) (uuid.UUID, bool, error) {
	return f.instID, f.fired, f.err
}

func newSvcWithFirer(repo schedule.ScheduleRepository, firer schedule.Firer) *schedule.Service {
	return schedule.NewService(repo, &fakeCanTrigger{allow: true}, schedule.WithFirer(firer))
}

// success — the schedule's owner may fire their own schedule.
func TestService_RunNow_OwnerFires(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	instID := uuid.New()
	firer := &fakeFirer{instID: instID, fired: true}
	svc := newSvcWithFirer(newSvcRepo(s), firer)

	res, err := svc.RunNow(ctxAs(owner), s.ID, schedule.RunNowInput{})
	require.NoError(t, err)
	assert.True(t, res.Fired)
	assert.Equal(t, instID, res.InstanceID)
}

// authz — an admin may fire any schedule, not only their own.
func TestService_RunNow_AdminFiresAnotherUser(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	firer := &fakeFirer{instID: uuid.New(), fired: true}
	svc := newSvcWithFirer(newSvcRepo(s), firer)

	_, err := svc.RunNow(ctxAs(admin()), s.ID, schedule.RunNowInput{})
	require.NoError(t, err, "admin must be able to fire another user's schedule")
}

// authz — a non-owner, non-admin user is denied.
func TestService_RunNow_NonOwner_Forbidden(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	firer := &fakeFirer{instID: uuid.New(), fired: true}
	svc := newSvcWithFirer(newSvcRepo(s), firer)

	_, err := svc.RunNow(ctxAs(user()), s.ID, schedule.RunNowInput{})
	assert.ErrorIs(t, err, schedule.ErrForbidden)
}

// failure — RunNow is unavailable when no firer has been wired.
func TestService_RunNow_FirerUnavailable(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	svc := newSvc(newSvcRepo(s), &fakeCanTrigger{allow: true}) // no WithFirer

	_, err := svc.RunNow(ctxAs(owner), s.ID, schedule.RunNowInput{})
	assert.ErrorIs(t, err, schedule.ErrRunNowUnavailable)
}

// error — no identity in context yields an error.
func TestService_RunNow_NoIdentity_Error(t *testing.T) {
	s := &schedule.Schedule{ID: uuid.New(), WorkflowName: "report", CronExpr: "0 9 * * *"}
	firer := &fakeFirer{instID: uuid.New(), fired: true}
	svc := newSvcWithFirer(newSvcRepo(s), firer)

	_, err := svc.RunNow(context.Background(), s.ID, schedule.RunNowInput{})
	require.Error(t, err, "no identity in context must return an error")
}

// error — schedule not found propagates the repo error.
func TestService_RunNow_ScheduleNotFound(t *testing.T) {
	firer := &fakeFirer{instID: uuid.New(), fired: true}
	svc := newSvcWithFirer(newSvcRepo(), firer) // empty repo

	_, err := svc.RunNow(ctxAs(user()), uuid.New(), schedule.RunNowInput{})
	require.Error(t, err, "unknown schedule ID must return an error")
}

// error — Firer.Fire errors propagate from RunNow.
func TestService_RunNow_FireError_Propagates(t *testing.T) {
	owner := user()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	boom := errors.New("engine exploded")
	svc := newSvcWithFirer(newSvcRepo(s), &fakeFirer{err: boom})

	_, err := svc.RunNow(ctxAs(owner), s.ID, schedule.RunNowInput{})
	assert.ErrorIs(t, err, boom)
}
