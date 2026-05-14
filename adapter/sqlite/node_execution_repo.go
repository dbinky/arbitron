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

type NodeExecutionRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

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
		LlmConversation: llmConvJSON,
		LlmModel:        ptrToNullString(exec.LLMModel),
		LlmTokensIn:     intPtrToNullInt64(exec.LLMTokensIn),
		LlmTokensOut:    intPtrToNullInt64(exec.LLMTokensOut),
		ScriptStdout:    ptrToNullString(exec.ScriptStdout),
		ScriptStderr:    ptrToNullString(exec.ScriptStderr),
		ScriptExitCode:  intPtrToNullInt64(exec.ScriptExitCode),
		StartedAt:       timeToString(exec.StartedAt),
		CompletedAt:     timeToNullString(exec.CompletedAt),
	})
	if err != nil {
		return fmt.Errorf("creating node execution: %w", err)
	}

	exec.StartedAt = parseTime(row.StartedAt)
	exec.CompletedAt = nullStringToTimePtr(row.CompletedAt)
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
		LlmConversation: llmConvJSON,
		LlmModel:        ptrToNullString(exec.LLMModel),
		LlmTokensIn:     intPtrToNullInt64(exec.LLMTokensIn),
		LlmTokensOut:    intPtrToNullInt64(exec.LLMTokensOut),
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
			return nil, fmt.Errorf("node execution not found: instance=%s node=%s", instanceID, nodeID)
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
	llmConv, err := fromNullJSONString(row.LlmConversation)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling llm conversation: %w", err)
	}

	return &instance.NodeExecution{
		ID:              parseUUID(row.ID),
		InstanceID:      parseUUID(row.InstanceID),
		NodeID:          row.NodeID,
		Attempt:         int(row.Attempt),
		Status:          instance.NodeExecutionStatus(row.Status),
		ResolvedInputs:  resolvedInputs,
		Output:          output,
		Error:           execError,
		LLMConversation: llmConv,
		LLMModel:        nullStringToPtr(row.LlmModel),
		LLMTokensIn:     nullInt64ToIntPtr(row.LlmTokensIn),
		LLMTokensOut:    nullInt64ToIntPtr(row.LlmTokensOut),
		ScriptStdout:    nullStringToPtr(row.ScriptStdout),
		ScriptStderr:    nullStringToPtr(row.ScriptStderr),
		ScriptExitCode:  nullInt64ToIntPtr(row.ScriptExitCode),
		StartedAt:       parseTime(row.StartedAt),
		CompletedAt:     nullStringToTimePtr(row.CompletedAt),
	}, nil
}
