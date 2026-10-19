package engine

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ErrGroupRepoPathInvalid marks a parallel_group repoPath that cannot be used
// as a repository hint inside the immediate parent's working area: it does not
// resolve, escapes the area, is not a directory, or is otherwise malformed. It
// is always reported before any task starts (wrapped in ErrGroupPreLaunch).
var ErrGroupRepoPathInvalid = errors.New("parallel_group repoPath is invalid")

// GroupConfigKeyResolvedRepoPath is the GroupRun.Config key under which the
// launcher records the runtime-resolved repoPath (slash-separated, relative to
// the immediate parent's working area). Absent when no repoPath is declared.
const GroupConfigKeyResolvedRepoPath = "resolvedRepoPath"

// GatherPreflighter is implemented by file working areas that can verify, before
// any task starts, that file gathering can work for a parent area: the host
// tools are available and the optional repository hint (relative to parentDir,
// "" = none) exists inside it and is usable.
type GatherPreflighter interface {
	PreflightGather(ctx context.Context, parentDir, repoRel string) error
}

// SnapshotInfo is one group-start snapshot or interrupted build.
type SnapshotInfo = instance.GroupSnapshotInfo

// SnapshotInventory lists the snapshots a file working area still holds. A
// snapshot that is no longer listed has been released (the pruner's durable
// "already pruned" marker).
type SnapshotInventory interface {
	ListSnapshots(ctx context.Context) ([]SnapshotInfo, error)
	HasSnapshot(ctx context.Context, handle string) (bool, error)
}

// ChildLister lists every child of a run (terminal ones included), one level.
// *adapter/sqlite.InstanceRepo satisfies it (P2-T12 ListChildren).
type ChildLister interface {
	ListChildren(ctx context.Context, parentID uuid.UUID) ([]instance.ChildRef, error)
}

// GatherAborter rolls a file gather back and settles its run on the failure
// route from outside the parent's run. *FileGatherer (P3-T6) satisfies it.
type GatherAborter interface {
	AbortGather(ctx context.Context, run *instance.GroupRun, outcome, reason string) (*GatherStep, error)
}

var _ GatherAborter = (*FileGatherer)(nil)

// GatherLocker lets the reconciler find out whether this process is writing a
// run's file gather right now. ok=false means a writer holds it; ok=true hands
// the caller exclusive use until it calls unlock.
type GatherLocker interface {
	TryLockGather(runID uuid.UUID) (unlock func(), ok bool)
}
