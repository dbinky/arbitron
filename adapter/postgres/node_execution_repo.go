package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/postgres/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NodeExecutionRepo implements instance.NodeExecutionRepository using Postgres via sqlc.
type NodeExecutionRepo struct {
	pool    *pgxpool.Pool
	queries *generated.Queries
}

// NewNodeExecutionRepo creates a new NodeExecutionRepo backed by the given connection pool.
func NewNodeExecutionRepo(pool *pgxpool.Pool) *NodeExecutionRepo {
	return &NodeExecutionRepo{
		pool:    pool,
		queries: generated.New(pool),
	}
}

func (r *NodeExecutionRepo) Create(exec *instance.NodeExecution) error {
	resolvedInputsJSON, err := toJSON(exec.ResolvedInputs)
	if err != nil {
		return fmt.Errorf("marshalling resolved inputs: %w", err)
	}
	outputJSON, err := toJSON(exec.Output)
	if err != nil {
		return fmt.Errorf("marshalling output: %w", err)
	}
	errorJSON, err := toJSON(exec.Error)
	if err != nil {
		return fmt.Errorf("marshalling error: %w", err)
	}
	llmConvJSON, err := toJSON(exec.LLMConversation)
	if err != nil {
		return fmt.Errorf("marshalling llm conversation: %w", err)
	}

	row, err := r.queries.CreateNodeExecution(context.Background(), generated.CreateNodeExecutionParams{
		ID:              toPgUUID(exec.ID),
		InstanceID:      toPgUUID(exec.InstanceID),
		NodeID:          exec.NodeID,
		Attempt:         int32(exec.Attempt),
		Status:          generated.NodeExecutionStatus(exec.Status),
		ResolvedInputs:  resolvedInputsJSON,
		Output:          outputJSON,
		Error:           errorJSON,
		LlmConversation: llmConvJSON,
		LlmModel:        toPgTextPtr(exec.LLMModel),
		LlmTokensIn:     toPgInt4Ptr(exec.LLMTokensIn),
		LlmTokensOut:    toPgInt4Ptr(exec.LLMTokensOut),
		ScriptStdout:    toPgTextPtr(exec.ScriptStdout),
		ScriptStderr:    toPgTextPtr(exec.ScriptStderr),
		ScriptExitCode:  toPgInt4Ptr(exec.ScriptExitCode),
		StartedAt:       toPgTimestamptz(exec.StartedAt),
		CompletedAt:     toPgTimestamptzPtr(exec.CompletedAt),
	})
	if err != nil {
		return fmt.Errorf("creating node execution: %w", err)
	}

	exec.StartedAt = fromPgTimestamptz(row.StartedAt)
	exec.CompletedAt = fromPgTimestamptzPtr(row.CompletedAt)
	return nil
}

func (r *NodeExecutionRepo) Update(exec *instance.NodeExecution) error {
	outputJSON, err := toJSON(exec.Output)
	if err != nil {
		return fmt.Errorf("marshalling output: %w", err)
	}
	errorJSON, err := toJSON(exec.Error)
	if err != nil {
		return fmt.Errorf("marshalling error: %w", err)
	}
	llmConvJSON, err := toJSON(exec.LLMConversation)
	if err != nil {
		return fmt.Errorf("marshalling llm conversation: %w", err)
	}

	err = r.queries.UpdateNodeExecution(context.Background(), generated.UpdateNodeExecutionParams{
		ID:              toPgUUID(exec.ID),
		Status:          generated.NodeExecutionStatus(exec.Status),
		Output:          outputJSON,
		Error:           errorJSON,
		LlmConversation: llmConvJSON,
		LlmModel:        toPgTextPtr(exec.LLMModel),
		LlmTokensIn:     toPgInt4Ptr(exec.LLMTokensIn),
		LlmTokensOut:    toPgInt4Ptr(exec.LLMTokensOut),
		ScriptStdout:    toPgTextPtr(exec.ScriptStdout),
		ScriptStderr:    toPgTextPtr(exec.ScriptStderr),
		ScriptExitCode:  toPgInt4Ptr(exec.ScriptExitCode),
		CompletedAt:     toPgTimestamptzPtr(exec.CompletedAt),
	})
	if err != nil {
		return fmt.Errorf("updating node execution: %w", err)
	}
	return nil
}

func (r *NodeExecutionRepo) ListByInstance(instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	rows, err := r.queries.ListNodeExecutionsByInstance(context.Background(), toPgUUID(instanceID))
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

func (r *NodeExecutionRepo) GetByInstanceAndNode(instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	row, err := r.queries.GetNodeExecutionByInstanceAndNode(context.Background(), generated.GetNodeExecutionByInstanceAndNodeParams{
		InstanceID: toPgUUID(instanceID),
		NodeID:     nodeID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("node execution not found: instance=%s node=%s", instanceID, nodeID)
		}
		return nil, fmt.Errorf("getting node execution: %w", err)
	}
	return r.rowToNodeExecution(row)
}

func (r *NodeExecutionRepo) rowToNodeExecution(row generated.NodeExecution) (*instance.NodeExecution, error) {
	resolvedInputs, err := fromJSON(row.ResolvedInputs)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling resolved inputs: %w", err)
	}
	output, err := fromJSON(row.Output)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling output: %w", err)
	}
	execError, err := fromJSON(row.Error)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling error: %w", err)
	}
	llmConv, err := fromJSON(row.LlmConversation)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling llm conversation: %w", err)
	}

	return &instance.NodeExecution{
		ID:              fromPgUUID(row.ID),
		InstanceID:      fromPgUUID(row.InstanceID),
		NodeID:          row.NodeID,
		Attempt:         int(row.Attempt),
		Status:          instance.NodeExecutionStatus(row.Status),
		ResolvedInputs:  resolvedInputs,
		Output:          output,
		Error:           execError,
		LLMConversation: llmConv,
		LLMModel:        fromPgTextPtr(row.LlmModel),
		LLMTokensIn:     fromPgInt4Ptr(row.LlmTokensIn),
		LLMTokensOut:    fromPgInt4Ptr(row.LlmTokensOut),
		ScriptStdout:    fromPgTextPtr(row.ScriptStdout),
		ScriptStderr:    fromPgTextPtr(row.ScriptStderr),
		ScriptExitCode:  fromPgInt4Ptr(row.ScriptExitCode),
		StartedAt:       fromPgTimestamptz(row.StartedAt),
		CompletedAt:     fromPgTimestamptzPtr(row.CompletedAt),
	}, nil
}
