package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupOrphanSnapshotGrace is how old a snapshot without any group-run row must
// be before the sweep releases it. A younger one may belong to a launch that
// has snapshotted but not yet persisted its roster.
const GroupOrphanSnapshotGrace = time.Hour

// GroupResourcePruner releases a group run's working-area
// resources once nothing can need them any more:
//
//   - the run is done (gather and recovery are over): its gather's rollback
//     pin — and with it the staging area — is released right away;
//   - every launched task row is terminal (no writer, including tasks that
//     continue independently after a first_success decision), none has a
//     non-terminal descendant, and every group run nested under a task is
//     done: then completed task workspaces are removed (or, when nothing was
//     applied, retained and logged like Engine.warnWorkspaceRetained) and the
//     backing snapshot is released LAST — its absence is the durable
//     "already pruned" marker.
//
// Failed/cancelled task workspaces are never touched: the engine retained them
// for debugging. Results snapshots are normally released by the launcher; the
// sweep retries missed releases and completed results-task workspace cleanup.
type GroupResourcePruner struct {
	groups      instance.GroupRunRepository
	instances   instance.InstanceRepository
	children    ChildLister
	workspaces  WorkspaceManager
	area        WorkingArea
	inventory   SnapshotInventory
	results     ResultsWorkspaceResources
	resultsRuns ResultsResourceRunInventory
	grace       time.Duration
	now         func() time.Time
}

// NewGroupResourcePruner constructs the pruner. Every dependency is required.
func NewGroupResourcePruner(groups instance.GroupRunRepository, instances instance.InstanceRepository, children ChildLister,
	workspaces WorkspaceManager, area WorkingArea, inventory SnapshotInventory) *GroupResourcePruner {
	switch {
	case groups == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil GroupRunRepository")
	case instances == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil InstanceRepository")
	case children == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil ChildLister")
	case workspaces == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil WorkspaceManager")
	case area == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil WorkingArea")
	case inventory == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil SnapshotInventory")
	}
	return &GroupResourcePruner{
		groups: groups, instances: instances, children: children, workspaces: workspaces,
		area: area, inventory: inventory, grace: GroupOrphanSnapshotGrace, now: time.Now,
	}
}

// ResultsWorkspaceResources supplies both results-mode snapshot operations
// and their disk inventory, including snapshots left before roster persistence.
type ResultsWorkspaceResources interface {
	GroupWorkspace
	SnapshotInventory
}

// ResultsResourceRunInventory names done results runs with completed tasks,
// even when launch already deleted their snapshot and delivered the output.
type ResultsResourceRunInventory interface {
	ListResultsResourceRuns(ctx context.Context) ([]uuid.UUID, error)
}

// SetResultsResources wires results-mode recovery before watchers/sweeps start.
// Both collaborators are required together; neither inventory alone covers
// snapshots left before persistence AND tasks retained after snapshot release.
func (p *GroupResourcePruner) SetResultsResources(ws ResultsWorkspaceResources, runs ResultsResourceRunInventory) {
	if ws == nil || runs == nil {
		panic("engine: results pruning requires snapshot and completed-run inventories")
	}
	p.results, p.resultsRuns = ws, runs
}

