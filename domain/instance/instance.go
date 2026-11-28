// Package instance defines the domain types for workflow execution instances.
package instance

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ModelPinsVersionLegacy marks a run created before model pins existed; its
// ModelPins is nil and it resolves profiles live.
const ModelPinsVersionLegacy = 0

// ModelPinsVersionSnapshot marks a run whose ModelPins were captured at trigger.
const ModelPinsVersionSnapshot = 1

// ModelPinsVersionUnreadable is a summary-only marker for a stored pin record
// that failed to decode. It is never persisted; pin resolution refuses it as an
// unsupported version instead of falling back to the live profile.
const ModelPinsVersionUnreadable = -1

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
	ErrCannotRetry               = errors.New("instance cannot be retried")
	ErrStaleSessionGeneration    = errors.New("instance: stale session generation")
	ErrSessionClosed             = errors.New("instance: interactive session is closed")
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
	ParentInstanceID *uuid.UUID
	ParentNodeID     *string
	SpawnMode        SpawnMode
	// SpawnDepth is how many spawn hops separate this run from its root: 0 for a
	// root run, 1 for its child, and so on. It is the persisted counter behind
	// the MaxSpawnDepth runtime ceiling — see MaxSpawnDepth for why the bound
	// cannot be computed at author time. Derived by Engine.Spawn from the
	// parent's stored value; never caller-supplied.
	SpawnDepth int
	// ModelPins maps each model profile alias the workflow uses to the specific
	// model it resolved to at trigger time; ModelPinsVersion discriminates the
	// record. Version 0 with nil ModelPins is a LEGACY run created before pins
	// existed: it keeps live profile resolution until it finishes and is never
	// backfilled (D1). Version 1 is a snapshot, including a non-nil empty map
	// for a workflow with no aliases. Any other combination is corruption.
	// Set once by the engine Trigger path and never rewritten.
	ModelPins        map[string]string
	ModelPinsVersion int
	// ModelPinRoutes records native pinned-only exceptions by alias. Only the
	// canonical azure/bedrock families are valid; absent entries remain strict.
	ModelPinRoutes map[string]string
	// ModelPinRouteConnections records, per ModelPinRoutes alias, the profile
	// connection_ref captured at trigger. Absent on older family-only rows.
	ModelPinRouteConnections map[string]string
	// GroupRunID / GroupTaskKey / GroupTaskKind are the group-task half of the
	// link, set ONLY when SpawnMode == SpawnModeGroupTask (a child created by a
	// parallel_group node). nil/empty for roots and for legacy spawn children.
	// Like ParentInstanceID there is no foreign key on the stored group run id.
	GroupRunID        *uuid.UUID
	GroupTaskKey      *string
	GroupTaskKind     GroupTaskKind
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
	// Recovery is derived at read time from interrupted attempts; never persisted.
	Recovery       []RecoveryNotice `json:"Recovery,omitempty"`
	WorkspaceMoves []WorkspaceMove  `json:"WorkspaceMoves,omitempty"`
}

// IsRoot reports whether this run has no spawning parent. It is the ONE place
// the "null link means root" convention is expressed, so read paths do not each
// re-derive it.
func (i Instance) IsRoot() bool {
	return i.ParentInstanceID == nil
}

// IsGroupTask reports whether this run is a task child of a parallel_group
// node: a parent link whose mode is SpawnModeGroupTask with the group run id
// and task key present. It is the ONE predicate the terminal dispatch and the
// group coordinator use to tell group tasks from legacy spawn children.
func (i Instance) IsGroupTask() bool {
	return i.ParentInstanceID != nil && i.SpawnMode == SpawnModeGroupTask &&
		i.GroupRunID != nil && i.GroupTaskKey != nil && *i.GroupTaskKey != ""
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
	// ModelPins, ModelPinsVersion and ModelPinRoutes carry the run's pin record
	// (see Instance) so the list enricher describes the model a pinned run
	// executes rather than the live profile. ModelPinsVersionUnreadable marks a
	// stored record that failed to decode; the row still lists.
	ModelPins        map[string]string `json:"-"`
	ModelPinsVersion int               `json:"-"`
	ModelPinRoutes   map[string]string `json:"-"`
	// ModelPinRouteConnections mirrors Instance.ModelPinRouteConnections.
	ModelPinRouteConnections map[string]string `json:"-"`
}

