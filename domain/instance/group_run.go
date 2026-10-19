package instance

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// This file holds the durable domain model of ONE execution of a
// parallel_group node: the group run, its ordered task roster, the frozen
// decision, and the repository port that persists them.
//
// It deliberately does NOT import domain/workflow (the same one-directional
// domain edge SpawnMode and Visibility preserve). The workflow-side vocabulary
// (form, completion rule, unfinished behaviour, gather mode) is carried here as
// plain strings whose values are workflow.GroupForm* / GroupRule* /
// GroupUnfinished* / GroupGather* — the Phase 1 parser (workflow.ParseGroupConfig)
// is the single authority that validates them before a run is created.

// GroupTaskKind distinguishes the two group forms at the TASK level: a branch
// task runs a body of the parent's own definition snapshot; a workflow task
// runs a separately named child workflow.
type GroupTaskKind string

const (
	// GroupTaskKindBranch: the task is a branch of the parent's definition,
	// started at the branch entry node (workflow form "branches").
	GroupTaskKindBranch GroupTaskKind = "branch"
	// GroupTaskKindWorkflow: the task is a child workflow (workflow form
	// "workflows").
	GroupTaskKindWorkflow GroupTaskKind = "workflow"
)

// Valid reports whether k is one of the two task kinds. The empty value is
// INVALID: a group-task child without a kind could not be told apart from the
// other form by any read path.
func (k GroupTaskKind) Valid() bool {
	return k == GroupTaskKindBranch || k == GroupTaskKindWorkflow
}

// GroupRunState is the lifecycle of one group run.
//
//	launching → running → decided → done
//
// Phase 3 inserts gathering → (resolving_conflicts) between decided and done
// for results_and_files groups. A launch failure may decide straight from
// launching.
type GroupRunState string

const (
	GroupRunLaunching          GroupRunState = "launching"
	GroupRunRunning            GroupRunState = "running"
	GroupRunDecided            GroupRunState = "decided"
	GroupRunGathering          GroupRunState = "gathering"           // Phase 3
	GroupRunResolvingConflicts GroupRunState = "resolving_conflicts" // Phase 3
	GroupRunDone               GroupRunState = "done"
)

// Valid reports whether s is one of the six run states.
func (s GroupRunState) Valid() bool {
	switch s {
	case GroupRunLaunching, GroupRunRunning, GroupRunDecided,
		GroupRunGathering, GroupRunResolvingConflicts, GroupRunDone:
		return true
	default:
		return false
	}
}

// IsDecidable reports whether a run in state s may still take its decision
// (the GroupRunRepository.Decide CAS precondition).
func (s GroupRunState) IsDecidable() bool {
	return s == GroupRunLaunching || s == GroupRunRunning
}

// GroupTaskLaunchState records whether the task's child has been handed to the
// engine. It is persisted BEFORE enqueue (pending) so a crash between roster
// registration and launch is recoverable without duplicating children.
type GroupTaskLaunchState string

const (
	GroupTaskPending      GroupTaskLaunchState = "pending"
	GroupTaskLaunched     GroupTaskLaunchState = "launched"
	GroupTaskLaunchFailed GroupTaskLaunchState = "launch_failed"
)

// Valid reports whether s is one of the three launch states.
func (s GroupTaskLaunchState) Valid() bool {
	return s == GroupTaskPending || s == GroupTaskLaunched || s == GroupTaskLaunchFailed
}

// GroupTaskStatus is the shared vocabulary of a task's LIVE status (what its
// child is doing now) and its FROZEN status (what the decision recorded).
type GroupTaskStatus string

const (
	GroupTaskStatusNotStarted GroupTaskStatus = "not_started" // live only: never launched
	GroupTaskStatusQueued     GroupTaskStatus = "queued"
	GroupTaskStatusRunning    GroupTaskStatus = "running"
	GroupTaskStatusWaiting    GroupTaskStatus = "waiting"
	GroupTaskStatusPaused     GroupTaskStatus = "paused"
	GroupTaskStatusCompleted  GroupTaskStatus = "completed"
	GroupTaskStatusFailed     GroupTaskStatus = "failed"
	GroupTaskStatusCancelled  GroupTaskStatus = "cancelled"
	GroupTaskStatusUnfinished GroupTaskStatus = "unfinished" // frozen only
)

