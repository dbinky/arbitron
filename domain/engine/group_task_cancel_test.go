package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coordinator's cancel_unfinished and dead-parent paths depend only on
// the task-scoped operation, never on the operator's subtree cancel.
var _ engine.GroupTaskCanceller = (*engine.Engine)(nil)

// paired:10 BREAK. A cancel_unfinished decision reads a task as open, the task
// completes, then the coordinator cancels it. The task's own fire-and-forget
// work is not the group's unfinished work: no subtree intent is stamped on the
// completed task and nothing below it is cancelled.
func TestCancelGroupTask_JustCompletedTaskKeepsItsIndependentDescendants(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	task := c.groupTaskOf(t, parent, instance.StateCompleted)
	fireForget := c.run(t, instance.StateRunning)
	c.links.link(task, fireForget)

	err := c.f.engine.CancelGroupTask(c.ctx, task)

	assert.ErrorIs(t, err, instance.ErrCannotCancel, "a terminal task is the benign race the coordinator tolerates")
	assert.False(t, c.links.isMarked(task), "a completed task carries no subtree cancellation intent")
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, task))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, fireForget), "the completed task's independent work keeps running")
	assert.Equal(t, 0, c.interventions.count())
}

// Contrast: Engine.Cancel on the same shape is the operator's subtree cancel
// and does stop the descendant. This is what the coordinator must not reach.
func TestCancelGroupTask_SubtreeCancelOnTheSameShapeStopsTheDescendant(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	task := c.groupTaskOf(t, parent, instance.StateCompleted)
	fireForget := c.run(t, instance.StateRunning)
	c.links.link(task, fireForget)

	require.NoError(t, c.f.engine.Cancel(c.ctx, task))

	assert.True(t, c.links.isMarked(task))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, fireForget))
}

// An open task is cancelled together with everything below it, including work
// under its terminal intermediates, exactly as the subtree cancel would.
func TestCancelGroupTask_OpenTaskIsCancelledWithItsDescendants(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	task := c.groupTaskOf(t, parent, instance.StateRunning)
	mid := c.run(t, instance.StateCompleted)
	grand := c.run(t, instance.StateRunning)
	c.links.link(task, mid)
	c.links.link(mid, grand)

	require.NoError(t, c.f.engine.CancelGroupTask(c.ctx, task))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, task))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, mid))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, grand))
	assert.False(t, c.links.isMarked(task), "an active task's cancelled state is its own durable intent")
}
