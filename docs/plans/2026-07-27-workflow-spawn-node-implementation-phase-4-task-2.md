# Phase 4 — Task 2: wait-for-child suspension

**Goal:** On first execution of a wait-for-child spawn node, verify both branch ports exist in the snapshot, spawn the linked child, write the node-execution record, transition the parent to waiting, and release queue capacity — in that exact order.

**Architecture:** Extends `domain/engine/executor_spawn.go`. The ordering is load-bearing and stated as an invariant: **enqueue the child → write the node execution record → transition the parent to waiting → notify the queue that capacity was freed.** Parent and child are enqueued under the *same* owner and compete for the same per-user capacity; with a per-user cap of one, the child sits queued until the parent's waiting transition releases the slot. Skipping the notify, or transitioning before the child is enqueued, produces a stall that looks like a hung workflow rather than an error. The executor writes its own node-execution record because `executeWithRetry` deliberately skips writing one on the wait path — the same reason the webhook executor writes its own.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 2: wait path — suspension

**Files:**
- Modify: `domain/engine/executor_spawn.go` (constructor + `Execute` split into `executeFireAndForget` / `executeWait`)
- Modify: `cmd/arbitron/main.go` (spawn executor construction — three new arguments)
- Test:   `domain/engine/executor_spawn_wait_test.go`
- Test:   `domain/engine/executor_spawn_test.go` (fixture constructor call)

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/executor_spawn_wait_test.go`:

```go
package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

// fakeSpawnWaits implements instance.SpawnWaitRepository. It HONOURS all three
// key parts (parent, node, mode): a fake that ignored the node id or the mode
// would make the mis-wake guard tests unfailable.
type fakeSpawnWaits struct {
	mu         sync.Mutex
	byKey      map[string]*instance.ChildLink
	findErr    error
	candidates []instance.SpawnWaitCandidate
	listErr    error
}

func newFakeSpawnWaits() *fakeSpawnWaits {
	return &fakeSpawnWaits{byKey: map[string]*instance.ChildLink{}}
}

func spawnKey(parent uuid.UUID, node string, mode instance.SpawnMode) string {
	return parent.String() + "|" + node + "|" + string(mode)
}

func (f *fakeSpawnWaits) set(parent uuid.UUID, node string, mode instance.SpawnMode, l *instance.ChildLink) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byKey[spawnKey(parent, node, mode)] = l
}

func (f *fakeSpawnWaits) FindChildBySpawnNode(_ context.Context, parentID uuid.UUID, nodeID string, mode instance.SpawnMode) (*instance.ChildLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.byKey[spawnKey(parentID, nodeID, mode)], nil
}

func (f *fakeSpawnWaits) ListWaitingOnTerminalChild(context.Context) ([]instance.SpawnWaitCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.candidates, nil
}

var _ instance.SpawnWaitRepository = (*fakeSpawnWaits)(nil)

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

type spawnWaitFixture struct {
	instances  *mockInstanceRepo
	nodeExecs  *mockNodeExecutionRepo
	spawnWaits *fakeSpawnWaits
	identities *fakeIdentityResolver
	spawner    *fakeChildSpawner
	preparer   *fakePreparer
	fileReader *mockFileReader
	queueMgr   *mockQueueManager
	exec       *engine.SpawnWorkflowExecutor
	parentID   uuid.UUID
	ownerID    uuid.UUID
	order      *[]string
}

// waitParentSnapshot builds the parent's WorkflowSnapshot. successTarget /
// failureTarget control which branch ports are wired.
func waitParentSnapshot(t *testing.T, successTarget, failureTarget string) map[string]any {
	t.Helper()
	wf := workflow.Workflow{
		Name: "parent",
		Nodes: []workflow.Node{
			{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
				ChildWorkflow: "child", SpawnMode: workflow.SpawnModeWaitForChild,
			}},
			{ID: "ok", Type: workflow.NodeTypeTransform},
			{ID: "bad", Type: workflow.NodeTypeTransform},
		},
	}
	if successTarget != "" {
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "spawn-1", Target: successTarget, Output: workflow.SpawnPortSuccess})
	}
	if failureTarget != "" {
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "spawn-1", Target: failureTarget, Output: workflow.SpawnPortFailure})
	}
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(b, &snap))
	return snap
}

