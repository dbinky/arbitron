package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinelInstanceRepo wraps the shared mock so a miss wraps instance.ErrNotFound,
// exactly as the SQLite repository does (adapter/sqlite/instance_repo.go:141-143).
// StartGroupTask distinguishes "no row under the reserved id yet" from a storage
// failure; the plain mock's unwrapped miss would look like the latter.
type sentinelInstanceRepo struct {
	*mockInstanceRepo
	reads []uuid.UUID // guarded by the embedded repository's mutex
}

func (r *sentinelInstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	r.reads = append(r.reads, id)
	_, ok := r.instances[id]
	injected := r.getErr
	r.mu.Unlock()
	if injected == nil && !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return r.mockInstanceRepo.Get(ctx, id)
}

func (r *sentinelInstanceRepo) lookupIDs() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.reads...)
}

var _ instance.InstanceRepository = (*sentinelInstanceRepo)(nil)

// groupTaskHarness wires a real Engine whose Enqueue records and leaves the run
// queued, so the created row is inspectable and nothing executes.
type groupTaskHarness struct {
	repo      *scopeAwareRepo
	instances *mockInstanceRepo
	lookups   *sentinelInstanceRepo
	bus       *mockEventBus
	qm        *mockQueueManager
	engine    *engine.Engine

	mu             sync.Mutex
	enqueued       []uuid.UUID
	enqueuedOwners []uuid.UUID
	enqueuedRows   map[uuid.UUID]*instance.Instance
}

func newGroupTaskHarness(t *testing.T) *groupTaskHarness {
	t.Helper()
	bus := newMockEventBus()
	h := newGroupTaskHarnessOn(t, bus)
	h.bus = bus
	return h
}

// newGroupTaskHarnessOn builds the harness over a caller-supplied bus (e.g. the
// real in-process bus a subtree bridge subscribes to). h.bus stays nil.
func newGroupTaskHarnessOn(t *testing.T, bus engine.EventBus) *groupTaskHarness {
	t.Helper()
	repo := newScopeAwareRepo()
	instRepo := newMockInstanceRepo()
	qm := newMockQueueManager()
	lookups := &sentinelInstanceRepo{mockInstanceRepo: instRepo}
	eng := wireModelPins(engine.NewEngine(repo, lookups, newMockNodeExecutionRepo(), newMockSnapshotRepo(),
		newMockUserRepo(), map[workflow.NodeType]engine.NodeExecutor{}, bus, qm, &mockQueueRepo{instRepo: instRepo}, ""))
	eng.SetGroupSupport(engine.GroupSupportResults)

	h := &groupTaskHarness{
		repo: repo, instances: instRepo, lookups: lookups, qm: qm, engine: eng,
		enqueuedRows: make(map[uuid.UUID]*instance.Instance),
	}
	qm.mu.Lock()
	qm.enqueueFn = func(ctx context.Context, id uuid.UUID, owner uuid.UUID) (bool, error) {
		row, err := instRepo.Get(ctx, id)
		if err != nil {
			return false, err
		}
		if row.OwnerID != owner {
			return false, fmt.Errorf("queue owner %s does not match persisted owner %s", owner, row.OwnerID)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.enqueued = append(h.enqueued, id)
		h.enqueuedOwners = append(h.enqueuedOwners, owner)
		h.enqueuedRows[id] = row
		return false, nil
	}
	qm.mu.Unlock()
	return h
}

func (h *groupTaskHarness) enqueuedIDs() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uuid.UUID(nil), h.enqueued...)
}

func (h *groupTaskHarness) rowAtEnqueue(id uuid.UUID) *instance.Instance {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enqueuedRows[id]
}

