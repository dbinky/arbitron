# Phase 3 — Task 3: `SpawnWorkflowExecutor` (fire-and-forget)

**Goal:** Execute one `spawn_workflow` node in fire-and-forget mode — re-check the mode, reconstruct the parent owner's identity, load the child's declared inputs, read optional guidance, prepare startup values, spawn the linked child, and return a normal node result carrying the child id.

**Architecture:** New file `domain/engine/executor_spawn.go`. Every collaborator is a port (`ChildInputPreparer`, `ChildSpawner`, `IdentityResolver`, `instance.InstanceRepository`, `FileReader`), so the executor is unit-testable with fakes and touches no LLM, no queue, and no engine. The mode is re-checked at execution time because instances run from a `WorkflowSnapshot` that may predate Phase 1's validation — the same defensive stance `kind_guard.go` documents for binding kinds. Wait-for-child fails LOUDLY here: silently treating it as fire-and-forget would leave the parent's `success`/`failure` ports unreachable and the parent would proceed as though a child had succeeded, which is a wrong answer rather than a visible gap.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 3: the spawn executor, fire-and-forget path

**Files:**
- Create: `domain/engine/executor_spawn.go`
- Test:   `domain/engine/executor_spawn_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/executor_spawn_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes. Each one HONOURS the argument that decides its behaviour: a fake that
// ignored the user id, the workflow name, or the spawn link would make these
// tests unfailable.
// ---------------------------------------------------------------------------

// fakeIdentityResolver resolves per user id and records what it was asked for.
type fakeIdentityResolver struct {
	byUser map[uuid.UUID]identity.Identity
	err    error
	asked  []uuid.UUID
}

func (r *fakeIdentityResolver) IdentityForUser(_ context.Context, userID uuid.UUID) (identity.Identity, error) {
	r.asked = append(r.asked, userID)
	if r.err != nil {
		return identity.Identity{}, r.err
	}
	id, ok := r.byUser[userID]
	if !ok {
		return identity.Identity{}, errors.New("no such user: " + userID.String())
	}
	return id, nil
}

var _ engine.IdentityResolver = (*fakeIdentityResolver)(nil)

// fakeChildSpawner records every call and honours the workflow name it is given.
type fakeChildSpawner struct {
	inputsByWorkflow map[string][]workflow.InputField
	inputsErr        error
	spawnErr         error
	spawned          []engine.SpawnRequest
	spawnedIdentity  []identity.Identity
	nextID           uuid.UUID
}

func (s *fakeChildSpawner) ChildInputs(_ context.Context, workflowName string) ([]workflow.InputField, error) {
	if s.inputsErr != nil {
		return nil, s.inputsErr
	}
	f, ok := s.inputsByWorkflow[workflowName]
	if !ok {
		return nil, errors.New("workflow not found: " + workflowName)
	}
	return f, nil
}

func (s *fakeChildSpawner) Spawn(ctx context.Context, req engine.SpawnRequest) (uuid.UUID, error) {
	if s.spawnErr != nil {
		return uuid.Nil, s.spawnErr
	}
	s.spawned = append(s.spawned, req)
	id, _ := identity.FromContext(ctx)
	s.spawnedIdentity = append(s.spawnedIdentity, id)
	if s.nextID == uuid.Nil {
		s.nextID = uuid.New()
	}
	return s.nextID, nil
}

var _ engine.ChildSpawner = (*fakeChildSpawner)(nil)

// fakePreparer records the request and returns canned values. It HONOURS the
// declared-inputs argument: with no declared inputs it returns an empty map.
type fakePreparer struct {
	values map[string]any
	err    error
	gotReq engine.ChildInputRequest
	calls  int
}

func (p *fakePreparer) Prepare(_ context.Context, req engine.ChildInputRequest) (map[string]any, error) {
	p.calls++
	p.gotReq = req
	if p.err != nil {
		return nil, p.err
	}
	if len(req.Inputs) == 0 {
		return map[string]any{}, nil
	}
	return p.values, nil
}

var _ engine.ChildInputPreparer = (*fakePreparer)(nil)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type spawnExecFixture struct {
	instances  *mockInstanceRepo
	identities *fakeIdentityResolver
	spawner    *fakeChildSpawner
	preparer   *fakePreparer
	fileReader *mockFileReader
	exec       *engine.SpawnWorkflowExecutor
	parentID   uuid.UUID
	ownerID    uuid.UUID
}

func newSpawnExecFixture(t *testing.T) *spawnExecFixture {
	t.Helper()
	owner := uuid.New()
	parent := &instance.Instance{
		ID:           uuid.New(),
		OwnerID:      owner,
		WorkflowName: "parent",
		State:        instance.StateRunning,
		Priority:     instance.PriorityHigh,
	}
	instances := newMockInstanceRepo()
	require.NoError(t, instances.Create(context.Background(), parent))

	ids := &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
		owner: {UserID: owner, Role: "user", Departments: []string{"engineering"}},
	}}
	spawner := &fakeChildSpawner{inputsByWorkflow: map[string][]workflow.InputField{
		"child": {
			{Name: "repo", Type: "string", Required: true},
			{Name: "dryRun", Type: "boolean", Required: false},
		},
		"no-inputs": {},
	}}
	prep := &fakePreparer{values: map[string]any{"repo": "arbitron/arbitron"}}
	fr := newMockFileReader()

	return &spawnExecFixture{
		instances:  instances,
		identities: ids,
		spawner:    spawner,
		preparer:   prep,
		fileReader: fr,
		exec:       engine.NewSpawnWorkflowExecutor(instances, prep, spawner, ids, fr),
		parentID:   parent.ID,
		ownerID:    owner,
	}
}

func ffNode(child string) workflow.Node {
	return workflow.Node{
		ID:   "spawn-1",
		Type: workflow.NodeTypeSpawnWorkflow,
		Config: workflow.NodeConfig{
			ChildWorkflow: child,
			SpawnMode:     workflow.SpawnModeFireAndForget,
			ModelProfile:  "chat-default",
		},
	}
}

func (f *spawnExecFixture) run(t *testing.T, node workflow.Node) (*engine.NodeResult, error) {
	t.Helper()
	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	return f.exec.Execute(ctx, node, variable.NewStore(), "parent")
}

// ---------------------------------------------------------------------------
// happy path
// ---------------------------------------------------------------------------

func TestSpawnExecutor_FireAndForget_HappyPath(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsWait, "fire-and-forget must return a NON-wait result so the engine advances")
	assert.Empty(t, res.OutputPort, "fire-and-forget routes along the single unnamed edge")

	require.Len(t, f.spawner.spawned, 1)
	req := f.spawner.spawned[0]
	assert.Equal(t, "child", req.ChildWorkflow)
	assert.Equal(t, map[string]any{"repo": "arbitron/arbitron"}, req.Variables)
	assert.Equal(t, f.parentID, req.Link.ParentInstanceID)
	assert.Equal(t, "spawn-1", req.Link.ParentNodeID)
	assert.Equal(t, instance.SpawnModeFireAndForget, req.Link.Mode)

	out, ok := res.Output.(map[string]any)
	require.True(t, ok, "the node output must be a map so downstream nodes can reference its fields")
	assert.Equal(t, f.spawner.nextID.String(), out["childInstanceId"])
	assert.Equal(t, "child", out["childWorkflow"])
	assert.Equal(t, workflow.SpawnModeFireAndForget, out["spawnMode"])
}

// ---------------------------------------------------------------------------
// success
// ---------------------------------------------------------------------------

// The child is created under the PARENT INSTANCE's owner, NOT the ambient
// context identity. This is the spec's owner-inheritance requirement and the
// whole reason the resolver exists — so the test runs with a DIFFERENT identity
// in context.
func TestSpawnExecutor_ChildOwnerIsParentOwnerNotAmbientIdentity(t *testing.T) {
	f := newSpawnExecFixture(t)
	intruder := uuid.New()

	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: intruder, Role: "admin"})
	ctx = engine.ContextWithInstanceID(ctx, f.parentID)

	_, err := f.exec.Execute(ctx, ffNode("child"), variable.NewStore(), "parent")
	require.NoError(t, err)

	require.Len(t, f.identities.asked, 1)
	assert.Equal(t, f.ownerID, f.identities.asked[0],
		"the identity must be reconstructed for the PARENT INSTANCE's owner")
	require.Len(t, f.spawner.spawnedIdentity, 1)
	assert.Equal(t, f.ownerID, f.spawner.spawnedIdentity[0].UserID,
		"the spawn must run under the parent owner's identity, not the ambient one")
	assert.NotEqual(t, intruder, f.spawner.spawnedIdentity[0].UserID)
}

// The reconstructed identity carries the owner's DEPARTMENTS so a
// department-scoped child workflow resolves.
func TestSpawnExecutor_ReconstructedIdentityCarriesDepartments(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)

	require.Len(t, f.spawner.spawnedIdentity, 1)
	assert.Equal(t, []string{"engineering"}, f.spawner.spawnedIdentity[0].Departments)
}

func TestSpawnExecutor_ChildInheritsParentPriority(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	require.Len(t, f.spawner.spawned, 1)
	assert.Equal(t, string(instance.PriorityHigh), f.spawner.spawned[0].Priority)
}

func TestSpawnExecutor_PassesChildContractAndParentContextToPreparer(t *testing.T) {
	f := newSpawnExecFixture(t)

	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("topic", "quantum"))
	vars.SetNodeOutput("analyzer", map[string]any{"branch": "release/2.0"})

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	_, err := f.exec.Execute(ctx, ffNode("child"), vars, "parent")
	require.NoError(t, err)

	got := f.preparer.gotReq
	assert.Equal(t, "spawn-1", got.NodeID)
	assert.Equal(t, "child", got.ChildWorkflow)
	assert.Equal(t, "chat-default", got.ProfileAlias)
	require.Len(t, got.Inputs, 2, "the CHILD's declared inputs are the contract")
	assert.Equal(t, "quantum", got.ParentContext["topic"])
	assert.NotNil(t, got.ParentContext["analyzer"],
		"the parent's prior NODE OUTPUTS must reach the preparer, not just its inputs")
}

func TestSpawnExecutor_GuidanceFileIsReadAndSubstituted(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.fileReader.files["parent/spawn-guidance.md"] = []byte("Prefer branch {{topic}} for the child.")

	node := ffNode("child")
	node.Config.TemplateFile = "spawn-guidance.md"

	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("topic", "release/2.0"))

	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	_, err := f.exec.Execute(ctx, node, vars, "parent")
	require.NoError(t, err)

	assert.Equal(t, "Prefer branch release/2.0 for the child.", f.preparer.gotReq.Guidance,
		"guidance prose must be read AND have its {{substitutions}} resolved")
}

func TestSpawnExecutor_NoGuidanceConfiguredIsNotAnError(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	assert.Empty(t, f.preparer.gotReq.Guidance)
}

// ---------------------------------------------------------------------------
// failure
// ---------------------------------------------------------------------------

// A child created from an incomplete payload is the worst failure mode here, so
// this asserts NO child is created.
func TestSpawnExecutor_PreparationFailure_NoChildCreated(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.preparer.err = errors.New("missing required input(s): repo")

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned, "no child may be created when preparation fails")
	assert.Contains(t, err.Error(), "repo")
}

func TestSpawnExecutor_ChildWorkflowInvisibleOrMissing_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.run(t, ffNode("not-visible"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
	assert.Equal(t, 0, f.preparer.calls, "an unresolvable child must fail before any LLM cost")
}

// Wait-for-child must fail LOUDLY in this phase, never fall through to
// fire-and-forget: falling through would leave the parent's success/failure
// ports unreachable and the parent would proceed as though a child had
// succeeded — a wrong answer rather than a visible gap.
func TestSpawnExecutor_WaitForChild_UnsupportedInThisPhase(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("child")
	node.Config.SpawnMode = workflow.SpawnModeWaitForChild

	res, err := f.run(t, node)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "wait_for_child")
	assert.Empty(t, f.spawner.spawned, "it must NOT fall through to fire-and-forget")
}

func TestSpawnExecutor_MissingChildWorkflowConfig_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("")

	res, err := f.run(t, node)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "childWorkflow")
}

// ---------------------------------------------------------------------------
// error
// ---------------------------------------------------------------------------

// Identity resolution failure is NOT downgraded to a system identity: falling
// back would silently change which workflows are visible to the spawn and could
// widen reach beyond the owner.
func TestSpawnExecutor_IdentityResolutionFailure_NoChildNoFallback(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.identities.err = errors.New("user store unavailable")

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned, "no child, and no fallback to a system identity")
}

func TestSpawnExecutor_ParentInstanceLoadFailure_Errors(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.instances.getErr = errors.New("db down")

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnExecutor_SpawnFailure_Surfaced(t *testing.T) {
	f := newSpawnExecFixture(t)
	sentinel := errors.New("enqueue failed")
	f.spawner.spawnErr = sentinel

	res, err := f.run(t, ffNode("child"))
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, sentinel, "an enqueue failure must be surfaced, not swallowed")
}

func TestSpawnExecutor_MissingInstanceIDInContext_Errors(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.exec.Execute(context.Background(), ffNode("child"), variable.NewStore(), "parent")
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "instance ID not found in context")
}

func TestSpawnExecutor_GuidanceFileUnreadable_Errors(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("child")
	node.Config.TemplateFile = "missing-guidance.md"

	res, err := f.run(t, node)
	require.Error(t, err, "a CONFIGURED template that cannot be read is a misconfiguration, "+
		"not an optional extra")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "missing-guidance.md")
	assert.Empty(t, f.spawner.spawned)
}

// ---------------------------------------------------------------------------
// edge cases
// ---------------------------------------------------------------------------

func TestSpawnExecutor_ChildWithZeroDeclaredInputs_SpawnsWithEmptyPayload(t *testing.T) {
	f := newSpawnExecFixture(t)

	res, err := f.run(t, ffNode("no-inputs"))
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, f.spawner.spawned, 1)
	assert.Empty(t, f.spawner.spawned[0].Variables)
}

// The executor does NOT trust that validation ran: a snapshot predating Phase 1
// can carry an unrecognized mode.
func TestSpawnExecutor_UnrecognizedSnapshotMode_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("child")
	node.Config.SpawnMode = "async"

	res, err := f.run(t, node)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "async")
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnExecutor_EmptySnapshotMode_Fails(t *testing.T) {
	f := newSpawnExecFixture(t)
	node := ffNode("child")
	node.Config.SpawnMode = ""

	res, err := f.run(t, node)
	require.Error(t, err, "an empty mode is an authoring error, never an inheritance request")
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
}

// A parent whose owner has NO departments still spawns a private child.
func TestSpawnExecutor_OwnerWithNoDepartments_StillSpawns(t *testing.T) {
	f := newSpawnExecFixture(t)
	f.identities.byUser[f.ownerID] = identity.Identity{UserID: f.ownerID, Role: "user"}

	res, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Len(t, f.spawner.spawned, 1)
}

// A fire-and-forget node reached TWICE (a graph back-edge, or a node-level
// retry) spawns TWICE. This is pinned deliberately rather than discovered
// later: fire-and-forget has no short-circuit, consistent with the documented
// re-execution semantics of loop revisits.
func TestSpawnExecutor_FireAndForgetSpawnsAgainOnReExecution(t *testing.T) {
	f := newSpawnExecFixture(t)

	_, err := f.run(t, ffNode("child"))
	require.NoError(t, err)
	f.spawner.nextID = uuid.New() // a distinct child on the second pass
	_, err = f.run(t, ffNode("child"))
	require.NoError(t, err)

	assert.Len(t, f.spawner.spawned, 2, "fire-and-forget re-execution creates a SECOND child")
}

// A spawn from a run that itself has a parent produces a correctly linked
// grandchild: the link names the IMMEDIATE parent.
func TestSpawnExecutor_GrandchildLinksToImmediateParent(t *testing.T) {
	f := newSpawnExecFixture(t)
	grandparent := uuid.New()

	mid := &instance.Instance{
		ID: uuid.New(), OwnerID: f.ownerID, WorkflowName: "mid",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		ParentInstanceID: &grandparent,
	}
	require.NoError(t, f.instances.Create(context.Background(), mid))

	ctx := engine.ContextWithInstanceID(context.Background(), mid.ID)
	_, err := f.exec.Execute(ctx, ffNode("child"), variable.NewStore(), "mid")
	require.NoError(t, err)

	require.Len(t, f.spawner.spawned, 1)
	assert.Equal(t, mid.ID, f.spawner.spawned[0].Link.ParentInstanceID)
	assert.NotEqual(t, grandparent, f.spawner.spawned[0].Link.ParentInstanceID)
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestSpawnExecutor
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/executor_spawn_test.go:...: undefined: engine.NewSpawnWorkflowExecutor
domain/engine/executor_spawn_test.go:...: undefined: engine.SpawnWorkflowExecutor
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [ ] **Step 3: Minimal implementation**

Create `domain/engine/executor_spawn.go`:

```go
package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// SpawnWorkflowExecutor executes one spawn_workflow node: prepare the child's
// startup values, start the child, and tell the engine how to proceed.
//
// Every collaborator is a PORT, so the executor holds no LLM, no queue, and no
// engine. Phase 3 implements fire-and-forget only; wait-for-child fails loudly
// (see Execute).
type SpawnWorkflowExecutor struct {
	instances  instance.InstanceRepository
	preparer   ChildInputPreparer
	spawner    ChildSpawner
	identities IdentityResolver
	fileReader FileReader
}

