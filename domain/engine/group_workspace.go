package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupWorkspace is the working-area half of a parallel_group launch: it takes
// ONE immutable copy of the parent's working area at the group's launch
// boundary and materializes an independent, writable copy of it for every
// task BEFORE that task is enqueued.
//
// Both group forms and both gather modes use it: a results-only group still
// gives every task the parent's files (relative layout and permissions
// preserved), so a task never starts in an empty directory and never writes
// into the parent's mutable area. Copies are by CONTENT — never hard links,
// writable symlinks out of the area, or Git metadata that points back into
// the parent's worktree — so no task can observe or alter another task's
// files, the parent's files, or the snapshot.
//
// Phase 2 implementation: adapter/filesystem.GroupWorkspaceAdapter (plain
// recursive copies). Phase 3 replaces it with the Git-backed WorkingArea
// (adapter/git), which embeds this interface and adds finalize/merge.
type GroupWorkspace interface {
	// SnapshotParent captures an immutable copy of parentDir (layout +
	// permissions) for groupRunID and returns an opaque handle for it. Content
	// the snapshot cannot represent safely fails with an error wrapping
	// ErrUnsupportedSnapshotContent that names the offending path.
	SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error)
	// ForkTask materializes an independent writable copy of the snapshot at
	// taskDir. taskDir may already exist but must then be empty.
	ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error
	// ReleaseSnapshot deletes the snapshot. An unknown (already released)
	// handle is not an error.
	ReleaseSnapshot(ctx context.Context, snapshotHandle string) error
}

// ErrUnsupportedSnapshotContent is wrapped by GroupWorkspace.SnapshotParent
// when the parent's working area contains something a task copy cannot
// reproduce without sharing state with the parent: an absolute or escaping
// symlink, a Git worktree/submodule pointer (a ".git" FILE), or a special
// file (device, socket, named pipe). It is a pre-launch failure: the group
// launcher (P2-T8) wraps it in ErrGroupPreLaunch so the node's ErrorBehavior
// applies and no task is ever started.
//
// It is the SAME error value as instance.ErrUnsupportedSnapshotContent (see
// domain/instance/group_snapshot.go for why the identity lives there):
// adapters wrap the instance name, domain code may test either.
var ErrUnsupportedSnapshotContent = instance.ErrUnsupportedSnapshotContent