func newSpawnWaitFixture(t *testing.T, snapshot map[string]any) *spawnWaitFixture {
	t.Helper()
	owner := uuid.New()
	order := &[]string{}

	instances := newMockInstanceRepo()
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "parent",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		WorkflowSnapshot: snapshot,
	}
	require.NoError(t, instances.Create(context.Background(), parent))
	instances.onUpdateState = func(_ uuid.UUID, st instance.State) {
		*order = append(*order, "state:"+string(st))
	}

	nodeExecs := newMockNodeExecutionRepo()
	nodeExecs.onCreate = func(*instance.NodeExecution) { *order = append(*order, "nodeExec") }

	spawner := &fakeChildSpawner{
		inputsByWorkflow: map[string][]workflow.InputField{"child": {}},
		onSpawn:          func() { *order = append(*order, "spawn") },
	}
	qm := newMockQueueManager()
	qm.onNotify = func() { *order = append(*order, "notify") }

	ids := &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
		owner: {UserID: owner, Role: "user", Departments: []string{"engineering"}},
	}}
	prep := &fakePreparer{values: map[string]any{}}
	fr := newMockFileReader()
	sw := newFakeSpawnWaits()

	return &spawnWaitFixture{
		instances: instances, nodeExecs: nodeExecs, spawnWaits: sw,
		identities: ids, spawner: spawner, preparer: prep, fileReader: fr, queueMgr: qm,
		exec: engine.NewSpawnWorkflowExecutor(
			instances, nodeExecs, sw, prep, spawner, ids, fr, qm,
		),
		parentID: parent.ID, ownerID: owner, order: order,
	}
}

func waitNode() workflow.Node {
	return workflow.Node{
		ID:   "spawn-1",
		Type: workflow.NodeTypeSpawnWorkflow,
		Config: workflow.NodeConfig{
			ChildWorkflow: "child",
			SpawnMode:     workflow.SpawnModeWaitForChild,
			ModelProfile:  "chat-default",
		},
	}
}

func (f *spawnWaitFixture) run(t *testing.T) (*engine.NodeResult, error) {
	t.Helper()
	ctx := engine.ContextWithInstanceID(context.Background(), f.parentID)
	return f.exec.Execute(ctx, waitNode(), variable.NewStore(), "parent")
}

// ---------------------------------------------------------------------------
// happy path
// ---------------------------------------------------------------------------

func TestSpawnWait_FirstExecution_SuspendsWithLinkedChild(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))

	res, err := f.run(t)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsWait, "the parent must suspend at an awaited spawn node")

	require.Len(t, f.spawner.spawned, 1)
	assert.Equal(t, instance.SpawnModeWaitForChild, f.spawner.spawned[0].Link.Mode)
	assert.Equal(t, f.parentID, f.spawner.spawned[0].Link.ParentInstanceID)
	assert.Equal(t, "spawn-1", f.spawner.spawned[0].Link.ParentNodeID)

	got, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, got.State)
}

// ---------------------------------------------------------------------------
// success — the ordering invariant
// ---------------------------------------------------------------------------

// ORDERING IS THE DEADLOCK GUARD. Parent and child are enqueued under the SAME
// owner and compete for the same per-user capacity; with a cap of one, the
// child sits queued until the parent's waiting transition releases the slot.
func TestSpawnWait_OrderingInvariant_SpawnThenRecordThenWaitingThenNotify(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))

	_, err := f.run(t)
	require.NoError(t, err)

	assert.Equal(t, []string{"spawn", "nodeExec", "state:waiting", "notify"}, *f.order,
		"the child must be enqueued BEFORE the parent transitions to waiting, and "+
			"NotifyCapacityFreed must come AFTER the transition")
}

