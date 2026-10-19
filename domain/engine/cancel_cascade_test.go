package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSpawnLinks implements instance.SpawnLinkRepository over an in-memory tree.
// ListActiveChildren HONOURS the parent id AND the child's current state — a
// fake that returned every child regardless of state would make the
// "already-terminal child is skipped" test unfailable.
//
// unfiltered disables that state check, making ListActiveChildren keep
// returning a link even after the linked instance has gone terminal — the
// shape of a corrupt or hand-edited row, or a race where a node hasn't yet
// recorded its terminal transition. It exists so a cycle test can prove the
// visited-set guard itself halts the traversal, rather than benefiting from
// the coincidence that cancelAuthorized always makes a node terminal (hence
// filtered out) before it could ever be re-listed.
type fakeSpawnLinks struct {
	mu         sync.Mutex
	children   map[uuid.UUID][]uuid.UUID
	states     func(uuid.UUID) instance.State
	listErr    map[uuid.UUID]error
	unfiltered bool
	marked     map[uuid.UUID]bool
	markErr    error
}

func newFakeSpawnLinks(states func(uuid.UUID) instance.State) *fakeSpawnLinks {
	return &fakeSpawnLinks{
		children: map[uuid.UUID][]uuid.UUID{},
		states:   states,
		listErr:  map[uuid.UUID]error{},
	}
}

func (f *fakeSpawnLinks) link(parent, child uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.children[parent] = append(f.children[parent], child)
}

func (f *fakeSpawnLinks) ListActiveChildren(_ context.Context, parentID uuid.UUID) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.listErr[parentID]; err != nil {
		return nil, err
	}
	out := []uuid.UUID{}
	for _, c := range f.children[parentID] {
		if f.unfiltered || !f.states(c).IsTerminal() {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSpawnLinks) ChildCountsByParents(_ context.Context, ids []uuid.UUID, _ instance.ChildVisibility) (map[uuid.UUID]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		if n := len(f.children[id]); n > 0 {
			out[id] = n
		}
	}
	return out, nil
}

// MarkSubtreeCancelRequested matches the production terminal-row predicate.
func (f *fakeSpawnLinks) MarkSubtreeCancelRequested(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return f.markErr
	}
	if !f.states(id).IsTerminal() {
		return nil
	}
	if f.marked == nil {
		f.marked = map[uuid.UUID]bool{}
	}
	f.marked[id] = true
	return nil
}

func (f *fakeSpawnLinks) isMarked(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.marked[id]
}

// ListChildren honours the parent, live state, and per-branch storage failure.
func (f *fakeSpawnLinks) ListChildren(_ context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.listErr[parentID]; err != nil {
		return nil, err
	}
	out := []instance.ChildRef{}
	for _, c := range f.children[parentID] {
		out = append(out, instance.ChildRef{ID: c, State: f.states(c)})
	}
	return out, nil
}

// ListOrphanedActiveChildren mirrors the bounded terminal-ancestor climb.
// Timestamp boundaries are covered by SQLite tests; this fake has no clock.
func (f *fakeSpawnLinks) ListOrphanedActiveChildren(_ context.Context) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parentOf := map[uuid.UUID]uuid.UUID{}
	for parent, kids := range f.children {
		for _, c := range kids {
			if c != parent {
				parentOf[c] = parent
			}
		}
	}
	out := []uuid.UUID{}
	for child := range parentOf {
		if f.states(child).IsTerminal() {
			continue
		}
		anc, ok := parentOf[child]
		for hops := 0; ok && hops < 64; hops++ {
			st := f.states(anc)
			if st == instance.StateCancelled || f.marked[anc] {
				out = append(out, child)
				break
			}
			if st != instance.StateCompleted && st != instance.StateFailed {
				break
			}
			anc, ok = parentOf[anc]
		}
	}
	return out, nil
}

var _ instance.SpawnLinkRepository = (*fakeSpawnLinks)(nil)

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

type cascadeFixture struct {
	f             *testFixtures
	links         *fakeSpawnLinks
	interventions *mockInterventionRepo
	owner         uuid.UUID
	ctx           context.Context
}

func newCascadeFixture(t *testing.T) *cascadeFixture {
	t.Helper()
	f := newTestFixtures()
	owner := uuid.New()
	links := newFakeSpawnLinks(func(id uuid.UUID) instance.State {
		inst, err := f.instanceRepo.Get(context.Background(), id)
		if err != nil {
			return instance.StateFailed
		}
		return inst.State
	})
	f.engine.SetSpawnLinks(links)
	// The test HOLDS the intervention mock so it can assert the audit trail
	// directly. Do NOT add a test-only accessor to *Engine for this: an
	// unexported method name is qualified by its defining package, so an
	// `interface{ count() int }` assertion written in package `engine` can never
	// be satisfied by a mock whose count() lives in package `engine_test`.
	interventions := newMockInterventionRepo()
	f.engine.SetInterventions(interventions)

	// The actor is an admin who does NOT own the runs: an intervention row is
	// only attributed for a non-owner actor (intervention_recorder.go), so an
	// owner-self-cancel fixture would make the audit-trail assertion unfailable.
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleAdmin, Departments: []string{"default"},
	})
	return &cascadeFixture{f: f, links: links, interventions: interventions, owner: owner, ctx: ctx}
}