// PruneRun releases what runID no longer needs and reports whether its
// snapshot is released (now or earlier). false with a nil error means a pin is
// still held; an error leaves everything for the next pass (idempotent).
func (p *GroupResourcePruner) PruneRun(ctx context.Context, runID uuid.UUID) (bool, error) {
	run, err := p.groups.Get(ctx, runID)
	if errors.Is(err, instance.ErrGroupRunNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("group pruner: load run %s: %w", runID, err)
	}
	if run.GatherMode == workflow.GroupGatherResults {
		return p.pruneResultsRun(ctx, run)
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles || strings.TrimSpace(run.WorkspaceSnapshot) == "" {
		return false, nil
	}
	if run.State != instance.GroupRunDone {
		return false, nil
	}
	present, err := p.inventory.HasSnapshot(ctx, run.WorkspaceSnapshot)
	if err != nil {
		return false, fmt.Errorf("group pruner: run %s: inspect snapshot: %w", run.ID, err)
	}
	if !present {
		return true, nil
	}
	if h, ok := GatherHandleFromReport(run.Gather); ok && h.RollbackRef != "" {
		if err := p.area.ReleaseRef(ctx, run.WorkspaceSnapshot, h.RollbackRef); err != nil {
			return false, fmt.Errorf("group pruner: run %s: release the gather's rollback pin %s: %w", run.ID, h.RollbackRef, err)
		}
	}
	pinned, completed, neverStarted, err := p.pins(ctx, run)
	if err != nil || pinned {
		return false, err
	}
	if err := p.removeNeverStarted(ctx, run, neverStarted); err != nil {
		return false, err
	}
	applied := run.Gather != nil && run.Gather.State == gatherStateApplied
	for _, id := range completed {
		if !applied {
			warnGroupTaskWorkspaceRetained(p.workspaces, id, "group_gather_not_applied")
			continue
		}
		if err := p.workspaces.RemoveInstanceDir(id); err != nil {
			return false, fmt.Errorf("group pruner: run %s: remove task workspace %s: %w", run.ID, id, err)
		}
	}
	if err := p.area.ReleaseSnapshot(ctx, run.WorkspaceSnapshot); err != nil {
		return false, fmt.Errorf("group pruner: run %s: release snapshot %s: %w", run.ID, run.WorkspaceSnapshot, err)
	}
	slog.Info("parallel_group: released the group's working-area resources",
		"groupRunID", run.ID, "snapshot", run.WorkspaceSnapshot, "gatherApplied", applied)
	return true, nil
}

// pins reports whether any writer or dependent still holds the run's
// resources, and otherwise lists the completed task instances and the reserved
// ids of tasks that never started (no child row: their fork is unused).
func (p *GroupResourcePruner) pins(ctx context.Context, run *instance.GroupRun) (bool, []uuid.UUID, []uuid.UUID, error) {
	var completed, released []uuid.UUID
	for _, t := range run.Tasks {
		child, err := p.instances.Get(ctx, t.ChildInstanceID)
		if err != nil {
			if t.LaunchState != instance.GroupTaskLaunched {
				// Never started: nothing writes there. Forks precede the
				// roster, so an interrupted launch leaves one on disk with no
				// child row; only a confirmed absence marks it releasable.
				if errors.Is(err, instance.ErrNotFound) {
					released = append(released, t.ChildInstanceID)
				}
				continue
			}
			return false, nil, nil, fmt.Errorf("group pruner: run %s task %q: load %s: %w", run.ID, t.Key, t.ChildInstanceID, err)
		}
		if !child.State.IsTerminal() {
			return true, nil, nil, nil
		}
		active, err := anyActiveDescendant(ctx, p.children, child.ID)
		if err != nil {
			return false, nil, nil, fmt.Errorf("group pruner: run %s task %q: list descendants: %w", run.ID, t.Key, err)
		}
		if active {
			return true, nil, nil, nil
		}
		nested, err := p.groups.ListByParent(ctx, child.ID)
		if err != nil {
			return false, nil, nil, fmt.Errorf("group pruner: run %s task %q: list nested groups: %w", run.ID, t.Key, err)
		}
		for _, n := range nested {
			if n.State != instance.GroupRunDone {
				return true, nil, nil, nil
			}
		}
		if child.State == instance.StateCompleted {
			completed = append(completed, child.ID)
		}
	}
	return false, completed, released, nil
}

// removeNeverStarted releases the forks of tasks a crash stopped before their
// child was created (the launcher's failLaunch/stopAfterDecision release them
// in-process). Only a done run gets here: it was decided on a partial roster,
// and StartGroupTask refuses a terminal parent, so no late start can use them.
func (p *GroupResourcePruner) removeNeverStarted(ctx context.Context, run *instance.GroupRun, ids []uuid.UUID) error {
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.workspaces.RemoveInstanceDir(id); err != nil {
			return fmt.Errorf("group pruner: run %s: remove never-started task workspace %s: %w", run.ID, id, err)
		}
	}
	return nil
}

// Sweep prunes every run that still holds a snapshot and releases orphaned
// snapshots (no run row) older than the grace period. Per-entry errors are
// joined; the listing error is returned alone.
func (p *GroupResourcePruner) Sweep(ctx context.Context) error {
	return errors.Join(p.sweepFiles(ctx), p.sweepResults(ctx))
}

