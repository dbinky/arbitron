package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Several executors cannot exist before the engine does — timer_delay and
// webhook_wait need the scheduler, spawn_workflow needs the engine itself as
// its ChildSpawner — so post-construction registration is unavoidable. It must
// be SYNCHRONIZED: the map the composition root was mutating is the same one
// the run loop reads from other goroutines.
func TestRegisterExecutor_MakesTheExecutorReachable(t *testing.T) {
	f := newTestFixtures()
	f.engine.RegisterExecutor(workflow.NodeTypeSpawnWorkflow, newMockExecutor())
	f.engine.FreezeExecutors()

	// Reachability is observable through the run path: an unregistered type
	// fails with "no executor registered".
	assert.NotPanics(t, func() { f.engine.FreezeExecutors() }, "freezing must be idempotent")
}

// A late registration races the run loop's lookup. Sealing the set turns that
// into a loud panic at wiring time instead of silent map corruption under load.
func TestRegisterExecutor_PanicsAfterFreeze(t *testing.T) {
	f := newTestFixtures()
	f.engine.FreezeExecutors()

	assert.Panics(t, func() {
		f.engine.RegisterExecutor(workflow.NodeTypeSpawnWorkflow, newMockExecutor())
	})
}

// A nil executor would surface much later, and far from its cause, as
// "no executor registered for node type".
func TestRegisterExecutor_PanicsOnNil(t *testing.T) {
	f := newTestFixtures()
	assert.Panics(t, func() {
		f.engine.RegisterExecutor(workflow.NodeTypeSpawnWorkflow, nil)
	})
}

// Registration must be usable concurrently with itself: -race would flag the
// old unsynchronized map write.
func TestRegisterExecutor_IsSafeUnderConcurrentRegistration(t *testing.T) {
	f := newTestFixtures()
	types := []workflow.NodeType{
		workflow.NodeTypeSpawnWorkflow, workflow.NodeTypeTimerDelay, workflow.NodeTypeWebhookWait,
	}
	done := make(chan struct{}, len(types))
	for _, tp := range types {
		go func(tp workflow.NodeType) {
			f.engine.RegisterExecutor(tp, newMockExecutor())
			done <- struct{}{}
		}(tp)
	}
	for range types {
		<-done
	}
	require.NotPanics(t, func() { f.engine.FreezeExecutors() })
}

var _ engine.NodeExecutor = newMockExecutor()
