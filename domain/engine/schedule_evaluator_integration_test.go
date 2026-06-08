package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
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
