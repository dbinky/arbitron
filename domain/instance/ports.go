package instance

import (
	"context"

	"github.com/google/uuid"
)

// InstanceRepository defines the port for persisting and querying workflow instances.
type InstanceRepository interface {
	Create(ctx context.Context, inst *Instance) error
	Get(ctx context.Context, id uuid.UUID) (*Instance, error)
	List(ctx context.Context, filters ListFilters) ([]InstanceSummary, error)
	UpdateState(ctx context.Context, id uuid.UUID, state State) error
	UpdateVariables(ctx context.Context, id uuid.UUID, variables map[string]any) error
	UpdateCurrentNode(ctx context.Context, id uuid.UUID, nodeID string) error
	SetFailed(ctx context.Context, id uuid.UUID, failedNodeID string) error
	SetCompleted(ctx context.Context, id uuid.UUID) error
	ListRunning(ctx context.Context) ([]Instance, error)
}

// NodeExecutionRepository defines the port for persisting and querying node executions.
type NodeExecutionRepository interface {
	Create(ctx context.Context, exec *NodeExecution) error
	Update(ctx context.Context, exec *NodeExecution) error
	ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]NodeExecution, error)
	GetByInstanceAndNode(ctx context.Context, instanceID uuid.UUID, nodeID string) (*NodeExecution, error)
}

// VariableSnapshotRepository defines the port for persisting and querying variable snapshots.
type VariableSnapshotRepository interface {
	Create(ctx context.Context, snapshot *VariableSnapshot) error
	ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]VariableSnapshot, error)
}

// UserRepository defines the port for querying users.
type UserRepository interface {
	GetDefaultUser(ctx context.Context) (uuid.UUID, error)
}