// executeWithRetry deliberately skips writing a node-execution record on the
// wait path, so the executor writes its own — otherwise the awaited spawn node
// would leave no trace entry at all.
func TestSpawnWait_WritesItsOwnNodeExecutionRecord(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))

	_, err := f.run(t)
	require.NoError(t, err)

	exec, err := f.nodeExecs.GetByInstanceAndNode(context.Background(), f.parentID, "spawn-1")
	require.NoError(t, err)
	require.NotNil(t, exec)
	assert.Equal(t, instance.NodeStatusSucceeded, exec.Status)
	assert.NotEmpty(t, exec.Output["childInstanceId"])
	assert.Equal(t, "child", exec.Output["childWorkflow"])
}

func TestSpawnWait_ResultCarriesChildID(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))

	res, err := f.run(t)
	require.NoError(t, err)
	out, ok := res.Output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, f.spawner.nextID.String(), out["childInstanceId"])
}

// ---------------------------------------------------------------------------
// failure — snapshot port checks, BEFORE any child exists
// ---------------------------------------------------------------------------

// findNextNode returns an empty string when no edge matches the port, which
// ends the run as if complete. Phase 1's validation requires both ports, but a
// snapshot predating it could reach here — so the executor verifies both ports
// at FIRST execution and fails the node then, before a child is created.
func TestSpawnWait_MissingFailurePort_FailsBeforeCreatingChild(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", ""))

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), workflow.SpawnPortFailure)
	assert.Empty(t, f.spawner.spawned, "no child may be created for an unroutable wait node")
	assert.Equal(t, 0, f.preparer.calls, "the port check must precede any LLM cost")
}

func TestSpawnWait_MissingSuccessPort_FailsBeforeCreatingChild(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "", "bad"))

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), workflow.SpawnPortSuccess)
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnWait_PreparationFailure_NoChildAndNoSuspension(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.preparer.err = errors.New("missing required input(s): repo")

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)

	got, gerr := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, gerr)
	assert.Equal(t, instance.StateRunning, got.State, "the parent must not be suspended")
}

// ---------------------------------------------------------------------------
// error
// ---------------------------------------------------------------------------

// If the child is enqueued but the waiting transition fails, the executor
// errors and the parent fails at that node. The child CONTINUES and remains
// visible in the hierarchy as an orphaned-but-linked run. This is preferred
// over rolling the child back: it may already be executing side effects, and
// cancelling it from a failing parent would layer a second failure on the first.
func TestSpawnWait_WaitingTransitionFails_ChildSurvivesAndNodeErrors(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.instances.updateStateErr = errors.New("db write failed")

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	require.Len(t, f.spawner.spawned, 1, "the child was already enqueued and is not rolled back")

	got, gerr := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, gerr)
	assert.NotEqual(t, instance.StateWaiting, got.State,
		"the parent must not be left in a half-suspended state")
}

// A storage failure on the short-circuit lookup must NOT be mistaken for "no
// child yet", which would spawn a second child.
func TestSpawnWait_LinkLookupFailure_DoesNotSpawnASecondChild(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.spawnWaits.findErr = errors.New("db down")

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
}

func TestSpawnWait_UnparseableSnapshot_Errors(t *testing.T) {
	f := newSpawnWaitFixture(t, map[string]any{"nodes": "not-an-array"})

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned)
}
```

Add the three test-only hooks the fixture uses. In
`domain/engine/engine_test.go`, on `mockInstanceRepo` add a field and honour it
in `UpdateState`:

```go
type mockInstanceRepo struct {
	// ... existing fields ...
	// updateStateErr, when set, makes UpdateState fail. Used to exercise the
	// half-suspended failure path.
	updateStateErr error
}
```

and at the top of `func (r *mockInstanceRepo) UpdateState(...)`:

```go
func (r *mockInstanceRepo) UpdateState(_ context.Context, id uuid.UUID, state instance.State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateStateErr != nil {
		return r.updateStateErr
	}
	// ... existing body ...
}
```

On `mockNodeExecutionRepo` add an `onCreate` hook invoked at the end of `Create`:

```go
	// onCreate, when set, is invoked from Create AFTER the record is stored.
	// Test-only ordering probe.
	onCreate func(*instance.NodeExecution)
