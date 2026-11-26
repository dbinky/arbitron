package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// ErrBaselineBlocked: a run has an active operation but no committed
// baseline. The initializer fails closed rather than snapshot interrupted edits.
var ErrBaselineBlocked = errors.New("runner: workspace baseline cannot be created while an operation of the run is active")

// BaselineDeps are the BaselineInitializer's collaborators (all required).
type BaselineDeps struct {
	Checkpoints CheckpointStore
	Ledger      Ledger
	Leases      ResourceLeaseStore
	Homes       HomeStore // RR-08 SetNonPortable
	Local       LocalCheckpointStore
}

// BaselineInitializer is the only caller of the bootstrap store path.
type BaselineInitializer struct {
	d     BaselineDeps
	local *LocalCheckpoints
}

var _ engine.WorkspaceBaselineInitializer = (*BaselineInitializer)(nil)

// NewWorkspaceBaselineInitializer validates deps; a missing collaborator panics.
func NewWorkspaceBaselineInitializer(d BaselineDeps) *BaselineInitializer {
	var missing []string
	if d.Checkpoints == nil {
		missing = append(missing, "Checkpoints")
	}
	if d.Ledger == nil {
		missing = append(missing, "Ledger")
	}
	if d.Leases == nil {
		missing = append(missing, "Leases")
	}
	if d.Homes == nil {
		missing = append(missing, "Homes")
	}
	if d.Local == nil {
		missing = append(missing, "Local")
	}
	if len(missing) > 0 {
		panic("runner: NewWorkspaceBaselineInitializer requires " + strings.Join(missing, ", "))
	}
	return &BaselineInitializer{d: d, local: &LocalCheckpoints{d: LocalCheckpointDeps{Local: d.Local, Homes: d.Homes, Checkpoints: d.Checkpoints, Leases: d.Leases}}}
}

// EnsureBaseline commits the run's canonical directory as its first committed
// checkpoint. An existing committed baseline is reused and never re-snapshotted.
func (i *BaselineInitializer) EnsureBaseline(ctx context.Context, runID uuid.UUID, dir string) error {
	if runID == uuid.Nil {
		return errors.New("runner: baseline requires a run id")
	}
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || filepath.Base(clean) != runID.String() {
		return fmt.Errorf("runner: baseline directory %q is not run %s's canonical workspace", dir, runID)
	}
	if _, ok, err := i.d.Checkpoints.Latest(ctx, runID); err != nil {
		return fmt.Errorf("runner: read committed baseline of run %s: %w", runID, err)
	} else if ok {
		return nil // committed baseline exists: never re-snapshot (restart copies may be dirty)
	}
	if home, err := i.d.Homes.GetHome(ctx, runID); err != nil {
		return err
	} else if home.NonPortableReason != "" && home.LocalCheckpoint != "" {
		return nil
	}
	jobs, err := i.d.Ledger.ListByRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("runner: read operations of run %s: %w", runID, err)
	}
	for _, j := range jobs {
		if j.State.Active() {
			return fmt.Errorf("%w: run %s job %s (%s) is %s", ErrBaselineBlocked, runID, j.ID, j.NodeID, j.State)
		}
	}
	_, _, err = i.d.Checkpoints.Commit(ctx, runID, clean)
	if errors.Is(err, ErrPromisorObjectsMissing) {
		// RR-08: no network fetch; the run stays on the built-in runner.
		if err := i.d.Homes.SetNonPortable(ctx, runID, NonPortablePromisorReason); err != nil {
			return fmt.Errorf("runner: mark run %s non-portable: %w", runID, err)
		}
		return i.local.Bootstrap(ctx, runID, clean)
	}
	if err != nil {
		if _, ok, lerr := i.d.Checkpoints.Latest(ctx, runID); lerr == nil && ok {
			return nil // a concurrent initializer committed first; its baseline stands
		}
		return fmt.Errorf("runner: create baseline checkpoint of run %s: %w", runID, err)
	}
	return nil
}