type Submitter struct {
	Name            string
	AccountIdentity string
}

type EnrichedInstanceSummary struct {
	InstanceSummary
	RunName      string
	Submitter    Submitter
	FriendlyStep string
	// Model, Effort, Backend, Provider and NodeType describe the CONFIGURED
	// binding of the relevant node (current node while active, failed node when
	// failed; empty for completed/cancelled) — not the last recorded execution.
	Model    string
	Effort   string
	Backend  string
	Provider string
	NodeType string
	// GitRef is the branch/worktree the run is working on ("" = unknown).
	GitRef        string
	RetryCount    int
	QueuePosition int
	FailedNodeID  *string
	// ChildCount is the number of spawned children the CALLER may see. It drives
	// the hierarchy expander: a row with 0 renders none. It is computed
	// post-authorization, so a run whose children are all invisible to the caller
	// reports 0 and leaks no existence.
	ChildCount int
	Execution  []ExecutionStatus `json:"execution,omitempty"`
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
	ID                   uuid.UUID
	InstanceID           uuid.UUID
	NodeID               string
	Attempt              int
	Status               NodeExecutionStatus
	ResolvedInputs       map[string]any
	Output               map[string]any
	Error                map[string]any
	LLMConversation      map[string]any
	LLMModel             *string
	LLMTokensIn          *int
	LLMTokensOut         *int
	ScriptStdout         *string
	ScriptStderr         *string
	ScriptExitCode       *int
	StartedAt            time.Time
	CompletedAt          *time.Time
	SessionInvocationID  uuid.UUID
	SessionCheckpointRef *string
	SessionPinRunner     *string
	SessionGeneration    int64
	SessionMoves         []SessionMoveRecord
	RunnerID             *string           `json:"runnerId,omitempty"`
	SourceID             *string           `json:"sourceId,omitempty"`
	PlacementReason      *string           `json:"placementReason,omitempty"`
	PlacementHistory     []PlacementRecord `json:"placementHistory,omitempty"`
	// Recovery contains interrupted attempts of this node, derived at read time.
	Recovery       []RecoveryNotice `json:"Recovery,omitempty"`
	WorkspaceMoves []WorkspaceMove  `json:"WorkspaceMoves,omitempty"`
}

// PlacementRecord is one persisted placement fact of a node-execution attempt.
type PlacementRecord struct {
	JobID      string    `json:"jobId"`
	RunnerID   string    `json:"runnerId"`
	SourceID   string    `json:"sourceId,omitempty"`
	Reason     string    `json:"reason"`
	Outcome    string    `json:"outcome"`
	Generation int64     `json:"generation"`
	At         time.Time `json:"at"`
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
	// Unconstrained is the EXPLICIT admin assertion: "this caller may see every
	// row, and I have checked". It is the only thing that turns off the
	// visibility predicate.
	//
	// SECURITY: it exists because "no narrowing supplied" is ambiguous — it is
	// equally the shape of an admin list and of a caller whose scope was never
	// computed (a missing identity, a handler that forgot to route through the
	// service). Inferring admin from the absence of a filter made the second
	// case return the full table. Like OwnerID/Departments/IncludePublic this is
	// SERVICE-SET, by ListInstances/ListEnriched from listConstraint, and must
	// never be set from request input.
	Unconstrained bool
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
	// ActiveDescendantAncestors narrows the list to terminal runs that are
	// ancestors, at any depth, of active work visible to the same caller.
	// Both the ancestor row and active descendant obey the service-set scope.
	ActiveDescendantAncestors bool
	Limit                     int
	Offset                    int
}

// CancelStatus names what a successful cancel request did to the instance it
// was addressed to. Cancelling a terminal parent succeeds because its unfinished
// descendants were stopped, but the parent's own outcome is never reopened, so
// only an instance that actually ended up cancelled may be reported as such.
func CancelStatus(state State) string {
	if state == StateCancelled {
		return "cancelled"
	}
	return "cancel_requested"
}
