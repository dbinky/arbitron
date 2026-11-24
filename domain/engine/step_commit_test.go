package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memCommitter is an in-memory StepCommitter over the shared mock repositories.
// Advance commits write the execution, variables and (nonterminal) continuation
// and close the invocation; suspend commits only record. It validates every
// commit with StepCommit.Validate, like the SQLite repository does.
type memCommitter struct {
	mu       sync.Mutex
	inst     *mockInstanceRepo
	execs    *mockNodeExecutionRepo
	invs     engine.InvocationStore
	commits  map[uuid.UUID]engine.StepCommit
	order    []engine.StepCommit
	failNode string
	failMode engine.StepCommitMode
	failErr  error
}

func newMemCommitter(f *testFixtures, invs engine.InvocationStore) *memCommitter {
	return &memCommitter{inst: f.instanceRepo, execs: f.nodeExecRepo, invs: invs, commits: map[uuid.UUID]engine.StepCommit{}}
}

func (m *memCommitter) failWith(node string, mode engine.StepCommitMode, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNode, m.failMode, m.failErr = node, mode, err
}

func (m *memCommitter) CommitStep(ctx context.Context, c engine.StepCommit) error {
	if err := c.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	fail := m.failErr != nil && (m.failNode == "" || m.failNode == c.NodeID) && (m.failMode == "" || m.failMode == c.Mode)
	failErr := m.failErr
	m.mu.Unlock()
	if fail {
		return failErr
	}
	if c.Mode == engine.StepCommitAdvance {
		if err := m.execs.Create(ctx, c.Execution); err != nil {
			return err
		}
		if err := m.inst.UpdateVariables(ctx, c.RunID, c.Variables); err != nil {
			return err
		}
		if c.NextNodeID != "" {
			if err := m.inst.UpdateCurrentNode(ctx, c.RunID, c.NextNodeID); err != nil {
				return err
			}
			if err := m.invs.Commit(ctx, c.InvocationID); err != nil {
				return err
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.Mode == engine.StepCommitAdvance {
		m.commits[c.InvocationID] = c
	}
	m.order = append(m.order, c)
	return nil
}

func (m *memCommitter) CommittedResult(_ context.Context, invocationID uuid.UUID) (*engine.StepCommit, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.commits[invocationID]
	if !ok {
		return nil, false, nil
	}
	cp := c
	return &cp, true, nil
}

func (m *memCommitter) committed() []engine.StepCommit {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]engine.StepCommit(nil), m.order...)
}

func TestStepCommit_ValidateDistinguishesModes(t *testing.T) {
	run, inv, other, child := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := &instance.NodeExecution{ID: uuid.New(), InstanceID: run, NodeID: "A", Attempt: 1, Status: instance.NodeStatusSucceeded}
	prep := engine.StepCompletion{JobFence: testJobFence(1)}
	advance := engine.StepCommit{Mode: engine.StepCommitAdvance, RunID: run, InvocationID: inv, NodeID: "A", Execution: exec, NextNodeID: "B"}
	suspend := engine.StepCommit{Mode: engine.StepCommitSuspend, RunID: run, InvocationID: inv, NodeID: "A",
		Completions: []engine.StepCompletion{prep},
		Suspension:  &engine.StepSuspension{Kind: "spawn_workflow", ChildLinkID: &child, State: map[string]any{"childInstanceId": child.String()}}}
	require.NoError(t, advance.Validate())
	require.NoError(t, suspend.Validate())

	withExec := func(mod func(*instance.NodeExecution)) engine.StepCommit {
		x := *exec
		mod(&x)
		c := advance
		c.Execution = &x
		return c
	}
	bad := map[string]engine.StepCommit{
		"zero run":                          func() engine.StepCommit { c := advance; c.RunID = uuid.Nil; return c }(),
		"unknown mode":                      func() engine.StepCommit { c := advance; c.Mode = ""; return c }(),
		"advance without execution":         func() engine.StepCommit { c := advance; c.Execution = nil; return c }(),
		"advance execution of another run":  withExec(func(x *instance.NodeExecution) { x.InstanceID = other }),
		"advance execution of another node": withExec(func(x *instance.NodeExecution) { x.NodeID = "Z" }),
		"advance with a failed execution":   withExec(func(x *instance.NodeExecution) { x.Status = instance.NodeStatusFailed }),
		"advance with a suspension":         func() engine.StepCommit { c := advance; c.Suspension = suspend.Suspension; return c }(),
		"suspend with an execution":         func() engine.StepCommit { c := suspend; c.Execution = exec; return c }(),
		"suspend that advances":             func() engine.StepCommit { c := suspend; c.NextNodeID = "B"; return c }(),
		"suspend without receipts":          func() engine.StepCommit { c := suspend; c.Completions = nil; return c }(),
		"suspend without suspension state":  func() engine.StepCommit { c := suspend; c.Suspension = nil; return c }(),
		"duplicate job in one commit": func() engine.StepCommit {
			c := advance
			c.Completions = []engine.StepCompletion{prep, prep}
			return c
		}(),
		"workspace receipt of another run": func() engine.StepCommit {
			c := advance
			c.Completions = []engine.StepCompletion{{JobFence: testJobFence(2), Workspace: &engine.WorkspaceCompletion{
				Ref: testCatalogRef(other, 2), Resource: testRunResource(other)}}}
			return c
		}(),
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, c.Validate(), engine.ErrInvalidStepCompletion)
		})
	}
}

