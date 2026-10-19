package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ instance.SpawnLinkRepository = engine.NoopSpawnLinks{}

// groupTaskOf creates a parallel-group task child of parent (the P2-T1 link
// shape: SpawnModeGroupTask plus group linkage), owned by the fixture owner.
func (c *cascadeFixture) groupTaskOf(t *testing.T, parent uuid.UUID, state instance.State) uuid.UUID {
	t.Helper()
	pid := parent
	node := "group-1"
	runID := uuid.New()
	key := "loser"
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: c.owner, WorkflowName: "wf",
		State: state, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
	}
	require.NoError(t, c.f.instanceRepo.Create(context.Background(), inst))
	c.links.link(parent, inst.ID)
	return inst.ID
}

// ---------- happy path ----------

// THE CORE P2-D ASSERTION. A completed intermediate (a branch that finished)
// still owns a running grandchild. ListActiveChildren never lists the
// completed intermediate, so a cascade walking it can never reach the
// grandchild. No `unfiltered` bypass here: the real traversal surface must
// produce the intermediate.
func TestCancelSubtree_GrandchildUnderCompletedIntermediateIsCancelled(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateRunning)
	mid := c.run(t, instance.StateCompleted)
	grand := c.run(t, instance.StateRunning)
	c.links.link(root, mid)
	c.links.link(mid, grand)

	require.NoError(t, c.f.engine.Cancel(c.ctx, root))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, root))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, mid), "a terminal intermediate is traversed, never rewritten")
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, grand))
	assert.Equal(t, 2, c.interventions.count(), "root and grandchild are audited; the untouched intermediate is not")
}

// THE CORE TERMINAL-PARENT ASSERTION. A first_success group decided with
// finish_independently leaves a loser running after its parent completed.
// Cancelling the completed parent must stop the loser, succeed, and leave the
// parent's own state, events and audit trail exactly as they were.
func TestCancelSubtree_CompletedParentStopsIndependentGroupTaskWithoutChangingItsState(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	loser := c.groupTaskOf(t, parent, instance.StateRunning)

	var mu sync.Mutex
	var transitioned []uuid.UUID
	c.f.instanceRepo.onUpdateState = func(id uuid.UUID, _ instance.State) {
		mu.Lock()
		transitioned = append(transitioned, id)
		mu.Unlock()
	}

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCompleted, c.stateOf(t, parent), "the parent's outcome is never reopened")
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, loser))
	mu.Lock()
	assert.Equal(t, []uuid.UUID{loser}, transitioned, "only the loser's state may be written")
	mu.Unlock()
	assert.Empty(t, c.f.eventBus.eventsOfType(parent, engine.EventTypeInstanceCancelled),
		"a completed parent must not announce a cancellation that did not happen to it")
	assert.Len(t, c.f.eventBus.eventsOfType(loser, engine.EventTypeInstanceCancelled), 1)

	parentRecords, err := c.interventions.ListByInstance(context.Background(), parent)
	require.NoError(t, err)
	assert.Empty(t, parentRecords, "no intervention is recorded against the untouched parent")
	loserRecords, err := c.interventions.ListByInstance(context.Background(), loser)
	require.NoError(t, err)
	assert.Len(t, loserRecords, 1)
}

// ---------- success ----------

// A crash-recovered (failed) parent can still have its independent work torn
// down, and the cascade climbs through every terminal kind on the way.
func TestCancelSubtree_DeepChainThroughMixedTerminalIntermediates(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateFailed)
	a := c.run(t, instance.StateCompleted)
	b := c.run(t, instance.StateFailed)
	d := c.run(t, instance.StateCancelled)
	leaf := c.run(t, instance.StateQueued)
	c.links.link(root, a)
	c.links.link(a, b)
	c.links.link(b, d)
	c.links.link(d, leaf)

	require.NoError(t, c.f.engine.Cancel(c.ctx, root))

	assert.Equal(t, instance.StateFailed, c.stateOf(t, root))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, a))
	assert.Equal(t, instance.StateFailed, c.stateOf(t, b))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, d))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, leaf))
}

// LEGACY UNCHANGED: an active parent's spawn children — awaited and
// fire-and-forget, active and terminal — get exactly the treatment they got
// before: every active one cancelled and audited, every terminal one left as it
// was.
func TestCancelSubtree_LegacySpawnChildrenOfActiveParentUnchanged(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateWaiting)
	awaited := c.run(t, instance.StateRunning)
	fireForget := c.run(t, instance.StateQueued)
	done := c.run(t, instance.StateCompleted)
	c.links.link(parent, awaited)
	c.links.link(parent, fireForget)
	c.links.link(parent, done)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, awaited))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, fireForget))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, done))
	assert.Equal(t, 3, c.interventions.count())
}

// ---------- failure ----------

// A terminal parent with nothing left to stop keeps the old contract exactly:
// the original ErrCannotCancel, naming the parent's state.
func TestCancelSubtree_TerminalParentWithOnlyTerminalDescendantsReturnsErrCannotCancel(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateCompleted)
	grand := c.run(t, instance.StateFailed)
	c.links.link(parent, child)
	c.links.link(child, grand)

	err := c.f.engine.Cancel(c.ctx, parent)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
	assert.Contains(t, err.Error(), "(state: completed)")
	assert.Equal(t, 0, c.interventions.count())
}

func TestCancelSubtree_TerminalParentWithoutChildrenReturnsErrCannotCancel(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCancelled)

	err := c.f.engine.Cancel(c.ctx, parent)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
}

