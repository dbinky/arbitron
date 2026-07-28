// Package instance defines the domain types for workflow execution instances.
package instance

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors for instance operations.
var (
	ErrNotFound           = errors.New("not found")
	ErrNotRunning         = errors.New("instance is not running")
	ErrNotPaused          = errors.New("instance is not paused")
	ErrNotFailed          = errors.New("instance is not failed")
	ErrNotQueued          = errors.New("instance is not queued")
	ErrNotWaiting         = errors.New("instance is not waiting")
	ErrNotInteractiveWait = errors.New("instance is not in interactive wait state")
	// ErrInteractiveTurnInProgress is returned when a message is sent to an
	// interactive session that already has a turn in flight. Interactive turns
	// resume a single stateful provider session (e.g. `claude --resume`), which
	// is not safe to drive concurrently; overlapping sends are rejected rather
	// than corrupting the session.
	ErrInteractiveTurnInProgress = errors.New("instance already has an interactive turn in progress")
	ErrCannotCancel              = errors.New("instance cannot be cancelled")
	ErrCannotRerun               = errors.New("instance cannot be rerun")
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

// IsTerminal reports whether s is a final state a run can no longer leave.
// This is the ONE definition of "done": the awaited-spawn resumption
// short-circuit, the child-terminal wake, and the startup reconciler all read
// it, so they cannot disagree about when a child has finished.
func (s State) IsTerminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

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

// Valid reports whether v is one of the three defined visibility levels.
func (v Visibility) Valid() bool {
	return v == VisibilityPrivate || v == VisibilityDepartment || v == VisibilityPublic
}

// VisibilityFromWorkflow maps a workflow visibility VALUE (passed as a string so
// domain/instance never imports domain/workflow — the same one-directional edge
// instance.Claims preserves) to the corresponding instance.Visibility. It is the
// ONE place the workflow→instance conversion lives, replacing the inline switches
// the engine hand-coded (PR #21).
//
// An unrecognized value maps to VisibilityPrivate (fail-closed to owner-only). A
// totality test over workflow.Visibility's values guards against a fourth level
// being added to workflow without extending this mapping — stronger protection
// than a defaulting switch buried in the engine, which would silently downgrade
// the new level with no failure.
func VisibilityFromWorkflow(workflowVisibility string) Visibility {
	switch workflowVisibility {
	case string(VisibilityPublic):
		return VisibilityPublic
	case string(VisibilityDepartment):
		return VisibilityDepartment
	default: // "private" or unrecognized ⇒ owner-only (fail closed)
		return VisibilityPrivate
	}
}

// Instance is a single execution of a workflow definition.
type Instance struct {
	ID      uuid.UUID
	OwnerID uuid.UUID
	// WorkflowID is the stable identity of the workflow this run was triggered
	// from (resolved name->id at trigger time via the workflow registry). It is
	// uuid.Nil for legacy runs created before id-references existed, or when no
	// registry is wired (local mode). WorkflowName is RETAINED as the human-facing
	// display label; WorkflowSnapshot remains the authority for execution. A rerun
	// resolves by WorkflowID (id->current name), falling back to WorkflowName when
	// the id is Nil.
	WorkflowID       uuid.UUID
	WorkflowName     string
	WorkflowSnapshot map[string]any
	// Department is the department this run was stamped under at trigger time
	// (a snapshot; "" => default group / private). Visibility is the run's
	// audience. Both are set by the engine Trigger path (Phase 3 Task 2).
	Department string
	Visibility Visibility
	// TriggerSource records how the run was started (manual / schedule /
	// run_now). It defaults to OriginManual; the engine stamps it from
	// OriginFromContext at trigger time (Phase 3). ScheduleID is the originating
	// schedule for schedule/run_now runs, nil otherwise. There is intentionally
	// NO foreign key on schedule_id: a schedule may be deleted while its
	// historical runs remain.
	TriggerSource TriggerSource
	ScheduleID    *uuid.UUID
	// ParentInstanceID / ParentNodeID / SpawnMode are the child's link back to
	// the parent's spawn node that created it (see SpawnLink). All three are
	// nil/empty for a root run — every instance created before this feature and
	// every manually triggered run after it. Null links are NORMAL, not an
	// error: read paths treat them as "this is a root run".
	//
	// There is intentionally NO foreign key on parent_instance_id, the same
	// deliberate choice recorded for ScheduleID above: a referenced row may be
	// removed while historical runs remain, and a purged parent must not
	// cascade-delete a child's execution history.
	ParentInstanceID  *uuid.UUID
	ParentNodeID      *string
	SpawnMode         SpawnMode
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

// IsRoot reports whether this run has no spawning parent. It is the ONE place
// the "null link means root" convention is expressed, so read paths do not each
// re-derive it.
func (i Instance) IsRoot() bool {
	return i.ParentInstanceID == nil
}

// InstanceSummary is a lightweight representation for listing instances.
type InstanceSummary struct {
	ID            uuid.UUID     `json:"id"`
	OwnerID       uuid.UUID     `json:"ownerId"`
	WorkflowName  string        `json:"workflowName"`
	State         State         `json:"state"`
	Priority      Priority      `json:"priority"`
	Department    string        `json:"department,omitempty"`
	Visibility    Visibility    `json:"visibility,omitempty"`
	TriggerSource TriggerSource `json:"triggerSource"`
	ScheduleID    *uuid.UUID    `json:"scheduleId,omitempty"`
	// ParentInstanceID is the spawning parent run, nil for a root. It is carried
	// on the summary (the projection the list path and enricher consume) so the
	// hierarchy read needs no per-row instance load.
	ParentInstanceID *uuid.UUID `json:"parentInstanceId,omitempty"`
	CurrentNodeID    *string    `json:"currentNodeId,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
	QueuedAt         *time.Time `json:"queuedAt,omitempty"`
	// FailedNodeID is the node the instance failed on (nil unless failed). Carried
	// on the summary so the list enricher resolves the friendly step/error anchor
	// without a per-row instance load.
	FailedNodeID *string `json:"failedNodeId,omitempty"`
	// Variables and WorkflowSnapshot are the run's variables and workflow
	// definition snapshot, carried on the summary so the list enricher derives the
	// run name and friendly step without a per-row instance load (avoids an N+1).
	Variables        map[string]any `json:"-"`
	WorkflowSnapshot map[string]any `json:"-"`
}

type Submitter struct {
	Name            string
	AccountIdentity string
}

type EnrichedInstanceSummary struct {
	InstanceSummary
	RunName       string
	Submitter     Submitter
	FriendlyStep  string
	Model         string
	Backend       string
	RetryCount    int
	QueuePosition int
	FailedNodeID  *string
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
	// Departments and IncludePublic widen a non-admin's list to the disjunction
	// owner_id=self OR visibility=public (when IncludePublic) OR
	// (visibility=department AND department IN Departments).
	// SECURITY: like OwnerID above, these are SERVICE-SET by ListInstances from
	// the request identity; list callers (HTTP/MCP) MUST NOT set them from
	// request input, or a non-admin could widen scope to other departments.
	Departments   []string
	IncludePublic bool
	// ParentScope narrows the list by position in the spawn hierarchy: unset
	// (no narrowing), roots-only, or the children of ParentInstanceID.
	//
	// Unlike OwnerID/Departments/IncludePublic above, this IS a legitimate
	// caller-supplied value: it is a NARROWING, never a widening, and composes
	// with — never replaces — the service-set constraints.
	ParentScope ParentScope
	// ParentInstanceID is the parent whose children to return. Read ONLY when
	// ParentScope == ParentScopeChildren; ignored otherwise.
	ParentInstanceID *uuid.UUID
	Limit            int
	Offset           int
}
