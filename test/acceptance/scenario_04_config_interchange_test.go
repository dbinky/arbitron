package acceptance

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
)

// graphShape is the binding-independent structure of a workflow: the set of node
// IDs→types, the set of edges, and the set of node template references. It is the
// canonical projection the #4 diff assertion compares — everything that lives in
// the graph, and nothing that lives in the external profile/connection registry.
type graphShape struct {
	Nodes     map[string]string `json:"nodes"`     // nodeID -> type
	Edges     []string          `json:"edges"`     // "source|output|target", sorted
	Templates map[string]string `json:"templates"` // nodeID -> templateFile
}

// shapeFromSnapshot projects a workflow snapshot (instance.WorkflowSnapshot or
// the on-disk workflow.json, both map[string]any) into its binding-independent
// graphShape. It deliberately reads ONLY node id/type/templateFile and edges —
// never model_profile / connection — so a difference in the external binding
// cannot register as a graph difference, and any difference it DOES report is a
// real change to node kinds, edges, or templates.
func shapeFromSnapshot(t *testing.T, snap map[string]any) graphShape {
	t.Helper()
	raw, err := json.Marshal(snap)
	require.NoError(t, err)

	var wf struct {
		Nodes []struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Config struct {
				TemplateFile string `json:"templateFile"`
			} `json:"config"`
		} `json:"nodes"`
		Edges []struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Output string `json:"output"`
		} `json:"edges"`
	}
	require.NoError(t, json.Unmarshal(raw, &wf))

	shape := graphShape{
		Nodes:     map[string]string{},
		Templates: map[string]string{},
	}
	for _, n := range wf.Nodes {
		shape.Nodes[n.ID] = n.Type
		if n.Config.TemplateFile != "" {
			shape.Templates[n.ID] = n.Config.TemplateFile
		}
	}
	for _, e := range wf.Edges {
		shape.Edges = append(shape.Edges, e.Source+"|"+e.Output+"|"+e.Target)
	}
	sort.Strings(shape.Edges)
	return shape
}

// TestAcceptance_04_ConfigInterchangeability proves spec §8 #4: the SAME workflow
// graph runs under both the Claude-subscription and open-weight configurations by
// changing ONLY profile references — no edits to node kinds, edges, or templates.
//
// LOAD-BEARING (design §6): the explicit structural-diff assertion. The workflow
// snapshot recorded on each instance is projected to its binding-independent
// graphShape (node ids/types, edges incl. the back-edge, template references) and
// the two projections are asserted byte-identical. The only thing that differs
// between the two runs is the external profile→connection binding, which is NOT
// part of the graph.
func TestAcceptance_04_ConfigInterchangeability(t *testing.T) {
	// --- Run A: Claude binding ---------------------------------------------
	runnerA := newFakeRunner()
	runnerA.on("--dangerously-skip-permissions", claudeAgentLines...)
	runnerA.withFallback(claudeAssessorLines(false)...)
	hA := newHarness(t, claudeBinding(runnerA))
	const guard = 4
	idChA := make(chan uuid.UUID, 1)
	runnerA.armGuard(guard, func() { _ = hA.engine.Cancel(<-idChA) })
	idA := hA.trigger(t, 2)
	idChA <- idA
	instA := hA.waitForTerminal(t, idA, 10*time.Second)
	require.Contains(t, []instance.State{instance.StateCancelled, instance.StateCompleted}, instA.State)

	// --- Run B: open-weight binding ----------------------------------------
	runnerB := newFakeRunner().withFallback("Editing.", "Done.")
	transportB := newFakeTransport(true)
	hB := newHarness(t, openWeightBinding(runnerB, transportB))
	resB := hB.runWorkedExample(t, 5)
	require.Equal(t, instance.StateCompleted, resB.instance.State,
		"the open-weight binding reaches the same loop structure and completes")

	// --- The mandatory config-diff assertion -------------------------------
	instARefetched, err := hA.instances.Get(context.Background(), idA)
	require.NoError(t, err)

	shapeA := shapeFromSnapshot(t, instARefetched.WorkflowSnapshot)
	shapeB := shapeFromSnapshot(t, resB.instance.WorkflowSnapshot)

	// EXPLICIT empty-diff: the two runs' graphs are structurally identical after
	// removing the profile/connection registry (which the projection never reads).
	assert.Equal(t, shapeA, shapeB,
		"the same graph (node kinds, edges incl. back-edge, templates) must run under both bindings")

	// And both match the on-disk workflow.json projection (the single source of
	// truth that neither binding edited).
	onDisk := shapeFromSnapshot(t, mustMarshalWorkflow(t, loadWorkedExampleGraph(t)))
	assert.Equal(t, onDisk, shapeA, "run A graph matches the committed workflow.json")
	assert.Equal(t, onDisk, shapeB, "run B graph matches the committed workflow.json")

	// Spot-check load-bearing graph facts survive the projection: the agent
	// entrypoint, the chat assessor, and the back-edge are all present and equal.
	assert.Equal(t, "agent_call", shapeA.Nodes["editor"])
	assert.Equal(t, "llm_call", shapeA.Nodes["assessor"])
	assert.Contains(t, shapeA.Edges, "loop-or-exit|loop|editor", "the back-edge is part of the shared graph")
	assert.Equal(t, "mission.md", shapeA.Templates["editor"])
	assert.Equal(t, "assessment.md", shapeA.Templates["assessor"])
}

// mustMarshalWorkflow round-trips a *workflow.Workflow through JSON into the
// map[string]any snapshot shape, matching how the engine snapshots a workflow.
func mustMarshalWorkflow(t *testing.T, wf any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}
