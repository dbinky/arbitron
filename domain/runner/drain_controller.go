package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// PinnedSessions counts interactive sessions pinned to a runner (Phase 3:
// engine.InteractiveSessionInventory, wired by P3 T13). An error is never read
// as zero.
type PinnedSessions interface {
	CountPinned(ctx context.Context, id RunnerID) (int, error)
}

// DrainCheckpoints reads the committed checkpoint (CheckpointStore.Latest).
type DrainCheckpoints interface {
	Latest(ctx context.Context, runID uuid.UUID) (CheckpointRef, bool, error)
}

// JobRequirements reads a job's complete persisted requirements (RunnerLedgerRepo).
type JobRequirements interface {
	Requirements(ctx context.Context, jobID uuid.UUID) (ExecRequirements, error)
}

// DrainSizes supplies a workspace's byte size (*WorkspaceSync).
type DrainSizes interface {
	WorkspaceBytes(ctx context.Context, runID uuid.UUID) int64
}

// DrainDeps: every field is required except Policy (zero is valid), Sizes
// (bytes recorded as 0) and Interval (default 30 s). Homes must be the
// copy-tracking store (P3 T11) so the drained copy's delete follows the handoff.
type DrainDeps struct {
	Pulls        DrainPuller
	Sessions     PinnedSessions
	Leases       ResourceLeaseStore
	Registry     Registry
	Ledger       Ledger
	Requirements JobRequirements
	Checkpoints  DrainCheckpoints
	Telemetry    TelemetrySource
	Homes        HomeStore
	Store        DrainStore
	Events       *EventRecorder
	Notifier     *PoolNotifier
	Policy       PlacementPolicy
	Sizes        DrainSizes
	Clock        func() time.Time
	Interval     time.Duration
}

// DrainController runs idempotent drain passes per draining runner.
type DrainController struct {
	d       DrainDeps
	mu      sync.Mutex // serialises passes
	drained map[RunnerID]bool
}

func NewDrainController(d DrainDeps) (*DrainController, error) {
	var missing []string
	add := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}
	add(d.Pulls != nil, "Pulls")
	add(d.Sessions != nil, "Sessions")
	add(d.Leases != nil, "Leases")
	add(d.Registry != nil, "Registry")
	add(d.Ledger != nil, "Ledger")
	add(d.Requirements != nil, "Requirements")
	add(d.Checkpoints != nil, "Checkpoints")
	add(d.Telemetry != nil, "Telemetry")
	add(d.Homes != nil, "Homes")
	add(d.Store != nil, "Store")
	add(d.Events != nil, "Events")
	add(d.Notifier != nil, "Notifier")
	add(d.Clock != nil, "Clock")
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: NewDrainController missing %s", strings.Join(missing, ", "))
	}
	if d.Interval <= 0 {
		d.Interval = 30 * time.Second
	}
	return &DrainController{d: d, drained: map[RunnerID]bool{}}, nil
}

// Begin starts a fresh drain record for id (Service.Drain calls it after SetDraining).
func (c *DrainController) Begin(ctx context.Context, id RunnerID) error {
	c.mu.Lock()
	delete(c.drained, id)
	c.mu.Unlock()
	return c.d.Store.Clear(ctx, id)
}