// gtParentWorkflow is a parent whose group node "g" fans out to branch bodies
// a1 and b1. "stray" exists in the snapshot but is not a task target.
func gtParentWorkflow() workflow.Workflow {
	return workflow.Workflow{
		Name: "parent-wf",
		Nodes: []workflow.Node{
			{ID: "g", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
				GatherMode:         workflow.GroupGatherResults,
			}}},
			{ID: "a1", Type: workflow.NodeTypeScript},
			{ID: "b1", Type: workflow.NodeTypeScript},
			{ID: "after", Type: workflow.NodeTypeScript},
			{ID: "fail", Type: workflow.NodeTypeScript},
			{ID: "stray", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "g", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "g", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "g", Target: "after", Output: workflow.GroupPortSuccess},
			{Source: "g", Target: "fail", Output: workflow.GroupPortFailure},
		},
	}
}

func gtSnapshot(t *testing.T, wf workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(b, &snap))
	return snap
}

func (h *groupTaskHarness) seedParent(t *testing.T, owner uuid.UUID, depth int, opts ...func(*instance.Instance)) *instance.Instance {
	t.Helper()
	wf := gtParentWorkflow()
	g := "g"
	p := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowID: uuid.New(), WorkflowName: wf.Name,
		WorkflowSnapshot: gtSnapshot(t, wf), Department: "engineering", Visibility: instance.VisibilityDepartment,
		State: instance.StateRunning, Priority: instance.PriorityHigh, SpawnDepth: depth, CurrentNodeID: &g,
	}
	for _, opt := range opts {
		opt(p)
	}
	require.NoError(t, h.instances.Create(context.Background(), p))
	return p
}

func gtChildWorkflow(name string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name,
		Inputs: []workflow.InputField{
			{Name: "repo", Type: "string", Required: true},
			{Name: "dryRun", Type: "boolean", Default: false},
		},
		Nodes: []workflow.Node{{ID: "c1", Type: workflow.NodeTypeScript, Entrypoint: true}},
	}
}

func gtWorkflowReq(parent *instance.Instance) engine.GroupTaskStartRequest {
	return engine.GroupTaskStartRequest{
		TaskInstanceID: uuid.New(), ParentInstanceID: parent.ID, ParentNodeID: "g", GroupRunID: uuid.New(),
		TaskKey: "review", Kind: instance.GroupTaskKindWorkflow, ChildWorkflow: "child-wf",
		Variables: map[string]any{"repo": "acme/widgets", "ticket": "T-1"},
		Priority:  string(instance.PriorityHigh),
	}
}

func gtBranchReq(parent *instance.Instance) engine.GroupTaskStartRequest {
	return engine.GroupTaskStartRequest{
		TaskInstanceID: uuid.New(), ParentInstanceID: parent.ID, ParentNodeID: "g", GroupRunID: uuid.New(),
		TaskKey: "a", Kind: instance.GroupTaskKindBranch, BranchEntry: "a1",
		Variables: map[string]any{"repo": "acme/widgets", "cfg": map[string]any{"depth": map[string]any{"leaf": "v"}}},
		Priority:  string(instance.PriorityLow),
	}
}

// ---------- happy path ----------

