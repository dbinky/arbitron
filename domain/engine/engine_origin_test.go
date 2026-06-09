package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A manual trigger carries no origin on the context: the engine must default
// the created instance to OriginManual with a nil ScheduleID.
func TestTrigger_ManualOrigin_DefaultsToManual(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), sharedWf("secret"))

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "secret", nil, "normal", "")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.OriginManual, inst.TriggerSource)
	assert.Nil(t, inst.ScheduleID, "manual run carries no originating schedule")
}

// A schedule (cron) origin on the context stamps OriginSchedule and the
// originating schedule id onto the created instance.
func TestTrigger_ScheduleOrigin_StampsScheduleSource(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), sharedWf("secret"))

	sid := uuid.New()
	ctx := instance.WithOrigin(ctxDepts(caller), instance.Origin{
		Source: instance.OriginSchedule, ScheduleID: &sid,
	})
	id, _, err := h.engine.TriggerWithDepartment(ctx, "secret", nil, "normal", "")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.OriginSchedule, inst.TriggerSource)
	require.NotNil(t, inst.ScheduleID)
	assert.Equal(t, sid, *inst.ScheduleID)
}

// A run-now origin on the context stamps OriginRunNow and the originating
// schedule id onto the created instance.
func TestTrigger_RunNowOrigin_StampsRunNowSource(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), sharedWf("secret"))

	sid := uuid.New()
	ctx := instance.WithOrigin(ctxDepts(caller), instance.Origin{
		Source: instance.OriginRunNow, ScheduleID: &sid,
	})
	id, _, err := h.engine.TriggerWithDepartment(ctx, "secret", nil, "normal", "")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.OriginRunNow, inst.TriggerSource)
	require.NotNil(t, inst.ScheduleID)
	assert.Equal(t, sid, *inst.ScheduleID)
}
