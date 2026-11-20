package instance

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// SpawnMode records HOW a child instance was created by its parent's spawn
// node. It is a self-owned type in domain/instance (the package that owns the
// stored field) so nothing here imports domain/workflow — the same
// one-directional domain edge Visibility and TriggerSource preserve. The
// public values MUST match workflow.SpawnModeFireAndForget / SpawnModeWaitForChild;
// SpawnModeGroupTask is internal to parallel-group execution.
type SpawnMode string

const (
	// SpawnModeFireAndForget: the parent continued immediately after starting
	// the child. The child's outcome never affects the parent.
	SpawnModeFireAndForget SpawnMode = "fire_and_forget"
	// SpawnModeWaitForChild: the parent suspended at the spawn node and resumes
	// through its success/failure port when the child reaches a terminal state.
	SpawnModeWaitForChild SpawnMode = "wait_for_child"
	// SpawnModeGroupTask: the child is one task of a parallel_group node's
	// group run (see GroupRun). It is INTERNAL — never an author-visible
	// spawn_workflow mode (workflow.IsValidSpawnMode still admits only the two
	// modes above). The parent is woken by the group coordinator, never by the
	// single-child wake in Engine.OnChildTerminal.
	//
	// Persistence: the 018 spawn_mode CHECK cannot admit this value without
	// rebuilding the instances table, so a group-task row stores spawn_mode
	// NULL plus group_run_id / group_task_key / group_task_kind (migration
	// 019); the SQLite read side maps that shape back to this mode. Legacy
	// spawn queries keep filtering on spawn_mode and therefore never match a
	// group task.
	SpawnModeGroupTask SpawnMode = "group_task"
)

// Valid reports whether m is one of the three recognized modes. The empty
// value is INVALID: an unset mode on a link would make the wake match and the
// resumption short-circuit ambiguous.
func (m SpawnMode) Valid() bool {
	return m == SpawnModeFireAndForget || m == SpawnModeWaitForChild || m == SpawnModeGroupTask
}

// SpawnLink is the child's back-reference to the exact parent NODE that created
// it. All three parts are load-bearing:
//
//   - ParentInstanceID identifies the run to wake / cascade to.
//   - ParentNodeID makes the link ADDRESSABLE: "the child spawned by this node
//     of this parent" is the key the resumption short-circuit and the wake both
//     match on. Without it, a fire-and-forget child from node A finishing while
//     the parent waits at node B would wrongly route node B.
//   - Mode is stored on the LINK rather than re-derived from the parent's
//     workflow snapshot, so both lookups are answerable without parsing every
//     parent's snapshot.
//
// There is intentionally NO foreign key on ParentInstanceID in storage, the
// same deliberate choice ScheduleID records: a purged parent must not
// cascade-delete a child's execution history.
type SpawnLink struct {
	ParentInstanceID uuid.UUID
	ParentNodeID     string
	Mode             SpawnMode

	// SpawnDepth is how many spawn hops separate the CHILD from its root run: a
	// root is 0, its child 1, and so on. It is DERIVED, never caller-supplied —
	// Engine.Spawn reads the parent's stored depth and adds one, so no executor
	// can forget to set it and no request can inflate it.
	SpawnDepth int

	// GroupRunID / GroupTaskKey / GroupTaskKind are set ONLY for
	// Mode == SpawnModeGroupTask: the group run that owns the child, the
	// task's key within that run, and the task's form. All three are required
	// for a group-task link and forbidden on every other mode (see Valid).
	GroupRunID    uuid.UUID
	GroupTaskKey  string
	GroupTaskKind GroupTaskKind
}

// MaxSpawnDepth is the runtime ceiling on spawn recursion: the deepest
// generation a run tree may reach before Engine.Spawn refuses to create another
// child.
//
// A BOUND IS REQUIRED, and author-time analysis cannot supply it. The spawn
// graph is a graph of workflow NAMES resolved at run time, so a cycle can be
// indirect (A spawns B spawns A) and need not be visible in any single
// definition. Unbounded, each generation costs a database row, a workspace, and
// a billed provider call — and because a shared or published workflow runs in
// the TRIGGERING user's queue under their ownership, the bill lands on the
// tenant who ran it, not the author who wrote the recursion.
//
// The value is deliberately generous: real nesting (a pipeline that fans out to
// per-task children which fan out again) is a handful of levels deep, so this
// is a runaway guard, not a design constraint an author should feel.
const MaxSpawnDepth = 10

