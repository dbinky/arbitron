package engine_test

import (
	"context"
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

// ---------------------------------------------------------------------------
// Loop-mechanics tests.
//
// These are CHARACTERIZATION tests over the engine's EXISTING primitives. They
// prove the native ralph loop runs on the engine's cyclic-edge behavior with no
// new engine code: the graph walker (engine.go:308, findNextNode at 735) follows
// a back-edge like any ordinary edge (no acyclicity check, no unique
// (instance,node) constraint), the real TransformExecutor re-executes on revisit
// (incrementing a counter), and the real ConditionalExecutor re-evaluates each
// pass (exiting on `done` or `counter >= max`). The counter persists in instance
// Variables and is visible in the VariableSnapshot trace.
//
// Loop graph (cyclic):
//
//	editor (agent_call, fake, entrypoint)
//	   -> bump (transform: iteration += 1)
//	   -> assessor (agent_call, fake; scripted done flag per pass)
//	   -> gate (conditional):
//	          done == true      -> exit
//	          iteration >= max  -> exit
//	          (default)         -> loop  ==> back-edge to editor
//	   exit -> finish (transform no-op, terminal: no outgoing edge => complete)
//	   loop -> editor
// ---------------------------------------------------------------------------

// scriptedAgentExecutor is a hermetic fake standing in for the agent_call /
// llm_call executors. Per node ID, it returns scripted outputs indexed by the
// pass count (how many times that node has been entered so far). It records the
// per-node call count so a test can assert the back-edge re-entered the node.
//
// It deliberately does NOT implement the interactive short-circuit (no
// GetByInstanceAndNode lookup): an agent_call re-executes on every revisit. This
// is the contrast called out in the plan — interactive would short-circuit, but
// agent_call/transform/conditional do not.
type scriptedAgentExecutor struct {
	mu sync.Mutex
	// scripts maps nodeID -> per-pass output. The output for pass i (0-based) is
	// scripts[nodeID][min(i, len-1)] so the last entry repeats for extra passes.
	scripts map[string][]map[string]any
	// counts maps nodeID -> number of times Execute was entered for that node.
	counts map[string]int
	// maxPasses, when > 0, is a SELF-IMPOSED in-fake guard for the no-exit test.
	// When any single node is entered more than maxPasses times the fake cancels
	// the run via the supplied cancel func. The engine itself never stops the
	// loop — this proves the engine adds no runaway guard.
	maxPasses int
	cancel    context.CancelFunc
	tripped   bool
}

func newScriptedAgentExecutor() *scriptedAgentExecutor {
	return &scriptedAgentExecutor{
		scripts: make(map[string][]map[string]any),
		counts:  make(map[string]int),
	}
}

func (e *scriptedAgentExecutor) script(nodeID string, outputs ...map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scripts[nodeID] = outputs
}

func (e *scriptedAgentExecutor) count(nodeID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.counts[nodeID]
}

func (e *scriptedAgentExecutor) Execute(_ context.Context, node workflow.Node, _ *variable.VariableStore, _ string) (*engine.NodeResult, error) {
	e.mu.Lock()
	pass := e.counts[node.ID]
	e.counts[node.ID]++

	// Self-imposed guard for the no-exit test: if the engine keeps looping past
	// maxPasses, the FAKE (not the engine) cancels the run context.
	if e.maxPasses > 0 && !e.tripped {
		total := e.counts[node.ID]
		if total > e.maxPasses {
			e.tripped = true
			cancel := e.cancel
			e.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return &engine.NodeResult{Output: map[string]any{}}, nil
		}
	}

	outputs := e.scripts[node.ID]
	e.mu.Unlock()

	if len(outputs) == 0 {
		return &engine.NodeResult{Output: map[string]any{}}, nil
	}
	idx := pass
	if idx >= len(outputs) {
		idx = len(outputs) - 1
	}
	return &engine.NodeResult{Output: outputs[idx]}, nil
}

// loopFixtures wires REAL TransformExecutor and ConditionalExecutor plus the
// scripted fake agent executor for the cyclic loop graph. It reuses the same
// in-memory repos / event bus / queue manager as the rest of the engine suite.
type loopFixtures struct {
	*testFixtures
	agent *scriptedAgentExecutor
}

func newLoopFixtures() *loopFixtures {
	base := newTestFixtures()
	agent := newScriptedAgentExecutor()

	// Real transform + conditional executors; fake agent for agent_call.
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeAgentCall:   agent,
		workflow.NodeTypeTransform:   engine.NewTransformExecutor(),
		workflow.NodeTypeConditional: engine.NewConditionalExecutor(),
	}

	eng := wireModelPins(engine.NewEngine(
		base.workflowRepo,
		base.instanceRepo,
		base.nodeExecRepo,
		base.snapshotRepo,
		base.userRepo,
		executors,
		base.eventBus,
		base.queueManager,
		&mockQueueRepo{instRepo: base.instanceRepo},
		"",
	))

	base.queueManager.mu.Lock()
	base.queueManager.enqueueFn = func(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	base.queueManager.mu.Unlock()

	base.engine = eng
	return &loopFixtures{testFixtures: base, agent: agent}
}

// loopWorkflow builds the cyclic ralph-style loop graph described above. The
// `iteration` counter is seeded numeric (input default 0) so increment succeeds;
// pass `seedIteration=false` to omit the seed (malformed-counter scenario).
func loopWorkflow(maxIterations float64, seedIteration bool) *workflow.Workflow {
	inputs := []workflow.InputField{}
	if seedIteration {
		inputs = append(inputs, workflow.InputField{
			Name: "iteration", Type: "number", Required: false, Default: float64(0),
		})
	}
	return &workflow.Workflow{
		Name:        "loop-wf",
		Description: "A cyclic ralph-style loop",
		Inputs:      inputs,
		Nodes: []workflow.Node{
			{ID: "editor", Type: workflow.NodeTypeAgentCall, Entrypoint: true},
			{ID: "bump", Type: workflow.NodeTypeTransform, Config: workflow.NodeConfig{
				Operations: []workflow.Operation{
					{Variable: "iteration", Operation: "increment", Value: float64(1)},
				},
			}},
			{ID: "assessor", Type: workflow.NodeTypeAgentCall},
			{ID: "gate", Type: workflow.NodeTypeConditional, Config: workflow.NodeConfig{
				Conditions: []workflow.Condition{
					{Field: "assessor.done", Operator: "eq", Value: true, Output: "exit"},
					{Field: "iteration", Operator: "gte", Value: maxIterations, Output: "exit"},
				},
				DefaultOutput: "loop",
			}},
			{ID: "finish", Type: workflow.NodeTypeTransform, Config: workflow.NodeConfig{
				Operations: []workflow.Operation{
					{Variable: "finished", Operation: "set", Value: true},
				},
			}},
		},
		Edges: []workflow.Edge{
			{Source: "editor", Target: "bump"},
			{Source: "bump", Target: "assessor"},
			{Source: "assessor", Target: "gate"},
			{Source: "gate", Target: "finish", Output: "exit"},
			{Source: "gate", Target: "editor", Output: "loop"}, // back-edge
		},
	}
}

// ---------------------------------------------------------------------------
// Happy path — multi-pass then exit on `done`.
// ---------------------------------------------------------------------------

func TestLoop_HappyPath_ExitsOnDone(t *testing.T) {
	f := newLoopFixtures()
	f.workflowRepo.add(loopWorkflow(100, true)) // high max so `done` wins

	// assessor: not done for two passes, then done on the third.
	f.agent.script("assessor",
		map[string]any{"done": false},
		map[string]any{"done": false},
		map[string]any{"done": true},
	)

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	// editor (the agent node) re-executed 3 times via the back-edge.
	assert.Equal(t, 3, f.agent.count("editor"), "editor should run once per pass")
	assert.Equal(t, 3, f.agent.count("assessor"), "assessor should run once per pass")

	// Counter incremented once per pass and persisted in instance Variables.
	assert.Equal(t, float64(3), inst.Variables["iteration"])
}

// ---------------------------------------------------------------------------
// Success — exit on `counter >= max` (not on `done`).
// ---------------------------------------------------------------------------

func TestLoop_ExitsOnCounterReachesMax(t *testing.T) {
	f := newLoopFixtures()
	f.workflowRepo.add(loopWorkflow(4, true)) // max_iterations = 4

	// assessor never reports done; only the counter condition can exit the loop.
	f.agent.script("assessor", map[string]any{"done": false})

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	// Exactly 4 passes: iteration reaches 4, the gte(4) condition fires, loop exits.
	assert.Equal(t, float64(4), inst.Variables["iteration"])
	assert.Equal(t, 4, f.agent.count("editor"), "loop should run exactly max passes")
	// Did not run forever within the test bound (waitForInstance reached Completed).
}

// ---------------------------------------------------------------------------
// Failure — counter not seeded / non-numeric -> transform errors, instance
// halts (the loop does NOT silently spin on a malformed counter).
// ---------------------------------------------------------------------------

func TestLoop_MalformedCounter_HaltsInsteadOfSpinning(t *testing.T) {
	f := newLoopFixtures()
	// seedIteration=false: `iteration` is never seeded, so applyIncrement errors
	// with "variable %q not found" on the first pass.
	f.workflowRepo.add(loopWorkflow(100, false))
	f.agent.script("assessor", map[string]any{"done": false})

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)
	// The transform node is the one that failed (default "halt" error behavior).
	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "bump", *inst.FailedNodeID)
	// editor ran exactly once (no infinite spin): we halted on the first transform.
	assert.Equal(t, 1, f.agent.count("editor"))
}

