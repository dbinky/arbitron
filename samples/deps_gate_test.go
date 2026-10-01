package samples_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// A dependency gate must send every question a person has to answer to that
// person.
//
// Run 5e86d796 (2026-09-29): deps-assess listed 7 open decisions only a human
// could make — and classed all of them NON-BLOCKING because the design named a
// fallback. deps-gate is first-match, and its first condition was
// `deps-assess.blocking_count == 0 -> proceed`, so `unresolved_count: 7` was
// never even read. The plans required those decisions, the implementer stopped
// at 3 of 17 tasks, and the run ended done-blocked without resolve-deps — the
// one step built to ask the human — ever running.
//
// The rule below: the gate routes on deps-count.needs_resolution (BLOCKING plus
// DECISION gap lines, counted by a script that fails closed), never on a field
// the LLM may or may not emit.
func depsGateRoute(t *testing.T, wf *workflow.Workflow, needs, iteration float64, assess map[string]any) string {
	t.Helper()
	var gate workflow.Node
	for _, n := range wf.Nodes {
		if n.ID == "deps-gate" {
			gate = n
		}
	}
	if gate.ID == "" {
		t.Fatal("feature-pipeline has no deps-gate node")
	}
	vars := variable.NewStore()
	vars.SetNodeOutput("deps-assess", assess)
	vars.SetNodeOutput("deps-count", map[string]any{"needs_resolution": needs})
	if err := vars.SetInput("deps_resolution_iteration", iteration); err != nil {
		t.Fatal(err)
	}
	res, err := engine.NewConditionalExecutor().Execute(context.Background(), gate, vars, wf.Name)
	if err != nil {
		t.Fatal(err)
	}
	return res.OutputPort
}

func TestDepsGateSendsHumanDecisionsToResolve(t *testing.T) {
	wf := loadSample(t, "feature-pipeline")
	// What run 5e86d796's deps-assess actually reported.
	llmSaidNothingBlocks := map[string]any{
		"blocking_count": float64(0), "has_blocking": false,
		"unresolved_count": float64(7), "has_unresolved": true,
	}

	if got := depsGateRoute(t, wf, 7, 0, llmSaidNothingBlocks); got != "resolve" {
		t.Errorf("7 decisions, first pass: routed %q, want resolve — the human must be asked", got)
	}
	if got := depsGateRoute(t, wf, 7, 1, llmSaidNothingBlocks); got != "blocked" {
		t.Errorf("still unresolved after a resolve pass: routed %q, want blocked (no livelock)", got)
	}
	if got := depsGateRoute(t, wf, 0, 0, llmSaidNothingBlocks); got != "proceed" {
		t.Errorf("nothing needs a person: routed %q, want proceed", got)
	}
	// deps-count fails closed (needs_resolution=1) when deps-assess output is
	// unreadable; the gate must not proceed on an LLM field that is missing.
	if got := depsGateRoute(t, wf, 1, 0, map[string]any{}); got != "resolve" {
		t.Errorf("unreadable deps-assess output: routed %q, want resolve", got)
	}
}

// The counter must actually sit between the assessor and the gate.
func TestDepsCountRunsBetweenAssessAndGate(t *testing.T) {
	wf := loadSample(t, "feature-pipeline")
	next := map[string]string{}
	for _, e := range wf.Edges {
		next[e.Source] = e.Target
	}
	if next["deps-assess"] != "deps-count" || next["deps-count"] != "deps-gate" {
		t.Errorf("want deps-assess -> deps-count -> deps-gate, got deps-assess -> %q, deps-count -> %q",
			next["deps-assess"], next["deps-count"])
	}
}
