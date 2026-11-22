package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// InvocationStore is the durable producer of node-visit invocations (RR-01).
type InvocationStore interface {
	// Begin returns the OPEN (uncommitted) invocation for (runID,nodeID) if one
	// exists (restart reuse), else creates a new one with visit = previous + 1.
	Begin(ctx context.Context, runID uuid.UUID, nodeID string) (Invocation, error)
	// Commit marks the invocation committed (node finished and engine advanced).
	// Committing an already committed invocation is a no-op.
	Commit(ctx context.Context, invocationID uuid.UUID) error
	Get(ctx context.Context, invocationID uuid.UUID) (Invocation, error)
}

// Invocation is one visit of a node within a run.
type Invocation struct {
	ID          uuid.UUID
	RunID       uuid.UUID
	NodeID      string
	Visit       int
	CreatedAt   time.Time
	CommittedAt *time.Time
}

// ErrInvocationNotFound is returned for an unknown invocation id.
var ErrInvocationNotFound = errors.New("engine: invocation not found")

var errInvocationKey = errors.New("engine: an invocation needs a run and a node")

type invocationKey struct {
	run  uuid.UUID
	node string
}

// memoryInvocationStore is the in-process InvocationStore. It is the default
// for an Engine, conversation handler and group-resolution bridge built
// without the durable store (unit tests); production installs
// adapter/sqlite.InvocationRepo at the composition root.
type memoryInvocationStore struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]Invocation
	open   map[invocationKey]uuid.UUID
	visits map[invocationKey]int
}

// NewMemoryInvocationStore returns an empty in-memory store.
func NewMemoryInvocationStore() InvocationStore {
	return &memoryInvocationStore{
		byID:   map[uuid.UUID]Invocation{},
		open:   map[invocationKey]uuid.UUID{},
		visits: map[invocationKey]int{},
	}
}

func (m *memoryInvocationStore) Begin(_ context.Context, runID uuid.UUID, nodeID string) (Invocation, error) {
	if runID == uuid.Nil || strings.TrimSpace(nodeID) == "" {
		return Invocation{}, errInvocationKey
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := invocationKey{run: runID, node: nodeID}
	if id, ok := m.open[k]; ok {
		return m.byID[id], nil
	}
	m.visits[k]++
	inv := Invocation{ID: uuid.New(), RunID: runID, NodeID: nodeID, Visit: m.visits[k], CreatedAt: time.Now().UTC()}
	m.byID[inv.ID] = inv
	m.open[k] = inv.ID
	return inv, nil
}

func (m *memoryInvocationStore) Commit(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrInvocationNotFound, id)
	}
	if inv.CommittedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	inv.CommittedAt = &now
	m.byID[id] = inv
	delete(m.open, invocationKey{run: inv.RunID, node: inv.NodeID})
	return nil
}

func (m *memoryInvocationStore) Get(_ context.Context, id uuid.UUID) (Invocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.byID[id]
	if !ok {
		return Invocation{}, fmt.Errorf("%w: %s", ErrInvocationNotFound, id)
	}
	return inv, nil
}

// SetInvocationStore installs the durable invocation producer. nil reinstalls
// the in-memory store (used only by legacy tests). Call before any run starts.
func (e *Engine) SetInvocationStore(s InvocationStore) {
	if s == nil {
		s = NewMemoryInvocationStore()
	}
	e.mu.Lock()
	e.invocations = s
	e.mu.Unlock()
}

// InvocationStore returns the installed store (read-back for wiring tests).
func (e *Engine) InvocationStore() InvocationStore { return e.invocationStore() }

func (e *Engine) invocationStore() InvocationStore {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.invocations
}