// ---------------------------------------------------------------------------
// Error — back-edge revisits the agent node (re-execution proof). A NEW
// NodeExecution record is created per pass (no unique (instance,node)
// constraint) and a per-pass VariableSnapshot is written for each revisit.
// ---------------------------------------------------------------------------

func TestLoop_BackEdgeReExecutesAgentNode(t *testing.T) {
	f := newLoopFixtures()
	f.workflowRepo.add(loopWorkflow(100, true))
	f.agent.script("assessor",
		map[string]any{"done": false},
		map[string]any{"done": false},
		map[string]any{"done": true},
	)

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	ctx := context.Background()

	// A new NodeExecution record per editor pass (3 total) — the back-edge
	// genuinely re-enters the node rather than treating it as already-done.
	execs, err := f.nodeExecRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	editorExecs := 0
	for _, ex := range execs {
		if ex.NodeID == "editor" {
			editorExecs++
		}
	}
	assert.Equal(t, 3, editorExecs, "one NodeExecution per editor revisit")

	// A per-pass VariableSnapshot is written for each editor revisit.
	snaps, err := f.snapshotRepo.ListByInstance(ctx, id)
	require.NoError(t, err)
	editorSnaps := 0
	for _, s := range snaps {
		if s.NodeID == "editor" {
			editorSnaps++
		}
	}
	assert.Equal(t, 3, editorSnaps, "one pre-execution VariableSnapshot per editor revisit")
}