// Valid reports whether s is one of the nine statuses (live or frozen).
func (s GroupTaskStatus) Valid() bool {
	switch s {
	case GroupTaskStatusNotStarted, GroupTaskStatusQueued, GroupTaskStatusRunning,
		GroupTaskStatusWaiting, GroupTaskStatusPaused, GroupTaskStatusCompleted,
		GroupTaskStatusFailed, GroupTaskStatusCancelled, GroupTaskStatusUnfinished:
		return true
	default:
		return false
	}
}

// IsLive reports whether s may appear as a task's LIVE status. "unfinished" is
// a decision-time verdict about a task that had not finished; no child is ever
// "unfinished", so it is frozen-only.
func (s GroupTaskStatus) IsLive() bool {
	return s.Valid() && s != GroupTaskStatusUnfinished
}

// IsTerminal reports whether s is a final child outcome (completed, failed or
// cancelled). It mirrors State.IsTerminal so the group code and the spawn code
// agree on "done". "unfinished" is NOT terminal: it describes a task that was
// still active when the decision froze it.
func (s GroupTaskStatus) IsTerminal() bool {
	return s == GroupTaskStatusCompleted || s == GroupTaskStatusFailed || s == GroupTaskStatusCancelled
}

// GroupTaskStatusFromState maps a child instance's lifecycle state to the live
// task status. It is the ONE place that conversion lives. The empty state (no
// child row yet) and any unrecognized value map to not_started: a task is only
// reported as active once its child is observably so. A totality test over
// every State value guards against a new State landing unmapped.
func GroupTaskStatusFromState(s State) GroupTaskStatus {
	switch s {
	case StateQueued:
		return GroupTaskStatusQueued
	case StateRunning:
		return GroupTaskStatusRunning
	case StateWaiting:
		return GroupTaskStatusWaiting
	case StatePaused:
		return GroupTaskStatusPaused
	case StateCompleted:
		return GroupTaskStatusCompleted
	case StateFailed:
		return GroupTaskStatusFailed
	case StateCancelled:
		return GroupTaskStatusCancelled
	default:
		return GroupTaskStatusNotStarted
	}
}

// GroupTask is one row of a group run's ordered task roster. The roster is
// persisted in full BEFORE any child is enqueued, with ChildInstanceID already
// reserved, so launch is idempotent across crashes.
type GroupTask struct {
	GroupRunID      uuid.UUID
	Position        int // author order, 0-based
	Key             string
	Kind            GroupTaskKind
	Origin          string    // branch entry node id, or child workflow name
	ChildInstanceID uuid.UUID // reserved BEFORE enqueue
	LaunchState     GroupTaskLaunchState
	LaunchError     string
	LiveStatus      GroupTaskStatus
	// TerminalSeq is the global, monotonically increasing order assigned ONCE
	// atomically with the child's terminal state (see GroupRunRepository). It lets first_success pick the earliest success
	// even when callbacks arrive out of order or after a restart. nil until
	// the task's terminal is recorded.
	TerminalSeq *int64
	TerminalAt  *time.Time
	// Result is the child's final variables minus reserved names; recorded for
	// failed/cancelled children too (available variables are retained).
	Result map[string]any
	// Failure is {"state","failedNodeId","error","attempt"} for failed/cancelled.
	Failure                 map[string]any
	CancelRequested         bool
	Blocking                bool // true until a decision releases it
	ContinuingIndependently bool
	FinalizedRef            string // Phase 3 (always empty in Phase 2)
}

// FrozenTask is a task's entry in the immutable decision snapshot.
type FrozenTask struct {
	Key             string          `json:"key"`
	Kind            GroupTaskKind   `json:"kind"`
	Origin          string          `json:"origin"`
	ChildInstanceID string          `json:"childInstanceId"`
	Status          GroupTaskStatus `json:"status"` // completed|failed|cancelled|unfinished
	Result          map[string]any  `json:"result,omitempty"`
	Failure         map[string]any  `json:"failure,omitempty"`
	CancelRequested bool            `json:"cancelRequested"`
	Winner          bool            `json:"winner"`
}

