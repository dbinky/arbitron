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
	// SetQueuedIfWaiting re-queues id ONLY IF it is still in the waiting state,
	// reporting whether the transition happened.
	//
	// It is the wake path's half of the same mutual exclusion
	// SpawnWaitRepository.ClaimWaitingParent provides: an unconditional
	// SetQueued can overtake an executor that has already taken the parent back
	// to `running`, leaving one run both queued and executing. A false return is
	// the normal "somebody else already moved it" outcome, not an error.
	SetQueuedIfWaiting(ctx context.Context, id uuid.UUID, queuedAt time.Time) (bool, error)
	ListWaitingWithTimer(ctx context.Context) ([]Instance, error)
	ListWaitingWithWebhook(ctx context.Context) ([]Instance, error)
	// ListQueued returns all currently-queued instances ordered by priority
	// (high → normal → low) then QueuedAt FIFO within a priority — the same
	// ordering NextEligible uses to pick the next item. An empty queue returns
	// an empty slice, not an error.
	ListQueued(ctx context.Context) ([]Instance, error)
}

// NodeExecutionRepository defines the port for persisting and querying node executions.
type NodeExecutionRepository interface {
	Create(ctx context.Context, exec *NodeExecution) error
	Update(ctx context.Context, exec *NodeExecution) error
	ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]NodeExecution, error)
	GetByInstanceAndNode(ctx context.Context, instanceID uuid.UUID, nodeID string) (*NodeExecution, error)
	// AggregateByInstances collapses all NodeExecution rows for the given
	// instance IDs into one aggregate per instance (latest model, max attempt).
	// Returns a map keyed by instance ID. Instances with no
	// executions are absent from the map (the caller treats absence as the
	// zero value). An empty ids slice returns an empty map, not an error.
	AggregateByInstances(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]NodeExecutionAggregate, error)
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
//
// TODO(group-permissions phase): KNOWN DEBT — bounded-context inversion. The
// identity-account model (User, UserRepository, Provisioner, UserService, role
// sentinels) lives in domain/instance for now, which forces authentication code
// (adapter/http/auth_handler.go, the auth middleware) to import the
// workflow-instance domain purely to authenticate. The correct home is
// domain/identity (the dependency floor); instance should keep only OwnerID.
// The relocation is DEFERRED to the FIRST task of the group-permissions phase
// (it is a genuine prerequisite there, and reshaping the package twice is churn).
// Tracked via PR #20 review (thread r3358274543). Do not relocate piecemeal.
type UserRepository interface {
	GetDefaultUser(ctx context.Context) (uuid.UUID, error)    // existing, unchanged
	GetByID(ctx context.Context, id uuid.UUID) (*User, error) // ErrNotFound if absent
	// UsersByID batch-resolves the given user IDs in a single query, returning a
	// map keyed by user ID. IDs with no row are simply absent from the map (no
	// error). An empty ids slice returns an empty map without touching the DB.
	// The list enricher uses it to resolve a page's distinct owners in one round
	// trip instead of N per-owner GetByID calls.
	UsersByID(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]User, error)
	GetBySubject(ctx context.Context, subject string) (*User, error) // ErrNotFound if new (JIT in Phase 2)
	// GetByEmail returns the user with the given email (case-insensitive),
	// ErrNotFound if absent. Empty email never matches. Edge-mode provisioning
	// keys on the verified email; subject is nil for edge-created users.
	GetByEmail(ctx context.Context, email string) (*User, error)
	Create(ctx context.Context, u *User) error
	// UpdateUsername refreshes a user's display name (used when the IdP changes
	// it on re-login). The IdP subject, not the username, is the identity.
	UpdateUsername(ctx context.Context, id uuid.UUID, username string) error
	// List returns all users (admin user-management surface).
	List(ctx context.Context) ([]User, error)
	// UpdateRole sets a user's role ('user' or 'admin').
	UpdateRole(ctx context.Context, id uuid.UUID, role string) error
	// CountAdmins returns the number of users with the admin role (the
	// last-admin demotion floor reads it).
	CountAdmins(ctx context.Context) (int, error)
	// ListDepartments returns the user's persisted, normalized department set
	// (sorted). Empty when the user has no stored departments. (RBAC Phase 1)
	ListDepartments(ctx context.Context, userID uuid.UUID) ([]string, error)
	// ReplaceDepartments atomically replaces the user's department set with the
	// given normalized values (delete-all + insert in one transaction). An empty
	// slice clears the set. Idempotent: re-running with the same values is a
	// no-op. The provisioner calls this on every login to snapshot membership.
	// (RBAC Phase 1)
	ReplaceDepartments(ctx context.Context, userID uuid.UUID, departments []string) error
}
