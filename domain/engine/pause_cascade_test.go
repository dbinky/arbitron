package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pausedBy reads the cascade-pause marker the engine recorded for id.
func (c *cascadeFixture) pausedBy(t *testing.T, id uuid.UUID) *uuid.UUID {
	t.Helper()
	by, err := c.links.CascadePausedBy(context.Background(), id)
	require.NoError(t, err)
	return by
}

// ---------- Pause ----------

func TestPause_CascadesToRunningWaitingQueuedChildren(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	running := c.run(t, instance.StateRunning)
	waiting := c.run(t, instance.StateWaiting)
	queued := c.run(t, instance.StateQueued)
	for _, k := range []uuid.UUID{running, waiting, queued} {
		c.links.link(parent, k)
	}

	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	assert.Equal(t, instance.StatePaused, c.stateOf(t, parent))
	for _, k := range []uuid.UUID{running, waiting, queued} {
		assert.Equal(t, instance.StatePaused, c.stateOf(t, k))
		require.NotNil(t, c.pausedBy(t, k))
		assert.Equal(t, parent, *c.pausedBy(t, k))
	}
	assert.Nil(t, c.pausedBy(t, parent), "the run the operator paused carries no cascade marker")
}

func TestPause_CascadesThroughGrandchildren_AndTerminalIntermediates(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	done := c.run(t, instance.StateCompleted)
	grand := c.run(t, instance.StateRunning)
	c.links.link(parent, done)
	c.links.link(done, grand)

	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	assert.Equal(t, instance.StateCompleted, c.stateOf(t, done))
	assert.Nil(t, c.pausedBy(t, done))
	assert.Equal(t, instance.StatePaused, c.stateOf(t, grand))
	require.NotNil(t, c.pausedBy(t, grand))
	assert.Equal(t, parent, *c.pausedBy(t, grand))
}

func TestPause_ManuallyPausedChild_MarkerUntouched(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	manual := c.run(t, instance.StatePaused)
	grand := c.run(t, instance.StateRunning)
	c.links.link(parent, manual)
	c.links.link(manual, grand)

	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	assert.Equal(t, instance.StatePaused, c.stateOf(t, manual))
	assert.Nil(t, c.pausedBy(t, manual), "an operator's own pause must not be adopted by the cascade")
	assert.Equal(t, instance.StatePaused, c.stateOf(t, grand))
	require.NotNil(t, c.pausedBy(t, grand))
	assert.Equal(t, parent, *c.pausedBy(t, grand))
}

// The deployed bug left paused parents with running children. Pausing such a
// parent again must sweep them rather than fail with "not running".
func TestPause_AlreadyPausedParent_SweepsActiveChildren(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StatePaused)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	assert.Equal(t, instance.StatePaused, c.stateOf(t, child))
	require.NotNil(t, c.pausedBy(t, child))
	assert.Equal(t, parent, *c.pausedBy(t, child))
}

func TestPause_AlreadyPausedParentNoActiveChildren_ReturnsErrNotRunning(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StatePaused)
	c.links.link(parent, c.run(t, instance.StateCompleted))

	err := c.f.engine.Pause(c.ctx, parent)

	require.ErrorIs(t, err, instance.ErrNotRunning)
}

func TestPause_OperatorClaimsCascadePausedChild(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	require.NoError(t, c.f.engine.Pause(c.ctx, child))

	assert.Equal(t, instance.StatePaused, c.stateOf(t, child))
	assert.Nil(t, c.pausedBy(t, child), "pausing it directly makes the pause the operator's own")
}

func TestPause_ChildFinishedMidWalk_Skipped(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	finishing := c.run(t, instance.StateRunning)
	sibling := c.run(t, instance.StateRunning)
	c.links.link(parent, finishing)
	c.links.link(parent, sibling)
	c.links.refusePause = map[uuid.UUID]bool{finishing: true}

	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	assert.Equal(t, instance.StateRunning, c.stateOf(t, finishing))
	assert.Nil(t, c.pausedBy(t, finishing))
	assert.Equal(t, instance.StatePaused, c.stateOf(t, sibling))
}

func TestPause_UnauthorizedChild_BranchSkipped(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.runInDepartment(t, instance.StateRunning, "eng")
	child := c.runInDepartment(t, instance.StateRunning, "finance")
	grand := c.runInDepartment(t, instance.StateRunning, "eng")
	c.links.link(parent, child)
	c.links.link(child, grand)
	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})

	require.NoError(t, c.f.engine.Pause(leadCtx, parent))

	assert.Equal(t, instance.StatePaused, c.stateOf(t, parent))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, grand),
		"the cascade must not reach past a child it could not authorize")
}

func TestPause_CascadeRecordsInterventionPerDescendant(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	grand := c.run(t, instance.StateQueued)
	c.links.link(parent, child)
	c.links.link(child, grand)

	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	assert.Equal(t, 3, c.interventions.count())
}

func TestPause_DirectPauseOfQueuedRun_StillRejected(t *testing.T) {
	c := newCascadeFixture(t)
	queued := c.run(t, instance.StateQueued)

	err := c.f.engine.Pause(c.ctx, queued)

	require.ErrorIs(t, err, instance.ErrNotRunning)
	assert.Equal(t, instance.StateQueued, c.stateOf(t, queued))
}

// ---------- Resume ----------

