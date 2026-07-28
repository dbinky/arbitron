package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedIdentityResolver reconstructs every user as a plain member of "default".
// Ownership alone authorizes the cancellation, which is the real production
// condition: children inherit their parent's owner.
type fixedIdentityResolver struct {
	err error
}

func (r fixedIdentityResolver) IdentityForUser(_ context.Context, userID uuid.UUID) (identity.Identity, error) {
	if r.err != nil {
		return identity.Identity{}, r.err
	}
	return identity.Identity{UserID: userID, Role: identity.RoleUser, Departments: []string{"default"}}, nil
}

// THE CORE ASSERTION. The in-process cascade is best-effort: a storage error on
// one branch, or a process death mid-walk, leaves an active run under a
// cancelled parent with nobody watching. Without this sweep that state is
// permanent — and invisible, because the operator was already told the tree was
// gone.
func TestOrphanReconciler_CancelsAChildStrandedUnderACancelledParent(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	// The cascade fails on THIS parent's branch, so the child survives the
	// operator's cancel exactly as it would after a mid-walk crash.
	c.links.listErr[parent] = errors.New("storage unavailable")
	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	require.Equal(t, instance.StateRunning, c.stateOf(t, child),
		"precondition: the cascade must have left this child stranded")

	delete(c.links.listErr, parent)
	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, child))
}

// The sweep goes through the authorized Cancel, which cascades, so a stranded
// SUBTREE is torn down whole rather than one level per tick.
func TestOrphanReconciler_CascadesIntoTheStrandedSubtree(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	grandchild := c.run(t, instance.StateQueued)
	c.links.link(parent, child)
	c.links.link(child, grandchild)

	c.links.listErr[parent] = errors.New("storage unavailable")
	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	require.Equal(t, instance.StateRunning, c.stateOf(t, child))

	delete(c.links.listErr, parent)
	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, child))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, grandchild))
}

// A healthy tree has no orphans: an active child of an ACTIVE parent must never
// be swept. This is the assertion that stops the sweep from becoming a
// run-killer.
func TestOrphanReconciler_LeavesChildrenOfActiveParentsAlone(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateWaiting)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo, fixedIdentityResolver{}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()))

	assert.Equal(t, instance.StateWaiting, c.stateOf(t, parent))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child))
}

// A resolution failure must SKIP the orphan, never fall back to a system
// identity: falling back would widen what the cancellation may reach.
func TestOrphanReconciler_SkipsWhenOwnerIdentityDoesNotResolve(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	c.links.listErr[parent] = errors.New("storage unavailable")
	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	delete(c.links.listErr, parent)

	r := engine.NewOrphanedChildReconciler(c.links, c.f.instanceRepo,
		fixedIdentityResolver{err: errors.New("user directory unavailable")}, c.f.engine)
	require.NoError(t, r.Reconcile(context.Background()), "a skipped orphan is not a sweep failure")

	assert.Equal(t, instance.StateRunning, c.stateOf(t, child))
}

// A nil link repository is a programming error that would silently downgrade
// descendant cancellation to a no-op, so both wiring sites reject it outright.
func TestSetSpawnLinks_PanicsOnNil(t *testing.T) {
	f := newTestFixtures()
	assert.Panics(t, func() { f.engine.SetSpawnLinks(nil) })

	e := engine.NewInstanceEnricher(nil, nil, nil, engine.NoopQueuePositionProvider{}, "")
	assert.Panics(t, func() { e.SetSpawnLinks(nil) })
}

// The opt-out is spelled explicitly and behaves as documented: no children
// reported, so Cancel touches only the named run.
func TestNoopSpawnLinks_DisablesDescendantCancellation(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	c.f.engine.SetSpawnLinks(engine.NoopSpawnLinks{})
	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child),
		"NoopSpawnLinks disables descendant cancellation, exactly as its doc comment states")
}
