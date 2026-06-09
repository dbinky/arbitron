package http

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToInstanceResponse_IncludesVisibility(t *testing.T) {
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "wf",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		Department: "engineering", Visibility: instance.VisibilityDepartment,
	}
	resp := toInstanceResponse(inst)
	assert.Equal(t, "engineering", resp.Department)
	assert.Equal(t, "department", resp.Visibility)
}

func TestToSummaryDTO_IncludesVisibility(t *testing.T) {
	s := instance.InstanceSummary{
		ID: uuid.New(), WorkflowName: "wf", State: instance.StateRunning,
		Priority: instance.PriorityNormal, Department: "engineering",
		Visibility: instance.VisibilityDepartment,
	}
	dto := toSummaryDTO(s)
	assert.Equal(t, "engineering", dto.Department)
	assert.Equal(t, "department", dto.Visibility)
}

// A schedule-originated summary emits triggerSource and scheduleId.
func TestToSummaryDTO_ScheduleOrigin(t *testing.T) {
	sid := uuid.New()
	s := instance.InstanceSummary{
		ID: uuid.New(), WorkflowName: "wf", State: instance.StateRunning,
		Priority:      instance.PriorityNormal,
		TriggerSource: instance.OriginSchedule,
		ScheduleID:    &sid,
	}
	dto := toSummaryDTO(s)
	assert.Equal(t, "schedule", dto.TriggerSource)
	require.NotNil(t, dto.ScheduleID)
	assert.Equal(t, sid.String(), *dto.ScheduleID)

	b, err := json.Marshal(dto)
	require.NoError(t, err)
	js := string(b)
	assert.Contains(t, js, `"triggerSource":"schedule"`)
	assert.Contains(t, js, `"scheduleId":"`+sid.String()+`"`)
}

// A manual summary emits triggerSource:"manual" and OMITS scheduleId.
func TestToSummaryDTO_ManualOrigin_OmitsScheduleID(t *testing.T) {
	s := instance.InstanceSummary{
		ID: uuid.New(), WorkflowName: "wf", State: instance.StateRunning,
		Priority:      instance.PriorityNormal,
		TriggerSource: instance.OriginManual,
	}
	dto := toSummaryDTO(s)
	assert.Equal(t, "manual", dto.TriggerSource)
	assert.Nil(t, dto.ScheduleID)

	b, err := json.Marshal(dto)
	require.NoError(t, err)
	js := string(b)
	assert.Contains(t, js, `"triggerSource":"manual"`)
	assert.False(t, strings.Contains(js, "scheduleId"), "manual summary must omit scheduleId")
}
