package http

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceHistory_DTOsPreserveExactOperation(t *testing.T) {
	job, inv := uuid.New(), uuid.New()
	moves := []instance.WorkspaceMove{{From: "mac-1", To: "mac-2", Generation: 2, TargetJobID: &job, InvocationID: &inv,
		NodeID: "edit", OperationID: "agent", FromGeneration: 1, ToGeneration: 2}}
	for name, dto := range map[string]any{
		"instance":  toInstanceResponse(&instance.Instance{WorkspaceMoves: moves}),
		"execution": toNodeExecutionResponse(&instance.NodeExecution{WorkspaceMoves: moves}),
	} {
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(dto)
			require.NoError(t, err)
			var got struct {
				Moves []instance.WorkspaceMove `json:"workspaceMoves"`
			}
			require.NoError(t, json.Unmarshal(b, &got))
			assert.Equal(t, moves, got.Moves)
		})
	}
}
