# Phase 5 — Task 1: recursive cancel cascade

**Goal:** Cancelling a run cancels every active child it spawned, recursively, in both spawn modes, with an intervention record per cancelled run and without ever resurrecting the parent.

**Architecture:** `Engine.Cancel` is split: `cancelAuthorized` keeps the existing authorize → transition → deactivate → publish → record-intervention body, and `Cancel` calls it and then `cascadeCancel`. **Ordering is a correctness requirement, not style:** the Phase-4 wake only re-queues a parent that is *still waiting*, so cancelling the parent first means it is already cancelled when its children reach their terminal state — the wake is a no-op and the parent cannot be resurrected through its own `failure` port by the very children being torn down. Spawn mode is **not** a filter: the spec states the requirement without qualification, and a fire-and-forget child left running after its parent is cancelled burns queue capacity on work whose requester is gone and is invisible in the flat list it was cancelled from.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 1: cascade + engine wiring

**Files:**
- Create: `domain/engine/cancel_cascade.go`
- Modify: `domain/engine/engine.go:22-49` (add the `spawnLinks` field), `:1385-1424` (split `Cancel`)
- Modify: `cmd/arbitron/main.go` (wire `SetSpawnLinks`)
- Test:   `domain/engine/cancel_cascade_test.go`

---

- [x] **Step 1: Write the failing test**

Create `domain/engine/cancel_cascade_test.go`:

```go
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
type fakeSpawnLinks struct {
	mu       sync.Mutex
	children map[uuid.UUID][]uuid.UUID
	states   func(uuid.UUID) instance.State
	listErr  map[uuid.UUID]error
	counts   map[uuid.UUID]int
	countErr error
}

func newFakeSpawnLinks(states func(uuid.UUID) instance.State) *fakeSpawnLinks {
	return &fakeSpawnLinks{
		children: map[uuid.UUID][]uuid.UUID{},
		states:   states,
		listErr:  map[uuid.UUID]error{},
		counts:   map[uuid.UUID]int{},
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
		if !f.states(c).IsTerminal() {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSpawnLinks) ChildCountsByParents(_ context.Context, ids []uuid.UUID, _ instance.ChildVisibility) (map[uuid.UUID]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.countErr != nil {
		return nil, f.countErr
	}
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		if n := len(f.children[id]); n > 0 {
			out[id] = n
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

	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: owner, Role: identity.RoleAdmin, Departments: []string{"default"},
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

func TestCancelCascade_ParentInAnUncancellableStateReturnsErrCannotCancel(t *testing.T) {
	c := newCascadeFixture(t)
	parent := c.run(t, instance.StateCompleted)
	child := c.run(t, instance.StateRunning)
	c.links.link(parent, child)

	err := c.f.engine.Cancel(c.ctx, parent)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrCannotCancel)
	assert.Equal(t, instance.StateRunning, c.stateOf(t, child),
		"a refused parent cancellation cascades to nothing")
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
```

Add the intervention mock and the test-only counter. In
`domain/engine/engine_test.go`, add:

```go
// mockInterventionRepo implements instance.InterventionRepository for tests.
type mockInterventionRepo struct {
	mu      sync.Mutex
	records []instance.Intervention
}

func newMockInterventionRepo() *mockInterventionRepo {
	return &mockInterventionRepo{}
}

func (r *mockInterventionRepo) Create(_ context.Context, iv *instance.Intervention) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, *iv)
	return nil
}

func (r *mockInterventionRepo) ListByInstance(_ context.Context, id uuid.UUID) ([]instance.Intervention, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []instance.Intervention
	for _, iv := range r.records {
		if iv.InstanceID == id {
			out = append(out, iv)
		}
	}
	return out, nil
}

func (r *mockInterventionRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}
```

> `instance.InterventionRepository` declares exactly `Create` and
> `ListByInstance` (`domain/instance/intervention.go:36-39`), so the mock above
> is complete. Confirm with
> `go doc github.com/arbitron/arbitron/domain/instance.InterventionRepository`
> before writing it, and add any method that has since been added, returning
> zero values.