// run creates an instance in the given state owned by the fixture owner.
func (c *cascadeFixture) run(t *testing.T, state instance.State) uuid.UUID {
	t.Helper()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: c.owner, WorkflowName: "wf",
		State: state, Priority: instance.PriorityNormal,
	}
	require.NoError(t, c.f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

// runInDepartment creates an instance owned by someone OTHER than the fixture
// owner, department-scoped, so a department-lead actor's canIntervene depends
// on department membership rather than ownership or admin.
func (c *cascadeFixture) runInDepartment(t *testing.T, state instance.State, dept string) uuid.UUID {
	t.Helper()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "wf",
		State: state, Priority: instance.PriorityNormal,
		Visibility: instance.VisibilityDepartment, Department: dept,
	}
	require.NoError(t, c.f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

func (c *cascadeFixture) stateOf(t *testing.T, id uuid.UUID) instance.State {
	t.Helper()
	got, err := c.f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return got.State
}

// ---------- happy path ----------

func TestCancelCascade_CancelsAnAwaitedChild(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateWaiting)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, child))
}

// THE SINGLE MOST IMPORTANT ASSERTION IN THIS PHASE — the one a mode-filtered
// cascade would fail. The spec states the requirement without qualification:
// "Cancelling a parent cancels active children recursively."
func TestCancelCascade_CancelsAFireAndForgetChild(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateQueued)
	c.links.link(parent, child)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, child),
		"fire-and-forget children are cancelled too: the spec's carve-outs are all "+
			"child->parent, never parent->child")
}

// ---------- success ----------

func TestCancelCascade_ReachesGrandchildrenAndDeeper(t *testing.T) {
	c := newCascadeFixture(t)
	root := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	grand := c.run(t, instance.StateQueued)
	great := c.run(t, instance.StateWaiting)
	c.links.link(root, child)
	c.links.link(child, grand)
	c.links.link(grand, great)

	require.NoError(t, c.f.engine.Cancel(c.ctx, root))

	for _, id := range []uuid.UUID{root, child, grand, great} {
		assert.Equal(t, instance.StateCancelled, c.stateOf(t, id))
	}
}

// ORDERING IS WHAT PREVENTS RESURRECTION: the parent must transition BEFORE any
// child does, so a child's terminal wake finds a non-waiting parent.
func TestCancelCascade_ParentTransitionsBeforeAnyChild(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateWaiting)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	var order []uuid.UUID
	var mu sync.Mutex
	c.f.instanceRepo.onUpdateState = func(id uuid.UUID, st instance.State) {
		if st == instance.StateCancelled {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
		}
	}

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	require.Len(t, order, 2)
	assert.Equal(t, parent, order[0],
		"the parent must be cancelled FIRST; reversing the order lets a dying child "+
			"flip the parent from cancelled back to queued")
	assert.Equal(t, child, order[1])
}

func TestCancelCascade_RecordsAnInterventionForEveryCancelledRun(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateRunning)
	grand := c.run(t, instance.StateRunning)
	c.links.link(parent, child)
	c.links.link(child, grand)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, 3, c.interventions.count(),
		"the cascade routes through the AUTHORIZED cancellation path, so every "+
			"cancelled run produces an audit record")
}

// A cancelled parent's awaited child reaching a terminal state afterwards must
// NOT re-queue the cancelled parent. This is the resurrection guard, asserted
// end-to-end across Phase 4's wake and this phase's cascade.
func TestCancelCascade_CancelledParentIsNotResurrectedByItsDyingChild(t *testing.T) {
	c := newCascadeFixture(t)
	node := "spawn-1"
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: c.owner, WorkflowName: "parent",
		State: instance.StateWaiting, Priority: instance.PriorityNormal, CurrentNodeID: &node,
	}
	require.NoError(t, c.f.instanceRepo.Create(context.Background(), parent))

	pid := parent.ID
	cn := node
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: c.owner, WorkflowName: "child",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &cn, SpawnMode: instance.SpawnModeWaitForChild,
	}
	require.NoError(t, c.f.instanceRepo.Create(context.Background(), child))
	c.links.link(parent.ID, child.ID)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent.ID))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent.ID))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, child.ID))

	// The child's terminal wake fires afterwards; the parent must stay cancelled.
	c.f.engine.OnChildTerminal(context.Background(), child.ID)
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent.ID),
		"a cancelled parent must never be resurrected by the child being torn down")
}

