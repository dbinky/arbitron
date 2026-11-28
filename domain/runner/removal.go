package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// CheckpointDeleter removes every server checkpoint of a run.
type CheckpointDeleter interface {
	Delete(ctx context.Context, runID uuid.UUID) error
}

// JoinCheckpointDeleters deletes a run's checkpoints from every store,
// attempting all of them and joining the errors.
func JoinCheckpointDeleters(ds ...CheckpointDeleter) CheckpointDeleter { return joinedDeleters(ds) }

type joinedDeleters []CheckpointDeleter

func (j joinedDeleters) Delete(ctx context.Context, runID uuid.UUID) error {
	var errs []error
	for _, d := range j {
		if err := d.Delete(ctx, runID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RemovalDeps are all required. Homes is the undecorated home repository:
// the notifier sends its own deletes.
type RemovalDeps struct {
	Inventory   CopyInventory
	Homes       HomeStore
	Checkpoints CheckpointDeleter
	Deleter     CopyDeleter
	Leases      ResourceLeaseStore
}

// RemovalNotifier implements engine.WorkspaceRemovalHook.
type RemovalNotifier struct{ d RemovalDeps }

func NewRemovalNotifier(d RemovalDeps) (*RemovalNotifier, error) {
	var missing []string
	if d.Inventory == nil {
		missing = append(missing, "Inventory")
	}
	if d.Homes == nil {
		missing = append(missing, "Homes")
	}
	if d.Checkpoints == nil {
		missing = append(missing, "Checkpoints")
	}
	if d.Deleter == nil {
		missing = append(missing, "Deleter")
	}
	if d.Leases == nil {
		missing = append(missing, "Leases")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: NewRemovalNotifier missing %s", strings.Join(missing, ", "))
	}
	return &RemovalNotifier{d: d}, nil
}

// WorkspaceRemoved logs failures; durable tombstones let RetryDeferred or a
// runner's next hello finish the work.
func (n *RemovalNotifier) WorkspaceRemoved(ctx context.Context, runID uuid.UUID) {
	if err := n.remove(ctx, runID); err != nil {
		slog.Warn("runner workspace removal", "run", runID, "error", err)
	}
}

// RetryDeferred finishes every removal still owed on the server side.
func (n *RemovalNotifier) RetryDeferred(ctx context.Context) error {
	owed, err := n.d.Inventory.PendingDeletes(ctx, BuiltInRunnerID)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range owed {
		if err := n.remove(ctx, c.RunID); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", c.RunID, err))
		}
	}
	return errors.Join(errs...)
}

func (n *RemovalNotifier) remove(ctx context.Context, runID uuid.UUID) error {
	claim, err := n.d.Leases.Claim(ctx, runKey(runID), "remove:"+runID.String(), BuiltInRunnerID)
	busy := errors.Is(err, ErrResourceBusy)
	if err != nil && !busy {
		return err
	}
	if !busy {
		defer func() {
			if err := n.d.Leases.Release(context.WithoutCancel(ctx), claim); err != nil {
				slog.Warn("releasing workspace removal claim", "run", runID, "error", err)
			}
		}()
	}
	h, err := n.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return err
	}
	upTo := h.Generation + 1 // covers every generation, including the home's
	holders, err := n.holders(ctx, runID, h)
	if err != nil {
		return err
	}
	if err := n.d.Inventory.MarkForDeletion(ctx, runID, BuiltInRunnerID, upTo, CopyDeletePending); err != nil {
		return err
	}
	for _, id := range holders {
		if err := n.d.Inventory.MarkForDeletion(ctx, runID, id, upTo, CopyDeletePending); err != nil {
			return err
		}
	}
	if busy {
		slog.Info("workspace removal deferred: a writer holds the run's resource", "run", runID)
		return nil
	}
	if h.RunnerID != ServerHome {
		if err := n.d.Homes.Release(ctx, runID, h.Generation); err != nil {
			return fmt.Errorf("releasing removed workspace home: %w", err)
		}
	}
	if err := n.d.Checkpoints.Delete(ctx, runID); err != nil {
		return fmt.Errorf("deleting removed workspace checkpoints: %w", err)
	}
	if err := n.d.Inventory.MarkDeleted(ctx, runID, BuiltInRunnerID); err != nil {
		return err
	}
	for _, id := range holders {
		n.d.Deleter.DeleteCopy(ctx, id, DeleteCopyPayload{RunID: runID, HomeGeneration: upTo, Reason: "workspace removed"})
	}
	return nil
}

// holders includes the current remote home and every known remote copy.
func (n *RemovalNotifier) holders(ctx context.Context, runID uuid.UUID, h Home) ([]RunnerID, error) {
	copies, err := n.d.Inventory.ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	set := map[RunnerID]bool{}
	if tracksCopies(h.RunnerID) {
		set[h.RunnerID] = true
	}
	for _, c := range copies {
		if tracksCopies(c.RunnerID) {
			set[c.RunnerID] = true
		}
	}
	out := make([]RunnerID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