No test-only accessor is added to `*Engine`. The fixture holds the mock and
asserts on it directly, which keeps the production type clean and — more
importantly — actually works: an unexported method name is qualified by the
package that declares it, so an `interface{ count() int }` assertion written
inside `package engine` can never match a `count()` defined in
`package engine_test`.

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestCancelCascade
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/cancel_cascade_test.go:...: f.engine.SetSpawnLinks undefined
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] **Step 3: Add the engine field**

In `domain/engine/engine.go`, add to the `Engine` struct after `enricher`:

```go
	enricher         *InstanceEnricher        // optional; when set, ListEnriched uses it
	// spawnLinks is the OPTIONAL link-addressed read surface. When unset (nil),
	// the cancel cascade and the enricher's child counts are no-ops — cancelling
	// still works exactly as before this feature.
	spawnLinks       instance.SpawnLinkRepository
```

- [x] **Step 4: Split `Cancel` and add the cascade**

In `domain/engine/engine.go`, rename the existing `Cancel` to `cancelAuthorized`
(changing only the signature line and its doc comment; the body is unchanged
except that the `OnChildTerminal` call added in Phase 4 Task 4 stays at the end):

```go
// cancelAuthorized authorizes and performs the cancellation of ONE instance:
// the state transition, wait-resource deactivation, the cancelled event, and
// the intervention audit record. It performs NO cascade — Cancel owns that, so
// the cascade can reuse this authorized path for every descendant and produce
// an audit record for each one.
func (e *Engine) cancelAuthorized(ctx context.Context, instanceID uuid.UUID) error {
	// ... existing Cancel body, unchanged ...
}
```

Then add the new public `Cancel` directly above it:

```go
// Cancel sets a running, queued, paused, or waiting instance to cancelled and
// cancels its context/timer as appropriate, then cancels every active child it
// spawned, recursively.
//
// ORDER IS A CORRECTNESS REQUIREMENT, NOT A STYLE PREFERENCE. The child-terminal
// wake only re-queues a parent that is STILL in the waiting state. Cancelling
// the parent FIRST means it is already cancelled by the time its children reach
// their terminal state, so the wake is a no-op and the parent cannot be
// resurrected through its own failure port by the very children being torn
// down. Reversing the order produces a parent that flips from cancelled back to
// queued.
func (e *Engine) Cancel(ctx context.Context, instanceID uuid.UUID) error {
	if err := e.cancelAuthorized(ctx, instanceID); err != nil {
		return err
	}
	e.cascadeCancel(ctx, instanceID, map[uuid.UUID]bool{instanceID: true})
	return nil
}
```

Create `domain/engine/cancel_cascade.go`:

