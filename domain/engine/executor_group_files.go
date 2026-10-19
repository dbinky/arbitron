package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// gatherGate serializes this process's writers of one run's file gather: the
// parent's executor (blocking lock, for the whole Gather call) and the
// gather reconciler (tryLock: a busy gate means the parent is writing right
// now and the reconciler leaves it alone). Entries are reference-counted, so
// the map only holds runs somebody is using. The zero value is ready to use.
type gatherGate struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*gatherGateEntry
}

type gatherGateEntry struct {
	mu   sync.Mutex
	refs int
}

func (g *gatherGate) acquire(id uuid.UUID) *gatherGateEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runs == nil {
		g.runs = make(map[uuid.UUID]*gatherGateEntry)
	}
	e := g.runs[id]
	if e == nil {
		e = &gatherGateEntry{}
		g.runs[id] = e
	}
	e.refs++
	return e
}

func (g *gatherGate) release(id uuid.UUID, e *gatherGateEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e.refs--
	if e.refs == 0 && g.runs[id] == e {
		delete(g.runs, id)
	}
}

func (g *gatherGate) lock(id uuid.UUID) func() {
	e := g.acquire(id)
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		g.release(id, e)
	}
}

func (g *gatherGate) tryLock(id uuid.UUID) (func(), bool) {
	e := g.acquire(id)
	if !e.mu.TryLock() {
		g.release(id, e)
		return nil, false
	}
	return func() {
		e.mu.Unlock()
		g.release(id, e)
	}, true
}

var _ GatherLocker = (*ParallelGroupExecutor)(nil)

// TryLockGather implements GatherLocker for the group gather reconciler: ok is
// false while this executor is inside a Gather call for runID.
func (x *ParallelGroupExecutor) TryLockGather(runID uuid.UUID) (func(), bool) {
	return x.gate.tryLock(runID)
}

// pendingFileGatherError blocks node ErrorBehavior after file gathering has
// begun. Only reconciliation may retry this operation: the parent's area can
// already be changed while its report or final output is not yet durable.
// suspend parks the parent with the running->waiting CAS (see park): an
// unconditional waiting write could revive a parent Engine.Cancel had just
// cancelled, and the reconciler would then wake it to finish the gather
// instead of rolling it back.
type pendingFileGatherError struct {
	cause   error
	suspend func(context.Context) (bool, error)
}

func (e *pendingFileGatherError) Error() string { return e.cause.Error() }
func (e *pendingFileGatherError) Unwrap() error { return e.cause }

// continueFileGather advances a results_and_files run that is ready to gather,
// in the parent's own goroutine, as the single writer of the parent's working
// area. The run is re-read under the gate (the reconciler may have settled it
// meanwhile). A terminal (cancelled) parent is never gathered for: the error
// wraps context.Canceled so Engine.handleNodeError halts without touching the
// parent's state, and the reconciler rolls the gather back. A failed rollback
// keeps the parent suspended (nil, nil => IsWait) instead of failing the node,
// so no error behaviour can continue past a half-restored area; the
// reconciler wakes it to retry. Other errors carry pendingFileGatherError so
// the engine suspends with a diagnostic trace instead of applying ErrorBehavior.
// A finished step is routed exactly once.
func (x *ParallelGroupExecutor) continueFileGather(ctx context.Context, node workflow.Node, run *instance.GroupRun, instanceID uuid.UUID) (result *NodeResult, err error) {
	defer func() {
		if err != nil {
			err = &pendingFileGatherError{cause: err, suspend: func(ctx context.Context) (bool, error) {
				return x.claims.SuspendRunning(ctx, instanceID)
			}}
		}
	}()
	unlock := x.gate.lock(run.ID)
	defer unlock()

	fresh, err := x.groups.Get(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: reload group run %s before gathering: %w", node.ID, run.ID, err)
	}
	if fresh.State == instance.GroupRunDone {
		return x.deliver(ctx, node, fresh)
	}
	if !fileGatherReady(fresh) {
		return nil, nil
	}
	parent, err := x.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: load parent instance: %w", node.ID, err)
	}
	if parent.State.IsTerminal() {
		return nil, fmt.Errorf("parallel_group %q: parent %s is %s, so its file gather is left to the group gather reconciler: %w",
			node.ID, instanceID, parent.State, context.Canceled)
	}
	step, err := x.gatherer.Gather(ctx, fresh, parent)
	if err != nil {
		if errors.Is(err, ErrGroupGatherRollbackFailed) {
			slog.Error("parallel_group: the parent's working area could not be restored; the parent stays suspended until a retried rollback succeeds",
				"nodeID", node.ID, "groupRunID", fresh.ID, "parentID", instanceID, "error", err)
			return nil, nil
		}
		return nil, fmt.Errorf("parallel_group %q: gather: %w", node.ID, err)
	}
	if step == nil || !step.Done {
		return nil, nil
	}
	if step.Port != workflow.GroupPortSuccess && step.Port != workflow.GroupPortFailure {
		return nil, fmt.Errorf("parallel_group %q: the gatherer finished with port %q (want %q or %q)",
			node.ID, step.Port, workflow.GroupPortSuccess, workflow.GroupPortFailure)
	}
	if err := x.groups.MarkContinuationDelivered(ctx, fresh.ID); err != nil {
		slog.Warn("parallel_group: recording delivery failed; a replay re-delivers the same output",
			"nodeID", node.ID, "groupRunID", fresh.ID, "error", err)
	}
	return &NodeResult{OutputPort: step.Port, Output: variable.DeepCopyMap(step.Output)}, nil
}
