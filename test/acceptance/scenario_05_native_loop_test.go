package acceptance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
)

// TestAcceptance_05_NativeLoop proves spec §8 #5: a workflow loop iterates
// multiple times and exits on its `conditional`, with the iteration counter
// persisted in instance variables AND visible in the trace.
//
// Requires: the engine's existing cyclic-edge behavior (no Phase 2/3 specifics).
// This re-asserts the engine-level loop proof end-to-end through the real
// agent_call + llm_call executors over the worked example.
//
// The scripted assessor (OpenRouter chat) reports done=false for the first two
// passes, then done=true on the third, so the loop runs exactly three passes and
// exits on the `assessor.done` condition of the loop-or-exit conditional.
func TestAcceptance_05_NativeLoop(t *testing.T) {
	runner := newFakeRunner().withFallback("Editing the top focus area.", "Done this pass.")
	// done: false, false, true → exit on the third assessment.
	transport := newScriptedTransport(false, false, true)
	h := newHarness(t, openWeightBinding(runner, transport))

	res := h.runWorkedExample(t, 10) // high max so the done-condition, not a cap, ends it
	require.Equal(t, instance.StateCompleted, res.instance.State)

	// The assessor (HTTP chat) was called once per pass: exactly three passes.
	assert.Len(t, transport.recorded(), 3, "the loop ran exactly three assess passes before exiting on done")

	// Counter persisted in instance Variables: incremented once per pass.
	require.Equal(t, float64(3), res.instance.Variables["iteration"],
		"the iteration counter is persisted in instance variables")

	// Counter visible in the ordered VariableSnapshot trace. Snapshots are
	// captured BEFORE each node executes, so the increment-counter node's
	// snapshots show the PRE-increment counter progressing 0, 1, 2 across passes.
	snaps, err := h.snapshots.ListByInstance(context.Background(), res.instance.ID)
	require.NoError(t, err)

	var bumpProgression []float64
	for _, s := range snaps {
		if s.NodeID != "increment-counter" {
			continue
		}
		n, ok := s.Variables["iteration"].(float64)
		require.True(t, ok, "iteration is numeric in the snapshot trace")
		bumpProgression = append(bumpProgression, n)
	}
	assert.Equal(t, []float64{0, 1, 2}, bumpProgression,
		"the counter progresses across passes in the trace (pre-increment values)")

	// The editor (agent) was re-entered once per pass via the back-edge: a new
	// NodeExecution record per pass (no unique (instance,node) constraint).
	execs, err := h.nodeExecs.ListByInstance(context.Background(), res.instance.ID)
	require.NoError(t, err)
	editorExecs := 0
	for _, ex := range execs {
		if ex.NodeID == "editor" {
			editorExecs++
		}
	}
	assert.Equal(t, 3, editorExecs, "the editor re-executed once per loop pass via the back-edge")
}

// TestAcceptance_05_NativeLoop_CounterCapExit exercises the second conditional
// branch: when `assessor.done` never fires, the loop must exit on
// `iteration >= max_iterations`. All prior loop tests exit on the done-branch;
// this is the only test that proves the gte (counter cap) branch is reachable
// end-to-end under a real engine run (spec §8 #5: "exits on its conditional").
func TestAcceptance_05_NativeLoop_CounterCapExit(t *testing.T) {
	runner := newFakeRunner().withFallback("Editing.", "Done.")
	// Assessor always reports done=false — the counter cap must fire instead.
	transport := newFakeTransport(false)
	h := newHarness(t, openWeightBinding(runner, transport))

	// max_iterations=2: the loop runs exactly 2 passes and exits on the cap.
	res := h.runWorkedExample(t, 2)
	require.Equal(t, instance.StateCompleted, res.instance.State,
		"the loop must complete via the counter cap even when assessor.done never fires")

	// Exactly 2 assess passes (one per pass; cap fires at iteration==2).
	assert.Len(t, transport.recorded(), 2,
		"the loop ran exactly 2 assess passes before the counter cap fired")

	// The final counter equals max_iterations: the gte branch fired.
	assert.Equal(t, float64(2), res.instance.Variables["iteration"],
		"iteration reached max_iterations (2): the counter cap ended the loop, not done=true")

	// assessor.done is false in the final persisted output, confirming the
	// done-exit branch did NOT fire.
	assessor, ok := res.instance.Variables["assessor"].(map[string]any)
	require.True(t, ok, "assessor structured output must be a map")
	assert.Equal(t, false, assessor["done"],
		"assessor.done is false: the counter cap fired, not the done-true branch")
}