// ErrSpawnDepthExceeded is returned when a spawn would create a child deeper
// than MaxSpawnDepth. It is a sentinel so the node's error behaviour can be
// distinguished from a transient spawn failure: retrying it can never succeed.
var ErrSpawnDepthExceeded = errors.New("maximum spawn depth exceeded")

// Valid reports whether the link is fully populated. A partial link is never
// written: a child with a parent id but no node id could not be matched by the
// wake and would strand its parent forever.
//
// SpawnDepth is deliberately NOT checked: 0 is meaningful (it is what a link
// carries before Engine.Spawn derives the real depth), and validity here is
// about matchability by the wake.
//
// A group-task link (Mode == SpawnModeGroupTask) additionally requires its
// group run id, task key and a valid task kind: without them the coordinator
// could not attribute the child's terminal to a roster row. Conversely a
// legacy-mode link carrying ANY group field is invalid — persistence writes
// the group columns only for group tasks, so such a link would silently lose
// half of what the caller asked for.
func (l SpawnLink) Valid() bool {
	if l.ParentInstanceID == uuid.Nil || l.ParentNodeID == "" || !l.Mode.Valid() {
		return false
	}
	if l.Mode == SpawnModeGroupTask {
		return l.GroupRunID != uuid.Nil && l.GroupTaskKey != "" && l.GroupTaskKind.Valid()
	}
	return l.GroupRunID == uuid.Nil && l.GroupTaskKey == "" && l.GroupTaskKind == ""
}

// ParentScope is the three-state parent narrowing on a list query.
//
// SECURITY POSTURE: unlike OwnerID/Departments/IncludePublic on ListFilters —
// which are SERVICE-SET from the request identity and must never come from
// request input — the parent scope IS legitimately caller-supplied. It is a
// NARROWING and never a widening: it composes with (never replaces) the
// service-set constraints, so a caller cannot see a run they could not already
// see by naming its parent.
type ParentScope string

const (
	// ParentScopeAny is the zero value: no parent narrowing at all.
	ParentScopeAny ParentScope = ""
	// ParentScopeRoots returns only instances with no parent link (top level).
	ParentScopeRoots ParentScope = "roots"
	// ParentScopeChildren returns only the children of ListFilters.ParentInstanceID.
	ParentScopeChildren ParentScope = "children"
)

// ParseParentScope decodes the wire form of the parent selector used by the
// REST query parameter and the MCP tool argument, so both surfaces share ONE
// decoder and cannot drift:
//
//	""              -> (ParentScopeAny,      nil)
//	"roots"         -> (ParentScopeRoots,    nil)
//	"<uuid>"        -> (ParentScopeChildren, &uuid)
//
// Anything else is an error; a malformed selector is rejected rather than
// silently degrading to an unfiltered list.
func ParseParentScope(raw string) (ParentScope, *uuid.UUID, error) {
	switch raw {
	case "":
		return ParentScopeAny, nil, nil
	case string(ParentScopeRoots):
		return ParentScopeRoots, nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return ParentScopeAny, nil, fmt.Errorf("invalid parent selector %q: expected \"roots\" or an instance UUID", raw)
	}
	return ParentScopeChildren, &id, nil
}

// ChildVisibility is the caller's already-computed, service-set visibility
// scope, passed verbatim into the batched child-count query.
//
// Counts MUST be computed post-authorization: a count that includes children
// the caller cannot see would render an expander whose expansion yields an
// empty list, leaking the existence of runs the caller may not view. This is
// the same non-disclosure discipline the shared-workflow visibility filter
// enforces.
type ChildVisibility struct {
	// Unconstrained is true for an admin: no visibility narrowing at all.
	Unconstrained bool
	OwnerID       *uuid.UUID
	Departments   []string
	IncludePublic bool
}

