package mcp_test

import (
	"context"
 "encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestInstanceList_CarriesExecutionStatus(t *testing.T) {
	id := uuid.New()
	eng := &stubEngine{listEnrichedFn: func(context.Context, instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: id, WorkflowName: "wf", State: instance.StateRunning},
			Execution:       []instance.ExecutionStatus{{RunID: id, NodeID: "draft", State: instance.ExecQueuedSlot, Reason: "queued · slot"}},
		}}, nil
	}}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)
	res := callTool(t, srv, "instance_list", nil)
	assert.False(t, res.IsError)
	text := extractText(t, res)
 var rows []instance.EnrichedInstanceSummary
 assert.NoError(t, json.Unmarshal([]byte(text), &rows))
 if assert.Len(t, rows, 1) && assert.Len(t, rows[0].Execution, 1) {
 assert.Equal(t, id, rows[0].Execution[0].RunID)
 assert.Equal(t, instance.ExecQueuedSlot, rows[0].Execution[0].State)
 }
}
