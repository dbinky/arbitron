package instance

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// SpawnMode records HOW a child instance was created by its parent's spawn
// node. It is a self-owned type in domain/instance (the package that owns the
// stored field) so nothing here imports domain/workflow — the same
// one-directional domain edge Visibility and TriggerSource preserve. The
// values MUST match workflow.SpawnModeFireAndForget / SpawnModeWaitForChild.
type SpawnMode string

const (
	// SpawnModeFireAndForget: the parent continued immediately after starting
	// the child. The child's outcome never affects the parent.
	SpawnModeFireAndForget SpawnMode = "fire_and_forget"
	// SpawnModeWaitForChild: the parent suspended at the spawn node and resumes
	// through its success/failure port when the child reaches a terminal state.
	SpawnModeWaitForChild SpawnMode = "wait_for_child"
)

// Valid reports whether m is one of the two recognized modes. The empty value
// is INVALID: an unset mode on a link would make the wake match and the
// resumption short-circuit ambiguous.
func (m SpawnMode) Valid() bool {
	return m == SpawnModeFireAndForget || m == SpawnModeWaitForChild
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
}

// Valid reports whether the link is fully populated. A partial link is never
// written: a child with a parent id but no node id could not be matched by the
// wake and would strand its parent forever.
func (l SpawnLink) Valid() bool {
	return l.ParentInstanceID != uuid.Nil && l.ParentNodeID != "" && l.Mode.Valid()
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
	// spawn mode — the cancel cascade covers every active child, not only
	// awaited ones. A parent with no active children returns an empty slice,
	// not an error.
	ListActiveChildren(ctx context.Context, parentID uuid.UUID) ([]uuid.UUID, error)
}
