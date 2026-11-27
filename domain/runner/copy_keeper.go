package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// CopyKeeperDeps are all required. Leases is shared with workspace sync,
// recovery, removal and drain.
type CopyKeeperDeps struct {
	Inventory CopyInventory
	Homes     HomeStore
	Deleter   CopyDeleter
	Leases    ResourceLeaseStore
}

// CopyKeeper retires old homes and reconciles runner-reported copies.
type CopyKeeper struct{ d CopyKeeperDeps }

func NewCopyKeeper(d CopyKeeperDeps) (*CopyKeeper, error) {
	var missing []string
	if d.Inventory == nil {
		missing = append(missing, "Inventory")
	}
	if d.Homes == nil {
		missing = append(missing, "Homes")
	}
	if d.Deleter == nil {
		missing = append(missing, "Deleter")
	}
	if d.Leases == nil {
		missing = append(missing, "Leases")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: NewCopyKeeper missing %s", strings.Join(missing, ", "))
	}
	return &CopyKeeper{d: d}, nil
}

// Server storage and the built-in runner hold no remote copy.
func tracksCopies(id RunnerID) bool { return id != ServerHome && id != BuiltInRunnerID }

func deleteHolder(runID uuid.UUID, target RunnerID, upTo int64) string {
	return "delete:" + runID.String() + "@" + strconv.FormatInt(upTo, 10) + "/" + string(target)
}

// authorizeCopyDelete holds a claim for the send when the resource is free.
// A writer on the deletion target blocks deletion. A writer elsewhere does
// not cover the target's copy; the generation bound protects newer copies.
func authorizeCopyDelete(ctx context.Context, leases ResourceLeaseStore, runID uuid.UUID, target RunnerID, upTo int64) (release func(), ok bool, err error) {
	key := runKey(runID)
	rec, active, err := leases.Active(ctx, key)
	if err != nil {
		return nil, false, fmt.Errorf("authorizing copy delete of %s on %s: %w", runID, target, err)
	}
	if active {
		if rec.Fence.Owner == string(target) {
			return nil, false, nil
		}
		return func() {}, true, nil
	}
	ref, err := leases.Claim(ctx, key, deleteHolder(runID, target, upTo), target)
	if errors.Is(err, ErrResourceBusy) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("authorizing copy delete of %s on %s: %w", runID, target, err)
	}
	return func() {
		if err := leases.Release(context.WithoutCancel(ctx), ref); err != nil {
			slog.Warn("releasing copy-delete claim", "run", runID, "runner", target, "error", err)
		}
	}, true, nil
}

func (k *CopyKeeper) send(ctx context.Context, target RunnerID, p DeleteCopyPayload) (bool, error) {
	release, ok, err := authorizeCopyDelete(ctx, k.d.Leases, p.RunID, target, p.HomeGeneration)
	if err != nil || !ok {
		return false, err
	}
	defer release()
	return k.d.Deleter.DeleteCopy(ctx, target, p), nil
}

// HomeMoved records the committed new home and tombstones the old copy before
// sending its generation-bounded delete. Offline/held deletes stay owed.
func (k *CopyKeeper) HomeMoved(ctx context.Context, runID uuid.UUID, from RunnerID, to Home) error {
	if tracksCopies(to.RunnerID) {
		if err := k.d.Inventory.RecordHeld(ctx, runID, to.RunnerID, to.Generation); err != nil {
			return err
		}
	}
	if !tracksCopies(from) || from == to.RunnerID {
		return nil
	}
	upTo := to.Generation - 1
	if err := k.d.Inventory.MarkForDeletion(ctx, runID, from, upTo, CopyDeletePending); err != nil {
		return err
	}
	dest := string(to.RunnerID)
	if to.RunnerID == ServerHome {
		dest = "server"
	}
	_, err := k.send(ctx, from, DeleteCopyPayload{RunID: runID, HomeGeneration: upTo, Reason: "home moved to " + dest})
	return err
}