func TestEngine_NodeExecutionCreateErrorFailsStep(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	f.nodeExecRepo.mu.Lock()
	f.nodeExecRepo.createErr = errors.New("database is locked")
	f.nodeExecRepo.mu.Unlock()

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed, instance.StateCompleted)
	require.Equal(t, instance.StateFailed, inst.State, "an unpersisted execution record must fail the step")
	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "A", *inst.FailedNodeID)
	require.Len(t, f.executor.getCalls(), 1, "the run must not advance past a step it could not record")

	evs := f.eventBus.eventsOfType(id, engine.EventTypeNodeFailed)
	require.Len(t, evs, 1, "the persistence failure is visible as node.failed")
	msg, _ := evs[0].Data.(map[string]any)["error"].(string)
	assert.Contains(t, msg, engine.ErrStepPersistence.Error())
	assert.Contains(t, msg, "database is locked")
}

func TestEngine_StepCommit_EverySuccessCommitsOnce(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	mc := newMemCommitter(f, invs)
	f.engine.SetStepCommitter(mc)
	job := testJobFence(1)
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		placements, _ := engine.PlacementSinkFromContext(ctx)
		placements.Record(engine.PlacementFact{JobID: job.JobID, RunnerID: job.RunnerID, Generation: job.Generation, Outcome: "completed", At: time.Now()})
		receipts, ok := engine.StepCompletionSinkFromContext(ctx)
		if !ok {
			return nil, errors.New("no completion sink on the attempt context")
		}
		if err := receipts.Record(engine.StepCompletion{JobFence: job}); err != nil {
			return nil, err
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	commits := mc.committed()
	require.Len(t, commits, 3)
	wantNext := map[string]string{"A": "B", "B": "C", "C": ""}
	for i, node := range []string{"A", "B", "C"} {
		c := commits[i]
		assert.Equal(t, engine.StepCommitAdvance, c.Mode)
		assert.Equal(t, node, c.NodeID)
		assert.Equal(t, id, c.RunID)
		assert.Equal(t, wantNext[node], c.NextNodeID)
		require.NotNil(t, c.Execution)
		assert.Equal(t, instance.NodeStatusSucceeded, c.Execution.Status)
		assert.Contains(t, c.Variables, node, "the commit carries the node's output")
		inv, err := invs.Get(context.Background(), c.InvocationID)
		require.NoError(t, err)
		assert.NotNil(t, inv.CommittedAt, "node %s's visit is closed once the run advanced or completed", node)
	}
	assert.Equal(t, []engine.StepCompletion{{JobFence: job}}, commits[0].Completions, "the validated attempt's receipts reach the commit")
	assert.Empty(t, commits[1].Completions)
	f.nodeExecRepo.mu.Lock()
	n := len(f.nodeExecRepo.executions)
	f.nodeExecRepo.mu.Unlock()
	assert.Equal(t, 3, n, "exactly one record per step: the engine did not also call Create")
}

func TestEngine_StepCommit_FailureFailsStepAndPersistsNothing(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	mc := newMemCommitter(f, invs)
	mc.failWith("B", "", errors.New("disk I/O error"))
	f.engine.SetStepCommitter(mc)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed, instance.StateCompleted)
	require.Equal(t, instance.StateFailed, inst.State)
	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "B", *inst.FailedNodeID)
	assert.NotContains(t, inst.Variables, "B", "a failed commit must not publish the step's output")
	for _, c := range f.executor.getCalls() {
		assert.NotEqual(t, "C", c.NodeID, "the run must not advance past an uncommitted step")
	}
	f.nodeExecRepo.mu.Lock()
	for _, ex := range f.nodeExecRepo.executions {
		assert.NotEqual(t, "B", ex.NodeID, "no execution record may exist for the uncommitted step")
	}
	f.nodeExecRepo.mu.Unlock()
	inv, err := invs.Begin(context.Background(), id, "B")
	require.NoError(t, err)
	assert.Nil(t, inv.CommittedAt, "B's visit stays open")
	require.Len(t, f.eventBus.eventsOfType(id, engine.EventTypeNodeFailed), 1)
}

