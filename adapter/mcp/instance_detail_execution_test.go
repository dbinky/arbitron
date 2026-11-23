package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

// The legacy double defaults to no active operation; detail tests below supply one.
func (s *stubEngine) GetExecutionStatus(ctx context.Context, id uuid.UUID) ([]instance.ExecutionStatus, error) {
	if s.executionFn != nil {
		return s.executionFn(ctx, id)
	}
	return nil, nil
}
func TestInstanceGet_CarriesExecutionStatus(t *testing.T) {
	id := uuid.New()
	eng := &stubEngine{getFn: func(context.Context, uuid.UUID) (*instance.Instance, error) { return &instance.Instance{ID: id}, nil }, executionFn: func(_ context.Context, got uuid.UUID) ([]instance.ExecutionStatus, error) {
		require.Equal(t, id, got)
		return []instance.ExecutionStatus{{RunID: got, State: instance.ExecQueuedSlot}}, nil
	}}
	srv := newInstanceServer(&stubInstanceRepo{}, eng)
	res := callTool(t, srv, "instance_get", map[string]any{"id": id.String()})
	require.False(t, res.IsError)
	var row struct {
		Execution []instance.ExecutionStatus `json:"execution"`
	}
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &row))
	require.Len(t, row.Execution, 1)
	require.Equal(t, id, row.Execution[0].RunID)
}
func TestInstanceDetail_StatusUnavailable(t *testing.T) {
	id := uuid.New()
	eng := &stubEngine{getFn: func(context.Context, uuid.UUID) (*instance.Instance, error) { return &instance.Instance{ID: id}, nil }, executionFn: func(context.Context, uuid.UUID) ([]instance.ExecutionStatus, error) {
		return nil, errors.New("secret ledger error")
	}}
	res := callTool(t, newInstanceServer(&stubInstanceRepo{}, eng), "instance_get", map[string]any{"id": id.String()})
	require.True(t, res.IsError)
	require.NotContains(t, extractText(t, res), "secret ledger")
}
