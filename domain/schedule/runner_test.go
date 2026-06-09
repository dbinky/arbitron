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

// --- runner fakes ---

type runnerTrigger struct {
	id       uuid.UUID
	created  bool
	err      error
	calls    int
	gotWF    string
	gotVars  map[string]any
	gotPrio  string
	gotDept  string
	gotIdent identity.Identity // identity read from the injected context
}

func (t *runnerTrigger) TriggerWithDepartment(ctx context.Context, wf string, vars map[string]any, prio, dept string) (uuid.UUID, bool, error) {
	t.calls++
	t.gotWF, t.gotVars, t.gotPrio, t.gotDept = wf, vars, prio, dept
	t.gotIdent, _ = identity.FromContext(ctx)
	return t.id, t.created, t.err
}

type runnerIdents struct {
	id  identity.Identity
	err error
}

func (r runnerIdents) IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error) {
	return r.id, r.err
}

type runnerActive struct {
	busy bool
	err  error
}

func (a runnerActive) HasActiveRun(ctx context.Context, wf string, owner uuid.UUID) (bool, error) {
	return a.busy, a.err
}

func runnerSchedule(creator uuid.UUID) schedule.Schedule {
	return schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "wf-report",
		CreatorID:    creator,
		Department:   "eng",
		Variables:    map[string]any{"k": "v"},
	}
}

// Happy path: Fire resolves the creator identity, injects it, and triggers the
// workflow with the schedule's variables/department. fired=true, instance id
// returned, LastFiredAt is NEVER touched (Runner has no repo).
func TestRunner_Fire_HappyPath(t *testing.T) {
	creator := uuid.New()
	wantInst := uuid.New()
	trig := &runnerTrigger{id: wantInst, created: true}
	idents := runnerIdents{id: identity.Identity{UserID: creator, Username: "creator", Role: identity.RoleUser}}
	r := schedule.NewRunner(trig, idents, runnerActive{})

	s := runnerSchedule(creator)
	inst, fired, err := r.Fire(context.Background(), s, false)

	require.NoError(t, err)
	assert.True(t, fired)
	assert.Equal(t, wantInst, inst)
	assert.Equal(t, 1, trig.calls)
	assert.Equal(t, "wf-report", trig.gotWF)
	assert.Equal(t, map[string]any{"k": "v"}, trig.gotVars)
	assert.Equal(t, "normal", trig.gotPrio)
	assert.Equal(t, "eng", trig.gotDept)
	assert.Equal(t, creator, trig.gotIdent.UserID, "trigger must run as the resolved creator identity")
}

// Collision, skip-when-busy=true + active run => skipped: fired=false, no
// trigger call, no error, nil instance id.
func TestRunner_Fire_SkipWhenBusy_Active(t *testing.T) {
	creator := uuid.New()
	trig := &runnerTrigger{id: uuid.New(), created: true}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{busy: true})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), true)

	require.NoError(t, err)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
	assert.Equal(t, 0, trig.calls, "no run must be started when busy and skipWhenBusy")
}

// Collision, skip-when-busy=true + NOT active => fires normally.
func TestRunner_Fire_SkipWhenBusy_NotActive(t *testing.T) {
	creator := uuid.New()
	wantInst := uuid.New()
	trig := &runnerTrigger{id: wantInst, created: true}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{busy: false})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), true)

	require.NoError(t, err)
	assert.True(t, fired)
	assert.Equal(t, wantInst, inst)
	assert.Equal(t, 1, trig.calls)
}

// Default collision (skipWhenBusy=false) + active run => fires ANYWAY. The
// active checker must not even be consulted; assert by leaving it erroring and
// confirming Fire still triggers.
func TestRunner_Fire_DefaultFiresWhileBusy(t *testing.T) {
	creator := uuid.New()
	wantInst := uuid.New()
	trig := &runnerTrigger{id: wantInst, created: true}
	// active returns busy=true AND an error; neither must affect a default fire.
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{busy: true, err: errors.New("boom")})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), false)

	require.NoError(t, err)
	assert.True(t, fired)
	assert.Equal(t, wantInst, inst)
	assert.Equal(t, 1, trig.calls)
}

// Failure: creator identity unresolvable => wrapped ErrCreatorUnresolved, no
// trigger call.
func TestRunner_Fire_CreatorUnresolved(t *testing.T) {
	creator := uuid.New()
	trig := &runnerTrigger{id: uuid.New(), created: true}
	r := schedule.NewRunner(trig, runnerIdents{err: errors.New("user gone")}, runnerActive{})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), false)

	require.Error(t, err)
	assert.ErrorIs(t, err, schedule.ErrCreatorUnresolved)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
	assert.Equal(t, 0, trig.calls)
}

// Failure: skip-when-busy active-check itself errors => surfaced as an error
// (not silently fired), no trigger call.
func TestRunner_Fire_ActiveCheckError(t *testing.T) {
	creator := uuid.New()
	trig := &runnerTrigger{id: uuid.New(), created: true}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{err: errors.New("db down")})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), true)

	require.Error(t, err)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
	assert.Equal(t, 0, trig.calls)
}

// Failure: trigger error surfaced (e.g. deleted workflow). Runner does NOT
// translate workflow.ErrNotFound (that cron-only handling stays in fireOne);
// it returns the error wrapped as-is with fired=false.
func TestRunner_Fire_TriggerError(t *testing.T) {
	creator := uuid.New()
	trigErr := errors.New("trigger failed")
	trig := &runnerTrigger{err: trigErr}
	r := schedule.NewRunner(trig, runnerIdents{id: identity.Identity{UserID: creator}}, runnerActive{})

	inst, fired, err := r.Fire(context.Background(), runnerSchedule(creator), false)

	require.Error(t, err)
	assert.ErrorIs(t, err, trigErr)
	assert.False(t, fired)
	assert.Equal(t, uuid.Nil, inst)
}
