package instance

import (
	"context"

	"github.com/google/uuid"
)

// ChildLink is the awaited child of ONE (parent instance, spawn node) pair,
// carrying just enough to decide the parent's next move: keep waiting, or route
// by the child's outcome.
type ChildLink struct {
	ChildID    uuid.UUID
	ChildState State
}

// SpawnWaitCandidate pairs a waiting parent with an awaited child that has
// ALREADY reached a terminal state — a parent the in-process wake missed
// (typically because the process died between the child's terminal write and
// the parent's re-queue).
type SpawnWaitCandidate struct {
	ParentID     uuid.UUID
	ParentNodeID string
	ChildID      uuid.UUID
	ChildState   State
}

// SpawnWaitRepository serves the two awaited-child lookups.
//
// It is a SECOND narrow port rather than an extension of SpawnLinkRepository by
// interface segregation: each method here has exactly one consumer — the
// executor's resumption short-circuit and the startup reconciler respectively —
// and neither consumer needs the counting/cascade capabilities.
type SpawnWaitRepository interface {
	// FindChildBySpawnNode returns the most recently created child linked to
	// (parentID, nodeID, mode), or (nil, nil) when there is none.
	//
	// Keying on the LINK — not on node-execution status — is deliberate: the
	// spawn node needs to know HOW the child ended in order to choose a port, and
	// the child's persisted state is the single authority for that. A second copy
	// in the node record could disagree with it after a retry or a late
	// cancellation.
	FindChildBySpawnNode(ctx context.Context, parentID uuid.UUID, nodeID string, mode SpawnMode) (*ChildLink, error)

	// ListWaitingOnTerminalChild returns every parent in the waiting state whose
	// awaited child (matching the parent's CURRENT node id, with
	// mode = wait_for_child) is already terminal. An empty result is not an error.
	ListWaitingOnTerminalChild(ctx context.Context) ([]SpawnWaitCandidate, error)
}