// Step runs one pass for a draining runner.
func (c *DrainController) Step(ctx context.Context, id RunnerID) (DrainProgress, error) {
	r, _, err := c.d.Registry.GetRunner(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	if !r.Draining {
		return DrainProgress{}, fmt.Errorf("%w: %s", ErrNotDraining, id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	active, err := c.d.Ledger.ListActiveByRunner(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	busy := make(map[uuid.UUID]bool, len(active))
	for _, j := range active {
		busy[j.Key.RunID] = true
	}
	homes, err := c.d.Homes.ListByRunner(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	for _, h := range homes {
		if busy[h.RunID] {
			if err := c.upsert(ctx, id, h.RunID, "", DrainItemPending, "waiting for the active step to finish"); err != nil {
				return DrainProgress{}, err
			}
			continue
		}
		if err := c.moveHome(ctx, id, h); err != nil {
			return DrainProgress{}, err
		}
	}
	p, err := c.progress(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	if p.Complete && !c.drained[id] {
		c.drained[id] = true
		c.d.Events.Record(ctx, PoolEvent{At: c.d.Clock(), Kind: EventRunnerDrained, RunnerID: id, Cause: Cause{Kind: "system", Actor: string(id)}})
		c.d.Notifier.Notify()
	}
	return p, nil
}

// Progress reports the runner's drain state without moving anything.
func (c *DrainController) Progress(ctx context.Context, id RunnerID) (DrainProgress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.progress(ctx, id)
}

// Tick runs one pass for every draining runner.
func (c *DrainController) Tick(ctx context.Context) error {
	rs, err := c.d.Registry.ListRunners(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rs {
		if r.Draining {
			if _, err := c.Step(ctx, r.ID); err != nil {
				errs = append(errs, fmt.Errorf("drain %s: %w", r.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Start drives Tick on the interval and on pool wake-ups until ctx ends.
func (c *DrainController) Start(ctx context.Context) {
	wake, cancel := c.d.Notifier.Subscribe()
	go func() {
		defer cancel()
		t := time.NewTicker(c.d.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-wake:
			}
			if err := c.Tick(ctx); err != nil {
				slog.Warn("drain pass", "error", err)
			}
		}
	}()
}

func (c *DrainController) upsert(ctx context.Context, id RunnerID, run uuid.UUID, group string, st DrainItemState, reason string) error {
	return c.d.Store.Upsert(ctx, DrainItem{RunnerID: id, Kind: DrainKindWorkspace, RunID: run, GroupKey: group, State: st, Reason: reason, UpdatedAt: c.d.Clock()})
}

// moveHome returns an error only for storage failures; every expected
// failure is a persisted item that keeps the drain incomplete.
func (c *DrainController) moveHome(ctx context.Context, id RunnerID, h Home) error {
	if h.NonPortableReason != "" {
		return c.upsert(ctx, id, h.RunID, "", DrainItemBlocked, "non_portable: "+h.NonPortableReason+"; recoverable from its local checkpoint but never movable")
	}
	req, found, err := c.lastRequirements(ctx, h.RunID)
	if err != nil {
		return err
	}
	target, owner, group := ServerHome, BuiltInRunnerID, ""
	if found {
		group = req.Group.String()
		d, err := c.place(ctx, req)
		if err != nil {
			return err
		}
		if !d.Placed {
			return c.upsert(ctx, id, h.RunID, group, DrainItemBlocked, fmt.Sprintf("no eligible runner for %s: %s", group, d.Reason))
		}
		target, owner = d.Runner, d.Runner
	}
	claim, err := c.d.Leases.Claim(ctx, runKey(h.RunID), fmt.Sprintf("drain:%s:%s@%d", id, h.RunID, h.Generation), owner)
	if errors.Is(err, ErrResourceBusy) {
		return c.upsert(ctx, id, h.RunID, group, DrainItemPending, "waiting: the workspace resource is held by another writer")
	}
	if err != nil {
		return err
	}
	defer func() {
		if err := c.d.Leases.Release(context.WithoutCancel(ctx), claim); err != nil {
			slog.Warn("releasing drain claim", "run", h.RunID, "error", err)
		}
	}()
	if target == ServerHome {
		// No workspace step ever ran on this home: the server checkpoint is the whole state.
		if err := c.d.Homes.Release(ctx, h.RunID, h.Generation); err != nil {
			return c.upsert(ctx, id, h.RunID, group, DrainItemPending, "home changed during the drain; retrying: "+err.Error())
		}
		return c.upsert(ctx, id, h.RunID, group, DrainItemDone, "released to server storage")
	}
	ref, _, err := c.d.Checkpoints.Latest(ctx, h.RunID)
	if err != nil {
		return err
	}
	if err := c.upsert(ctx, id, h.RunID, group, DrainItemMoving, fmt.Sprintf("target %s pulling committed checkpoint %d", target, ref.Version)); err != nil {
		return err
	}
	rc, err := c.d.Pulls.PullCommitted(ctx, h.RunID, target, ref, claim)
	if err != nil {
		return c.upsert(ctx, id, h.RunID, group, DrainItemBlocked, fmt.Sprintf("target pull failed on %s: %v; the committed checkpoint and the source copy are kept", target, err))
	}
	if rc.OperationID == "" || rc.RunnerID != target || rc.Ref != ref || rc.Resource != claim {
		return c.upsert(ctx, id, h.RunID, group, DrainItemBlocked, fmt.Sprintf("target %s returned a receipt that does not match this drain transfer", target))
	}
	if err := c.d.Leases.Check(ctx, claim); err != nil {
		return c.upsert(ctx, id, h.RunID, group, DrainItemBlocked, "drain claim lost before the handoff: "+err.Error())
	}
	var bytes int64
	if c.d.Sizes != nil {
		bytes = c.d.Sizes.WorkspaceBytes(ctx, h.RunID)
	}
	cause := HomeMoveCause{OperationID: rc.OperationID, FromGeneration: h.Generation, ToGeneration: h.Generation + 1}
	if _, err := c.d.Homes.HandOff(ctx, h.RunID, target, fmt.Sprintf("drain: %s -> %s", id, target), bytes, h.Generation, cause); err != nil {
		if errors.Is(err, ErrHomeGenerationConflict) {
			return c.upsert(ctx, id, h.RunID, group, DrainItemPending, "home changed during the transfer; retrying next pass")
		}
		return err
	}
	return c.upsert(ctx, id, h.RunID, group, DrainItemDone, "moved to "+string(target))
}

// lastRequirements returns the complete requirements of the run's most
// recent workspace job, re-aimed at choosing a new home.
func (c *DrainController) lastRequirements(ctx context.Context, run uuid.UUID) (ExecRequirements, bool, error) {
	jobs, err := c.d.Ledger.ListByRun(ctx, run)
	if err != nil {
		return ExecRequirements{}, false, err
	}
	var last *Job
	for i := range jobs {
		j := &jobs[i]
		if j.Op != OpAgent && j.Op != OpProcess {
			continue
		}
		if last == nil || j.CreatedAt.After(last.CreatedAt) {
			last = j
		}
	}
	if last == nil {
		return ExecRequirements{}, false, nil
	}
	req, err := c.d.Requirements.Requirements(ctx, last.ID)
	if err != nil {
		return ExecRequirements{}, false, err
	}
	req.RunID, req.PinnedRunner = run, ""
	if req.Affinity == AffinityWorkspace {
		req.Affinity = AffinityNone // the current home is the drained runner; rank a new one
	}
	if c.d.Sizes != nil {
		req.WorkspaceBytes = c.d.Sizes.WorkspaceBytes(ctx, run)
	}
	return req, true, nil
}

// place ranks targets on a fresh snapshot. Moving a home takes no execution
// slot, so slot use is ignored; capability, health, disk telemetry and the
// Draining/Cordoned flags still apply.
func (c *DrainController) place(ctx context.Context, req ExecRequirements) (Decision, error) {
	snap, err := c.d.Registry.Snapshot(ctx)
	if err != nil {
		return Decision{}, err
	}
	snap.Now = c.d.Clock()
	snap.Telemetry = c.d.Telemetry.Latest()
	snap = freshTelemetry(snap, c.d.Policy)
	// A pull consumes disk but no execution slot. AffinityNone avoids the old
	// home; retain workspace eligibility and fresh disk evidence explicitly.
	var targets []Runner
	for _, target := range snap.Runners {
		telemetry := snap.Telemetry[target.ID]
		if homeIneligible(target, telemetry) != "" {
			continue
		}
		if telemetry.DiskFreeBytes != nil && req.WorkspaceBytes > 0 && *telemetry.DiskFreeBytes < uint64(req.WorkspaceBytes) {
			continue
		}
		targets = append(targets, target)
	}
	snap.Runners = targets
	snap.SlotsInUse, snap.SourceInFlight = nil, nil
	return Place(req, snap, c.d.Policy), nil
}

func (c *DrainController) progress(ctx context.Context, id RunnerID) (DrainProgress, error) {
	active, err := c.d.Ledger.ListActiveByRunner(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	homes, err := c.d.Homes.ListByRunner(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	sessions, err := c.d.Sessions.CountPinned(ctx, id)
	if err != nil {
		return DrainProgress{}, fmt.Errorf("drain %s: session inventory unavailable: %w", id, err)
	}
	items, err := c.d.Store.List(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	homed := make(map[uuid.UUID]bool, len(homes))
	for _, h := range homes {
		homed[h.RunID] = true
	}
	var blocked []DrainBlocker
	for _, it := range items {
		if it.Kind == DrainKindWorkspace && it.State == DrainItemBlocked && homed[it.RunID] {
			run := it.RunID
			blocked = append(blocked, DrainBlocker{Kind: DrainKindWorkspace, RunID: &run, Group: it.GroupKey, Reason: it.Reason})
		}
	}
	if sessions > 0 {
		blocked = append(blocked, DrainBlocker{Kind: DrainKindSession,
			Reason: fmt.Sprintf("%d interactive session(s) pinned to %s; waiting for each to reach a terminal boundary", sessions, id)})
	}
	return DrainProgress{ActiveSteps: len(active), HomedWorkspaces: len(homes), Sessions: sessions, Blocked: blocked,
		Complete: len(active) == 0 && len(homes) == 0 && sessions == 0 && len(blocked) == 0}, nil
}
