package workflow

// FileChecker is a minimal subset of the engine.FileReader port used for
// validating file references within a workflow definition. Keeping this as a
// domain-level interface avoids an import dependency on the engine package.
type FileChecker interface {
	FileExists(workflowName string, relativePath string) bool
}

// ValidationError describes a single validation problem found in a workflow.
type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"nodeId,omitempty"`
	EdgeIdx int    `json:"edgeIdx,omitempty"`
}

// validInputTypes is the set of types allowed for workflow input fields.
var validInputTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"boolean": true,
	"object":  true,
	"array":   true,
}

// Validate performs structural and semantic validation of a workflow definition.
// Pass a non-nil FileChecker to validate file references (templateFile,
// scriptFile); pass nil to skip file-existence checks.
func Validate(wf *Workflow, checker FileChecker) []ValidationError {
	var errs []ValidationError

	errs = append(errs, validateEntryNodes(wf)...)
	errs = append(errs, validateDuplicateNodeIDs(wf)...)

	nodeIDs := buildNodeIDSet(wf)
	errs = append(errs, validateEdgeIntegrity(wf, nodeIDs)...)
	errs = append(errs, validateConditionalEdges(wf)...)
	errs = append(errs, validateConnectivity(wf, nodeIDs)...)
	errs = append(errs, validateInputTypes(wf)...)

	if checker != nil {
		errs = append(errs, validateFileReferences(wf, checker)...)
	}

	return errs
}

// validateEntryNodes checks that exactly one node is marked as the entrypoint.
func validateEntryNodes(wf *Workflow) []ValidationError {
	var entryIDs []string
	for _, n := range wf.Nodes {
		if n.Entrypoint {
			entryIDs = append(entryIDs, n.ID)
		}
	}

	switch len(entryIDs) {
	case 0:
		return []ValidationError{{
			Code:    "NO_ENTRY_NODE",
			Message: "workflow must have exactly one entry node",
		}}
	case 1:
		return nil
	default:
		return []ValidationError{{
			Code:    "MULTIPLE_ENTRY_NODES",
			Message: "workflow has multiple entry nodes: " + joinIDs(entryIDs),
		}}
	}
}

// validateDuplicateNodeIDs checks that all node IDs are unique.
func validateDuplicateNodeIDs(wf *Workflow) []ValidationError {
	seen := make(map[string]bool, len(wf.Nodes))
	var errs []ValidationError

	for _, n := range wf.Nodes {
		if seen[n.ID] {
			errs = append(errs, ValidationError{
				Code:    "DUPLICATE_NODE_ID",
				Message: "duplicate node ID: " + n.ID,
				NodeID:  n.ID,
			})
		}
		seen[n.ID] = true
	}

	return errs
}

// buildNodeIDSet returns a set of all node IDs in the workflow.
func buildNodeIDSet(wf *Workflow) map[string]bool {
	set := make(map[string]bool, len(wf.Nodes))
	for _, n := range wf.Nodes {
		set[n.ID] = true
	}
	return set
}

// validateEdgeIntegrity checks that every edge source and target references
// an existing node.
func validateEdgeIntegrity(wf *Workflow, nodeIDs map[string]bool) []ValidationError {
	var errs []ValidationError

	for i, e := range wf.Edges {
		if !nodeIDs[e.Source] {
			errs = append(errs, ValidationError{
				Code:    "INVALID_EDGE_SOURCE",
				Message: "edge source references unknown node: " + e.Source,
				EdgeIdx: i,
			})
		}
		if !nodeIDs[e.Target] {
			errs = append(errs, ValidationError{
				Code:    "INVALID_EDGE_TARGET",
				Message: "edge target references unknown node: " + e.Target,
				EdgeIdx: i,
			})
		}
	}

	return errs
}

// validateConditionalEdges checks that every conditional node's condition
// outputs and defaultOutput have a matching edge from that node.
func validateConditionalEdges(wf *Workflow) []ValidationError {
	// Build a map of (source, output) pairs from edges.
	edgeOutputs := make(map[string]map[string]bool)
	for _, e := range wf.Edges {
		if edgeOutputs[e.Source] == nil {
			edgeOutputs[e.Source] = make(map[string]bool)
		}
		edgeOutputs[e.Source][e.Output] = true
	}

	var errs []ValidationError

	for _, n := range wf.Nodes {
		if n.Type != NodeTypeConditional {
			continue
		}

		sourceEdges := edgeOutputs[n.ID]

		// Check each condition's output
		for _, cond := range n.Config.Conditions {
			if sourceEdges == nil || !sourceEdges[cond.Output] {
				errs = append(errs, ValidationError{
					Code:    "MISSING_CONDITIONAL_EDGE",
					Message: "conditional node " + n.ID + " missing edge for output: " + cond.Output,
					NodeID:  n.ID,
				})
			}
		}

		// Check defaultOutput
		if n.Config.DefaultOutput != "" {
			if sourceEdges == nil || !sourceEdges[n.Config.DefaultOutput] {
				errs = append(errs, ValidationError{
					Code:    "MISSING_CONDITIONAL_EDGE",
					Message: "conditional node " + n.ID + " missing edge for defaultOutput: " + n.Config.DefaultOutput,
					NodeID:  n.ID,
				})
			}
		}
	}

	return errs
}

// validateConnectivity performs a BFS from the entry node and reports any
// nodes that are not reachable.
func validateConnectivity(wf *Workflow, nodeIDs map[string]bool) []ValidationError {
	// Find the entry node.
	var entryID string
	for _, n := range wf.Nodes {
		if n.Entrypoint {
			entryID = n.ID
			break
		}
	}
	if entryID == "" {
		// No entry node; the entry-node check already flagged this.
		return nil
	}

	// Build adjacency list from edges.
	adj := make(map[string][]string)
	for _, e := range wf.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
	}

	// BFS.
	visited := make(map[string]bool)
	queue := []string{entryID}
	visited[entryID] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, next := range adj[current] {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}

	// Report unreachable nodes.
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if !visited[n.ID] {
			errs = append(errs, ValidationError{
				Code:    "UNREACHABLE_NODE",
				Message: "node " + n.ID + " is not reachable from the entry node",
				NodeID:  n.ID,
			})
		}
	}

	return errs
}

// validateInputTypes checks that all workflow inputs use a recognized type.
func validateInputTypes(wf *Workflow) []ValidationError {
	var errs []ValidationError

	for _, inp := range wf.Inputs {
		if !validInputTypes[inp.Type] {
			errs = append(errs, ValidationError{
				Code:    "INVALID_INPUT_TYPE",
				Message: "input " + inp.Name + " has invalid type: " + inp.Type,
			})
		}
	}

	return errs
}

// validateFileReferences checks that templateFile and scriptFile values
// reference files that actually exist on disk.
func validateFileReferences(wf *Workflow, checker FileChecker) []ValidationError {
	var errs []ValidationError

	for _, n := range wf.Nodes {
		if n.Config.TemplateFile != "" {
			if !checker.FileExists(wf.Name, n.Config.TemplateFile) {
				errs = append(errs, ValidationError{
					Code:    "MISSING_FILE",
					Message: "node " + n.ID + " references missing templateFile: " + n.Config.TemplateFile,
					NodeID:  n.ID,
				})
			}
		}
		if n.Config.ScriptFile != "" {
			if !checker.FileExists(wf.Name, n.Config.ScriptFile) {
				errs = append(errs, ValidationError{
					Code:    "MISSING_FILE",
					Message: "node " + n.ID + " references missing scriptFile: " + n.Config.ScriptFile,
					NodeID:  n.ID,
				})
			}
		}
	}

	return errs
}

// joinIDs concatenates a slice of strings with commas.
func joinIDs(ids []string) string {
	result := ""
	for i, id := range ids {
		if i > 0 {
			result += ", "
		}
		result += id
	}
	return result
}