func TestStartGroupTask_WorkflowKind_CreatesTheLinkedChildUnderTheReservedID(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	parent := h.seedParent(t, owner, 2)
	req := gtWorkflowReq(parent)
	req.Variables["cfg"] = map[string]any{"nested": []any{"original"}}

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	child := h.rowAtEnqueue(req.TaskInstanceID)
	require.NotNil(t, child, "the RESERVED row and its complete link must already exist at enqueue")
	assert.Equal(t, h.instances.get(req.TaskInstanceID), child)
	assert.Equal(t, owner, child.OwnerID)
	assert.Equal(t, "child-wf", child.WorkflowName)
	require.NotNil(t, child.ParentInstanceID)
	assert.Equal(t, parent.ID, *child.ParentInstanceID)
	require.NotNil(t, child.ParentNodeID)
	assert.Equal(t, "g", *child.ParentNodeID)
	assert.Equal(t, instance.SpawnModeGroupTask, child.SpawnMode)
	require.NotNil(t, child.GroupRunID)
	assert.Equal(t, req.GroupRunID, *child.GroupRunID)
	require.NotNil(t, child.GroupTaskKey)
	assert.Equal(t, "review", *child.GroupTaskKey)
	assert.Equal(t, instance.GroupTaskKindWorkflow, child.GroupTaskKind)
	assert.Equal(t, 3, child.SpawnDepth, "depth derives from the parent's persisted depth")
	assert.Equal(t, instance.PriorityHigh, child.Priority)
	assert.Equal(t, instance.StateQueued, child.State)
	assert.Nil(t, child.CurrentNodeID, "a workflow task starts at its own entrypoint")
	assert.Equal(t, instance.OriginManual, child.TriggerSource)
	assert.Equal(t, "acme/widgets", child.Variables["repo"])
	assert.Equal(t, "T-1", child.Variables["ticket"], "base variables beyond the child's declared inputs ride along")
	assert.Equal(t, false, child.Variables["dryRun"], "declared defaults still apply")
	assert.Equal(t, instance.VisibilityPrivate, child.Visibility, "a private child workflow stamps a private run, as Trigger does")
	assert.NotNil(t, child.WorkflowSnapshot)

	req.Variables["cfg"].(map[string]any)["nested"].([]any)[0] = "mutated"
	assert.Equal(t, "original", child.Variables["cfg"].(map[string]any)["nested"].([]any)[0])
	assert.Equal(t, "original", child.OriginalVariables["cfg"].(map[string]any)["nested"].([]any)[0])

	assert.Equal(t, []uuid.UUID{req.TaskInstanceID}, h.enqueuedIDs())
	assert.Equal(t, []uuid.UUID{owner}, h.enqueuedOwners)
	assert.Len(t, h.bus.eventsOfType(req.TaskInstanceID, "instance.created"), 1)
}

func TestStartGroupTask_BranchKind_RunsTheParentsSnapshotFromItsEntry(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	child := h.rowAtEnqueue(req.TaskInstanceID)
	require.NotNil(t, child, "the branch link and cursor must already be persisted at enqueue")
	assert.Equal(t, h.instances.get(req.TaskInstanceID), child)
	require.NotNil(t, child.ParentInstanceID)
	assert.Equal(t, parent.ID, *child.ParentInstanceID)
	require.NotNil(t, child.ParentNodeID)
	assert.Equal(t, req.ParentNodeID, *child.ParentNodeID)
	assert.Equal(t, parent.WorkflowSnapshot, child.WorkflowSnapshot, "the branch runs the parent's definition snapshot")
	require.NotNil(t, child.CurrentNodeID)
	assert.Equal(t, "a1", *child.CurrentNodeID, "Engine.run honours a preset cursor: the branch starts at its entry")
	assert.Equal(t, owner, child.OwnerID)
	assert.Equal(t, parent.WorkflowID, child.WorkflowID)
	assert.Equal(t, "parent-wf", child.WorkflowName)
	assert.Equal(t, "engineering", child.Department)
	assert.Equal(t, instance.VisibilityDepartment, child.Visibility)
	assert.Equal(t, instance.PriorityHigh, child.Priority, "branch priority is the PARENT's, not the request's")
	assert.Equal(t, instance.SpawnModeGroupTask, child.SpawnMode)
	assert.Equal(t, instance.GroupTaskKindBranch, child.GroupTaskKind)
	require.NotNil(t, child.GroupTaskKey)
	assert.Equal(t, "a", *child.GroupTaskKey)
	require.NotNil(t, child.GroupRunID)
	assert.Equal(t, req.GroupRunID, *child.GroupRunID)
	assert.Equal(t, 1, child.SpawnDepth)
	assert.Equal(t, instance.StateQueued, child.State)
	assert.Equal(t, req.Variables, child.Variables)
	assert.Equal(t, child.Variables, child.OriginalVariables)
	assert.Equal(t, []uuid.UUID{req.TaskInstanceID}, h.enqueuedIDs())
	assert.Len(t, h.bus.eventsOfType(req.TaskInstanceID, "instance.created"), 1)

	// Deep independence: neither the request map nor the child's snapshot shares
	// state with the parent.
	req.Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"] = "mutated"
	assert.Equal(t, "v", child.Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])
	child.WorkflowSnapshot["nodes"].([]any)[0].(map[string]any)["id"] = "mutated"
	assert.Equal(t, "g", h.instances.get(parent.ID).WorkflowSnapshot["nodes"].([]any)[0].(map[string]any)["id"])
}