// Authorization is checked on the terminal parent itself: a department peer
// who may VIEW but not intervene gets ErrForbidden and nothing below it moves,
// even a descendant the actor could authorize in isolation.
func TestCancelSubtree_TerminalParentAuthorizationFailureCascadesNothing(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.runInDepartment(t, instance.StateCompleted, "finance")
	child := c.runInDepartment(t, instance.StateRunning, "eng")
	c.links.link(parent, child)

	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})

	err := c.f.engine.Cancel(leadCtx, parent)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrForbidden)
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child))
	assert.Equal(t, 0, c.interventions.count())
}

// Authorization still gates TERMINAL intermediates: the cascade re-authorizes
// every node it traverses, so a completed intermediate the actor may not
// intervene on halts its branch exactly as an active one does.
func TestCancelSubtree_AuthorizationFailureOnTerminalIntermediateHaltsBranch(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.runInDepartment(t, instance.StateRunning, "eng")
	mid := c.runInDepartment(t, instance.StateCompleted, "finance")
	grand := c.runInDepartment(t, instance.StateRunning, "eng")
	c.links.link(root, mid)
	c.links.link(mid, grand)

	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})

	require.NoError(t, c.f.engine.Cancel(leadCtx, root))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, root))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, mid))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, grand),
		"the cascade must not reach past a terminal node it could not authorize")
}

// ---------- error ----------

// A storage failure listing a terminal parent's children leaves nothing
// cancelled, so the caller gets the honest answer: the parent could not be
// cancelled.
func TestCancelSubtree_TerminalParentListFailureReturnsErrCannotCancel(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)
	c.links.listErr[parent] = errors.New("db down")

	err := c.f.engine.Cancel(c.ctx, parent)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child))
}

// ---------- edge cases ----------

func TestNoopSpawnLinks_ListChildrenReportsNone(t *testing.T) {
	got, err := engine.NoopSpawnLinks{}.ListChildren(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// The orphan sweep now finds a stranded run several levels below the cancelled
// root, through completed and failed intermediates — the exact residue an
// interrupted terminal-intermediate cascade leaves.
func TestOrphanReconciler_SweepsGreatGrandchildUnderCancelledRootThroughTerminalIntermediates(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateCancelled)
	mid1 := c.run(t, instance.StateCompleted)
	mid2 := c.run(t, instance.StateFailed)
	leaf := c.run(t, instance.StateRunning)
	c.links.link(root, mid1)
	c.links.link(mid1, mid2)
	c.links.link(mid2, leaf)

	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, leaf))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, mid1))
	assert.Equal(t, instance.StateFailed, c.stateOf(t, mid2))
}

// Independent work under a COMPLETED root carries no cancellation intent and
// must survive the sweep.
func TestOrphanReconciler_LeavesIndependentWorkUnderCompletedRootAlone(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateCompleted)
	loser := c.groupTaskOf(t, root, instance.StateRunning)

	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateRunning, c.stateOf(t, loser))
}

// ---------- durable terminal-parent intent ----------

// THE DURABILITY ASSERTION. The in-process walk below a completed parent is
// interrupted (the child listing fails, standing in for a crash part-way
// through), so nothing is cancelled and the call reports ErrCannotCancel — but
// the request was recorded first, and the orphan sweep finishes the job.
func TestCancelSubtree_InterruptedTerminalParentWalkIsFinishedBySweep(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	loser := c.groupTaskOf(t, parent, instance.StateRunning)
	c.links.listErr[parent] = errors.New("db down")

	err := c.f.engine.Cancel(c.ctx, parent)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
	assert.Equal(t, instance.StateRunning, c.stateOf(t, loser), "the interrupted walk reached nothing")
	assert.True(t, c.links.isMarked(parent), "the subtree cancellation must be recorded BEFORE the walk")

	delete(c.links.listErr, parent)
	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, loser), "the sweep finishes the recorded request")
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, parent), "the parent's outcome is never reopened")
}

// If the intent cannot be recorded, the call fails closed: a success answer
// must always mean the request is durable.
func TestCancelSubtree_IntentWriteFailureFailsClosed(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)
	c.links.markErr = errors.New("disk full")

	err := c.f.engine.Cancel(c.ctx, parent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recording subtree cancellation")
	assert.Contains(t, err.Error(), "disk full")
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child), "nothing is cascaded without a durable request")
	assert.Equal(t, 0, c.interventions.count())
}

// An ACTIVE parent's own cancelled state is its durable intent; it is never
// marked, and a denied caller marks nothing.
func TestCancelSubtree_OnlyAuthorizedTerminalParentsAreMarked(t *testing.T) {
	c := newCascadeFixture(t)
	active := c.run(t, instance.StateRunning)
	require.NoError(t, c.f.engine.Cancel(c.ctx, active))
	assert.False(t, c.links.isMarked(active))

	denied := c.runInDepartment(t, instance.StateCompleted, "finance")
	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})
	assert.ErrorIs(t, c.f.engine.Cancel(leadCtx, denied), instance.ErrForbidden)
	assert.False(t, c.links.isMarked(denied), "an unauthorized caller records no intent")
}

func TestNoopSpawnLinks_MarkSubtreeCancelRequestedIsANoOp(t *testing.T) {
	assert.NoError(t, engine.NoopSpawnLinks{}.MarkSubtreeCancelRequested(context.Background(), uuid.New()))
}
