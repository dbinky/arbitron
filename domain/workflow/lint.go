package workflow

// LintWorkflow runs advisory (non-gating) lints over a workflow definition and
// returns any findings. Unlike Validate, the findings returned here NEVER fail
// workflow validation: they carry Severity "warning" and are surfaced purely
// informationally. Callers must treat this as a separate channel from Validate
// and must not block a run on its results.
//
// Currently the only lint is the interactive-in-loop advisory.
func LintWorkflow(wf *Workflow) []ValidationError {
	var findings []ValidationError
	findings = append(findings, lintInteractiveInLoop(wf)...)
	return findings
}

// lintInteractiveInLoop flags every interactive node that sits on a cycle
// (i.e. is reachable from itself via a back-edge). The interactive executor
// short-circuits on a prior-succeeded (instance, node) execution and replays
// its first answer on every subsequent loop pass, so an interactive node in a
// loop body is almost always an authoring mistake. This is advisory only:
// cyclic graphs remain allowed by design, so the finding is a "warning" and is
// never returned by Validate.
func lintInteractiveInLoop(wf *Workflow) []ValidationError {
	adj := buildAdjacency(wf)

	var findings []ValidationError
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeInteractiveLLM {
			continue
		}
		if nodeOnCycle(n.ID, adj) {
			findings = append(findings, ValidationError{
				Code:     "INTERACTIVE_IN_LOOP",
				Message:  "interactive node " + n.ID + " sits on a cycle; it short-circuits on a prior-succeeded execution and will replay its first answer on every loop pass",
				NodeID:   n.ID,
				Severity: "warning",
			})
		}
	}
	return findings
}

// buildAdjacency constructs a successor adjacency list from the workflow edges.
// Shared by the connectivity check and the cycle-detection lint.
func buildAdjacency(wf *Workflow) map[string][]string {
	adj := make(map[string][]string)
	for _, e := range wf.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
	}
	return adj
}

// nodeOnCycle reports whether start is reachable from itself by following
// edges, i.e. start lies on a cycle (including a direct self-edge). It runs a
// BFS from start's successors and returns true if start is re-reached. This is
// the same adjacency+BFS reachability pattern used by validateConnectivity,
// generalized to start from a given node's successors.
func nodeOnCycle(start string, adj map[string][]string) bool {
	visited := make(map[string]bool)
	queue := append([]string(nil), adj[start]...)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current == start {
			return true
		}
		if visited[current] {
			continue
		}
		visited[current] = true
		queue = append(queue, adj[current]...)
	}
	return false
}