// ---------- error / failure ----------

func TestStartGroupTask_DepthCeiling(t *testing.T) {
	for _, kind := range []instance.GroupTaskKind{instance.GroupTaskKindBranch, instance.GroupTaskKindWorkflow} {
		t.Run(string(kind), func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, instance.MaxSpawnDepth)
			req := gtBranchReq(parent)
			if kind == instance.GroupTaskKindWorkflow {
				req = gtWorkflowReq(parent)
			}

			err := h.engine.StartGroupTask(ctxAs(owner), req)
			require.ErrorIs(t, err, instance.ErrSpawnDepthExceeded)
			assert.Nil(t, h.instances.get(req.TaskInstanceID), "the refusal lands before the row")
			assert.Empty(t, h.enqueuedIDs())
		})
	}
}

func TestStartGroupTask_AllowsTheDeepestPermittedGeneration(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, instance.MaxSpawnDepth-1)
	req := gtBranchReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
	assert.Equal(t, instance.MaxSpawnDepth, h.instances.get(req.TaskInstanceID).SpawnDepth)
}

func TestStartGroupTask_WorkflowKind_InvisibleChildIsNotFound(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Shared(), gtChildWorkflow("child-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"child-wf": {Publisher: uuid.New(), Visibility: workflow.VisibilityDepartment, Department: "sales"},
	}})
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)

	err := h.engine.StartGroupTask(ctxWithDept(owner, "engineering"), req)
	require.ErrorIs(t, err, workflow.ErrNotFound, "a task must never learn that a workflow its owner cannot see exists")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_WorkflowKind_ArchivedChildRefused(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	res := newArchiveResolver()
	res.register("child-wf", uuid.New())
	res.markArchived("child-wf")
	h.engine.SetWorkflowResolver(res)
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, workflow.ErrArchived)
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_WorkflowKind_MissingRequiredChildInput(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)
	req.Variables = map[string]any{"ticket": "T-1"}

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required input: repo")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_WorkflowKind_GroupSupportGuardApplies(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	files := gtParentWorkflow()
	files.Name = "child-wf"
	files.Nodes[0].Config.Group.GatherMode = workflow.GroupGatherResultsAndFiles
	files.Nodes[0].Config.Group.ResolverProfile = "resolver"
	h.repo.put(workflow.Private(owner), &files)
	parent := h.seedParent(t, owner, 0)
	req := gtWorkflowReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, engine.ErrGroupExecutionUnsupported,
		"a child workflow with a file-gathering group is refused on a results-only server, exactly as Trigger refuses it")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
}

func TestStartGroupTask_BranchKind_GroupSupportGuardApplies(t *testing.T) {
	h := newGroupTaskHarness(t)
	h.engine.SetGroupSupport(engine.GroupSupportNone)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, engine.ErrGroupExecutionUnsupported)
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_BranchEntryMustBeATaskTargetOfTheGroupNode(t *testing.T) {
	cases := []struct {
		entry string
		want  string
	}{
		{"missing", "is not a node of the parent's workflow snapshot"},
		{"stray", `is not the target of a "task" edge from group node "g"`},
		{"after", `is not the target of a "task" edge from group node "g"`},
	}
	for _, c := range cases {
		t.Run(c.entry, func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			parent := h.seedParent(t, owner, 0)
			req := gtBranchReq(parent)
			req.BranchEntry = c.entry

			err := h.engine.StartGroupTask(ctxAs(owner), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
			assert.Nil(t, h.instances.get(req.TaskInstanceID))
			assert.Empty(t, h.enqueuedIDs())
		})
	}
}

