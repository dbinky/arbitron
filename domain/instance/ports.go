package instance

import (
	"github.com/google/uuid"
)

// InstanceRepository defines the port for persisting and querying workflow instances.
type InstanceRepository interface {
	Create(inst *Instance) error
	Get(id uuid.UUID) (*Instance, error)
	List(filters ListFilters) ([]InstanceSummary, error)
	UpdateState(id uuid.UUID, state State) error
	UpdateVariables(id uuid.UUID, variables map[string]any) error
	UpdateCurrentNode(id uuid.UUID, nodeID string) error
	SetFailed(id uuid.UUID, failedNodeID string) error
	SetCompleted(id uuid.UUID) error
	ListRunning() ([]Instance, error)
}

// NodeExecutionRepository defines the port for persisting and querying node executions.
type NodeExecutionRepository interface {
	Create(exec *NodeExecution) error
	Update(exec *NodeExecution) error
	ListByInstance(instanceID uuid.UUID) ([]NodeExecution, error)
	GetByInstanceAndNode(instanceID uuid.UUID, nodeID string) (*NodeExecution, error)
}

// VariableSnapshotRepository defines the port for persisting and querying variable snapshots.
type VariableSnapshotRepository interface {
	Create(snapshot *VariableSnapshot) error
	ListByInstance(instanceID uuid.UUID) ([]VariableSnapshot, error)
}

// UserRepository defines the port for querying users.
type UserRepository interface {
	GetDefaultUser() (uuid.UUID, error)
}