func TestEngine_DispatchedWorkspaceMissingReceiptFailsClosed(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	mc := newMemCommitter(f, invs)
	f.engine.SetStepCommitter(mc)
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		// A dispatched job finished but no receipt was recorded for it.
		placements, _ := engine.PlacementSinkFromContext(ctx)
		placements.Record(engine.PlacementFact{JobID: uuid.New(), RunnerID: "builtin", Generation: 1, Outcome: "completed", At: time.Now()})
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed, instance.StateCompleted)
	require.Equal(t, instance.StateFailed, inst.State)
	assert.Equal(t, "A", *inst.FailedNodeID)
	assert.Empty(t, mc.committed(), "nothing is committed without every receipt")
	require.Len(t, f.executor.getCalls(), 1)
	f.nodeExecRepo.mu.Lock()
	assert.Empty(t, f.nodeExecRepo.executions, "no successful execution is persisted")
	f.nodeExecRepo.mu.Unlock()
	evs := f.eventBus.eventsOfType(id, engine.EventTypeNodeFailed)
	require.Len(t, evs, 1)
	msg, _ := evs[0].Data.(map[string]any)["error"].(string)
	assert.Contains(t, msg, engine.ErrInvalidStepCompletion.Error())
}

// recordingReleaser stands in for P3 T6's StepDispatcher.ReleaseCompletions.
type recordingReleaser struct {
	mu     sync.Mutex
	calls  [][]engine.StepCompletion
	causes []error
	err    error
}

func (r *recordingReleaser) ReleaseCompletions(_ context.Context, cs []engine.StepCompletion, cause error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, cs)
	r.causes = append(r.causes, cause)
	return r.err
}

func (r *recordingReleaser) snapshot() ([][]engine.StepCompletion, []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]engine.StepCompletion(nil), r.calls...), append([]error(nil), r.causes...)
}

func recordReceipt(ctx context.Context, job engine.JobFenceRef) error {
	sink, ok := engine.StepCompletionSinkFromContext(ctx)
	if !ok {
		return errors.New("no completion sink on the attempt context")
	}
	return sink.Record(engine.StepCompletion{JobFence: job})
}

func releaserFixture(t *testing.T) (*testFixtures, *memCommitter, *recordingReleaser) {
	t.Helper()
	f := newTestFixtures()
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	mc := newMemCommitter(f, invs)
	f.engine.SetStepCommitter(mc)
	rel := &recordingReleaser{}
	f.engine.SetStepCompletionReleaser(rel)
	return f, mc, rel
}