// GroupDecision is the atomic, immutable verdict of a group run: the rule that
// fired, the outcome, the winner (first_success) and the ordered task
// snapshot. Once written (GroupRunRepository.Decide) it never changes; later
// task terminals update live status only.
type GroupDecision struct {
	Outcome   string       `json:"outcome"` // "success" | "failure"
	Rule      string       `json:"rule"`
	Behavior  string       `json:"behavior"`
	WinnerKey string       `json:"winnerKey,omitempty"`
	Reason    string       `json:"reason"` // e.g. "all tasks completed", "task b failed", "launch failed: ..."
	DecidedAt time.Time    `json:"decidedAt"`
	Tasks     []FrozenTask `json:"tasks"` // author order
}

// GroupRun is the durable record of one execution of a parallel_group node.
type GroupRun struct {
	ID                 uuid.UUID
	ParentInstanceID   uuid.UUID
	NodeID             string
	Attempt            int // 1
	Form               string
	CompletionRule     string
	UnfinishedBehavior string
	GatherMode         string
	Config             map[string]any      // JSON snapshot of workflow.GroupConfig
	BodyOwnership      map[string][]string // taskKey -> body node ids (branches)
	StartVariables     map[string]any      // deep copy, reserved names excluded
	// WorkspaceSnapshot is the handle returned by
	// engine.GroupWorkspace.SnapshotParent ("" when the parent had no workspace).
	WorkspaceSnapshot     string
	State                 GroupRunState
	Decision              *GroupDecision
	FinalOutput           map[string]any // set once; immutable afterwards
	ContinuationPort      string         // "success"|"failure"; set with FinalOutput
	ContinuationReady     bool
	ContinuationDelivered bool
	ActionsApplied        bool // unfinished-task actions fully applied after decision
	LaunchError           string
	// Gather is the durable file-gather state and immutable final report.
	// It is nil until first written.
	Gather    *GatherReport
	CreatedAt time.Time
	UpdatedAt time.Time
	Tasks     []GroupTask // ordered by Position
}

// TaskByKey returns a pointer to the roster entry with the given key.
func (r *GroupRun) TaskByKey(key string) (*GroupTask, bool) {
	for i := range r.Tasks {
		if r.Tasks[i].Key == key {
			return &r.Tasks[i], true
		}
	}
	return nil, false
}

// TaskByChild returns a pointer to the roster entry whose reserved child id is
// childID.
func (r *GroupRun) TaskByChild(childID uuid.UUID) (*GroupTask, bool) {
	for i := range r.Tasks {
		if r.Tasks[i].ChildInstanceID == childID {
			return &r.Tasks[i], true
		}
	}
	return nil, false
}

// Sentinel errors of the group-run repository.
var (
	ErrGroupRunExists   = errors.New("group run already exists for this parent node")
	ErrGroupRunNotFound = errors.New("group run not found")
	// ErrGroupTaskNotFound is returned by the per-task repository operations
	// when the run exists but has no task with the given key.
	ErrGroupTaskNotFound = errors.New("group task not found")
)