// ReconcileCopies delivers authorized deletes itself while holding their
// claims. Absence on a later hello confirms an owed deletion.
func (k *CopyKeeper) ReconcileCopies(ctx context.Context, id RunnerID, held []HeldCopyInfo) ([]DeleteCopyPayload, error) {
	pending, err := k.d.Inventory.PendingDeletes(ctx, id)
	if err != nil {
		return nil, err
	}
	owed := make(map[uuid.UUID]RunnerCopy, len(pending))
	for _, c := range pending {
		owed[c.RunID] = c
	}
	reported := make(map[uuid.UUID]bool, len(held))
	var sent []DeleteCopyPayload
	var errs []error
	for _, c := range held {
		reported[c.RunID] = true
		p, ok, err := k.reconcileOne(ctx, id, c, owed)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			sent = append(sent, p)
		}
	}
	for run := range owed {
		if !reported[run] {
			if err := k.d.Inventory.MarkDeleted(ctx, run, id); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return sent, errors.Join(errs...)
}

func (k *CopyKeeper) reconcileOne(ctx context.Context, id RunnerID, c HeldCopyInfo, owed map[uuid.UUID]RunnerCopy) (DeleteCopyPayload, bool, error) {
	p := DeleteCopyPayload{RunID: c.RunID, HomeGeneration: c.HomeGeneration}
	if o, ok := owed[c.RunID]; ok && c.HomeGeneration <= o.HomeGeneration {
		p.Reason = "owed deletion"
	} else {
		h, err := k.d.Homes.GetHome(ctx, c.RunID)
		if err != nil {
			return p, false, err
		}
		if h.RunnerID == id && c.HomeGeneration == h.Generation {
			return p, false, k.d.Inventory.RecordHeld(ctx, c.RunID, id, h.Generation)
		}
		if h.RunnerID == id && c.HomeGeneration > h.Generation {
			slog.Warn("runner reports a copy newer than its home; keeping it", "run", c.RunID, "runner", id, "reported", c.HomeGeneration, "home", h.Generation)
			return p, false, nil
		}
		p.Reason = "stale copy"
		if err := k.d.Inventory.MarkForDeletion(ctx, c.RunID, id, c.HomeGeneration, CopyStale); err != nil {
			return p, false, err
		}
	}
	ok, err := k.send(ctx, id, p)
	return p, ok, err
}

// copyTrackingHomes reports committed home changes. Tracking failure cannot
// undo a committed handoff; reconnect reconciliation rediscovers stale copies.
type copyTrackingHomes struct {
	HomeStore
	k *CopyKeeper
}

func NewCopyTrackingHomes(inner HomeStore, k *CopyKeeper) HomeStore {
	if inner == nil || k == nil {
		panic("runner: NewCopyTrackingHomes requires a HomeStore and a CopyKeeper")
	}
	return copyTrackingHomes{HomeStore: inner, k: k}
}

func (c copyTrackingHomes) AcquireResource(ctx context.Context, runID uuid.UUID, owner RunnerID, expectGen int64) (Home, error) {
	prev, err := c.HomeStore.GetHome(ctx, runID)
	if err != nil {
		return Home{}, err
	}
	h, err := c.HomeStore.AcquireResource(ctx, runID, owner, expectGen)
	if err != nil {
		return h, err
	}
	c.moved(ctx, runID, prev, h)
	return h, nil
}

func (c copyTrackingHomes) HandOff(ctx context.Context, runID uuid.UUID, to RunnerID, reason string, bytes, expectGen int64, cause HomeMoveCause) (Home, error) {
	prev, err := c.HomeStore.GetHome(ctx, runID)
	if err != nil {
		return Home{}, err
	}
	h, err := c.HomeStore.HandOff(ctx, runID, to, reason, bytes, expectGen, cause)
	if err != nil {
		return h, err
	}
	c.moved(ctx, runID, prev, h)
	return h, nil
}

func (c copyTrackingHomes) Release(ctx context.Context, runID uuid.UUID, expectGen int64) error {
	prev, err := c.HomeStore.GetHome(ctx, runID)
	if err != nil {
		return err
	}
	if err := c.HomeStore.Release(ctx, runID, expectGen); err != nil {
		return err
	}
	h, err := c.HomeStore.GetHome(ctx, runID)
	if err != nil {
		slog.Warn("reading released home for copy tracking", "run", runID, "error", err)
		return nil
	}
	c.moved(ctx, runID, prev, h)
	return nil
}

func (c copyTrackingHomes) moved(ctx context.Context, runID uuid.UUID, prev, h Home) {
	if h.Generation == prev.Generation {
		return
	}
	if err := c.k.HomeMoved(ctx, runID, prev.RunnerID, h); err != nil {
		slog.Warn("tracking runner copies after a home move", "run", runID, "error", err)
	}
}
