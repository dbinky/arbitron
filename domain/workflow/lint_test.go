package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// interactiveNode returns a fully-configured interactive node (so the hard
// validation passes) with the given ID. The lint is independent of these
// config fields; they exist only so the same fixture can be fed to Validate
// to prove the advisory does not gate.
func interactiveNode(id string, entry bool) workflow.Node {
	return workflow.Node{
		ID:         id,
		Type:       workflow.NodeTypeInteractiveLLM,
		Entrypoint: entry,
		Config: workflow.NodeConfig{
			OrchestrationFile: "orch.md",
			TemplateFile:      "tmpl.md",
		},
	}
}

func transformNode(id string, entry bool) workflow.Node {
	return workflow.Node{
		ID:         id,
		Type:       workflow.NodeTypeTransform,
		Entrypoint: entry,
	}
}

// findingsWithCode filters a finding slice to those matching code.
func findingsWithCode(findings []workflow.ValidationError, code string) []workflow.ValidationError {
	var out []workflow.ValidationError
	for _, f := range findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

// Happy path: an interactive node sitting on a cycle is flagged exactly once.
func TestLintWorkflow_InteractiveOnCycle_Flagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "loop",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveNode("ask", false),
			transformNode("work", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
			{Source: "ask", Target: "work"},
			{Source: "work", Target: "ask"}, // back-edge: ask is on a cycle
		},
	}

	findings := workflow.LintWorkflow(wf)

	flagged := findingsWithCode(findings, "INTERACTIVE_IN_LOOP")
	require.Len(t, flagged, 1)
	assert.Equal(t, "ask", flagged[0].NodeID)
	assert.Equal(t, "warning", flagged[0].Severity)
}

// Success: a linear (acyclic) interactive node yields no finding.
func TestLintWorkflow_LinearInteractive_NotFlagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "linear",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveNode("ask", false),
			transformNode("done", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
			{Source: "ask", Target: "done"},
		},
	}

	findings := workflow.LintWorkflow(wf)

	assert.Empty(t, findingsWithCode(findings, "INTERACTIVE_IN_LOOP"))
}

// Failure-category assertion (load-bearing): a workflow that triggers the lint
// still PASSES Validate. The advisory is present in the lint channel but absent
// from the hard-error set, so the cyclic graph remains allowed.
func TestLintWorkflow_AdvisoryDoesNotGateValidate(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "loop",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveNode("ask", false),
			transformNode("work", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
			{Source: "ask", Target: "work"},
			{Source: "work", Target: "ask"},
		},
	}

	// The hard validation must pass: a cyclic graph with an interactive node
	// is allowed by design.
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.Empty(t, errs, "cyclic interactive graph must still pass Validate")

	// No INTERACTIVE_IN_LOOP entry may appear in the hard-error set.
	assert.Empty(t, findingsWithCode(errs, "INTERACTIVE_IN_LOOP"),
		"advisory must not appear in the gating error list")

	// But the advisory IS surfaced on the separate lint channel.
	flagged := findingsWithCode(workflow.LintWorkflow(wf), "INTERACTIVE_IN_LOOP")
	require.Len(t, flagged, 1)
	assert.Equal(t, "warning", flagged[0].Severity)
}

// Edge: a direct self-loop on an interactive node is flagged.
func TestLintWorkflow_SelfLoop_Flagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "selfloop",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveNode("ask", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
			{Source: "ask", Target: "ask"}, // self-edge
		},
	}

	flagged := findingsWithCode(workflow.LintWorkflow(wf), "INTERACTIVE_IN_LOOP")
	require.Len(t, flagged, 1)
	assert.Equal(t, "ask", flagged[0].NodeID)
}

// Edge: an interactive node on a longer multi-node cycle is flagged.
func TestLintWorkflow_MultiNodeCycle_Flagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "bigloop",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveNode("ask", false),
			transformNode("a", false),
			transformNode("b", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
			{Source: "ask", Target: "a"},
			{Source: "a", Target: "b"},
			{Source: "b", Target: "ask"}, // cycle ask -> a -> b -> ask
		},
	}

	flagged := findingsWithCode(workflow.LintWorkflow(wf), "INTERACTIVE_IN_LOOP")
	require.Len(t, flagged, 1)
	assert.Equal(t, "ask", flagged[0].NodeID)
}

