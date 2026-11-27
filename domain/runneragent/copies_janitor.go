package runneragent

import "context"

const KindDeleteCopy = "delete_copy"

// HeldCopy is a local workspace copy reported in hello.
type HeldCopy struct {
	RunID          string
	HomeGeneration int64
}

// DeleteCopy is the server's generation-bounded deletion order.
type DeleteCopy struct {
	RunID          string
	HomeGeneration int64
	Reason         string
}

// CopyJanitor must keep a copy whose generation is newer than upTo.
type CopyJanitor interface {
	HeldCopies(ctx context.Context) ([]HeldCopy, error)
	DeleteCopy(ctx context.Context, runID string, upTo int64) (deleted bool, err error)
}

func (a *Agent) heldCopies(ctx context.Context) []HeldCopy {
	if a.deps.Copies == nil {
		return nil
	}
	cs, err := a.deps.Copies.HeldCopies(ctx)
	if err != nil {
		a.deps.Logger.Warn("listing local workspace copies", "error", err)
		return nil
	}
	return cs
}

// onDeleteCopy runs in the receive loop before accepting later dispatches.
func (a *Agent) onDeleteCopy(ctx context.Context, d *DeleteCopy) {
	if d == nil || a.deps.Copies == nil {
		return
	}
	deleted, err := a.deps.Copies.DeleteCopy(ctx, d.RunID, d.HomeGeneration)
	switch {
	case err != nil:
		a.deps.Logger.Warn("deleting workspace copy", "runID", d.RunID, "error", err)
	case deleted:
		a.deps.Logger.Info("deleted workspace copy", "runID", d.RunID, "upTo", d.HomeGeneration, "reason", d.Reason)
	default:
		a.deps.Logger.Info("kept workspace copy (absent or newer)", "runID", d.RunID, "upTo", d.HomeGeneration)
	}
}
