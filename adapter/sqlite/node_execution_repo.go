package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// NodeExecutionRepo implements instance.NodeExecutionRepository backed by SQLite.
type NodeExecutionRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

// NewNodeExecutionRepo creates a NodeExecutionRepo using the given database connection.
func NewNodeExecutionRepo(db *sql.DB) *NodeExecutionRepo {
	return &NodeExecutionRepo{
		db:      db,
		queries: generated.New(db),
	}
}

func (r *NodeExecutionRepo) Create(ctx context.Context, exec *instance.NodeExecution) error {
	resolvedInputsJSON, err := toNullJSONString(exec.ResolvedInputs)
	if err != nil {
		return fmt.Errorf("marshalling resolved inputs: %w", err)
	}
	outputJSON, err := toNullJSONString(exec.Output)
	if err != nil {
		return fmt.Errorf("marshalling output: %w", err)
	}
	errorJSON, err := toNullJSONString(exec.Error)
	if err != nil {
		return fmt.Errorf("marshalling error: %w", err)
	}
	llmConvJSON, err := toNullJSONString(exec.LLMConversation)
	if err != nil {
		return fmt.Errorf("marshalling llm conversation: %w", err)
	}

	row, err := r.queries.CreateNodeExecution(ctx, generated.CreateNodeExecutionParams{
		ID:              uuidToString(exec.ID),
		InstanceID:      uuidToString(exec.InstanceID),
		NodeID:          exec.NodeID,
		Attempt:         int64(exec.Attempt),
		Status:          string(exec.Status),
		ResolvedInputs:  resolvedInputsJSON,
		Output:          outputJSON,
		Error:           errorJSON,
		LLMConversation: llmConvJSON,
		LLMModel:        ptrToNullString(exec.LLMModel),
		LLMTokensIn:     intPtrToNullInt64(exec.LLMTokensIn),
		LLMTokensOut:    intPtrToNullInt64(exec.LLMTokensOut),
		ScriptStdout:    ptrToNullString(exec.ScriptStdout),
		ScriptStderr:    ptrToNullString(exec.ScriptStderr),
		ScriptExitCode:  intPtrToNullInt64(exec.ScriptExitCode),
		StartedAt:       timeToString(exec.StartedAt),
		CompletedAt:     timeToNullString(exec.CompletedAt),
	})
	if err != nil {
		return fmt.Errorf("creating node execution: %w", err)
	}

	startedAt, err := parseTime(row.StartedAt)
	if err != nil {
		return fmt.Errorf("parsing started_at: %w", err)
	}
	completedAt, err := nullStringToTimePtr(row.CompletedAt)
	if err != nil {
		return fmt.Errorf("parsing completed_at: %w", err)
	}
	exec.StartedAt = startedAt
	exec.CompletedAt = completedAt
	return nil
}

func (r *NodeExecutionRepo) Update(ctx context.Context, exec *instance.NodeExecution) error {
	outputJSON, err := toNullJSONString(exec.Output)
	if err != nil {
		return fmt.Errorf("marshalling output: %w", err)
	}
	errorJSON, err := toNullJSONString(exec.Error)
	if err != nil {
		return fmt.Errorf("marshalling error: %w", err)
	}
	llmConvJSON, err := toNullJSONString(exec.LLMConversation)
	if err != nil {
		return fmt.Errorf("marshalling llm conversation: %w", err)
	}

	err = r.queries.UpdateNodeExecution(ctx, generated.UpdateNodeExecutionParams{
		ID:              uuidToString(exec.ID),
		Status:          string(exec.Status),
		Output:          outputJSON,
		Error:           errorJSON,
		LLMConversation: llmConvJSON,
		LLMModel:        ptrToNullString(exec.LLMModel),
		LLMTokensIn:     intPtrToNullInt64(exec.LLMTokensIn),
		LLMTokensOut:    intPtrToNullInt64(exec.LLMTokensOut),
		ScriptStdout:    ptrToNullString(exec.ScriptStdout),
		ScriptStderr:    ptrToNullString(exec.ScriptStderr),
		ScriptExitCode:  intPtrToNullInt64(exec.ScriptExitCode),
		CompletedAt:     timeToNullString(exec.CompletedAt),
	})
	if err != nil {
		return fmt.Errorf("updating node execution: %w", err)
	}
	return nil
}

func (r *NodeExecutionRepo) ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	rows, err := r.queries.ListNodeExecutionsByInstance(ctx, uuidToString(instanceID))
	if err != nil {
		return nil, fmt.Errorf("listing node executions: %w", err)
	}

	executions := make([]instance.NodeExecution, 0, len(rows))
	for _, row := range rows {
		exec, err := r.rowToNodeExecution(row)
		if err != nil {
			return nil, err
		}
		executions = append(executions, *exec)
	}
	return executions, nil
}

func (r *NodeExecutionRepo) GetByInstanceAndNode(ctx context.Context, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	row, err := r.queries.GetNodeExecutionByInstanceAndNode(ctx, generated.GetNodeExecutionByInstanceAndNodeParams{
		InstanceID: uuidToString(instanceID),
		NodeID:     nodeID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("node execution instance=%s node=%s: %w", instanceID, nodeID, instance.ErrNotFound)
		}
		return nil, fmt.Errorf("getting node execution: %w", err)
	}
	return r.rowToNodeExecution(row)
}

func (r *NodeExecutionRepo) rowToNodeExecution(row generated.NodeExecution) (*instance.NodeExecution, error) {
	resolvedInputs, err := fromNullJSONString(row.ResolvedInputs)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling resolved inputs: %w", err)
	}
	output, err := fromNullJSONString(row.Output)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling output: %w", err)
	}
	execError, err := fromNullJSONString(row.Error)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling error: %w", err)
	}
	llmConv, err := fromNullJSONString(row.LLMConversation)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling llm conversation: %w", err)
	}

	id, err := parseUUID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}
	instanceID, err := parseUUID(row.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}
	startedAt, err := parseTime(row.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}
	completedAt, err := nullStringToTimePtr(row.CompletedAt)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}

	return &instance.NodeExecution{
		ID:              id,
		InstanceID:      instanceID,
		NodeID:          row.NodeID,
		Attempt:         int(row.Attempt),
		Status:          instance.NodeExecutionStatus(row.Status),
		ResolvedInputs:  resolvedInputs,
		Output:          output,
		Error:           execError,
		LLMConversation: llmConv,
		LLMModel:        nullStringToPtr(row.LLMModel),
		LLMTokensIn:     nullInt64ToIntPtr(row.LLMTokensIn),
		LLMTokensOut:    nullInt64ToIntPtr(row.LLMTokensOut),
		ScriptStdout:    nullStringToPtr(row.ScriptStdout),
		ScriptStderr:    nullStringToPtr(row.ScriptStderr),
		ScriptExitCode:  nullInt64ToIntPtr(row.ScriptExitCode),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
	}, nil
}