func (p *GroupResourcePruner) sweepFiles(ctx context.Context) error {
	infos, err := p.inventory.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("group pruner: list snapshots: %w", err)
	}
	var errs []error
	for _, info := range infos {
		id, perr := uuid.Parse(info.Handle)
		if perr != nil || id == uuid.Nil {
			continue
		}
		_, err := p.groups.Get(ctx, id)
		if errors.Is(err, instance.ErrGroupRunNotFound) {
			if p.now().Sub(info.ModTime) >= p.grace {
				if rerr := p.area.ReleaseSnapshot(ctx, info.Handle); rerr != nil {
					errs = append(errs, fmt.Errorf("group pruner: release orphaned snapshot %s: %w", info.Handle, rerr))
				}
			}
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("group pruner: load run %s: %w", id, err))
			continue
		}
		if _, err := p.PruneRun(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// anyActiveDescendant walks the spawn-link tree below rootID, through terminal
// intermediates, looking for a non-terminal run.
func anyActiveDescendant(ctx context.Context, children ChildLister, rootID uuid.UUID) (bool, error) {
	seen := map[uuid.UUID]bool{rootID: true}
	frontier := []uuid.UUID{rootID}
	for depth := 0; len(frontier) > 0 && depth <= instance.MaxSpawnDepth; depth++ {
		var next []uuid.UUID
		for _, id := range frontier {
			kids, err := children.ListChildren(ctx, id)
			if err != nil {
				return false, err
			}
			for _, c := range kids {
				if !c.State.IsTerminal() {
					return true, nil
				}
				if !seen[c.ID] {
					seen[c.ID] = true
					next = append(next, c.ID)
				}
			}
		}
		frontier = next
	}
	return false, nil
}

// warnGroupTaskWorkspaceRetained mirrors Engine.warnWorkspaceRetained
// (workspace.go:68-88) for a group task workspace kept past its run.
func warnGroupTaskWorkspaceRetained(ws WorkspaceManager, id uuid.UUID, reason string) {
	bytes, files, err := ws.InstanceDirStats(id)
	if err != nil {
		slog.Warn("workspace retained but stat failed", "instanceID", id, "reason", reason, "error", err)
		return
	}
	if files == 0 && bytes == 0 {
		return
	}
	slog.Warn("workspace retained on disk", "instanceID", id, "reason", reason, "bytes", bytes, "files", files)
}

// Every results fork is completed BEFORE CreateWithTasks. A persisted run
// therefore no longer needs its source snapshot, even if launch was interrupted.
// Completed workspaces have a different lifetime and cannot use snapshot
// absence as an "already pruned" marker.
func (p *GroupResourcePruner) pruneResultsRun(ctx context.Context, run *instance.GroupRun) (bool, error) {
	if p.results == nil {
		return false, nil
	}
	if run.WorkspaceSnapshot != "" {
		if err := p.results.ReleaseSnapshot(ctx, run.WorkspaceSnapshot); err != nil {
			return false, fmt.Errorf("group pruner: run %s: release results snapshot: %w", run.ID, err)
		}
	}
	if run.State != instance.GroupRunDone {
		return false, nil
	}
	pinned, completed, neverStarted, err := p.pins(ctx, run)
	if err != nil || pinned {
		return false, err
	}
	if err := p.removeNeverStarted(ctx, run, neverStarted); err != nil {
		return false, err
	}
	for _, id := range completed {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := p.workspaces.RemoveInstanceDir(id); err != nil {
			return false, fmt.Errorf("group pruner: run %s: remove results task workspace %s: %w", run.ID, id, err)
		}
	}
	return true, nil
}

func (p *GroupResourcePruner) sweepResults(ctx context.Context) error {
	if p.results == nil {
		return nil
	}
	var errs []error
	infos, err := p.results.ListSnapshots(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("group pruner: list results snapshots: %w", err))
	}
	seen := map[uuid.UUID]bool{}
	for _, info := range infos {
		if info.GroupRunID == uuid.Nil {
			continue
		}
		run, err := p.groups.Get(ctx, info.GroupRunID)
		if errors.Is(err, instance.ErrGroupRunNotFound) {
			if p.now().Sub(info.ModTime) >= p.grace {
				if err := p.results.ReleaseSnapshot(ctx, info.Handle); err != nil {
					errs = append(errs, fmt.Errorf("group pruner: release orphaned results snapshot %s: %w", info.Handle, err))
				}
			}
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if run.GatherMode != workflow.GroupGatherResults {
			continue
		}
		seen[run.ID] = true
		if _, err := p.pruneResultsRun(ctx, run); err != nil {
			errs = append(errs, err)
		}
	}
	// This query must still run when listing snapshots fails: done tasks are
	// discoverable independently of their already-released source snapshot.
	ids, err := p.resultsRuns.ListResultsResourceRuns(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("group pruner: list results task resources: %w", err))
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if _, err := p.PruneRun(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
