package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- minimal schedule ports for the integration test ---

type integScheduleRepo struct {
	s         *schedule.Schedule
	lastFired *time.Time
}

func (r *integScheduleRepo) Create(context.Context, *schedule.Schedule) error { return nil }
func (r *integScheduleRepo) Get(context.Context, uuid.UUID) (*schedule.Schedule, error) {
	return r.s, nil
}
func (r *integScheduleRepo) List(context.Context, schedule.ListFilters) ([]schedule.Schedule, error) {
	return []schedule.Schedule{*r.s}, nil
}
func (r *integScheduleRepo) ListEnabled(context.Context) ([]schedule.Schedule, error) {
	if !r.s.Enabled {
		return nil, nil
	}
	return []schedule.Schedule{*r.s}, nil
}
func (r *integScheduleRepo) Update(context.Context, *schedule.Schedule) error { return nil }
func (r *integScheduleRepo) Delete(context.Context, uuid.UUID) error          { return nil }
func (r *integScheduleRepo) SetLastFired(_ context.Context, _ uuid.UUID, at time.Time) error {
	t := at
	r.lastFired = &t
	r.s.LastFiredAt = &t
	return nil
}

type integActive struct{}

func (integActive) HasActiveRun(context.Context, string, uuid.UUID) (bool, error) {
	return false, nil
}

type integIdents struct{}

func (integIdents) IdentityForUser(_ context.Context, userID uuid.UUID) (identity.Identity, error) {
	return identity.Identity{UserID: userID, Username: "creator", Role: identity.RoleUser}, nil
}

type integAlerter struct{ alerts int }

func (a *integAlerter) Alert(context.Context, schedule.Schedule, notification.Type) error {
	a.alerts++
	return nil
}

type integClock struct{ t time.Time }

func (c integClock) Now() time.Time { return c.t }

// deptIdents is an IdentityResolver that returns an identity carrying the given
// departments for any userID. Used when the schedule's department override must
// pass the resolveStamp membership check.
type deptIdents struct{ depts []string }

func (d deptIdents) IdentityForUser(_ context.Context, userID uuid.UUID) (identity.Identity, error) {
	return identity.Identity{UserID: userID, Username: "creator", Role: identity.RoleUser, Departments: d.depts}, nil
}

// collidingActive is an ActiveRunChecker that always reports a live run, so the
// evaluator skips and raises a schedule_skipped alert.
type collidingActive struct{}

func (collidingActive) HasActiveRun(context.Context, string, uuid.UUID) (bool, error) {
	return true, nil
}

// --- happy path (existing) ---

// TestEvaluator_FiresScriptOnlyWorkflow_EnqueuesUnderCreator wires the schedule
// evaluator to the REAL engine and a script-only (no-LLM) workflow. A due
// schedule must trigger the engine, which creates an instance owned by the
// schedule's creator and enqueues it under the creator's capacity. Confirms the
// spec's "a cron schedule starts a script-only workflow," attributed to the
// creator. (The engine harness keeps the instance queued rather than running it
// to completion, per the existing trigger-harness design.)
func TestEvaluator_FiresScriptOnlyWorkflow_EnqueuesUnderCreator(t *testing.T) {
	h := newTriggerHarness()
	h.repo.put(workflow.Shared(), sharedWf("nightly-report")) // script-only entrypoint

	creator := uuid.New()
	created := time.Date(2026, 6, 8, 6, 0, 0, 0, time.UTC)
	s := &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "nightly-report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
		CreatorID:    creator,
		Enabled:      true,
		CreatedAt:    created,
	}
	repo := &integScheduleRepo{s: s}
	al := &integAlerter{}
	clk := integClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)} // past the 09:00 fire

	ev := schedule.NewEvaluator(repo, h.engine, integActive{}, integIdents{}, al,
		schedule.WithClock(clk))

	require.NoError(t, ev.Tick(context.Background()))

	// The engine recorded the enqueue under the creator (attribution to creator).
	assert.Equal(t, creator, h.enqueuedOwner, "scheduled run enqueued under the creator's capacity")
	assert.Equal(t, 0, al.alerts, "a clean fire raises no alert")
	require.NotNil(t, repo.lastFired, "fire position recorded")
}

// --- success: schedule.Department flows through fireOne → TriggerWithDepartment → instance stamp ---

