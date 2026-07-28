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

// The node-execution write sits BEFORE the waiting transition in the
// load-bearing ordering (enqueue -> node-exec record -> waiting -> notify), so
// a failure here must surface the same way a failure at the later waiting
// transition does: the child is already enqueued and is not rolled back, and
// the parent is left in its prior (non-waiting) state rather than half
// suspended. TestWebhookWaitExecutor_NodeExecCreateFails_ReturnsError pins the
// identical shape for the sibling webhook-wait executor; this had no
// equivalent for the spawn wait path.
func TestSpawnWait_NodeExecutionRecordWriteFails_ChildSurvivesAndParentNotSuspended(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.nodeExecs.createErr = errors.New("db write failed")

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	require.Len(t, f.spawner.spawned, 1, "the child was already enqueued and is not rolled back")

	got, gerr := f.instances.Get(context.Background(), f.parentID)
	require.NoError(t, gerr)
	assert.Equal(t, instance.StateRunning, got.State,
		"the parent must not be left in a half-suspended state")
}

// executeWait loads the parent a SECOND time (distinct from prepareAndSpawn's
// own load) purely to check the snapshot's branch ports before any child is
// created. A failure at THIS load must surface the same way every other
// pre-spawn failure does: no child, parent untouched.
func TestSpawnWait_ParentLoadForBranchPortCheck_FailsBeforeCreatingChild(t *testing.T) {
	f := newSpawnWaitFixture(t, waitParentSnapshot(t, "ok", "bad"))
	f.instances.getErr = errors.New("db down")

	res, err := f.run(t)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Empty(t, f.spawner.spawned, "no child may be created when the parent cannot be loaded")
	assert.Equal(t, 0, f.preparer.calls, "the load failure must precede any LLM cost")
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