// NewSpawnWorkflowExecutor constructs the executor.
func NewSpawnWorkflowExecutor(
	instances instance.InstanceRepository,
	preparer ChildInputPreparer,
	spawner ChildSpawner,
	identities IdentityResolver,
	fileReader FileReader,
) *SpawnWorkflowExecutor {
	return &SpawnWorkflowExecutor{
		instances:  instances,
		preparer:   preparer,
		spawner:    spawner,
		identities: identities,
		fileReader: fileReader,
	}
}

var _ NodeExecutor = (*SpawnWorkflowExecutor)(nil)

// Execute runs one spawn node.
//
// Every failure below returns a plain error; the engine then applies the node's
// resolved ErrorBehavior (retry / default output / error-node routing). The
// executor encodes no control-flow policy of its own.
func (e *SpawnWorkflowExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	// 1. The run this node belongs to.
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("spawn_workflow %q: instance ID not found in context", node.ID)
	}

	// 2. Re-check the configured contract. Instances execute from a
	// WorkflowSnapshot that may PREDATE Phase 1's validation, so the executor
	// does not trust that validation ran — the same defensive stance the binding
	// kind guard documents. An unrecognized (or empty) mode is an error, never a
	// default.
	mode := node.Config.SpawnMode
	if !workflow.IsValidSpawnMode(mode) {
		return nil, fmt.Errorf("spawn_workflow %q: unrecognized spawnMode %q (must be %q or %q)",
			node.ID, mode, workflow.SpawnModeFireAndForget, workflow.SpawnModeWaitForChild)
	}
	if node.Config.ChildWorkflow == "" {
		return nil, fmt.Errorf("spawn_workflow %q: childWorkflow is not configured", node.ID)
	}
	if mode == workflow.SpawnModeWaitForChild {
		// FAIL LOUDLY. Silently treating this as fire-and-forget would leave the
		// parent's success/failure ports unreachable and the parent would proceed
		// as though a child had succeeded — a wrong answer rather than a visible
		// gap.
		return nil, fmt.Errorf("spawn_workflow %q: spawnMode %q is not yet supported",
			node.ID, workflow.SpawnModeWaitForChild)
	}

	// 3. Load the parent run for its owner and priority.
	parent, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: load parent instance: %w", node.ID, err)
	}

	// 4. Reconstruct the PARENT OWNER's identity and derive the child-spawn
	// context. This is what makes the child inherit the parent's owner rather
	// than the ambient caller. A resolution failure is NOT downgraded to a system
	// identity: falling back would silently change which workflows are visible to
	// the spawn and could widen reach beyond the owner.
	ownerIdentity, err := e.identities.IdentityForUser(ctx, parent.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: resolve owner identity: %w", node.ID, err)
	}
	childCtx := identity.WithIdentity(ctx, ownerIdentity)

	// 5. Load the child's declared inputs THROUGH that identity. If the parent's
	// owner cannot see the child workflow, this returns not-found and the node
	// fails — fail closed, with no existence leak.
	inputs, err := e.spawner.ChildInputs(childCtx, node.Config.ChildWorkflow)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: load child workflow %q: %w",
			node.ID, node.Config.ChildWorkflow, err)
	}

	// 6. Optional guidance prose. An UNCONFIGURED file is not an error; a
	// configured file that cannot be read is a misconfiguration.
	guidance := ""
	if node.Config.TemplateFile != "" {
		b, rerr := e.fileReader.ReadFile(ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
		if rerr != nil {
			return nil, fmt.Errorf("spawn_workflow %q: read guidance %q: %w",
				node.ID, node.Config.TemplateFile, rerr)
		}
		guidance = variable.Resolve(string(b), vars)
	}

	// 7. Prepare the startup values. A contract failure here is the spec's
	// "required startup values cannot be produced" condition and fails the node
	// BEFORE any child exists.
	values, err := e.preparer.Prepare(childCtx, ChildInputRequest{
		NodeID:        node.ID,
		ChildWorkflow: node.Config.ChildWorkflow,
		Inputs:        inputs,
		ParentContext: vars.Snapshot(),
		Guidance:      guidance,
		ProfileAlias:  node.Config.ModelProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: %w", node.ID, err)
	}

	// 8. Create and enqueue the child under the parent's owner, with the link set.
	//
	// If creation partially succeeded (the child exists queued with its link but
	// the enqueue errored), a retry of this node would create a SECOND child.
	// That is accepted for fire-and-forget, where child outcomes do not affect
	// the parent — recorded here so the behaviour is a decision, not a surprise.
	childID, err := e.spawner.Spawn(childCtx, SpawnRequest{
		ChildWorkflow: node.Config.ChildWorkflow,
		Variables:     values,
		Priority:      string(parent.Priority),
		Link: instance.SpawnLink{
			ParentInstanceID: instanceID,
			ParentNodeID:     node.ID,
			Mode:             instance.SpawnMode(mode),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, err)
	}

	// 9. A normal (non-wait) result. Recording the child id in the node output
	// makes the child addressable from the parent's variable store, so downstream
	// nodes and the trace can reference the spawned run.
	return &NodeResult{Output: map[string]any{
		"childInstanceId": childID.String(),
		"childWorkflow":   node.Config.ChildWorkflow,
		"spawnMode":       mode,
	}}, nil
}
```

- [ ] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run TestSpawnExecutor -v
```

Expected output:

```
--- PASS: TestSpawnExecutor_FireAndForget_HappyPath (0.00s)
--- PASS: TestSpawnExecutor_ChildOwnerIsParentOwnerNotAmbientIdentity (0.00s)
--- PASS: TestSpawnExecutor_ReconstructedIdentityCarriesDepartments (0.00s)
--- PASS: TestSpawnExecutor_ChildInheritsParentPriority (0.00s)
--- PASS: TestSpawnExecutor_PassesChildContractAndParentContextToPreparer (0.00s)
--- PASS: TestSpawnExecutor_GuidanceFileIsReadAndSubstituted (0.00s)
--- PASS: TestSpawnExecutor_NoGuidanceConfiguredIsNotAnError (0.00s)
--- PASS: TestSpawnExecutor_PreparationFailure_NoChildCreated (0.00s)
--- PASS: TestSpawnExecutor_ChildWorkflowInvisibleOrMissing_Fails (0.00s)
--- PASS: TestSpawnExecutor_WaitForChild_UnsupportedInThisPhase (0.00s)
--- PASS: TestSpawnExecutor_MissingChildWorkflowConfig_Fails (0.00s)
--- PASS: TestSpawnExecutor_IdentityResolutionFailure_NoChildNoFallback (0.00s)
--- PASS: TestSpawnExecutor_ParentInstanceLoadFailure_Errors (0.00s)
--- PASS: TestSpawnExecutor_SpawnFailure_Surfaced (0.00s)
--- PASS: TestSpawnExecutor_MissingInstanceIDInContext_Errors (0.00s)
--- PASS: TestSpawnExecutor_GuidanceFileUnreadable_Errors (0.00s)
--- PASS: TestSpawnExecutor_ChildWithZeroDeclaredInputs_SpawnsWithEmptyPayload (0.00s)
--- PASS: TestSpawnExecutor_UnrecognizedSnapshotMode_Fails (0.00s)
--- PASS: TestSpawnExecutor_EmptySnapshotMode_Fails (0.00s)
--- PASS: TestSpawnExecutor_OwnerWithNoDepartments_StillSpawns (0.00s)
--- PASS: TestSpawnExecutor_FireAndForgetSpawnsAgainOnReExecution (0.00s)
--- PASS: TestSpawnExecutor_GrandchildLinksToImmediateParent (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.6s
```

- [ ] **Step 5: Confirm the package still passes**

```bash
go test ./domain/engine/
```

Expected: `ok  	github.com/arbitron/arbitron/domain/engine`

- [ ] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/executor_spawn.go domain/engine/executor_spawn_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): add the spawn_workflow executor (fire-and-forget)"
```
