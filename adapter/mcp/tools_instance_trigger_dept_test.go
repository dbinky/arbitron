package mcp_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// The instance_trigger handler threads the optional department arg to the engine.
func TestInstanceTrigger_PassesDepartment(t *testing.T) {
	eng := &stubEngine{
		triggerFn: func(_ string, _ map[string]any, _ string) (uuid.UUID, bool, error) {
			return uuid.New(), true, nil
		},
	}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)
	result := callTool(t, srv, "instance_trigger", map[string]any{
		"workflowName": "wf",
		"department":   "engineering",
	})

	assert.False(t, result.IsError)
	assert.Equal(t, "engineering", eng.capturedDepartment)
}

// An out-of-department override surfaces as a tool error (validation).
func TestInstanceTrigger_InvalidDepartment_Error(t *testing.T) {
	eng := &stubEngine{
		triggerFn: func(_ string, _ map[string]any, _ string) (uuid.UUID, bool, error) {
			return uuid.Nil, false, engine.ErrInvalidDepartmentOverride
		},
	}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)
	result := callTool(t, srv, "instance_trigger", map[string]any{
		"workflowName": "wf",
		"department":   "finance",
	})

	assert.True(t, result.IsError)
	text := extractText(t, result)
	assert.Contains(t, text, "invalid department")
}