// Edge: an interactive node sitting OFF a cycle (on a linear spur) is not
// flagged, even though a cycle exists elsewhere in the graph.
func TestLintWorkflow_InteractiveOffCycle_NotFlagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "spur",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			transformNode("a", false),
			transformNode("b", false),
			interactiveNode("ask", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "a"},
			{Source: "a", Target: "b"},
			{Source: "b", Target: "a"},   // cycle a <-> b (no interactive)
			{Source: "a", Target: "ask"}, // interactive on a spur off the cycle
		},
	}

	assert.Empty(t, findingsWithCode(workflow.LintWorkflow(wf), "INTERACTIVE_IN_LOOP"))
}

// Non-interactive node on a cycle is never flagged.
func TestLintWorkflow_NonInteractiveOnCycle_NotFlagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "loop",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			transformNode("a", false),
			transformNode("b", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "a"},
			{Source: "a", Target: "b"},
			{Source: "b", Target: "a"}, // cycle of transform nodes only
		},
	}

	assert.Empty(t, workflow.LintWorkflow(wf))
}

// Edge: no interactive nodes and no cycles each produce zero advisories.
func TestLintWorkflow_NoInteractiveNoCycle_Empty(t *testing.T) {
	// Cyclic graph, no interactive node.
	cyclic := &workflow.Workflow{
		Name: "cyclic",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			transformNode("a", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "a"},
			{Source: "a", Target: "entry"},
		},
	}
	assert.Empty(t, workflow.LintWorkflow(cyclic))

	// Acyclic graph with an interactive node.
	acyclic := &workflow.Workflow{
		Name: "acyclic",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveNode("ask", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
		},
	}
	assert.Empty(t, workflow.LintWorkflow(acyclic))
}

// interactiveAgentNode returns a fully-configured interactive_agent node so the
// hard validation passes; config fields exist only to satisfy validateNodeConfigs.
func interactiveAgentNode(id string, entry bool) workflow.Node {
	return workflow.Node{
		ID:         id,
		Type:       workflow.NodeTypeInteractiveAgent,
		Entrypoint: entry,
		Config: workflow.NodeConfig{
			OrchestrationFile: "orch.md",
			TemplateFile:      "tmpl.md",
		},
	}
}

// Happy path: an interactive_agent node sitting on a cycle is flagged exactly once.
func TestLintWorkflow_InteractiveAgentOnCycle_Flagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "agent-loop",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveAgentNode("ask", false),
			transformNode("work", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
			{Source: "ask", Target: "work"},
			{Source: "work", Target: "ask"}, // back-edge: ask is on a cycle
		},
	}

	findings := workflow.LintWorkflow(wf)

	flagged := findingsWithCode(findings, "INTERACTIVE_IN_LOOP")
	require.Len(t, flagged, 1)
	assert.Equal(t, "ask", flagged[0].NodeID)
	assert.Equal(t, "warning", flagged[0].Severity)
}

// A linear (acyclic) interactive_agent node yields no finding.
func TestLintWorkflow_LinearInteractiveAgent_NotFlagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "agent-linear",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveAgentNode("ask", false),
			transformNode("done", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask"},
			{Source: "ask", Target: "done"},
		},
	}

	assert.Empty(t, findingsWithCode(workflow.LintWorkflow(wf), "INTERACTIVE_IN_LOOP"))
}

// Multiple interactive nodes on the same cycle each produce their own finding.
func TestLintWorkflow_MultipleInteractiveOnCycle_EachFlagged(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "multiloop",
		Nodes: []workflow.Node{
			transformNode("entry", true),
			interactiveNode("ask1", false),
			interactiveNode("ask2", false),
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "ask1"},
			{Source: "ask1", Target: "ask2"},
			{Source: "ask2", Target: "ask1"}, // cycle containing both
		},
	}

	flagged := findingsWithCode(workflow.LintWorkflow(wf), "INTERACTIVE_IN_LOOP")
	require.Len(t, flagged, 2)

	ids := map[string]bool{}
	for _, f := range flagged {
		ids[f.NodeID] = true
		assert.Equal(t, "warning", f.Severity)
	}
	assert.True(t, ids["ask1"])
	assert.True(t, ids["ask2"])
}