func TestStartGroupTask_RequiresTheParentOwnersIdentity(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)

	req := gtBranchReq(parent)
	err := h.engine.StartGroupTask(context.Background(), req)
	require.ErrorIs(t, err, engine.ErrInvalidGroupTaskRequest, "no identity on ctx: never fall back to the default user")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))

	req = gtBranchReq(parent)
	err = h.engine.StartGroupTask(ctxAs(uuid.New()), req)
	require.ErrorIs(t, err, identity.ErrForbidden, "a task may only be created under the parent owner's identity")
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_UnreadableParentFailsClosed(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	req := engine.GroupTaskStartRequest{
		TaskInstanceID: uuid.New(), ParentInstanceID: uuid.New(), ParentNodeID: "g", GroupRunID: uuid.New(),
		TaskKey: "a", Kind: instance.GroupTaskKindBranch, BranchEntry: "a1",
	}

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.ErrorIs(t, err, instance.ErrNotFound)
	assert.Empty(t, h.instances.all())
	assert.Empty(t, h.enqueuedIDs())
}

func TestStartGroupTask_ReservedIDLookupStorageErrorFailsClosed(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	h.instances.getErr = errors.New("database is locked")
	req := gtBranchReq(parent)

	err := h.engine.StartGroupTask(ctxAs(owner), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is locked", "a storage failure must not be mistaken for 'no row yet'")
	h.instances.getErr = nil
	assert.Nil(t, h.instances.get(req.TaskInstanceID))
	assert.Empty(t, h.enqueuedIDs())
}

// ---------- edge cases ----------

func TestStartGroupTask_IncompleteRequestRejectedBeforeAnyRow(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *engine.GroupTaskStartRequest)
	}{
		{"no reserved id", func(r *engine.GroupTaskStartRequest) { r.TaskInstanceID = uuid.Nil }},
		{"no parent", func(r *engine.GroupTaskStartRequest) { r.ParentInstanceID = uuid.Nil }},
		{"no parent node", func(r *engine.GroupTaskStartRequest) { r.ParentNodeID = "" }},
		{"no group run", func(r *engine.GroupTaskStartRequest) { r.GroupRunID = uuid.Nil }},
		{"no task key", func(r *engine.GroupTaskStartRequest) { r.TaskKey = "" }},
		{"unknown kind", func(r *engine.GroupTaskStartRequest) { r.Kind = instance.GroupTaskKind("spawn") }},
		{"workflow kind without a child workflow", func(r *engine.GroupTaskStartRequest) {
			r.Kind = instance.GroupTaskKindWorkflow
			r.BranchEntry = ""
			r.ChildWorkflow = ""
		}},
		{"workflow kind with a branch entry", func(r *engine.GroupTaskStartRequest) {
			r.Kind = instance.GroupTaskKindWorkflow
			r.ChildWorkflow = "child-wf"
		}},
		{"branch kind without an entry", func(r *engine.GroupTaskStartRequest) { r.BranchEntry = "" }},
		{"branch kind with a child workflow", func(r *engine.GroupTaskStartRequest) { r.ChildWorkflow = "child-wf" }},
		{"reserved workspace variable", func(r *engine.GroupTaskStartRequest) {
			r.Variables = map[string]any{"workspace": "/somewhere/else"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, 0)
			req := gtBranchReq(parent)
			c.mutate(&req)

			err := h.engine.StartGroupTask(ctxAs(owner), req)
			require.ErrorIs(t, err, engine.ErrInvalidGroupTaskRequest)
			assert.Empty(t, h.lookups.lookupIDs(), "invalid requests must fail before even reading storage")
			assert.Len(t, h.instances.all(), 1, "only the parent row exists")
			assert.Empty(t, h.enqueuedIDs())
		})
	}
}