// ---------- failure ----------

func TestCancelCascade_AlreadyTerminalChildIsSkippedNotFailed(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	done := c.run(t, instance.StateCompleted)
	alive := c.run(t, instance.StateRunning)
	c.links.link(parent, done)
	c.links.link(parent, alive)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent),
		"an already-terminal child is the common case and must not fail the cascade")
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, done))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, alive))
}

// An already-terminal child still descends into its OWN active grandchildren.
// The "still descend" branch in cascadeCancel exists precisely because a
// fire-and-forget child can go terminal (e.g. complete) while its own
// fire-and-forget grandchild is still running — replacing that recursive call
// with a bare `continue` (matching every other failure branch) would silently
// orphan the grandchild. `unfiltered` simulates the race/corrupt-row shape
// where ListActiveChildren still reports the already-terminal child as active
// (see the visited-guard tests above for the identical technique); without it
// the fake's own terminal filter would drop `child` before the loop ever
// reached cancelAuthorized on it, making this branch untestable through the
// fake at all.
func TestCancelCascade_AlreadyTerminalChildStillDescendsIntoActiveGrandchild(t *testing.T) {
	c := newCascadeFixture(t)
	c.links.unfiltered = true
	parent := c.run(t, instance.StateRunning)
	child := c.run(t, instance.StateCompleted)
	grandchild := c.run(t, instance.StateRunning)
	c.links.link(parent, child)
	c.links.link(child, grandchild)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCompleted, c.stateOf(t, child),
		"an already-terminal child is left exactly as it was")
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, grandchild),
		"the terminal child's own active grandchild must still be reached")

	grandRecords, err := c.interventions.ListByInstance(context.Background(), grandchild)
	require.NoError(t, err)
	assert.Len(t, grandRecords, 1,
		"the grandchild must get its own intervention record, not be silently orphaned")
}

func TestCancelCascade_TerminalParentStillCascadesToActiveChild(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	assert.Equal(t, instance.StateCompleted, c.stateOf(t, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, child))
}

// ---------- error ----------

// A storage failure listing one branch leaves the PARENT cancelled and the
// operation reported as successful: the parent's state is the caller's
// contract; a descendant's failure to comply is an operational event.
func TestCancelCascade_StorageFailureStillReportsSuccess(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	c.links.listErr[parent] = errors.New("db down")

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
}

// A ListActiveChildren storage failure is scoped to the ONE branch it occurs
// on: it must not truncate a SIBLING branch's cancellation or its audit
// trail. branchA's own descendants are unreachable (branchA's OWN
// ListActiveChildren call fails), but branchA itself is still cancelled
// (cancelAuthorized on branchA runs BEFORE the cascade recurses into it and
// hits the failing list call) and branchB's entire subtree — sibling to the
// failure, sharing nothing but the same parent — is cancelled and recorded
// exactly as if branchA had never failed.
func TestCancelCascade_StorageFailureOnOneBranchDoesNotTruncateSiblingBranch(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	branchA := c.run(t, instance.StateRunning)
	branchB := c.run(t, instance.StateRunning)
	grandchildB := c.run(t, instance.StateRunning)
	c.links.link(parent, branchA)
	c.links.link(parent, branchB)
	c.links.link(branchB, grandchildB)
	c.links.listErr[branchA] = errors.New("db down listing branchA's children")

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, branchA),
		"branchA's OWN cancellation happens before the failing list call, so it is still cancelled")
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, branchB),
		"a storage error on branchA's branch must not stop branchB, an unrelated sibling, from being cancelled")
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, grandchildB),
		"branchB's own descendants are unaffected by branchA's storage error")
	assert.Equal(t, 4, c.interventions.count(),
		"parent, branchA, branchB, and grandchildB must each get an audit record — "+
			"branchA's storage error must not silently drop branchB's subtree from the audit trail")
}

// A department-lead actor (non-owner, non-admin) is authorized on the parent
// by department but NOT on a child scoped to a DIFFERENT department — children
// are NOT department-inherited from their parent (only ownership is; Department
// is derived per-child from that child's own workflow publication). The cascade
// must not silently reach past the node it could not authorize: cancelling a
// grandchild whose OWN department the actor happens to lead, while the
// intervening child stays running unauthorized-and-untouched, would leave a
// worse-than-either-outcome hole in the tree — a live node sandwiched between
// two cancelled ones with no operator-visible error (only a Warn log).
func TestCancelCascade_AuthorizationFailureOnAChildHaltsThatBranch(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.runInDepartment(t, instance.StateRunning, "eng")
	child := c.runInDepartment(t, instance.StateRunning, "finance")
	grandchild := c.runInDepartment(t, instance.StateRunning, "eng")
	c.links.link(parent, child)
	c.links.link(child, grandchild)

	// Actor leads "eng" (authorizes the parent AND would authorize the
	// grandchild in isolation) but only MEMBERS "finance" (view-only, no
	// intervene) — so cancelAuthorized on the child fails with ErrForbidden,
	// not ErrCannotCancel.
	leadCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleUser,
		Departments:    []string{"eng", "finance"},
		LedDepartments: []string{"eng"},
	})

	require.NoError(t, c.f.engine.Cancel(leadCtx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child),
		"the actor does not lead the child's department, so the child is correctly left uncancelled")
	assert.Equal(t, instance.StateRunning, c.stateOf(t, grandchild),
		"the cascade must not reach past a child it could not authorize, even though the "+
			"actor could authorize the grandchild in isolation")
	assert.Equal(t, 1, c.interventions.count(),
		"only the parent's own cancellation is authorized; a denied child must never produce "+
			"an audit record implying a cancellation that did not happen")
}

