package schedule_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- run-now service fixtures ---

type runNowRepo struct {
	m map[uuid.UUID]*schedule.Schedule
}

func newRunNowRepo(ss ...*schedule.Schedule) *runNowRepo {
	r := &runNowRepo{m: map[uuid.UUID]*schedule.Schedule{}}
	for _, s := range ss {
		r.m[s.ID] = s
	}
	return r
}

func (r *runNowRepo) Create(context.Context, *schedule.Schedule) error { return nil }
func (r *runNowRepo) Get(_ context.Context, id uuid.UUID) (*schedule.Schedule, error) {
	s, ok := r.m[id]
	if !ok {
		return nil, schedule.ErrNotFound
	}
	cp := *s
	return &cp, nil
}
func (r *runNowRepo) List(context.Context, schedule.ListFilters) ([]schedule.Schedule, error) {
	return nil, nil
}
func (r *runNowRepo) ListEnabled(context.Context) ([]schedule.Schedule, error) { return nil, nil }
func (r *runNowRepo) Update(context.Context, *schedule.Schedule) error         { return nil }
func (r *runNowRepo) Delete(context.Context, uuid.UUID) error                  { return nil }
func (r *runNowRepo) SetLastFired(context.Context, uuid.UUID, time.Time) error { return nil }

// allowCanTriggerRN is the can-trigger gate stub (RunNow does NOT call it, but
// NewService requires a non-nil CanTrigger).
type allowCanTriggerRN struct{}

func (allowCanTriggerRN) CanTrigger(context.Context, identity.Identity, string) (bool, error) {
	return true, nil
}

// stubFirer records its Fire call and returns canned results.
type stubFirer struct {
	calls       int
	gotSchedule schedule.Schedule
	gotSkip     bool
	retInstance uuid.UUID
	retFired    bool
	retErr      error
}

func (f *stubFirer) Fire(_ context.Context, s schedule.Schedule, skipWhenBusy bool) (uuid.UUID, bool, error) {
	f.calls++
	f.gotSchedule = s
	f.gotSkip = skipWhenBusy
	return f.retInstance, f.retFired, f.retErr
}

func ownerIdent(uid uuid.UUID) identity.Identity {
	return identity.Identity{UserID: uid, Username: "owner", Role: identity.RoleUser}
}

func adminIdent() identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}
}

// ctxAs is declared in service_test.go (same schedule_test package) and reused here.

func enabledSchedule(creator uuid.UUID) *schedule.Schedule {
	return &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "wf",
		CreatorID:    creator,
		Department:   "eng",
		Enabled:      true,
		Variables:    map[string]any{"k": "v"},
	}
}

// Happy path: owner runs their schedule => firer called with the loaded schedule
// and the requested skip flag; result mirrors the firer.
func TestService_RunNow_HappyPath(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	wantInst := uuid.New()
	firer := &stubFirer{retInstance: wantInst, retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	res, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{SkipWhenBusy: false})

	require.NoError(t, err)
	assert.Equal(t, wantInst, res.InstanceID)
	assert.True(t, res.Fired)
	require.Equal(t, 1, firer.calls)
	assert.Equal(t, s.ID, firer.gotSchedule.ID)
	assert.Equal(t, "wf", firer.gotSchedule.WorkflowName)
	assert.False(t, firer.gotSkip)
}

// SkipWhenBusy is passed through to the firer.
func TestService_RunNow_PassesSkipWhenBusy(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{SkipWhenBusy: true})

	require.NoError(t, err)
	assert.True(t, firer.gotSkip)
}

// Collision/skipped: firer reports fired=false (busy) => RunNow returns
// Fired=false, nil instance, no error.
func TestService_RunNow_Skipped(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.Nil, retFired: false}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	res, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{SkipWhenBusy: true})

	require.NoError(t, err)
	assert.False(t, res.Fired)
	assert.Equal(t, uuid.Nil, res.InstanceID)
}

// Authorization: admin may run another user's schedule.
func TestService_RunNow_AdminRunsOthersSchedule(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	res, err := svc.RunNow(ctxAs(adminIdent()), s.ID, schedule.RunNowInput{})

	require.NoError(t, err)
	assert.True(t, res.Fired)
	assert.Equal(t, 1, firer.calls)
}

// Authorization: a non-owner non-admin is forbidden; the firer is NOT called.
func TestService_RunNow_NonOwnerForbidden(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	stranger := ownerIdent(uuid.New()) // different UserID, RoleUser
	_, err := svc.RunNow(ctxAs(stranger), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrForbidden)
	assert.Equal(t, 0, firer.calls)
}

// Authorization: no request identity => forbidden (never leaks existence).
func TestService_RunNow_NoIdentityForbidden(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(context.Background(), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrForbidden)
	assert.Equal(t, 0, firer.calls)
}

// Not found: unknown id => ErrNotFound (authz check loads via repo.Get).
func TestService_RunNow_NotFound(t *testing.T) {
	firer := &stubFirer{}
	svc := schedule.NewService(newRunNowRepo(), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(adminIdent()), uuid.New(), schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrNotFound)
	assert.Equal(t, 0, firer.calls)
}

// Firer unwired => ErrRunNowUnavailable. Authorization is still enforced first:
// here the owner is authorized, so we reach the nil-firer guard.
func TestService_RunNow_FirerUnwired(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}) // no WithFirer

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrRunNowUnavailable)
}

// Firer error (e.g. creator unresolved) is surfaced unchanged.
func TestService_RunNow_FirerError(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retErr: schedule.ErrCreatorUnresolved}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrCreatorUnresolved)
}

// HasFirer reports wiring state for adapter gating.
func TestService_HasFirer(t *testing.T) {
	wired := schedule.NewService(newRunNowRepo(), allowCanTriggerRN{}, schedule.WithFirer(&stubFirer{}))
	unwired := schedule.NewService(newRunNowRepo(), allowCanTriggerRN{})
	assert.True(t, wired.HasFirer())
	assert.False(t, unwired.HasFirer())
}
