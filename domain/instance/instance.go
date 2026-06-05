// Package instance defines the domain types for workflow execution instances.
package instance

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors for instance operations.
var (
	ErrNotFound             = errors.New("not found")
	ErrNotRunning           = errors.New("instance is not running")
	ErrNotPaused            = errors.New("instance is not paused")
	ErrNotFailed            = errors.New("instance is not failed")
	ErrNotQueued            = errors.New("instance is not queued")
	ErrNotWaiting           = errors.New("instance is not waiting")
	ErrNotInteractiveWait   = errors.New("instance is not in interactive wait state")
	ErrCannotCancel         = errors.New("instance cannot be cancelled")
	ErrCannotRerun          = errors.New("instance cannot be rerun")
)

// State represents the lifecycle state of a workflow instance.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateWaiting   State = "waiting"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StatePaused    State = "paused"
)

// Priority represents the scheduling priority of a workflow instance.
type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

// Visibility is an instance's audience, stamped at trigger time from the
// resolved workflow publication. Its values mirror the workflow visibility
// model. It is redeclared locally (NOT imported from domain/workflow) to keep
// the domain edge one-directional, the same pattern as instance.Claims
// redeclaring identity.Claims.
type Visibility string

const (
	VisibilityPrivate    Visibility = "private"
	VisibilityDepartment Visibility = "department"
	VisibilityPublic     Visibility = "public"
)

// Instance is a single execution of a workflow definition.
type Instance struct {
	ID                uuid.UUID
	OwnerID           uuid.UUID
	WorkflowName      string
	WorkflowSnapshot  map[string]any
	// Department is the department this run was stamped under at trigger time
	// (a snapshot; "" => default group / private). Visibility is the run's
	// audience. Both are set by the engine Trigger path (Phase 3 Task 2).
	Department        string
	Visibility        Visibility
	State             State
	Priority          Priority
	Variables         map[string]any
	OriginalVariables map[string]any
	CurrentNodeID     *string
	FailedNodeID      *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
	QueuedAt          *time.Time
	WaitingUntil      *time.Time
	WebhookToken      *string
	WebhookSecretHash *string
	WebhookReceivedAt *time.Time
}

// InstanceSummary is a lightweight representation for listing instances.
type InstanceSummary struct {
	ID            uuid.UUID  `json:"id"`
	WorkflowName  string     `json:"workflowName"`
	State         State      `json:"state"`
	Priority      Priority   `json:"priority"`
	CurrentNodeID *string    `json:"currentNodeId,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
	QueuedAt      *time.Time `json:"queuedAt,omitempty"`
}

// NodeExecutionStatus represents the outcome of a single node execution.
type NodeExecutionStatus string

const (
	NodeStatusSucceeded NodeExecutionStatus = "succeeded"
	NodeStatusFailed    NodeExecutionStatus = "failed"
	NodeStatusSkipped   NodeExecutionStatus = "skipped"
)

// NodeExecution records the result of executing a single node within an instance.
type NodeExecution struct {
	ID              uuid.UUID
	InstanceID      uuid.UUID
	NodeID          string
	Attempt         int
	Status          NodeExecutionStatus
	ResolvedInputs  map[string]any
	Output          map[string]any
	Error           map[string]any
	LLMConversation map[string]any
	LLMModel        *string
	LLMTokensIn     *int
	LLMTokensOut    *int
	ScriptStdout    *string
	ScriptStderr    *string
	ScriptExitCode  *int
	StartedAt       time.Time
	CompletedAt     *time.Time
}

// VariableSnapshot captures the state of instance variables after a node executes.
type VariableSnapshot struct {
	ID         uuid.UUID
	InstanceID uuid.UUID
	NodeID     string
	Variables  map[string]any
	CapturedAt time.Time
}

// WorkflowStats summarises execution metrics for a workflow over a time range.
type WorkflowStats struct {
	TotalRuns       int     `json:"totalRuns"`
	SuccessCount    int     `json:"successCount"`
	FailureCount    int     `json:"failureCount"`
	AvgDurationSecs float64 `json:"avgDurationSeconds"`
}

// ListFilters specifies filtering and pagination options for listing instances.
type ListFilters struct {
	State        *State
	WorkflowName *string
	Since        *time.Time
	// OwnerID, when non-nil, restricts results to instances owned by that user.
	// SECURITY: this is set by the engine's ListInstances service from the
	// request identity (non-admin => own UserID); list callers (HTTP/MCP handlers)
	// MUST NOT set it from request input, or a non-admin could widen scope.
	OwnerID *uuid.UUID
	Limit   int
	Offset  int
}