// TestEvaluator_DepartmentFromSchedule_FlowsToInstance is the full-stack
// department-flow test: the schedule's Department field is passed as the
// departmentOverride to TriggerWithDepartment, which stamps the instance's
// Department. The creator identity must carry that department or the stamp
// check fails closed (ErrInvalidDepartmentOverride).
func TestEvaluator_DepartmentFromSchedule_FlowsToInstance(t *testing.T) {
	h := newTriggerHarness()
	h.repo.put(workflow.Shared(), sharedWf("dept-wf"))
	setPublication(h, "dept-wf", workflow.VisibilityDepartment, "platform")

	creator := uuid.New()
	created := time.Date(2026, 6, 8, 6, 0, 0, 0, time.UTC)
	s := &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "dept-wf",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
		CreatorID:    creator,
		Department:   "platform",
		Enabled:      true,
		CreatedAt:    created,
	}
	repo := &integScheduleRepo{s: s}
	al := &integAlerter{}
	clk := integClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}

	// deptIdents must include "platform" so resolveStamp's membership check passes.
	ev := schedule.NewEvaluator(repo, h.engine, integActive{}, deptIdents{depts: []string{"platform"}}, al,
		schedule.WithClock(clk))

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, creator, h.enqueuedOwner, "instance enqueued under the creator")

	// Retrieve the created instance and assert the department was stamped.
	h.instances.mu.Lock()
	var inst *instance.Instance
	for _, v := range h.instances.instances {
		inst = v
		break
	}
	h.instances.mu.Unlock()
	require.NotNil(t, inst, "trigger must create an instance")
	assert.Equal(t, "platform", inst.Department,
		"schedule.Department must propagate to the stamped instance via TriggerWithDepartment")
}

// --- failure: collision skip raises alert and advances position, no instance created ---

// TestEvaluator_CollisionSkip_NoTrigger_AlertRaised asserts that when a live
// run exists for the schedule (HasActiveRun=true), the evaluator skips the
// occurrence, raises a schedule_skipped alert, and advances the position so the
// same occurrence is not retried. No instance is created.
func TestEvaluator_CollisionSkip_NoTrigger_AlertRaised(t *testing.T) {
	h := newTriggerHarness()
	h.repo.put(workflow.Shared(), sharedWf("report"))

	creator := uuid.New()
	created := time.Date(2026, 6, 8, 6, 0, 0, 0, time.UTC)
	s := &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
		CreatorID:    creator,
		Enabled:      true,
		CreatedAt:    created,
	}
	repo := &integScheduleRepo{s: s}
	al := &integAlerter{}
	clk := integClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}

	ev := schedule.NewEvaluator(repo, h.engine, collidingActive{}, integIdents{}, al,
		schedule.WithClock(clk))

	require.NoError(t, ev.Tick(context.Background()))

	assert.Equal(t, uuid.Nil, h.enqueuedOwner, "collision must not enqueue any instance")
	assert.Equal(t, 0, len(h.instances.instances), "no instance must be created on collision")
	assert.Equal(t, 1, al.alerts, "a schedule_skipped alert must be raised")
	require.NotNil(t, repo.lastFired, "position must be advanced so the same occurrence is not retried")
}

// --- error: Recover advances position + alerts, does NOT fire missed occurrences ---

// TestEvaluator_Recover_NoBackfill asserts the no-backfill misfire policy:
// when Recover() finds a schedule whose last position is in the past (missed
// occurrences during downtime), it advances the position to the latest missed
// occurrence and raises a schedule_missed alert — but NEVER fires the missed
// runs. After Recover, the first Tick fires the NEXT future occurrence only.
func TestEvaluator_Recover_NoBackfill(t *testing.T) {
	h := newTriggerHarness()
	h.repo.put(workflow.Shared(), sharedWf("daily"))

	creator := uuid.New()
	// Created yesterday at 08:00; cron fires daily at 09:00 UTC.
	// Yesterday's 09:00 was missed during downtime.
	created := time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)
	s := &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "daily",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
		CreatorID:    creator,
		Enabled:      true,
		CreatedAt:    created,
		// LastFiredAt is nil (never fired before downtime).
	}
	repo := &integScheduleRepo{s: s}
	al := &integAlerter{}
	// "now" = today 09:30 — both yesterday 09:00 AND today 09:00 are in the past.
	clk := integClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}

	ev := schedule.NewEvaluator(repo, h.engine, integActive{}, integIdents{}, al,
		schedule.WithClock(clk))

	require.NoError(t, ev.Recover(context.Background()))

	assert.Equal(t, 0, len(h.instances.instances),
		"Recover must NOT fire missed occurrences (no backfill)")
	assert.Equal(t, 1, al.alerts,
		"Recover must raise exactly one schedule_missed alert for the downtime window")
	require.NotNil(t, repo.lastFired,
		"Recover must advance the position to the latest missed occurrence")
	// The latest occurrence at or before 09:30 today is today's 09:00.
	assert.Equal(t, time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC), *repo.lastFired,
		"position advanced to the latest missed occurrence (today 09:00), not yesterday")
}