// GroupRunRepository persists group runs and their rosters. Every mutating
// method that can race (TransitionState, Decide, SetFinalOutput) is a
// compare-and-swap whose boolean result reports whether THIS caller won;
// losing is not an error.
//
// Task terminal receipts (TerminalSeq, LiveStatus, result, failure, terminal
// time) have no writer on this port: an implementation MUST record them
// atomically with the child's terminal InstanceRepository write, assigning
// TerminalSeq once from a durable, monotonically increasing allocator, and
// keep them immutable afterwards (see InstanceRepository, TERMINAL RECEIPTS).
// The coordinator only reads them and stalls a group whose terminal child has
// no receipt.
type GroupRunRepository interface {
	// CreateWithTasks atomically inserts the run and its full roster.
	// ErrGroupRunExists on a (parent, node, attempt) conflict.
	CreateWithTasks(ctx context.Context, run *GroupRun) error
	// Get loads a run with its roster ordered by Position. ErrGroupRunNotFound.
	Get(ctx context.Context, id uuid.UUID) (*GroupRun, error)
	// GetByParentNode returns the latest attempt for (parent, node), or
	// (nil, nil) when there is none.
	GetByParentNode(ctx context.Context, parentID uuid.UUID, nodeID string) (*GroupRun, error)
	// GetByChild returns the run owning the task whose child is childID, or
	// (nil, nil) when childID is not a group task child.
	GetByChild(ctx context.Context, childID uuid.UUID) (*GroupRun, error)
	// ListByParent returns every run of parentID, oldest first.
	ListByParent(ctx context.Context, parentID uuid.UUID) ([]GroupRun, error)
	// ListUnsettled returns the ids of runs with state != done OR
	// !continuation_delivered OR !actions_applied, plus runs whose failed or
	// cancelled parent still owns active task instances. Oldest first, once
	// per run; completed parents retain legitimate independent work.
	ListUnsettled(ctx context.Context) ([]uuid.UUID, error)
	MarkTaskLaunched(ctx context.Context, runID uuid.UUID, key string) error
	MarkTaskLaunchFailed(ctx context.Context, runID uuid.UUID, key, reason string) error
	// TransitionState is a CAS from -> to.
	TransitionState(ctx context.Context, runID uuid.UUID, from, to GroupRunState) (bool, error)
	// UpdateTaskLiveStatus never overwrites a terminal live status.
	UpdateTaskLiveStatus(ctx context.Context, runID uuid.UUID, key string, status GroupTaskStatus) error
	// Decide is a CAS: state IN (launching, running) -> decided AND decision IS
	// NULL, in ONE transaction that also sets Blocking=false for every task.
	Decide(ctx context.Context, runID uuid.UUID, d GroupDecision) (bool, error)
	MarkTaskCancelRequested(ctx context.Context, runID uuid.UUID, key string) error
	MarkTaskContinuingIndependently(ctx context.Context, runID uuid.UUID, key string) error
	MarkActionsApplied(ctx context.Context, runID uuid.UUID) error
	// SetFinalOutput is a CAS: state from -> done AND final_output IS NULL;
	// it also records the port and sets continuation_ready=true.
	SetFinalOutput(ctx context.Context, runID uuid.UUID, port string, output map[string]any, from GroupRunState) (bool, error)
	MarkContinuationDelivered(ctx context.Context, runID uuid.UUID) error
	// SetGatherReport replaces the validated, normalized report. Unknown runs
	// return ErrGroupRunNotFound; done runs return ErrGatherReportFrozen;
	// malformed reports return ErrInvalidGatherReport.
	SetGatherReport(ctx context.Context, runID uuid.UUID, report GatherReport) error
	// ReplaceGatherReport is SetGatherReport as a CAS: it writes only while
	// the stored report is still expected (nil: none stored yet, see
	// SameGatherReport), else ErrGatherReportStale. Every writer of a live
	// gather uses it, so none can erase another's progress or attempt outcome.
	ReplaceGatherReport(ctx context.Context, runID uuid.UUID, expected *GatherReport, report GatherReport) error
	// SetTaskFinalizedRef publishes the task's immutable working-area ref.
	// Repeating the same ref is idempotent; a different ref returns an error.
	// Unknown runs or keys return ErrGroupRunNotFound.
	SetTaskFinalizedRef(ctx context.Context, runID uuid.UUID, key, ref string) error
}

// NodeAddressedWaker re-queues a waiting parent ONLY IF it is still waiting at
// the given node. It is the group coordinator's wake primitive: the existing
// QueueRepository.SetQueuedIfWaiting checks state alone, so a parent that has
// moved on to (and is waiting at) a LATER node could be woken by a group it
// has already left. Satisfied by *sqlite.QueueRepo (P2-T4).
type NodeAddressedWaker interface {
	SetQueuedIfWaitingAtNode(ctx context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error)
}
