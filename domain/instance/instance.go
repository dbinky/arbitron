// Package instance defines the domain types for workflow execution instances.
package instance

import (
	"time"

	"github.com/google/uuid"
)

// State represents the lifecycle state of a workflow instance.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
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

// Instance is a single execution of a workflow definition.
type Instance struct {
	ID                uuid.UUID
	OwnerID           uuid.UUID
	WorkflowName      string
	WorkflowSnapshot  map[string]any
	State             State
	Priority          Priority
	Variables         map[string]any
	OriginalVariables map[string]any
	CurrentNodeID     *string
	FailedNodeID      *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
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

// ListFilters specifies filtering and pagination options for listing instances.
type ListFilters struct {
	State        *State
	WorkflowName *string
	Limit        int
	Offset       int
}
