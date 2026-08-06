# Phase 3 — Task 2: `ChildSpawner` and `IdentityResolver` ports, satisfied by the engine

**Goal:** Declare the two ports the spawn executor depends on and implement `ChildSpawner` on `*Engine` — `ChildInputs` (the child's declared inputs, resolved through the *same* visibility filter the trigger path uses) and `Spawn` (create + enqueue the linked child).

**Architecture:** New file `domain/engine/engine_spawn.go` holds both methods; `engine.go` keeps only the extracted `sharedVisibilityFilter` helper, which `TriggerWithDepartment` and `ChildInputs` now share so the two can never drift. `IdentityResolver` is redeclared in `domain/engine` rather than imported from `domain/schedule`, keeping that domain edge one-directional — the same practice `instance.Claims` and `instance.Visibility` follow; the composition root passes the **same concrete value** to both, so a scheduled fire and a spawned run can never resolve a user differently.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 2: ports + `Engine.ChildInputs` + `Engine.Spawn`

**Files:**
- Create: `domain/engine/engine_spawn.go`
- Modify: `domain/engine/ports.go` (append the two port declarations)
- Modify: `domain/engine/engine.go:288-307` (extract `sharedVisibilityFilter`)
- Test:   `domain/engine/engine_spawn_test.go`

---

- [x] **Step 1: Write the failing test**

Create `domain/engine/engine_spawn_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compile-time proof that *Engine satisfies the port the executor depends on.
var _ engine.ChildSpawner = (*engine.Engine)(nil)

func spawnerFixture(t *testing.T) *testFixtures {
	t.Helper()
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "child",
		Inputs: []workflow.InputField{
			{Name: "repo", Type: "string", Description: "target repo", Required: true},
			{Name: "dryRun", Type: "boolean", Required: false},
		},
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})
	return f
}

// ---------- ChildInputs ----------

func TestEngineChildInputs_ReturnsDeclaredContract(t *testing.T) {
	f := spawnerFixture(t)

	got, err := f.engine.ChildInputs(context.Background(), "child")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "repo", got[0].Name)
	assert.True(t, got[0].Required)
	assert.Equal(t, "dryRun", got[1].Name)
	assert.False(t, got[1].Required)
}

func TestEngineChildInputs_UnknownWorkflowErrors(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.ChildInputs(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound,
		"a workflow the caller cannot see is indistinguishable from one that does not exist")
}

func TestEngineChildInputs_ZeroDeclaredInputs(t *testing.T) {
	f := spawnerFixture(t)
	f.workflowRepo.add(&workflow.Workflow{
		Name: "no-inputs",
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})

	got, err := f.engine.ChildInputs(context.Background(), "no-inputs")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// The lookup is scoped to the identity on the context, exactly as the trigger
// path is: a private workflow of user A is not visible to user B.
func TestEngineChildInputs_ScopedToContextIdentity(t *testing.T) {
	f := spawnerFixture(t)
	other := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: other, Role: "user", Departments: []string{"default"},
	})

	// The mock workflow repo is scope-agnostic, so this asserts the CALL SHAPE:
	// ChildInputs resolves through the same owner-scoped LoadVisibleFiltered the
	// trigger path uses, with the owner taken from the context identity.
	got, err := f.engine.ChildInputs(ctx, "child")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// ---------- Spawn ----------

func TestEngineSpawn_CreatesLinkedChildUnderRequestedOwner(t *testing.T) {
	f := spawnerFixture(t)
	owner := uuid.New()
	parentID := uuid.New()

	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: owner, Role: "user", Departments: []string{"default"},
	})

	childID, err := f.engine.Spawn(ctx, engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "arbitron/arbitron"},
		Priority:      string(instance.PriorityHigh),
		Link: instance.SpawnLink{
			ParentInstanceID: parentID,
			ParentNodeID:     "spawn-1",
			Mode:             instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, childID)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.Equal(t, owner, got.OwnerID, "the child inherits the identity injected by the caller")
	require.NotNil(t, got.ParentInstanceID)
	assert.Equal(t, parentID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeFireAndForget, got.SpawnMode)
	assert.Equal(t, instance.PriorityHigh, got.Priority,
		"a child of a high-priority run competes on equal footing with its parent's siblings")
}

func TestEngineSpawn_PropagatesMissingRequiredInput(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{}, // "repo" is required
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: uuid.New(), ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo")
}

func TestEngineSpawn_UnknownChildWorkflowErrors(t *testing.T) {
	f := spawnerFixture(t)

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "nope",
		Variables:     map[string]any{},
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: uuid.New(), ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

// An INVALID link is rejected before any instance is created: a child that
// cannot be matched by the wake would strand its parent forever, which is worse
// than a failed spawn.
func TestEngineSpawn_RejectsInvalidLinkBeforeCreating(t *testing.T) {
	f := spawnerFixture(t)
	before := len(f.instanceRepo.all())

	_, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "r"},
		Priority:      string(instance.PriorityNormal),
		Link:          instance.SpawnLink{ParentNodeID: "spawn-1"}, // no parent id, no mode
	})
	require.Error(t, err)
	assert.Equal(t, before, len(f.instanceRepo.all()), "no child may be created for an invalid link")
}

// A grandchild is linked to ITS parent, not to the root.
func TestEngineSpawn_GrandchildLinksToItsOwnParent(t *testing.T) {
	f := spawnerFixture(t)
	root := uuid.New()

	childID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: root, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)

	grandID, err := f.engine.Spawn(context.Background(), engine.SpawnRequest{
		ChildWorkflow: "child", Variables: map[string]any{"repo": "r"},
		Priority: string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: childID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
		},
	})
	require.NoError(t, err)

	grand := f.instanceRepo.get(grandID)
	require.NotNil(t, grand)
	require.NotNil(t, grand.ParentInstanceID)
	assert.Equal(t, childID, *grand.ParentInstanceID)
	assert.NotEqual(t, root, *grand.ParentInstanceID)
}
```

Add the `all()` accessor to the existing mock in `domain/engine/engine_test.go`
(beside the existing `get`, ~line 351):

```go
// all returns every instance the repo holds. Test-only.
func (r *mockInstanceRepo) all() []*instance.Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*instance.Instance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, inst)
	}
	return out
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run 'TestEngineChildInputs|TestEngineSpawn'
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/engine_spawn_test.go:16:23: undefined: engine.ChildSpawner
domain/engine/engine_spawn_test.go:...: f.engine.ChildInputs undefined
domain/engine/engine_spawn_test.go:...: undefined: engine.SpawnRequest
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] **Step 3: Declare the ports**

Append to `domain/engine/ports.go`:

```go
// IdentityResolver turns a user id into that user's full runtime identity
// (role, departments, led departments).
//
// It is a structurally identical twin of schedule.IdentityResolver, REDECLARED
// rather than imported because domain/engine must not depend on
// domain/schedule. This mirrors the codebase's established practice of
// redeclaring a type across a domain edge to keep the dependency
// one-directional (instance.Claims vs identity.Claims; instance.Visibility vs
// workflow.Visibility).
//
// The composition root passes the SAME concrete value to both the schedule
// evaluator and the spawn executor, so a scheduled fire and a spawned run can
// never resolve a user differently.
type IdentityResolver interface {
	IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error)
}

