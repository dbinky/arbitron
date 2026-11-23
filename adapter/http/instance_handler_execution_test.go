package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstanceHandler_List_CarriesExecutionStatus(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	now := time.Now()
	reset := now.Add(time.Hour).UTC().Truncate(time.Second)
	svc.listEnrichedFn = func(context.Context, instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: id, WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: now, UpdatedAt: now},
			Execution: []instance.ExecutionStatus{{RunID: id, NodeID: "draft", OperationID: "node", JobID: uuid.New(), State: instance.ExecWaitingCapacity,
				Group: "anthropic_subscription:anthropic", Reason: "waiting · capacity: no eligible source", NextReset: &reset}},
		}}, nil
	}
	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/instances", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rows))
	require.Len(t, rows, 1)
	exec, ok := rows[0]["execution"].([]any)
	require.True(t, ok, "execution is on the wire")
	require.Len(t, exec, 1)
	first := exec[0].(map[string]any)
	assert.Equal(t, "waiting_capacity", first["state"])
	assert.Equal(t, "anthropic_subscription:anthropic", first["group"])
	assert.Equal(t, reset.Format(time.RFC3339), first["nextReset"])
}
