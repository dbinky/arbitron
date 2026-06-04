package instance

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// InstanceRepository defines the port for persisting and querying workflow instances.
// Implementations must wrap domain sentinel errors (e.g., ErrNotFound) via
// fmt.Errorf %w so that the HTTP adapter's errors.Is-based error mapping works
// correctly.
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
	UpdatePriority(ctx context.Context, id uuid.UUID, priority Priority) error
	SetWaiting(ctx context.Context, id uuid.UUID, waitingUntil *time.Time) error
	SetWebhookFields(ctx context.Context, id uuid.UUID, token *string, secretHash *string) error
	SetWebhookReceived(ctx context.Context, id uuid.UUID, receivedAt time.Time) error
	Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*WorkflowStats, error)
}

// QueueRepository defines the port for queue-specific persistence operations.
type QueueRepository interface {
	CountRunning(ctx context.Context) (int, error)
	CountRunningByOwner(ctx context.Context, ownerID uuid.UUID) (int, error)
	CountQueued(ctx context.Context) (int, error)
	CountWaiting(ctx context.Context) (int, error)
	NextEligible(ctx context.Context, globalCap int, perUserCap int) (*Instance, error)
	SetQueued(ctx context.Context, id uuid.UUID, queuedAt time.Time) error
	ListWaitingWithTimer(ctx context.Context) ([]Instance, error)
	ListWaitingWithWebhook(ctx context.Context) ([]Instance, error)
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

// UserRepository defines the port for querying and persisting users. It is
// expanded in place (Phase 6) so the engine, which already imports it for
// GetDefaultUser, picks up the new methods without a relocation. Implementations
// must wrap a missing row as ErrNotFound via fmt.Errorf %w.
type UserRepository interface {
	GetDefaultUser(ctx context.Context) (uuid.UUID, error)             // existing, unchanged
	GetByID(ctx context.Context, id uuid.UUID) (*User, error)          // ErrNotFound if absent
	GetBySubject(ctx context.Context, subject string) (*User, error)   // ErrNotFound if new (JIT in Phase 2)
	Create(ctx context.Context, u *User) error
}