// SpawnRequest is everything ChildSpawner needs to create one linked child.
type SpawnRequest struct {
	// ChildWorkflow is the child workflow's name.
	ChildWorkflow string
	// Variables are the prepared startup values.
	Variables map[string]any
	// Priority is inherited from the parent run: a child of a high-priority run
	// competes on equal footing with its parent's siblings. Nothing requires an
	// independent priority axis.
	Priority string
	// Link is the child's back-reference to the spawning node. It MUST be valid;
	// an incomplete link is rejected before any instance is created, because a
	// child that cannot be matched by the wake would strand its parent forever.
	Link instance.SpawnLink
}

// ChildSpawner creates and enqueues a child workflow instance linked to its
// parent, and answers what a child workflow declares as its inputs.
//
// Both operations are on ONE port so the spawn executor depends on a
// single-operation-shaped interface rather than on the whole Engine — an
// isolation boundary and what makes the executor unit-testable. *Engine
// satisfies it.
type ChildSpawner interface {
	// ChildInputs returns the child workflow's declared input contract, resolved
	// through the SAME owner-scoped visibility filter the trigger path uses. A
	// workflow the caller cannot see returns ErrNotFound — indistinguishable
	// from "does not exist", so existence is never leaked.
	ChildInputs(ctx context.Context, workflowName string) ([]workflow.InputField, error)
	// Spawn creates the child under the identity carried on ctx, with the link
	// already set, and enqueues it. It returns the new child instance's id.
	Spawn(ctx context.Context, req SpawnRequest) (uuid.UUID, error)
}
```

Add the two imports to `domain/engine/ports.go`:

```go
import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)
```

- [x] **Step 4: Extract the shared visibility filter**

In `domain/engine/engine.go`, replace the inline filter construction inside
`TriggerWithDepartment` (lines 288-306, the `var filter workflow.SharedVisibilityFilter`
block through its closing brace) with:

```go
	filter := e.sharedVisibilityFilter(ctx)