```

```go
	if r.onCreate != nil {
		r.onCreate(exec)
	}
```

On `mockQueueManager` add an `onNotify` hook invoked at the start of
`NotifyCapacityFreed`:

```go
	// onNotify, when set, is invoked from NotifyCapacityFreed. Test-only
	// ordering probe.
	onNotify func()
```

```go
	if q.onNotify != nil {
		q.onNotify()
	}
```

On `fakeChildSpawner` (in `domain/engine/executor_spawn_test.go`) add:

```go
	// onSpawn, when set, is invoked at the start of Spawn. Test-only ordering probe.
	onSpawn func()
```

```go
func (s *fakeChildSpawner) Spawn(ctx context.Context, req engine.SpawnRequest) (uuid.UUID, error) {
	if s.onSpawn != nil {
		s.onSpawn()
	}
	// ... existing body ...
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestSpawnWait
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/executor_spawn_wait_test.go:...: too many arguments in call to engine.NewSpawnWorkflowExecutor
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [ ] **Step 3: Extend the executor**

Rewrite the struct, constructor, and `Execute` in
`domain/engine/executor_spawn.go`. Replace everything from `type
SpawnWorkflowExecutor struct` through the end of `Execute` with:

```go
// SpawnWorkflowExecutor executes one spawn_workflow node: prepare the child's
// startup values, start the child, and tell the engine how to proceed.
//
// Every collaborator is a PORT, so the executor holds no LLM and no engine.
type SpawnWorkflowExecutor struct {
	instances  instance.InstanceRepository
	nodeExecs  instance.NodeExecutionRepository
	spawnWaits instance.SpawnWaitRepository
	preparer   ChildInputPreparer
	spawner    ChildSpawner
	identities IdentityResolver
	fileReader FileReader
	queueMgr   QueueManager
}

// NewSpawnWorkflowExecutor constructs the executor.
func NewSpawnWorkflowExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	spawnWaits instance.SpawnWaitRepository,
	preparer ChildInputPreparer,
	spawner ChildSpawner,
	identities IdentityResolver,
	fileReader FileReader,
	queueMgr QueueManager,
) *SpawnWorkflowExecutor {
	return &SpawnWorkflowExecutor{
		instances:  instances,
		nodeExecs:  nodeExecs,
		spawnWaits: spawnWaits,
		preparer:   preparer,
		spawner:    spawner,
		identities: identities,
		fileReader: fileReader,
		queueMgr:   queueMgr,
	}
}

var _ NodeExecutor = (*SpawnWorkflowExecutor)(nil)

// Execute runs one spawn node.
//
// Every failure below returns a plain error; the engine then applies the node's
// resolved ErrorBehavior (retry / default output / error-node routing). The
// executor encodes no control-flow policy of its own.
func (e *SpawnWorkflowExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("spawn_workflow %q: instance ID not found in context", node.ID)
	}

	// Re-check the configured contract. Instances execute from a
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
		return e.executeWait(ctx, node, vars, workflowName, instanceID)
	}
	return e.executeFireAndForget(ctx, node, vars, workflowName, instanceID)
}

// executeFireAndForget starts the child and returns a NORMAL result so the
// engine advances along the node's single unnamed edge.
func (e *SpawnWorkflowExecutor) executeFireAndForget(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error) {
	childID, err := e.prepareAndSpawn(ctx, node, vars, workflowName, instanceID, instance.SpawnModeFireAndForget)
	if err != nil {
		return nil, err
	}
	return &NodeResult{Output: spawnOutput(childID, node.Config.ChildWorkflow, workflow.SpawnModeFireAndForget)}, nil
}

// executeWait implements the wait-for-child path.
//
// On re-entry it SHORT-CIRCUITS on the LINK rather than on node-execution
// status: the spawn node needs to know HOW the child ended in order to choose a
// port, and the child's persisted state is the single authority for that.
func (e *SpawnWorkflowExecutor) executeWait(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error) {
	link, err := e.spawnWaits.FindChildBySpawnNode(ctx, instanceID, node.ID, instance.SpawnModeWaitForChild)
	if err != nil {
		// A storage failure must NOT be mistaken for "no child yet", which would
		// spawn a second child.
		return nil, fmt.Errorf("spawn_workflow %q: look up awaited child: %w", node.ID, err)
	}
	if link != nil {
		return e.resumeFromLink(node, link)
	}

	// FIRST EXECUTION. Verify both branch ports are present in the SNAPSHOT
	// before a child is created. findNextNode returns an empty string when no
	// edge matches the port, which ends the run as if complete — so a snapshot
	// predating Phase 1's validation would silently complete the parent after the
	// child finished. Fail here instead, while nothing has been created yet.
	parent, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: load parent instance: %w", node.ID, err)
	}
	if err := assertSpawnBranchPorts(node.ID, parent.WorkflowSnapshot); err != nil {
		return nil, err
	}

	startedAt := time.Now()

	// ORDER IS LOAD-BEARING (the deadlock guard):
	//   enqueue the child -> write the node execution record ->
	//   transition the parent to waiting -> notify that capacity was freed.
	// Parent and child are enqueued under the SAME owner and compete for the
	// same per-user capacity; with a cap of one, the child sits queued until the
	// parent's waiting transition releases the slot. Skipping the notify, or
	// transitioning before the child is enqueued, produces a stall that looks
	// like a hung workflow rather than an error.
	childID, err := e.prepareAndSpawn(ctx, node, vars, workflowName, instanceID, instance.SpawnModeWaitForChild)
	if err != nil {
		return nil, err
	}

	output := spawnOutput(childID, node.Config.ChildWorkflow, workflow.SpawnModeWaitForChild)

	// executeWithRetry deliberately skips writing a node-execution record on the
	// wait path, so the executor writes its own — otherwise the awaited spawn
	// node would leave no trace entry at all.
	completedAt := time.Now()
	if err := e.nodeExecs.Create(ctx, &instance.NodeExecution{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		NodeID:      node.ID,
		Attempt:     1,
		Status:      instance.NodeStatusSucceeded,
		Output:      output,
		StartedAt:   startedAt,
		CompletedAt: &completedAt,
	}); err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: write node execution record: %w", node.ID, err)
	}

	// waiting_until stays NULL: this wait has no deadline, so the timer recovery
	// sweep (which filters on a non-null waiting_until) must not pick it up. Same
	// choice the webhook executor makes for an untimed wait.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		// The child is already enqueued and is deliberately NOT rolled back: it may
		// already be executing side effects, and cancelling it from a failing
		// parent would layer a second failure on the first. It stays visible in the
		// hierarchy as an orphaned-but-linked run.
		return nil, fmt.Errorf("spawn_workflow %q: set waiting state: %w", node.ID, err)
	}

	e.queueMgr.NotifyCapacityFreed(ctx)

	return &NodeResult{IsWait: true, Output: output}, nil
}

// resumeFromLink decides the parent's next move from an existing link.
// (The terminal-routing arm lands in Phase 4 Task 3.)
func (e *SpawnWorkflowExecutor) resumeFromLink(node workflow.Node, link *instance.ChildLink) (*NodeResult, error) {
	// The child is still active: return a wait result WITHOUT re-spawning and
	// without re-transitioning state. This is the idempotent path that makes a
	// spurious re-entry harmless.
	return &NodeResult{IsWait: true, Output: spawnOutput(link.ChildID, node.Config.ChildWorkflow, node.Config.SpawnMode)}, nil
}

// prepareAndSpawn is the sequence both modes share: reconstruct the parent
// owner's identity, load the child's declared inputs through it, read optional
// guidance, prepare the startup values, and create the linked child.
func (e *SpawnWorkflowExecutor) prepareAndSpawn(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID, mode instance.SpawnMode) (uuid.UUID, error) {
	parent, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: load parent instance: %w", node.ID, err)
	}

	// Reconstruct the PARENT OWNER's identity. A resolution failure is NOT
	// downgraded to a system identity: falling back would silently change which
	// workflows are visible to the spawn and could widen reach beyond the owner.
	ownerIdentity, err := e.identities.IdentityForUser(ctx, parent.OwnerID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: resolve owner identity: %w", node.ID, err)
	}
	childCtx := identity.WithIdentity(ctx, ownerIdentity)

	// If the parent's owner cannot see the child workflow this returns not-found
	// and the node fails — fail closed, with no existence leak.
	inputs, err := e.spawner.ChildInputs(childCtx, node.Config.ChildWorkflow)
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: load child workflow %q: %w",
			node.ID, node.Config.ChildWorkflow, err)
	}

	guidance := ""
	if node.Config.TemplateFile != "" {
		b, rerr := e.fileReader.ReadFile(ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
		if rerr != nil {
			return uuid.Nil, fmt.Errorf("spawn_workflow %q: read guidance %q: %w",
				node.ID, node.Config.TemplateFile, rerr)
		}
		guidance = variable.Resolve(string(b), vars)
	}

	values, err := e.preparer.Prepare(childCtx, ChildInputRequest{
		NodeID:        node.ID,
		ChildWorkflow: node.Config.ChildWorkflow,
		Inputs:        inputs,
		ParentContext: vars.Snapshot(),
		Guidance:      guidance,
		ProfileAlias:  node.Config.ModelProfile,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: %w", node.ID, err)
	}

	childID, err := e.spawner.Spawn(childCtx, SpawnRequest{
		ChildWorkflow: node.Config.ChildWorkflow,
		Variables:     values,
		Priority:      string(parent.Priority),
		Link: instance.SpawnLink{
			ParentInstanceID: instanceID,
			ParentNodeID:     node.ID,
			Mode:             mode,
		},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, err)
	}
	return childID, nil
}

// spawnOutput is the ONE shape of a spawn node's output. Recording the child id
// makes the child addressable from the parent's variable store, so downstream
// nodes and the trace can reference the spawned run.
func spawnOutput(childID uuid.UUID, childWorkflow string, mode string) map[string]any {
	return map[string]any{
		"childInstanceId": childID.String(),
		"childWorkflow":   childWorkflow,
		"spawnMode":       mode,
	}
}

// assertSpawnBranchPorts verifies that BOTH the success and failure ports have
// an outgoing edge in the parent's snapshot.
func assertSpawnBranchPorts(nodeID string, snapshot map[string]any) error {
	wf, _, _, err := parseWorkflowGraph(snapshot)
	if err != nil {
		return fmt.Errorf("spawn_workflow %q: parse workflow snapshot: %w", nodeID, err)
	}
	ports := map[string]bool{}
	for _, edge := range wf.Edges {
		if edge.Source == nodeID {
			ports[edge.Output] = true
		}
	}
	for _, want := range []string{workflow.SpawnPortSuccess, workflow.SpawnPortFailure} {
		if !ports[want] {
			return fmt.Errorf("spawn_workflow %q: wait_for_child requires an outgoing edge for the %q port; "+
				"without it the parent would silently complete when the child finishes", nodeID, want)
		}
	}
	return nil
}
```

Update the imports of `domain/engine/executor_spawn.go`:

```go
import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)
```

- [ ] **Step 4: Update the Phase 3 fixture constructor call**

In `domain/engine/executor_spawn_test.go`, in `newSpawnExecFixture`, replace the
executor construction with the eight-argument form:

```go
	nodeExecs := newMockNodeExecutionRepo()
	sw := newFakeSpawnWaits()
	qm := newMockQueueManager()

	return &spawnExecFixture{
		instances:  instances,
		identities: ids,
		spawner:    spawner,
		preparer:   prep,
		fileReader: fr,
		exec:       engine.NewSpawnWorkflowExecutor(instances, nodeExecs, sw, prep, spawner, ids, fr, qm),
		parentID:   parent.ID,
		ownerID:    owner,
	}
```

The Phase 3 test `TestSpawnExecutor_WaitForChild_UnsupportedInThisPhase` is now
obsolete — wait-for-child IS supported. Delete that test function; the wait path
is covered by `TestSpawnWait_*`.

- [ ] **Step 5: Update the composition root**

In `cmd/arbitron/main.go`, replace the spawn executor construction with the
eight-argument form:

```go
	childInputPreparer := engine.NewChildInputPreparer(providerFactory, profileResolver)
	executors[workflow.NodeTypeSpawnWorkflow] = engine.NewSpawnWorkflowExecutor(
		instanceRepo,
		nodeExecRepo,
		instanceRepo, // *adaptsqlite.InstanceRepo also satisfies instance.SpawnWaitRepository
		childInputPreparer,
		eng,
		identityResolver,
		fileReader,
		queueMgr,
	)
```

> `instanceRepo` is already the CONCRETE `*adaptsqlite.InstanceRepo`
> (`cmd/arbitron/main.go:330`), which satisfies `instance.InstanceRepository`,
> `instance.SpawnLinkRepository`, and `instance.SpawnWaitRepository` alike — the
> same way it already satisfies `schedule.ActiveRunChecker`. No alias or cast is
> needed; pass it for both the first and third arguments.

- [ ] **Step 6: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/ cmd/arbitron/
go build ./... && go test ./domain/engine/ -run 'TestSpawnWait|TestSpawnExecutor' -v 2>&1 | tail -40
```

Expected output:

```
--- PASS: TestSpawnWait_FirstExecution_SuspendsWithLinkedChild (0.00s)
--- PASS: TestSpawnWait_OrderingInvariant_SpawnThenRecordThenWaitingThenNotify (0.00s)
--- PASS: TestSpawnWait_WritesItsOwnNodeExecutionRecord (0.00s)
--- PASS: TestSpawnWait_ResultCarriesChildID (0.00s)
--- PASS: TestSpawnWait_MissingFailurePort_FailsBeforeCreatingChild (0.00s)
--- PASS: TestSpawnWait_MissingSuccessPort_FailsBeforeCreatingChild (0.00s)
--- PASS: TestSpawnWait_PreparationFailure_NoChildAndNoSuspension (0.00s)
--- PASS: TestSpawnWait_WaitingTransitionFails_ChildSurvivesAndNodeErrors (0.00s)
--- PASS: TestSpawnWait_LinkLookupFailure_DoesNotSpawnASecondChild (0.00s)
--- PASS: TestSpawnWait_UnparseableSnapshot_Errors (0.00s)
--- PASS: TestSpawnExecutor_FireAndForget_HappyPath (0.00s)
... (all remaining Phase 3 cases)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.6s
```

- [ ] **Step 7: Full Go gate**

```bash
go test ./...
```

Expected: `ok` for every package, no `FAIL` lines.

- [ ] **Step 8: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/executor_spawn.go domain/engine/executor_spawn_test.go domain/engine/executor_spawn_wait_test.go domain/engine/engine_test.go cmd/arbitron/main.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): suspend the parent at an awaited spawn node with the ordering invariant"
```
