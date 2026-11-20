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

	// ListRunningOnTerminalChild is ListWaitingOnTerminalChild for a parent in
	// the RUNNING state: the shape a parent is left in when some path strands it
	// at the spawn node with no walker. Whether a candidate is really stranded
	// (no live walker) is decided by the engine, never by this query.
	ListRunningOnTerminalChild(ctx context.Context) ([]SpawnWaitCandidate, error)

	// ClaimWaitingParent atomically moves parentID from `waiting` at nodeID to
	// `running`, reporting whether THIS caller won the claim.
	//
	// It exists because a waiting parent has TWO possible movers: the spawn
	// executor, which discovers that the child already finished and wants to
	// resume in-process, and the wake path, which re-queues it. A read-then-write
	// on either side can be overtaken by the other, leaving a parent that is
	// simultaneously queued and being executed — one run, two goroutines.
	//
	// The predicate is state AND node, not state alone: a parent that has moved
	// on to a LATER spawn node is no longer the parent this claim is about.
	//
	// A losing claim is not an error. It means the other mover won, and the
	// caller must take its no-op path.
	ClaimWaitingParent(ctx context.Context, parentID uuid.UUID, nodeID string) (bool, error)

	// SuspendRunning atomically moves id from `running` to `waiting`, reporting
	// whether the transition happened. It is the parking half of the same
	// exclusion: Engine.Cancel commits `cancelled` before it cancels the run's
	// context, so an executor's unconditional waiting write in that interval
	// would revive a cancelled run. A false return means the run already left
	// `running` (cancelled, paused) and the executor must halt, not wait.
	SuspendRunning(ctx context.Context, id uuid.UUID) (bool, error)
}