// ---------- edge cases ----------

func TestCancelCascade_NoLinkRepositoryWiredIsANoOp(t *testing.T) {
	f := newTestFixtures()
	f.engine.SetInterventions(newMockInterventionRepo())
	owner := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: owner, Role: identity.RoleAdmin,
	})
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "wf",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), inst))

	require.NoError(t, f.engine.Cancel(ctx, inst.ID),
		"cancellation must still work when no spawn-link repository is wired")
}

func TestCancelCascade_RunWithNoChildrenSucceeds(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
}

// A MIXED parent with both awaited and fire-and-forget children cancels all of
// them.
func TestCancelCascade_MixedModeChildrenAllCancelled(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	awaited := c.run(t, instance.StateWaiting)
	fireForget := c.run(t, instance.StateQueued)
	c.links.link(parent, awaited)
	c.links.link(parent, fireForget)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, awaited))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, fireForget))
}

// A corrupt SELF-REFERENTIAL link halts the traversal instead of recursing
// without bound. The link graph is acyclic by construction (a child is always
// created after its parent), so this guard exists to make the failure mode a
// stopped traversal rather than a stack overflow.
func TestCancelCascade_SelfReferentialLinkHaltsTraversal(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateRunning)
	c.links.link(parent, parent)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
}

func TestCancelCascade_MutualLinkCycleHaltsTraversal(t *testing.T) {
	c := newCascadeFixture(t)
	a := c.run(t, instance.StateRunning)
	b := c.run(t, instance.StateRunning)
	c.links.link(a, b)
	c.links.link(b, a)

	require.NoError(t, c.f.engine.Cancel(c.ctx, a))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, a))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, b))
}

// The two tests above pass today, but they pass for the WRONG reason:
// cancelAuthorized transitions each instance to a terminal state before the
// recursive call that would re-list it, and ListActiveChildren filters out
// terminal instances — so the cycle "halts" because the terminal filter
// already dropped the node from the list, not because the visited-set guard's
// own `if visited[childID] { continue }` branch ever fired. These two tests
// set `unfiltered: true` so ListActiveChildren keeps returning an
// already-cancelled node as if it were still active — the shape of a corrupt
// or hand-edited link row — removing that coincidental protection so the
// guard itself has to do the work. The intervention count pins it precisely:
// a second `cancelAuthorized` call on an already-terminal node would still
// return ErrCannotCancel and keep recursing without ever failing an
// assertion, so only the exact count (not just "ends cancelled") proves the
// traversal stopped rather than looped.
func TestCancelCascade_VisitedGuardHaltsSelfReferentialCycleWithoutStateCoincidence(t *testing.T) {
	c := newCascadeFixture(t)
	c.links.unfiltered = true
	parent := c.run(t, instance.StateRunning)
	c.links.link(parent, parent)

	require.NoError(t, c.f.engine.Cancel(c.ctx, parent))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, parent))
	assert.Equal(t, 1, c.interventions.count(),
		"cancelAuthorized must be invoked exactly once; a repeat invocation means "+
			"the visited map, not the terminal-state filter, failed to halt the cycle")
}

func TestCancelCascade_VisitedGuardHaltsMutualCycleWithoutStateCoincidence(t *testing.T) {
	c := newCascadeFixture(t)
	c.links.unfiltered = true
	a := c.run(t, instance.StateRunning)
	b := c.run(t, instance.StateRunning)
	c.links.link(a, b)
	c.links.link(b, a)

	require.NoError(t, c.f.engine.Cancel(c.ctx, a))

	assert.Equal(t, instance.StateCancelled, c.stateOf(t, a))
	assert.Equal(t, instance.StateCancelled, c.stateOf(t, b))
	assert.Equal(t, 2, c.interventions.count(),
		"cancelAuthorized must be invoked exactly once per instance; a repeat "+
			"invocation on either node means the visited map, not the "+
			"terminal-state filter, failed to halt the cycle")
}
