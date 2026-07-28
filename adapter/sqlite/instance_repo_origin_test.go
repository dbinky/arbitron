package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOriginInstance builds a minimal queued instance with the given origin.
func newOriginInstance(src instance.TriggerSource, scheduleID *uuid.UUID) *instance.Instance {
	return &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{"name": "wf"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
		TriggerSource:    src,
		ScheduleID:       scheduleID,
	}
}

// summaryFor finds the listed summary for the given instance id.
func summaryFor(t *testing.T, repo *InstanceRepo, id uuid.UUID) instance.InstanceSummary {
	t.Helper()
	summaries, err := repo.List(context.Background(), instance.ListFilters{Unconstrained: true, Limit: 100})
	require.NoError(t, err)
	for _, s := range summaries {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("instance %s not found in List", id)
	return instance.InstanceSummary{}
}

// A schedule-originated run round-trips TriggerSource + ScheduleID through both
// Get and List.
func TestInstanceRepoOrigin_Schedule_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	sid := uuid.New()

	inst := newOriginInstance(instance.OriginSchedule, &sid)
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.OriginSchedule, got.TriggerSource)
	require.NotNil(t, got.ScheduleID)
	assert.Equal(t, sid, *got.ScheduleID)

	summary := summaryFor(t, repo, inst.ID)
	assert.Equal(t, instance.OriginSchedule, summary.TriggerSource)
	require.NotNil(t, summary.ScheduleID)
	assert.Equal(t, sid, *summary.ScheduleID)
}

// A run_now run round-trips its origin and schedule id.
func TestInstanceRepoOrigin_RunNow_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	sid := uuid.New()

	inst := newOriginInstance(instance.OriginRunNow, &sid)
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.OriginRunNow, got.TriggerSource)
	require.NotNil(t, got.ScheduleID)
	assert.Equal(t, sid, *got.ScheduleID)

	summary := summaryFor(t, repo, inst.ID)
	assert.Equal(t, instance.OriginRunNow, summary.TriggerSource)
	require.NotNil(t, summary.ScheduleID)
	assert.Equal(t, sid, *summary.ScheduleID)
}

// A manual run round-trips OriginManual with a nil ScheduleID (SQL NULL).
func TestInstanceRepoOrigin_Manual_NilScheduleID(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := newOriginInstance(instance.OriginManual, nil)
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.OriginManual, got.TriggerSource)
	assert.Nil(t, got.ScheduleID, "manual run stores NULL schedule_id")

	summary := summaryFor(t, repo, inst.ID)
	assert.Equal(t, instance.OriginManual, summary.TriggerSource)
	assert.Nil(t, summary.ScheduleID)
}

// A run whose originating schedule no longer exists still lists with its stored
// ScheduleID — there is no FK to violate.
func TestInstanceRepoOrigin_DeletedSchedule_StillLists(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	// An arbitrary schedule id for which no schedules row exists.
	sid := uuid.New()

	inst := newOriginInstance(instance.OriginSchedule, &sid)
	require.NoError(t, repo.Create(context.Background(), inst), "no FK on schedule_id, so create succeeds")

	summary := summaryFor(t, repo, inst.ID)
	assert.Equal(t, instance.OriginSchedule, summary.TriggerSource)
	require.NotNil(t, summary.ScheduleID, "deleted-schedule run keeps its stored id")
	assert.Equal(t, sid, *summary.ScheduleID)
}