func TestEngine_ValidationFailureReleasesAttemptReceipts(t *testing.T) {
	f, mc, rel := releaserFixture(t)
	f.workflowRepo.add(linearWorkflow())
	jobA, jobB := testJobFence(1), testJobFence(1)
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		if err := recordReceipt(ctx, jobA); err != nil {
			return nil, err
		}
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})
	f.executor.onNode("B", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		if err := recordReceipt(ctx, jobB); err != nil {
			return nil, err
		}
		return nil, errors.New(`script "B": stdout is not valid JSON`) // transport succeeded, validation failed
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed, instance.StateCompleted)
	require.Equal(t, instance.StateFailed, inst.State)
	assert.Equal(t, "B", *inst.FailedNodeID)

	calls, causes := rel.snapshot()
	require.Len(t, calls, 1, "only the failed attempt is released; a committed attempt never is")
	assert.Equal(t, []engine.StepCompletion{{JobFence: jobB}}, calls[0], "exactly that attempt's receipts")
	assert.ErrorContains(t, causes[0], "not valid JSON", "the release carries the failure cause")
	require.Len(t, mc.committed(), 1)
	assert.Equal(t, []engine.StepCompletion{{JobFence: jobA}}, mc.committed()[0].Completions)
}

func TestEngine_StepCommitFailureReleasesAttemptReceipts(t *testing.T) {
	f, mc, rel := releaserFixture(t)
	f.workflowRepo.add(linearWorkflow())
	mc.failWith("A", "", errors.New("disk I/O error"))
	jobA := testJobFence(1)
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		if err := recordReceipt(ctx, jobA); err != nil {
			return nil, err
		}
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	calls, causes := rel.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, []engine.StepCompletion{{JobFence: jobA}}, calls[0])
	assert.ErrorContains(t, causes[0], "disk I/O error")
}

func TestEngine_MissingReleaserFailsClosed(t *testing.T) {
	f, _, _ := releaserFixture(t)
	f.engine.SetStepCompletionReleaser(nil)
	wf := singleNodeWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "retry", Retries: 2}
	f.workflowRepo.add(wf)
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		if err := recordReceipt(ctx, testJobFence(1)); err != nil {
			return nil, err
		}
		return nil, errors.New("provider failed")
	})
	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Len(t, f.executor.getCalls(), 1, "unreleased receipts prohibit a retry")
	evs := f.eventBus.eventsOfType(id, engine.EventTypeNodeFailed)
	require.Len(t, evs, 1)
	assert.Contains(t, evs[0].Data.(map[string]any)["error"], engine.ErrStepPersistence.Error())
}

func TestEngine_ReleaseFailureIsStepPersistence(t *testing.T) {
	f, _, rel := releaserFixture(t)
	rel.err = errors.New("ledger unavailable")
	wf := singleNodeWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "retry", Retries: 2}
	f.workflowRepo.add(wf)
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		if err := recordReceipt(ctx, testJobFence(1)); err != nil {
			return nil, err
		}
		return nil, errors.New("provider 500")
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Len(t, f.executor.getCalls(), 1, "a release failure never triggers another provider retry")
	evs := f.eventBus.eventsOfType(id, engine.EventTypeNodeFailed)
	require.Len(t, evs, 1)
	msg, _ := evs[0].Data.(map[string]any)["error"].(string)
	assert.Contains(t, msg, engine.ErrStepPersistence.Error())
	assert.Contains(t, msg, "ledger unavailable")
}

func TestEngine_SuspendMissingChildLinkReleasesReceipts(t *testing.T) {
	f, mc, rel := releaserFixture(t)
	f.workflowRepo.add(awaitedSpawnWorkflow())
	f.engine.RegisterExecutor(workflow.NodeTypeSpawnWorkflow, f.executor)
	job := testJobFence(1)
	f.executor.onNode("S", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		if err := recordReceipt(ctx, job); err != nil {
			return nil, err
		}
		return &engine.NodeResult{IsWait: true, Output: map[string]any{"spawnMode": "wait_for_child"}}, nil
	})
	id, _, err := f.engine.Trigger(context.Background(), "await-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Empty(t, mc.committed())
	calls, causes := rel.snapshot()
	require.Equal(t, [][]engine.StepCompletion{{{JobFence: job}}}, calls)
	require.Len(t, causes, 1)
	assert.ErrorIs(t, causes[0], engine.ErrInvalidStepCompletion)
}

// fakeAwaitedSpawn models the durable child-link short circuit on re-entry.
type fakeAwaitedSpawn struct {
	mu                         sync.Mutex
	insts                      *mockInstanceRepo
	prepJob                    engine.JobFenceRef
	links                      map[uuid.UUID]uuid.UUID
	finished                   map[uuid.UUID]bool
	entries, prepared, spawned int
}

