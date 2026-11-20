package engine

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func registerLocked(e *Engine, id uuid.UUID) context.Context {
	e.walkersMu.Lock()
	defer e.walkersMu.Unlock()
	return e.registerWalkerLocked(id)
}

func deregisterLocked(e *Engine, ctx context.Context, id uuid.UUID) {
	e.walkersMu.Lock()
	defer e.walkersMu.Unlock()
	e.deregisterWalkerLocked(ctx, id)
}

func registeredFor(e *Engine, id uuid.UUID) *walkerReg {
	e.walkersMu.Lock()
	defer e.walkersMu.Unlock()
	return e.contexts[id]
}

// A walker removes only the registration it owns. A predecessor's cleanup runs
// after its successor (Retry, Resume) registered under the same instance id and
// must leave the successor in place; the successor's own cleanup removes it.
func TestWalkerRegistration_PredecessorCleanupPreservesSuccessor(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, nil, "")
	id := uuid.New()

	predecessor := registerLocked(e, id)
	predecessorReg, _ := predecessor.Value(walkerRegKey{}).(*walkerReg)
	require.NotNil(t, predecessorReg)

	successor := registerLocked(e, id)
	successorReg, _ := successor.Value(walkerRegKey{}).(*walkerReg)
	require.NotNil(t, successorReg)
	require.NotSame(t, predecessorReg, successorReg)
	require.Same(t, successorReg, registeredFor(e, id), "the successor replaced the predecessor's entry")

	deregisterLocked(e, predecessor, id)
	assert.Same(t, successorReg, registeredFor(e, id), "predecessor cleanup must not remove its successor")

	deregisterLocked(e, successor, id)
	assert.Nil(t, registeredFor(e, id), "the successor's own cleanup removes its entry")
}

// A context that carries no registration (a caller that never registered)
// cleans up nothing, rather than deleting whoever is registered.
func TestWalkerRegistration_TokenlessContextIsHarmless(t *testing.T) {
	e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, nil, "")
	id := uuid.New()

	live := registerLocked(e, id)
	liveReg, _ := live.Value(walkerRegKey{}).(*walkerReg)

	deregisterLocked(e, context.Background(), id)
	assert.Same(t, liveReg, registeredFor(e, id), "a tokenless context must not remove a live registration")

	// And it is harmless when nothing is registered at all.
	other := uuid.New()
	deregisterLocked(e, context.Background(), other)
	assert.Nil(t, registeredFor(e, other))
}