```go
package engine

import (
	"context"
	"errors"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// SetSpawnLinks wires the link-addressed read surface used by the cancel
// cascade and the list enricher's child counts. It is OPTIONAL: when unset,
// cancellation behaves exactly as it did before this feature and rows report no
// children.
func (e *Engine) SetSpawnLinks(repo instance.SpawnLinkRepository) {
	e.spawnLinks = repo
}

// cascadeCancel cancels every active child of parentID, depth-first.
//
// SCOPE: EVERY active child, BOTH spawn modes. The spec states the requirement
// without qualification. The tempting reading — that fire-and-forget children
// should survive because the parent disclaimed the child's lifecycle — confuses
// two directions: the spec's fire-and-forget carve-outs are all child->parent
// ("fire-and-forget child outcomes do not affect the parent"), and nothing
// weakens the parent->child direction. The outcome the cascade serves —
// operators tearing down a run tree without hunting for orphans — is defeated
// precisely by the children nobody is waiting on, since those are the ones no
// later failure will surface.
//
// AUTHORIZATION needs no special-casing: children inherit the parent's owner, so
// an identity authorized to intervene on the parent is authorized on every
// descendant. Routing through cancelAuthorized (rather than writing states
// directly) preserves the intervention audit trail for each cancelled run.
//
// PARTIAL FAILURE IS TOLERATED, NOT FATAL. The parent's own cancellation has
// already succeeded and must not be reported as failed because a descendant was
// uncooperative. A child already in a terminal state is the COMMON case and
// returns ErrCannotCancel, which the cascade treats as success.
//
// The `visited` set makes corrupt or hand-edited link data degrade into a
// halted traversal rather than unbounded recursion. The link graph is acyclic
// by construction — a child is always created after its parent — so this guard
// is unreachable in practice.
func (e *Engine) cascadeCancel(ctx context.Context, parentID uuid.UUID, visited map[uuid.UUID]bool) {
	if e.spawnLinks == nil {
		return
	}
	children, err := e.spawnLinks.ListActiveChildren(ctx, parentID)
	if err != nil {
		// Logged, not fatal: the parent's state is the caller's contract. Any child
		// left running remains visible in the hierarchy, so the inconsistency is
		// observable rather than hidden.
		slog.Error("cancel cascade: failed to list active children",
			"parentID", parentID, "error", err)
		return
	}

	for _, childID := range children {
		if visited[childID] {
			continue
		}
		visited[childID] = true

		if cerr := e.cancelAuthorized(ctx, childID); cerr != nil {
			if !errors.Is(cerr, instance.ErrCannotCancel) {
				slog.Warn("cancel cascade: failed to cancel child",
					"parentID", parentID, "childID", childID, "error", cerr)
			}
			// Continue regardless: an already-terminal child may still have active
			// grandchildren of its own.
		}
		e.cascadeCancel(ctx, childID, visited)
	}
}
```

- [x] **Step 5: Wire it at the composition root**

In `cmd/arbitron/main.go`, immediately after `eng.SetInterventions(interventionRepo)`
(~line 828), add:

```go
	// 12a-bis-3. Wire the link-addressed read surface so cancelling a run tears
	// down its spawned children recursively, and so the list enricher can resolve
	// a page's child counts in one batched call.
	eng.SetSpawnLinks(instanceRepo)
```

- [x] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/ cmd/arbitron/
go build ./... && go test ./domain/engine/ -run TestCancelCascade -v
```

Expected output:

```
--- PASS: TestCancelCascade_CancelsAnAwaitedChild (0.00s)
--- PASS: TestCancelCascade_CancelsAFireAndForgetChild (0.00s)
--- PASS: TestCancelCascade_ReachesGrandchildrenAndDeeper (0.00s)
--- PASS: TestCancelCascade_ParentTransitionsBeforeAnyChild (0.00s)
--- PASS: TestCancelCascade_RecordsAnInterventionForEveryCancelledRun (0.00s)
--- PASS: TestCancelCascade_CancelledParentIsNotResurrectedByItsDyingChild (0.00s)
--- PASS: TestCancelCascade_AlreadyTerminalChildIsSkippedNotFailed (0.00s)
--- PASS: TestCancelCascade_ParentInAnUncancellableStateReturnsErrCannotCancel (0.00s)
--- PASS: TestCancelCascade_StorageFailureStillReportsSuccess (0.00s)
--- PASS: TestCancelCascade_NoLinkRepositoryWiredIsANoOp (0.00s)
--- PASS: TestCancelCascade_RunWithNoChildrenSucceeds (0.00s)
--- PASS: TestCancelCascade_MixedModeChildrenAllCancelled (0.00s)
--- PASS: TestCancelCascade_SelfReferentialLinkHaltsTraversal (0.00s)
--- PASS: TestCancelCascade_MutualLinkCycleHaltsTraversal (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.7s
```

- [x] **Step 7: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines. In particular every existing
`Cancel` test (`domain/engine`, `adapter/http`, `adapter/mcp`) must still pass —
the split changed no observable behaviour for a childless run.

- [x] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/cancel_cascade.go domain/engine/cancel_cascade_test.go domain/engine/engine.go domain/engine/engine_test.go cmd/arbitron/main.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): cancel spawned children recursively when a run is cancelled"
```