func newFakeAwaitedSpawn(f *testFixtures) *fakeAwaitedSpawn {
	return &fakeAwaitedSpawn{insts: f.instanceRepo, prepJob: testJobFence(1), links: map[uuid.UUID]uuid.UUID{}, finished: map[uuid.UUID]bool{}}
}
func (s *fakeAwaitedSpawn) Execute(ctx context.Context, _ workflow.Node, _ *variable.VariableStore, _ string) (*engine.NodeResult, error) {
	parent, _ := engine.InstanceIDFromContext(ctx)
	s.mu.Lock()
	s.entries++
	child, linked := s.links[parent]
	done := s.finished[parent]
	s.mu.Unlock()
	if linked {
		out := map[string]any{"childInstanceId": child.String(), "spawnMode": "wait_for_child"}
		if done {
			out["childState"] = "completed"
			return &engine.NodeResult{OutputPort: "success", Output: out}, nil
		}
		return &engine.NodeResult{IsWait: true, Output: out}, nil
	}
	receipts, ok := engine.StepCompletionSinkFromContext(ctx)
	if !ok {
		return nil, errors.New("no completion sink on the attempt context")
	}
	if err := receipts.Record(engine.StepCompletion{JobFence: s.prepJob}); err != nil {
		return nil, err
	}
	child = uuid.New()
	s.mu.Lock()
	s.prepared++
	s.spawned++
	s.links[parent] = child
	s.mu.Unlock()
	if err := s.insts.UpdateState(ctx, parent, instance.StateWaiting); err != nil {
		return nil, err
	}
	return &engine.NodeResult{IsWait: true, Output: map[string]any{"childInstanceId": child.String(), "spawnMode": "wait_for_child"}}, nil
}
func (s *fakeAwaitedSpawn) finish(parent uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished[parent] = true
}
func (s *fakeAwaitedSpawn) child(parent uuid.UUID) uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.links[parent]
}
func (s *fakeAwaitedSpawn) counts() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries, s.prepared, s.spawned
}
func awaitedSpawnWorkflow() *workflow.Workflow {
	return &workflow.Workflow{Name: "await-wf", Nodes: []workflow.Node{{ID: "S", Type: workflow.NodeTypeSpawnWorkflow, Entrypoint: true}, {ID: "D", Type: workflow.NodeTypeScript}}, Edges: []workflow.Edge{{Source: "S", Target: "D", Output: "success"}}}
}
func suspendFixture(t *testing.T) (*testFixtures, *fakeAwaitedSpawn, engine.InvocationStore, *memCommitter) {
	t.Helper()
	f := newTestFixtures()
	f.workflowRepo.add(awaitedSpawnWorkflow())
	spawn := newFakeAwaitedSpawn(f)
	f.engine.RegisterExecutor(workflow.NodeTypeSpawnWorkflow, spawn)
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	mc := newMemCommitter(f, invs)
	f.engine.SetStepCommitter(mc)
	return f, spawn, invs, mc
}
func TestEngine_SuspendCommitReleasesSuboperationSlotKeepsInvocationOpen(t *testing.T) {
	ctx := context.Background()
	f, spawn, invs, mc := suspendFixture(t)
	id, _, err := f.engine.Trigger(ctx, "await-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)
	require.Eventually(t, func() bool { return len(mc.committed()) == 1 }, 5*time.Second, 10*time.Millisecond, "the wait must commit the preparation receipt")
	s := mc.committed()[0]
	assert.Equal(t, engine.StepCommitSuspend, s.Mode)
	assert.Equal(t, "S", s.NodeID)
	assert.Nil(t, s.Execution)
	assert.Empty(t, s.NextNodeID)
	assert.Equal(t, []engine.StepCompletion{{JobFence: spawn.prepJob}}, s.Completions)
	require.NotNil(t, s.Suspension)
	assert.Equal(t, string(workflow.NodeTypeSpawnWorkflow), s.Suspension.Kind)
	require.NotNil(t, s.Suspension.ChildLinkID)
	assert.Equal(t, spawn.child(id), *s.Suspension.ChildLinkID)
	inv, err := invs.Get(ctx, s.InvocationID)
	require.NoError(t, err)
	assert.Nil(t, inv.CommittedAt)
	inst, err := f.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "S", *inst.CurrentNodeID)
	spawn.finish(id)
	require.NoError(t, f.engine.StartInstance(ctx, id))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	commits := mc.committed()
	require.Len(t, commits, 3)
	assert.Equal(t, engine.StepCommitAdvance, commits[1].Mode)
	assert.Equal(t, s.InvocationID, commits[1].InvocationID)
	assert.Empty(t, commits[1].Completions)
	assert.Equal(t, "D", commits[1].NextNodeID)
	_, prepared, spawned := spawn.counts()
	assert.Equal(t, 1, prepared)
	assert.Equal(t, 1, spawned)
}
func TestEngine_SuspendRestartReusesChildAndPreparedInputs(t *testing.T) {
	ctx := context.Background()
	f, spawn, invs, mc := suspendFixture(t)
	id, _, err := f.engine.Trigger(ctx, "await-wf", nil, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(mc.committed()) == 1 }, 5*time.Second, 10*time.Millisecond)
	eng2 := restartedEngine(f, map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeSpawnWorkflow: spawn, workflow.NodeTypeScript: f.executor}, invs)
	eng2.SetStepCommitter(mc)
	require.NoError(t, eng2.StartInstance(ctx, id))
	require.Eventually(t, func() bool { e, _, _ := spawn.counts(); return e == 2 }, 5*time.Second, 10*time.Millisecond)
	_, prepared, spawned := spawn.counts()
	assert.Equal(t, 1, prepared)
	assert.Equal(t, 1, spawned)
	require.Len(t, mc.committed(), 1)
	assert.Equal(t, spawn.child(id), *mc.committed()[0].Suspension.ChildLinkID)
}
func TestEngine_SuspendCommitFailureCreatesNoDuplicateChild(t *testing.T) {
	ctx := context.Background()
	f, spawn, _, mc := suspendFixture(t)
	mc.failWith("", engine.StepCommitSuspend, errors.New("disk I/O error"))
	id, _, err := f.engine.Trigger(ctx, "await-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, "S", *inst.FailedNodeID)
	require.Len(t, f.eventBus.eventsOfType(id, engine.EventTypeNodeFailed), 1)
	assert.Empty(t, mc.committed())
	mc.failWith("", "", nil)
	require.NoError(t, f.engine.StartInstance(ctx, id))
	require.Eventually(t, func() bool { e, _, _ := spawn.counts(); return e == 2 }, 5*time.Second, 10*time.Millisecond)
	_, prepared, spawned := spawn.counts()
	assert.Equal(t, 1, prepared)
	assert.Equal(t, 1, spawned)
}

// suspendOnlyCommitter reports a suspension record for every invocation.
type suspendOnlyCommitter struct{ *memCommitter }

func (s suspendOnlyCommitter) CommittedResult(_ context.Context, inv uuid.UUID) (*engine.StepCommit, bool, error) {
	child := uuid.New()
	return &engine.StepCommit{Mode: engine.StepCommitSuspend, InvocationID: inv,
		Suspension: &engine.StepSuspension{Kind: "spawn_workflow", ChildLinkID: &child}}, true, nil
}

func TestEngine_ReplayExecutesSuspendedInvocation(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	f.engine.SetStepCommitter(suspendOnlyCommitter{newMemCommitter(f, invs)})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Len(t, f.executor.getCalls(), 1, "a suspension record is not a completion: the node re-enters and executes")
}

func TestEngine_StepCommit_NewLoopVisitExecutes(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "loop-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{{Source: "A", Target: "B"}, {Source: "B", Target: "A", Output: "again"}},
	})
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	mc := newMemCommitter(f, invs)
	f.engine.SetStepCommitter(mc)
	bVisits := 0
	f.executor.onNode("B", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		bVisits++
		if bVisits == 1 {
			return &engine.NodeResult{OutputPort: "again"}, nil
		}
		return &engine.NodeResult{OutputPort: "done"}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	var aRuns int
	for _, c := range f.executor.getCalls() {
		if c.NodeID == "A" {
			aRuns++
		}
	}
	assert.Equal(t, 2, aRuns, "the replay key is the invocation: a new loop visit still executes")
	assert.Len(t, mc.committed(), 4)
}