func TestStartGroupTask_IdempotentRecallCreatesAndEnqueuesOnce(t *testing.T) {
	for _, kind := range []instance.GroupTaskKind{instance.GroupTaskKindBranch, instance.GroupTaskKindWorkflow} {
		t.Run(string(kind), func(t *testing.T) {
			h := newGroupTaskHarness(t)
			owner := uuid.New()
			h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
			parent := h.seedParent(t, owner, 0)
			req := gtBranchReq(parent)
			if kind == instance.GroupTaskKindWorkflow {
				req = gtWorkflowReq(parent)
			}

			require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
			require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req), "a re-call (crash recovery, replay) is not an error")

			assert.Len(t, h.instances.all(), 2, "parent + exactly one task row")
			assert.Equal(t, []uuid.UUID{req.TaskInstanceID}, h.enqueuedIDs(), "never enqueued twice")
			assert.Equal(t, 1, h.qm.notifyCount(), "a still-queued row is re-offered to the queue evaluator")
			assert.Len(t, h.bus.eventsOfType(req.TaskInstanceID, "instance.created"), 1)
		})
	}
}

func TestStartGroupTask_RecallForAnAlreadyRunningTaskIsANoOp(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)
	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
	require.NoError(t, h.instances.UpdateState(context.Background(), req.TaskInstanceID, instance.StateRunning))

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))
	assert.Equal(t, 0, h.qm.notifyCount(), "a row that already left the queue is not re-offered")
	assert.Equal(t, instance.StateRunning, h.instances.get(req.TaskInstanceID).State)
}

func TestStartGroupTask_ReservedIDBelongingToAnotherTaskIsRefused(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	req := gtBranchReq(parent)
	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	other := req
	other.TaskKey = "b"
	other.BranchEntry = "b1"
	err := h.engine.StartGroupTask(ctxAs(owner), other)
	require.ErrorIs(t, err, engine.ErrInvalidGroupTaskRequest)
	assert.Equal(t, "a", *h.instances.get(req.TaskInstanceID).GroupTaskKey, "the existing row is untouched")
	assert.Len(t, h.enqueuedIDs(), 1)
}

// ---------- regression: legacy entry points are unchanged ----------

func TestTrigger_AfterRefactor_RootRunUnchanged(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))

	id, started, err := h.engine.Trigger(ctxAs(owner), "child-wf", map[string]any{"repo": "r"}, "normal")
	require.NoError(t, err)
	assert.False(t, started)
	assert.NotEqual(t, uuid.Nil, id, "a trigger mints a fresh id")
	inst := h.instances.get(id)
	require.NotNil(t, inst)
	assert.True(t, inst.IsRoot())
	assert.Equal(t, instance.SpawnMode(""), inst.SpawnMode)
	assert.Nil(t, inst.GroupRunID)
	assert.Nil(t, inst.GroupTaskKey)
	assert.Equal(t, instance.GroupTaskKind(""), inst.GroupTaskKind)
	assert.Equal(t, 0, inst.SpawnDepth)
	assert.Equal(t, []uuid.UUID{id}, h.enqueuedIDs())
	assert.Len(t, h.bus.eventsOfType(id, "instance.created"), 1)
}