```

Then add the extracted helper immediately after `triggerAdminOverrode`
(~line 272):

```go
// sharedVisibilityFilter builds the shared-scope gate for a workflow read.
//
// It is shared by TriggerWithDepartment and ChildInputs so a spawn node's
// child-workflow lookup can NEVER resolve a workflow the same caller could not
// trigger directly. Gating keys on the CONTEXT identity (a REST/MCP caller, or
// the reconstructed owner identity a spawn injects). A system trigger (no
// context identity) skips gating — a scheduler/webhook trigger already passed
// authorization at registration time. A nil filter disables shared-scope gating
// for callers with no publication lookup.
func (e *Engine) sharedVisibilityFilter(ctx context.Context) workflow.SharedVisibilityFilter {
	id, ok := identity.FromContext(ctx)
	if !ok || e.publishers == nil {
		return nil
	}
	pubs := e.publishers
	return func(ctx context.Context, name string) (bool, error) {
		pub, found, err := pubs.PublicationOf(ctx, name)
		if err != nil {
			return false, err
		}
		if !found {
			return true, nil // unpublished shared folder ⇒ open
		}
		return workflow.CanSeePublication(id, pub), nil
	}
}
```

- [x] **Step 5: Implement `ChildInputs` and `Spawn`**

Create `domain/engine/engine_spawn.go`:

```go
package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Compile-time assertion: the engine satisfies the port the spawn executor
// depends on.
var _ ChildSpawner = (*Engine)(nil)

// ChildInputs implements ChildSpawner.
//
// It resolves the child workflow through the IDENTICAL owner-scoped,
// visibility-filtered load the trigger path performs, so a spawn node can never
// reach a workflow the same caller could not trigger directly. If the parent's
// owner cannot see the child, this returns not-found — the visibility filter's
// contract is to never reveal that a department-scoped workflow exists to an
// outsider.
func (e *Engine) ChildInputs(ctx context.Context, workflowName string) ([]workflow.InputField, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner, ok := triggerOwner(ctx, e.users)
	if !ok {
		return nil, fmt.Errorf("child inputs %q: failed to resolve owner", workflowName)
	}
	wf, _, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, e.sharedVisibilityFilter(ctx))
	if err != nil {
		return nil, fmt.Errorf("child inputs %q: %w", workflowName, err)
	}
	return wf.Inputs, nil
}

// Spawn implements ChildSpawner.
//
// It is deliberately THIN: it derives a context carrying the link and delegates
// to the existing trigger path, which resolves the owner from the context
// identity, gates the shared read by that identity's departments, refuses an
// archived workflow, validates the child's declared inputs, stamps the link
// onto the Instance literal, creates the row, and enqueues it. Every one of
// those behaviours is inherited rather than reimplemented — that is why owner
// inheritance and correct authorization need no owner-override refactor of the
// trigger path.
//
// The CALLER is responsible for injecting the parent owner's reconstructed
// identity into ctx before calling; Spawn does not resolve identities.
func (e *Engine) Spawn(ctx context.Context, req SpawnRequest) (uuid.UUID, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !req.Link.Valid() {
		// Reject BEFORE creating anything: a child whose link cannot be matched
		// by the wake would strand its parent forever, which is worse than a
		// failed spawn.
		return uuid.Nil, fmt.Errorf("spawn child workflow %q: incomplete spawn link %+v", req.ChildWorkflow, req.Link)
	}

	ctx = instance.WithSpawnLink(ctx, req.Link)
	childID, _, err := e.TriggerWithDepartment(ctx, req.ChildWorkflow, req.Variables, req.Priority, "")
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn child workflow %q: %w", req.ChildWorkflow, err)
	}
	return childID, nil
}
```

- [x] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run 'TestEngineChildInputs|TestEngineSpawn' -v
```

Expected output:

```
--- PASS: TestEngineChildInputs_ReturnsDeclaredContract (0.00s)
--- PASS: TestEngineChildInputs_UnknownWorkflowErrors (0.00s)
--- PASS: TestEngineChildInputs_ZeroDeclaredInputs (0.00s)
--- PASS: TestEngineChildInputs_ScopedToContextIdentity (0.00s)
--- PASS: TestEngineSpawn_CreatesLinkedChildUnderRequestedOwner (0.00s)
--- PASS: TestEngineSpawn_PropagatesMissingRequiredInput (0.00s)
--- PASS: TestEngineSpawn_UnknownChildWorkflowErrors (0.00s)
--- PASS: TestEngineSpawn_RejectsInvalidLinkBeforeCreating (0.00s)
--- PASS: TestEngineSpawn_GrandchildLinksToItsOwnParent (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.5s
```

- [x] **Step 7: Confirm the extracted filter did not change trigger behaviour**

```bash
go test ./domain/engine/ ./adapter/http/ ./adapter/mcp/
```

Expected: `ok` for all three — in particular
`domain/engine/engine_trigger_scope_test.go` must still pass unchanged.

- [x] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/engine_spawn.go domain/engine/engine_spawn_test.go domain/engine/ports.go domain/engine/engine.go domain/engine/engine_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): add ChildSpawner/IdentityResolver ports and satisfy ChildSpawner on the engine"
```
