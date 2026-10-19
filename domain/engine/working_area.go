package engine

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// WorkingArea extends the Phase 2 GroupWorkspace (snapshot/fork/release) with
// the mergeable operations file gathering needs. Every "ref" is an immutable
// name inside the snapshot's private backing store, never a moving branch of an
// author repository. Implemented by adapter/git.WorkingArea.
type WorkingArea interface {
	GroupWorkspace
	// FinalizeTask records taskDir's full content (committed or not, untracked
	// and ignored files included, author .git metadata excluded) as an
	// immutable ref whose parent is the snapshot baseline, and returns it
	// ("refs/arbitron/tasks/<taskInstanceID>"). An unchanged taskDir is valid
	// and yields a ref whose tree equals the baseline. The first publication
	// wins atomically; later calls return that ref without recapturing content.
	FinalizeTask(ctx context.Context, snapshotHandle string, taskInstanceID uuid.UUID, taskDir string) (string, error)
	// BeginGather records an exact rollback snapshot of parentDir (dirty,
	// untracked and ignored content included) and prepares a staging area
	// holding that content. Re-calling it for the same gatherID returns the
	// SAME handle and never re-snapshots, so a restart after a partial Apply
	// still rolls back to the true pre-gather content.
	BeginGather(ctx context.Context, snapshotHandle string, gatherID uuid.UUID, parentDir string) (GatherHandle, error)
	// MergeTask three-way merges one finalized task ref (relative to the
	// common baseline) into the staging area. Conflicts are reported, not
	// errors; conflict markers are left in the staging files for resolution.
	// Re-calling it for a ref already merged reports Clean; re-calling it for
	// the ref whose merge is in progress reports that merge's conflicts.
	MergeTask(ctx context.Context, h GatherHandle, taskRef string) (MergeOutcome, error)
	// ConflictPaths returns the staging paths that are still conflicted:
	// unmerged index entries, plus previously conflicted paths whose content
	// still carries conflict markers. Sorted, unique; empty when resolved.
	// ErrPendingMergeChanged means the expected merge was aborted or replaced;
	// the gather must roll back rather than accept the now-clean index.
	ConflictPaths(ctx context.Context, h GatherHandle) ([]string, error)
	// CommitResolution records the resolved staging content as the next staged
	// result. Returns an error wrapping ErrStillConflicted (and changes
	// nothing) while ConflictPaths is non-empty.
	CommitResolution(ctx context.Context, h GatherHandle, message string) error
	// Apply makes parentDir equal the staged result: written, overwritten and
	// deleted files, modes and symlinks. Author .git metadata is never touched.
	// Idempotent.
	Apply(ctx context.Context, h GatherHandle) error
	// Rollback restores the exact pre-gather parentDir content recorded by
	// BeginGather (author .git metadata untouched). Idempotent.
	Rollback(ctx context.Context, h GatherHandle) error
	// ReleaseRef deletes one ref from the snapshot's backing store. Releasing a
	// gather's rollback ref also removes that gather's staging area. An
	// unknown handle or ref is not an error.
	ReleaseRef(ctx context.Context, snapshotHandle, ref string) error
}

// GatherHandle identifies one gather operation. It is persisted verbatim in
// instance.GatherReport.Handle (as a JSON object) so a restarted gatherer can
// resume against the same staging area and rollback snapshot.
type GatherHandle struct {
	SnapshotHandle string    `json:"snapshotHandle"`
	GatherID       uuid.UUID `json:"gatherId"`
	StagingDir     string    `json:"stagingDir"`
	RollbackRef    string    `json:"rollbackRef"`
	ParentDir      string    `json:"parentDir"`
}

// MergeOutcome is the result of one MergeTask.
type MergeOutcome struct {
	Clean         bool     `json:"clean"`
	ConflictPaths []string `json:"conflictPaths"`
}

// ErrStillConflicted reports that a staging area still has unresolved
// conflicts (CommitResolution / Apply refuse to proceed).
var ErrStillConflicted = errors.New("working area still has unresolved merge conflicts")

// ErrPendingMergeChanged reports lost merge provenance, which cannot be
// repaired merely by staging marker-free files. The gather must be aborted.
var ErrPendingMergeChanged = errors.New("working area pending merge was aborted or replaced")
