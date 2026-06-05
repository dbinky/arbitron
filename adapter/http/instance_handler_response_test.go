package http

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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