// runWith creates a fixture-owned instance after applying mutate.
func (c *cascadeFixture) runWith(t *testing.T, state instance.State, mutate func(*instance.Instance)) uuid.UUID {
	t.Helper()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: c.owner, WorkflowName: "wf",
		State: state, Priority: instance.PriorityNormal,
	}
	mutate(inst)
	require.NoError(t, c.f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

// timerRun is a run waiting on an unexpired timer: Resume returns it to
// waiting without starting a graph walk, so its state is deterministic.
func (c *cascadeFixture) timerRun(t *testing.T) uuid.UUID {
	t.Helper()
	future := time.Now().Add(time.Hour)
	return c.runWith(t, instance.StateWaiting, func(i *instance.Instance) { i.WaitingUntil = &future })
}

func TestResume_ResumesCascadePausedChildren(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.timerRun(t)
	timer := c.timerRun(t)
	running := c.run(t, instance.StateRunning)
	c.links.link(parent, timer)
	c.links.link(parent, running)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))
	require.Equal(t, instance.StatePaused, c.stateOf(t, timer))

	require.NoError(t, c.f.engine.Resume(c.ctx, parent))

	assert.Equal(t, instance.StateWaiting, c.stateOf(t, timer), "an unexpired timer child goes back to waiting")
	assert.NotEqual(t, instance.StatePaused, c.stateOf(t, running), "a running-origin child is restarted")
	assert.Nil(t, c.pausedBy(t, timer))
	assert.Nil(t, c.pausedBy(t, running))
}

func TestResume_QueuedOriginChild_ReturnsToQueueWithOriginalQueuedAt(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.timerRun(t)
	queuedAt := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	child := c.runWith(t, instance.StateQueued, func(i *instance.Instance) { i.QueuedAt = &queuedAt })
	c.links.link(parent, child)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))
	require.Equal(t, instance.StatePaused, c.stateOf(t, child))

	require.NoError(t, c.f.engine.Resume(c.ctx, parent))

	got, err := c.f.instanceRepo.Get(context.Background(), child)
	require.NoError(t, err)
	assert.Equal(t, instance.StateQueued, got.State, "a queued child must not jump the queue on resume")
	require.NotNil(t, got.QueuedAt)
	assert.True(t, queuedAt.Equal(*got.QueuedAt), "the child keeps its place in the queue")
}

func TestResume_ManuallyPausedChild_StaysPaused(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.timerRun(t)
	manual := c.run(t, instance.StatePaused)
	grand := c.timerRun(t)
	c.links.link(parent, manual)
	c.links.link(manual, grand)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	require.NoError(t, c.f.engine.Resume(c.ctx, parent))

	assert.Equal(t, instance.StatePaused, c.stateOf(t, manual))
	assert.Equal(t, instance.StateWaiting, c.stateOf(t, grand),
		"a cascade-paused grandchild below a manually paused child is still resumed")
}

func TestResume_ClaimedChild_StaysPaused(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.timerRun(t)
	child := c.timerRun(t)
	c.links.link(parent, child)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))
	require.NoError(t, c.f.engine.Pause(c.ctx, child))

	require.NoError(t, c.f.engine.Resume(c.ctx, parent))

	assert.Equal(t, instance.StatePaused, c.stateOf(t, child))
}

func TestResume_ChildAlreadyResumedDirectly_NoOp(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.timerRun(t)
	child := c.timerRun(t)
	c.links.link(parent, child)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))
	require.NoError(t, c.f.engine.Resume(c.ctx, child))
	require.NoError(t, c.f.instanceRepo.UpdateState(context.Background(), child, instance.StateCompleted))

	require.NoError(t, c.f.engine.Resume(c.ctx, parent))

	assert.Equal(t, instance.StateCompleted, c.stateOf(t, child))
}

// Resuming a child directly must honour where the cascade took it from: a
// child paused out of the queue goes back to the queue at its old position,
// not straight to running ahead of the queue and outside the capacity caps.
func TestResume_DirectResumeOfQueuedOriginChild_ReturnsToQueue(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.timerRun(t)
	queuedAt := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	child := c.runWith(t, instance.StateQueued, func(i *instance.Instance) { i.QueuedAt = &queuedAt })
	c.links.link(parent, child)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	require.NoError(t, c.f.engine.Resume(c.ctx, child))

	got, err := c.f.instanceRepo.Get(context.Background(), child)
	require.NoError(t, err)
	assert.Equal(t, instance.StateQueued, got.State)
	require.NotNil(t, got.QueuedAt)
	assert.True(t, queuedAt.Equal(*got.QueuedAt))
	assert.Nil(t, c.pausedBy(t, child))
}

// Resuming one run in the middle of a cascade-paused tree resumes that run's
// own subtree too: an operator resuming a stage during a global pause expects
// the stage to run, not to wait forever on a grandchild still held paused.
func TestResume_MidTreeResume_ResumesItsCascadePausedSubtree(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.timerRun(t)
	mid := c.timerRun(t)
	leaf := c.timerRun(t)
	other := c.timerRun(t)
	c.links.link(root, mid)
	c.links.link(mid, leaf)
	c.links.link(root, other)
	require.NoError(t, c.f.engine.Pause(c.ctx, root))

	require.NoError(t, c.f.engine.Resume(c.ctx, mid))

	assert.Equal(t, instance.StateWaiting, c.stateOf(t, mid))
	assert.Equal(t, instance.StateWaiting, c.stateOf(t, leaf), "the resumed run's subtree runs too")
	assert.Nil(t, c.pausedBy(t, leaf))
	assert.Equal(t, instance.StatePaused, c.stateOf(t, other), "siblings outside the resumed subtree stay paused")
	assert.Equal(t, instance.StatePaused, c.stateOf(t, root))
}

func TestResume_DirectResumeOfCascadePausedChild_ClearsMarker(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.timerRun(t)
	child := c.timerRun(t)
	c.links.link(parent, child)
	require.NoError(t, c.f.engine.Pause(c.ctx, parent))

	require.NoError(t, c.f.engine.Resume(c.ctx, child))

	assert.Nil(t, c.pausedBy(t, child))
}
