package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/semaphore"
)

var (
	// ErrExecutionRevisionStale marks evidence gathered against an execution
	// revision that has since changed.
	ErrExecutionRevisionStale = errors.New("provider: execution revision changed")
	// ErrExecutionRevisionUnavailable marks an inventory that could not be
	// fingerprinted. It is never treated as an empty (vacuously current) one.
	ErrExecutionRevisionUnavailable = errors.New("provider: execution revision unavailable")
)

// ExecutionRevision is the immutable token an inventory hands to selection and
// the CAS: the digest ID plus the persisted revisions validated inside the SQL
// transaction.
type ExecutionRevision struct {
	ID             string
	PolicyRevision int64
	Connections    map[string]int64
	WorkflowHashes map[string]string
}

// Clone returns a deep copy.
func (r ExecutionRevision) Clone() ExecutionRevision {
	c := r
	if r.Connections != nil {
		c.Connections = make(map[string]int64, len(r.Connections))
		for k, v := range r.Connections {
			c.Connections[k] = v
		}
	}
	if r.WorkflowHashes != nil {
		c.WorkflowHashes = make(map[string]string, len(r.WorkflowHashes))
		for k, v := range r.WorkflowHashes {
			c.WorkflowHashes[k] = v
		}
	}
	return c
}

// ExecutionGate serializes every execution-affecting mutation (connection,
// profile, workflow and identity-policy edits) against model advancement and
// run-snapshot creation. Readers share a lease; mutators take the write lease.
//
// Callbacks must not re-enter the gate: a WithCurrent callback commits through
// the adapter transaction helper directly, never through a public mutator.
type ExecutionGate struct {
	leaseOnce sync.Once
	lease     *semaphore.Weighted
	srcMu     sync.RWMutex
	current   func(context.Context) (string, error)
}

func NewExecutionGate() *ExecutionGate { return &ExecutionGate{} }

// Readers consume one unit, and a mutation consumes the whole capacity. The
// semaphore queues writers ahead of later readers and removes cancelled
// waiters without spawning goroutines or retaining a lease. Lazy construction
// preserves the gate's usable zero value.
const executionGateCapacity int64 = 1<<63 - 1

func (g *ExecutionGate) acquire(ctx context.Context, weight int64) error {
	g.leaseOnce.Do(func() { g.lease = semaphore.NewWeighted(executionGateCapacity) })
	return g.lease.Acquire(ctx, weight)
}

// SetCurrentRevision late-binds the inventory fingerprint after the inventory
// has been constructed from the services this gate is injected into.
func (g *ExecutionGate) SetCurrentRevision(fn func(context.Context) (string, error)) {
	g.srcMu.Lock()
	g.current = fn
	g.srcMu.Unlock()
}

func (g *ExecutionGate) revisionSource() func(context.Context) (string, error) {
	g.srcMu.RLock()
	defer g.srcMu.RUnlock()
	return g.current
}

func (g *ExecutionGate) check(ctx context.Context, fn func(context.Context) error) error {
	if g == nil {
		return errors.New("provider: execution gate not configured")
	}
	if fn == nil {
		return errors.New("provider: execution gate callback is nil")
	}
	return ctx.Err()
}

// WithRead holds the shared lease without an expected revision (run-snapshot
// creation).
func (g *ExecutionGate) WithRead(ctx context.Context, fn func(context.Context) error) error {
	if err := g.check(ctx, fn); err != nil {
		return err
	}
	if err := g.acquire(ctx, 1); err != nil {
		return err
	}
	defer g.lease.Release(1)
	return fn(ctx)
}

// WithCurrent holds the shared lease, recomputes the current inventory
// revision and runs fn only when it equals expected.
func (g *ExecutionGate) WithCurrent(ctx context.Context, expected string, fn func(context.Context) error) error {
	if err := g.check(ctx, fn); err != nil {
		return err
	}
	if err := g.acquire(ctx, 1); err != nil {
		return err
	}
	defer g.lease.Release(1)
	src := g.revisionSource()
	if src == nil {
		return fmt.Errorf("%w: no inventory revision source", ErrExecutionRevisionUnavailable)
	}
	current, err := src(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrExecutionRevisionUnavailable, err)
	}
	if expected == "" || current == "" {
		return fmt.Errorf("%w: empty revision", ErrExecutionRevisionUnavailable)
	}
	if current != expected {
		return ErrExecutionRevisionStale
	}
	return fn(ctx)
}

// WithMutation holds the exclusive lease for an execution-affecting write.
func (g *ExecutionGate) WithMutation(ctx context.Context, fn func(context.Context) error) error {
	if err := g.check(ctx, fn); err != nil {
		return err
	}
	if err := g.acquire(ctx, executionGateCapacity); err != nil {
		return err
	}
	defer g.lease.Release(executionGateCapacity)
	return fn(ctx)
}