// ---------------------------------------------------------------------------
// Edge case — counter persisted in instance Variables AND visible in the
// VariableSnapshot trace progressing 0 -> 1 -> 2 -> ... across passes
// (acceptance #5 at the engine level).
// ---------------------------------------------------------------------------

func TestLoop_CounterPersistedAndVisibleInTrace(t *testing.T) {
	f := newLoopFixtures()
	f.workflowRepo.add(loopWorkflow(100, true))
	f.agent.script("assessor",
		map[string]any{"done": false},
		map[string]any{"done": false},
		map[string]any{"done": true},
	)

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	// Persisted in instance Variables.
	assert.Equal(t, float64(3), inst.Variables["iteration"])

	// Visible in the ordered VariableSnapshot trace. Snapshots are captured
	// BEFORE each node executes, so the `bump` node's snapshots show the counter
	// progressing 0, 1, 2 across the three passes (the pre-increment value).
	ctx := context.Background()
	snaps, err := f.snapshotRepo.ListByInstance(ctx, id)
	require.NoError(t, err)

	var bumpCounter []float64
	for _, s := range snaps {
		if s.NodeID != "bump" {
			continue
		}
		v, ok := s.Variables["iteration"]
		require.True(t, ok, "iteration present in bump snapshot")
		n, ok := v.(float64)
		require.True(t, ok, "iteration is numeric in snapshot")
		bumpCounter = append(bumpCounter, n)
	}
	// Pre-increment counter values across the three passes.
	assert.Equal(t, []float64{0, 1, 2}, bumpCounter, "counter progresses across recorded passes")

	// The assessor (agent) snapshots show the POST-increment counter 1, 2, 3.
	var assessorCounter []float64
	for _, s := range snaps {
		if s.NodeID != "assessor" {
			continue
		}
		if n, ok := s.Variables["iteration"].(float64); ok {
			assessorCounter = append(assessorCounter, n)
		}
	}
	assert.Equal(t, []float64{1, 2, 3}, assessorCounter)
}

// ---------------------------------------------------------------------------
// Documented-risk note — NO auto-kill. A no-exit loop (conditions never fire,
// defaultOutput always loops) keeps producing node executions. It is bounded
// ONLY by a self-imposed guard inside the FAKE executor, which cancels the run
// context. This confirms the engine adds NO runaway guard (accepted MVP risk):
// the engine kept looping until the test stopped it.
// ---------------------------------------------------------------------------

func TestLoop_NoExitLoop_EngineDoesNotAutoKill(t *testing.T) {
	f := newLoopFixtures()
	// max is enormous and assessor never reports done => neither exit condition
	// ever fires; the gate always routes to `loop`.
	f.workflowRepo.add(loopWorkflow(1e9, true))
	f.agent.script("assessor", map[string]any{"done": false})

	// idCh hands the running instance id to the fake's cancel closure. It is
	// buffered so Trigger never blocks; the closure reads it (blocking) only once
	// the guard trips, by which point Trigger has long since published the id.
	idCh := make(chan uuid.UUID, 1)

	// Arm the fake's self-imposed guard: after the editor node has been entered
	// more than maxPasses times, the FAKE cancels the engine's run context (via
	// engine.Cancel). This keeps the test FAST and deterministic — we never
	// actually loop unboundedly.
	const guard = 6
	f.agent.mu.Lock()
	f.agent.maxPasses = guard
	f.agent.cancel = func() { _ = f.engine.Cancel(bgAdmin(), <-idCh) }
	f.agent.mu.Unlock()

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)
	idCh <- id

	// The instance is cancelled by the FAKE (via engine.Cancel), never by any
	// engine-internal iteration cap.
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCancelled)
	assert.Equal(t, instance.StateCancelled, inst.State)

	// The engine kept re-entering the loop body well past any plausible bound:
	// the editor ran at least the guard threshold's worth of passes before the
	// fake stopped it — proving no engine-side runaway guard kicked in.
	assert.GreaterOrEqual(t, f.agent.count("editor"), guard,
		"engine kept looping until the fake stopped it (no auto-kill)")
}