// ChildRef is a child and its advisory current state. Cancellation re-reads
// and re-authorizes every child before acting on it.
type ChildRef struct {
	ID    uuid.UUID
	State State
	// PausedBy is the ancestor whose cascading pause stopped this run, nil when
	// the run is not cascade-paused (including a pause an operator placed on
	// it directly).
	PausedBy *uuid.UUID
}

// SpawnLinkRepository answers link-shaped questions without any caller knowing
// how links are stored.
//
// It is declared as a NARROW port rather than as a widening of
// InstanceRepository so that the many existing in-memory InstanceRepository
// implementations across the test suites keep compiling. The concrete
// *sqlite.InstanceRepo satisfies both, exactly as it already satisfies
// schedule.ActiveRunChecker alongside instance.InstanceRepository.
type SpawnLinkRepository interface {
	// ChildCountsByParents returns the number of children per parent id,
	// restricted to what vis permits the caller to see.
	//
	// Contract (mirrors NodeExecutionRepository.AggregateByInstances):
	//   - Parents with no visible children are ABSENT from the map; absence
	//     means zero, not an error.
	//   - An empty parentIDs slice returns an empty map WITHOUT touching storage.
	// It exists so a page of the run list resolves every expander in ONE round
	// trip instead of N+1 queries.
	ChildCountsByParents(ctx context.Context, parentIDs []uuid.UUID, vis ChildVisibility) (map[uuid.UUID]int, error)

	// ListActiveChildren returns the ids of parentID's children that are still
	// in a cancellable state (queued, running, waiting, paused), REGARDLESS of
	// spawn mode. No active children is an empty slice, not an error. The
	// cancel cascade walks ListChildren to include terminal intermediates.
	ListActiveChildren(ctx context.Context, parentID uuid.UUID) ([]uuid.UUID, error)

	// ListChildren returns EVERY child, including terminal ones, with its
	// advisory state, in creation order, one level deep, regardless of spawn
	// mode. No children is an empty slice, not an error.
	ListChildren(ctx context.Context, parentID uuid.UUID) ([]ChildRef, error)

	// MarkSubtreeCancelRequested durably records cancellation intent for a
	// terminal run's subtree before the best-effort cascade. Active or unknown
	// rows are untouched; storage errors are returned.
	MarkSubtreeCancelRequested(ctx context.Context, id uuid.UUID) error

	// ListOrphanedActiveChildren returns active runs with a cancelled ancestor,
	// or a terminal ancestor stamped at or after the run's creation. The climb
	// crosses completed/failed intermediates but stops at active ancestors:
	// cancelling that topmost orphan cascades through its subtree. Unmarked
	// completed roots retain legitimate independent work. An empty result is
	// normal, not an error. This sweep finishes interrupted cancel cascades.
	ListOrphanedActiveChildren(ctx context.Context) ([]uuid.UUID, error)

	// MarkCascadePaused atomically moves id to paused, recording by as the
	// ancestor whose pause stopped it and the state it was in, ONLY IF id is
	// running, waiting or queued. false means the run was not in a pausable
	// state (it finished or was already paused) — the normal outcome of a
	// race, not an error.
	MarkCascadePaused(ctx context.Context, id, by uuid.UUID) (bool, error)

	// ClaimCascadePause clears id's cascade-pause marker ONLY IF id is paused
	// and was cascade-paused by by, returning the state it was paused from.
	// claimed is false when that predicate does not hold.
	ClaimCascadePause(ctx context.Context, id, by uuid.UUID) (from State, claimed bool, err error)

	// ClearCascadePause unconditionally clears id's cascade-pause marker.
	ClearCascadePause(ctx context.Context, id uuid.UUID) error

	// CascadePausedBy returns the ancestor whose cascading pause stopped id,
	// or nil when id is not cascade-paused.
	CascadePausedBy(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)

	// SuspendStrandedAt atomically moves id from `running` AT nodeID to
	// `waiting`, reporting whether the transition happened. It is the write
	// behind adopting a parent stranded at an awaited spawn node; the node
	// predicate keeps a parent that moved on from being parked at the old node.
	SuspendStrandedAt(ctx context.Context, id uuid.UUID, nodeID string) (bool, error)
}