func TestSpawn_AfterRefactor_LegacyLinkUnchanged(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	h.repo.put(workflow.Private(owner), gtChildWorkflow("child-wf"))
	parent := h.seedParent(t, owner, 4)

	childID, err := h.engine.Spawn(ctxAs(owner), engine.SpawnRequest{
		ChildWorkflow: "child-wf", Variables: map[string]any{"repo": "r"}, Priority: "normal",
		Link: instance.SpawnLink{ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeWaitForChild},
	})
	require.NoError(t, err)
	child := h.instances.get(childID)
	require.NotNil(t, child)
	assert.Equal(t, instance.SpawnModeWaitForChild, child.SpawnMode)
	assert.Equal(t, 5, child.SpawnDepth)
	assert.Nil(t, child.GroupRunID, "a legacy spawn carries no group linkage")
	assert.Nil(t, child.GroupTaskKey)
	assert.Equal(t, instance.GroupTaskKind(""), child.GroupTaskKind)
}

// ---------- model pins (D1) ----------

// A branch task runs the parent's own definition, so it inherits the parent's
// exact pin record: models, pinned-only routes and their connections. A model
// advancement after the parent started must not reach its branches.
func TestStartGroupTask_BranchKind_InheritsTheParentsPins(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0, func(p *instance.Instance) {
		p.ModelPins = map[string]string{"worker": "model-X"}
		p.ModelPinRoutes = map[string]string{"pinned": "azure"}
		p.ModelPinRouteConnections = map[string]string{"pinned": "az-conn"}
		p.ModelPinsVersion = instance.ModelPinsVersionSnapshot
	})
	req := gtBranchReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	child := h.rowAtEnqueue(req.TaskInstanceID)
	require.NotNil(t, child)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, child.ModelPinsVersion)
	assert.Equal(t, map[string]string{"worker": "model-X"}, child.ModelPins, "not the snapshotter's current pinned-worker")
	assert.Equal(t, parent.ModelPinRoutes, child.ModelPinRoutes)
	assert.Equal(t, parent.ModelPinRouteConnections, child.ModelPinRouteConnections)

	// Independent copies: a later write to the child cannot reach the parent.
	child.ModelPins["worker"] = "mutated"
	assert.Equal(t, "model-X", parent.ModelPins["worker"])
}

// EDGE — a legacy (pre-pin) parent has no record to inherit. Its branch is a new
// instance, so it captures a fresh v1 snapshot like any other trigger.
func TestStartGroupTask_BranchKind_LegacyParentCapturesFreshPins(t *testing.T) {
	h := newGroupTaskHarness(t)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0)
	require.Equal(t, 0, parent.ModelPinsVersion)
	req := gtBranchReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	child := h.rowAtEnqueue(req.TaskInstanceID)
	require.NotNil(t, child)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, child.ModelPinsVersion)
	assert.NotNil(t, child.ModelPins, "a v1 snapshot is a non-nil map even with no aliases")
}

// A group task created while its parent is paused (the launch was in flight
// when the pause cascade walked the parent's children) starts cascade-paused
// on behalf of the pause holding the parent, as a spawned child does.
func TestStartGroupTask_ParentPaused_TaskStartsCascadePaused(t *testing.T) {
	h := newGroupTaskHarness(t)
	links := newFakeSpawnLinks(func(id uuid.UUID) instance.State {
		inst, err := h.instances.Get(context.Background(), id)
		if err != nil {
			return instance.StateFailed
		}
		return inst.State
	})
	links.setState = func(id uuid.UUID, st instance.State) {
		_ = h.instances.UpdateState(context.Background(), id, st)
	}
	h.engine.SetSpawnLinks(links)
	owner := uuid.New()
	parent := h.seedParent(t, owner, 0, func(p *instance.Instance) { p.State = instance.StatePaused })
	req := gtBranchReq(parent)

	require.NoError(t, h.engine.StartGroupTask(ctxAs(owner), req))

	got, err := h.instances.Get(context.Background(), req.TaskInstanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StatePaused, got.State)
	by, err := links.CascadePausedBy(context.Background(), req.TaskInstanceID)
	require.NoError(t, err)
	require.NotNil(t, by)
	assert.Equal(t, parent.ID, *by)
}
