package workflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// FileChecker is a minimal subset of the engine.FileReader port used for
// validating file references within a workflow definition. Keeping this as a
// domain-level interface avoids an import dependency on the engine package.
type FileChecker interface {
	FileExists(workflowName string, relativePath string) bool
}

// ProfileChecker resolves a node's model_profile alias to its capability
// kind for validation. Mirrors FileChecker: an injected domain capability,
// not an adapter import. Returns ok=false for an unknown alias.
type ProfileChecker interface {
	ProfileKind(alias string) (kind string, ok bool)
}

// ConnectionChecker is the minimal subset used to validate that a profile's
// ConnectionRef names an existing connection. The concrete implementation is
// injected at wiring time; the domain depends only on this interface, not on
// the full ConnectionStore (so no adapter or provider-persistence concern
// leaks into the domain).
type ConnectionChecker interface {
	// ProfileConnectionRef returns the ConnectionRef for a profile alias, and
	// whether the alias is known.
	ProfileConnectionRef(alias string) (connectionRef string, aliasKnown bool)
	// ConnectionExists reports whether a connection with the given name exists.
	ConnectionExists(name string) bool
}

// WorkflowChecker reports whether a workflow NAME resolves to a workflow the
// author can see. It is the spawn-node analogue of ProfileChecker: an injected
// domain capability, not an adapter import.
//
// It is nil-skippable like every other checker, and it is deliberately
// name-only — resolution at run time is by name too, through the run owner's
// visibility filter, so an existence answer given here for a different identity
// is advisory about the shape of the graph, not a substitute for the run-time
// fail-closed load.
type WorkflowChecker interface {
	// WorkflowExists reports whether a workflow with the given name resolves.
	// A workflow the author cannot see is indistinguishable from one that does
	// not exist — existence is never leaked.
	WorkflowExists(name string) bool
}

// ValidateOption is an additive knob on Validate. It exists so a new checker
// can be introduced without a positional-parameter change at more than a
// hundred call sites; the four original checkers stay positional.
type ValidateOption func(*validateOptions)

type validateOptions struct {
	bundles     ScriptBundleChecker
	bundleCtx   context.Context
	bundleOwner uuid.UUID
	workflows   WorkflowChecker
	// children loads child workflow contracts for workflows-form group tasks
	// (WithChildContractReader, validation_group_children.go).
	children ChildContractReader
	// groupCheckersRequired turns an absent group checker into
	// GROUP_CHECKER_UNAVAILABLE (WithGroupCheckersRequired).
	groupCheckersRequired bool
}

// WithWorkflowChecker enables author-time existence validation of every
// spawn_workflow node's childWorkflow.
func WithWorkflowChecker(wc WorkflowChecker) ValidateOption {
	return func(o *validateOptions) { o.workflows = wc }
}

// ValidationError describes a single validation problem found in a workflow.
//
// Severity distinguishes hard validation failures from non-gating advisories.
// An empty Severity means "error" (a hard failure) for back-compatibility.
// Validate and LintWorkflow may return advisories with Severity "warning";
// those are surfaced informationally and never gate validation.
type ValidationError struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	NodeID   string `json:"nodeId,omitempty"`
	EdgeIdx  int    `json:"edgeIdx,omitempty"`
	Severity string `json:"severity,omitempty"`
	// TaskKey identifies the parallel_group task a finding is about (set
	// together with NodeID = the group node id); empty for every other finding.
	TaskKey string `json:"taskKey,omitempty"`
}

// validInputTypes is the set of types allowed for workflow input fields.
var validInputTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"boolean": true,
	"object":  true,
	"array":   true,
}

// reservedInputNames are variable names the engine seeds itself; workflow
// authors must not declare inputs with these names.
var reservedInputNames = map[string]bool{
	"workspace": true,
}

// validErrorBehaviorTypes is the set of recognized error behavior types.
var validErrorBehaviorTypes = map[string]bool{
	"":      true, // empty means "use workflow default"
	"halt":  true,
	"retry": true,
	"skip":  true,
	"route": true,
}

// Validate performs structural and semantic validation of a workflow definition.
// Pass a non-nil FileChecker to validate file references (templateFile,
// scriptFile); pass nil to skip file-existence checks. Pass a non-nil
// ProfileChecker to validate that llm_call/agent_call nodes reference a
// model_profile of the correct capability kind; pass nil to skip that pass.
// Pass a non-nil ConnectionChecker to validate that a profile's non-empty
// ConnectionRef names an existing connection; pass nil to skip that pass. Each
// checker is independently nil-skippable unless WithGroupCheckersRequired is
// supplied for a workflow containing parallel_group nodes.
func Validate(wf *Workflow, checker FileChecker, profiles ProfileChecker, connChecker ConnectionChecker, opts ...ValidateOption) []ValidationError {
	var o validateOptions
	for _, apply := range opts {
		apply(&o)
	}

	var errs []ValidationError

	errs = append(errs, validateEntryNodes(wf)...)
	errs = append(errs, validateNodeIDs(wf)...)
	errs = append(errs, validateDuplicateNodeIDs(wf)...)

	nodeIDs := buildNodeIDSet(wf)
	errs = append(errs, validateEdgeIntegrity(wf, nodeIDs)...)
	errs = append(errs, validateConditionalEdges(wf)...)
	errs = append(errs, validateConnectivity(wf, nodeIDs)...)
	errs = append(errs, validateInputTypes(wf)...)
	errs = append(errs, validateInputNames(wf)...)
	errs = append(errs, validateReservedInputNames(wf)...)
	errs = append(errs, validateNodeConfigs(wf)...)
	errs = append(errs, validatePortableNodes(wf, &o)...)
	errs = append(errs, validateErrorBehaviors(wf)...)
	errs = append(errs, validateGitVariable(wf)...)

	if checker != nil {
		errs = append(errs, validateFileReferences(wf, checker)...)
	}

	if profiles != nil {
		errs = append(errs, validateModelProfiles(wf, profiles)...)
	}

	if connChecker != nil {
		errs = append(errs, validateConnectionReferences(wf, connChecker)...)
	}

	if o.workflows != nil {
		errs = append(errs, validateChildWorkflowReferences(wf, o.workflows)...)
	}

	// spawn_workflow input contract checks against the child's declared inputs
	// (validation_spawn_inputs.go). Never gates writes.
	if o.children != nil {
		errs = append(errs, validateSpawnChildInputs(wf, o)...)
	}

	// parallel_group checks (validation_group.go). A no-op without groups, so
	// legacy workflows see exactly the findings they always did.
	errs = append(errs, validateGroups(wf, checker, profiles, o)...)

	return errs
}

// validateChildWorkflowReferences checks that every spawn_workflow node names a
// child workflow that actually resolves.
//
// It runs in the SAME spawn-node traversal shape as the connection check above
// and closes the same class of gap: a bad reference that passes publish and
// fails only at run time, after the parent has already burned a queue slot and
// reached the spawn node. An empty childWorkflow is skipped — validateNodeConfigs
// owns that error and double-reporting helps nobody.
func validateChildWorkflowReferences(wf *Workflow, workflows WorkflowChecker) []ValidationError {
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeSpawnWorkflow || n.Config.ChildWorkflow == "" {
			continue
		}
		if !workflows.WorkflowExists(n.Config.ChildWorkflow) {
			errs = append(errs, ValidationError{
				Code: "UNKNOWN_CHILD_WORKFLOW",
				Message: "spawn_workflow node " + n.ID + " references unknown workflow " +
					strconv.Quote(n.Config.ChildWorkflow),
				NodeID: n.ID,
			})
		}
	}
	return errs
}

// validateConnectionReferences checks that every node whose model_profile
// resolves to a non-empty ConnectionRef names a connection that actually
// exists. An empty ConnectionRef is the claude-cli no-key subscription path and
// is valid with no existence check. An unknown alias is skipped here because
// validateModelProfiles (Phase 1) already reports it; double-reporting is
// avoided.
//
// spawn_workflow is INCLUDED. Its model_profile drives a real provider call —
// the child-input preparation — so a profile naming a deleted connection is
// exactly as broken here as on an llm_call, and omitting the type meant such a
// workflow passed publish and failed only at run time, after the parent had
// already reached the spawn node.
func validateConnectionReferences(wf *Workflow, connChecker ConnectionChecker) []ValidationError {
	var errs []ValidationError
	for _, n := range wf.Nodes {
		switch n.Type {
		case NodeTypeLLMCall, NodeTypeAgentCall, NodeTypeSpawnWorkflow:
		default:
			continue
		}

		alias := n.Config.ModelProfile
		if alias == "" {
			// Phase 1 reports the missing model_profile for the types that require
			// it. A spawn node's alias is legitimately OPTIONAL — a child declaring
			// zero inputs never makes a preparation call — so an empty alias here is
			// not an error at all; it becomes a node-named runtime error out of
			// ChildInputPreparer.Prepare only if a call is actually attempted.
			continue
		}

		connRef, aliasKnown := connChecker.ProfileConnectionRef(alias)
		if !aliasKnown {
			// Phase 1 owns the unknown-alias error; do not double-report.
			continue
		}
		if connRef == "" {
			// claude-cli no-key subscription path: valid, no existence check.
			continue
		}
		if !connChecker.ConnectionExists(connRef) {
			errs = append(errs, ValidationError{
				Code:    "UNKNOWN_CONNECTION",
				Message: "profile " + alias + " references unknown connection " + connRef,
				NodeID:  n.ID,
			})
		}
	}
	return errs
}

// validateModelProfiles checks that every model consumer node references a
// model_profile whose capability kind matches the node type: llm_call requires
// a chat profile, agent_call requires an agent profile. A missing alias, an
// unknown alias, or a kind mismatch each produces a specific error so the
// failure is caught at workflow_validate, before any run.
func validateModelProfiles(wf *Workflow, profiles ProfileChecker) []ValidationError {
	var errs []ValidationError
	nodes := make(map[string]Node, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodes[n.ID] = n
	}
	// ModelConsumers is the one enumeration run pin capture and the path
	// inventory also use. A spawn node requires a chat profile: its
	// input-preparation call is tool-SUPPRESSED, so an agent-kind binding would
	// escalate a reasoning call to an RCE-capable one (the author-time half of
	// domain/engine's expectedKindForNodeType guard).
	for _, c := range ModelConsumers(wf) {
		if c.Source == ConsumerSourceGroupResolver {
			continue // validateGroupNode reports it with GROUP_* codes from the same consumer.
		}
		n, wantKind := nodes[c.NodeID], c.Kind

		if n.Config.ModelProfile == "" {
			// A spawn node's alias is OPTIONAL: a child that declares zero inputs
			// never makes a preparation call, and validation cannot load the child
			// to know. An empty alias fails closed at run time only if a call is
			// actually made. Every other type here REQUIRES it.
			if n.Type == NodeTypeSpawnWorkflow {
				continue
			}
			errs = append(errs, ValidationError{
				Code:    "MISSING_REQUIRED_CONFIG",
				Message: string(n.Type) + " node " + n.ID + " must have a model_profile",
				NodeID:  n.ID,
			})
			continue
		}

		// SF2 (PR #18, comment 3350531429): node.Config.Model is read by no
		// execution path once the node resolves via model_profile — the model
		// always comes from the resolved binding. Setting both is a silent
		// no-op (the model knob is ignored), so reject it with an explicit,
		// conflict-naming error rather than letting the operator discover the
		// trap at runtime (or never).
		if n.Config.Model != "" {
			errs = append(errs, ValidationError{
				Code:    "MODEL_PROFILE_CONFLICT",
				Message: "node " + n.ID + " sets model but resolves via model_profile; model is ignored — remove the model field",
				NodeID:  n.ID,
			})
		}

		kind, ok := profiles.ProfileKind(n.Config.ModelProfile)
		if !ok {
			errs = append(errs, ValidationError{
				Code:    "UNKNOWN_PROFILE",
				Message: "node " + n.ID + " references unknown model_profile: " + n.Config.ModelProfile,
				NodeID:  n.ID,
			})
			continue
		}

		if kind != wantKind {
			errs = append(errs, ValidationError{
				Code:    "PROFILE_KIND_MISMATCH",
				Message: string(n.Type) + " node " + n.ID + " requires a " + wantKind + " profile but " + n.Config.ModelProfile + " is " + kind,
				NodeID:  n.ID,
			})
		}
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
	adj := buildAdjacency(wf)

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

// validateFileReferences checks that templateFile, scriptFile, and
// orchestrationFile values reference files that actually exist on disk. A
// malformed @lib path is INVALID_LIB_PATH and never reaches the checker.
func validateFileReferences(wf *Workflow, checker FileChecker) []ValidationError {
	var errs []ValidationError

	for _, n := range wf.Nodes {
		for _, ref := range []struct{ field, path string }{
			{"templateFile", n.Config.TemplateFile},
			{"scriptFile", n.Config.ScriptFile},
			{"orchestrationFile", n.Config.OrchestrationFile},
		} {
			if ref.path == "" {
				continue
			}
			if _, isLib, err := ParseLibPath(ref.path); isLib && err != nil {
				errs = append(errs, ValidationError{
					Code:    "INVALID_LIB_PATH",
					Message: "node " + n.ID + " references invalid library path " + ref.field + ": " + ref.path + " (" + libPathReason(err) + ")",
					NodeID:  n.ID,
				})
				continue
			}
			if !checker.FileExists(wf.Name, ref.path) {
				errs = append(errs, ValidationError{
					Code:    "MISSING_FILE",
					Message: "node " + n.ID + " references missing " + ref.field + ": " + ref.path,
					NodeID:  n.ID,
				})
			}
		}
	}

	return errs
}

// libPathReason strips the ErrInvalidLibPath prefix from a ParseLibPath error.
func libPathReason(err error) string {
	return strings.TrimPrefix(err.Error(), ErrInvalidLibPath.Error()+": ")
}

// validateNodeIDs checks that no node has an empty ID.
func validateNodeIDs(wf *Workflow) []ValidationError {
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.ID == "" {
			errs = append(errs, ValidationError{
				Code:    "EMPTY_NODE_ID",
				Message: "node has an empty ID; all nodes must have a non-empty unique ID",
			})
		}
	}
	return errs
}

// validateInputNames checks that no input field has an empty name.
func validateInputNames(wf *Workflow) []ValidationError {
	var errs []ValidationError
	for _, inp := range wf.Inputs {
		if inp.Name == "" {
			errs = append(errs, ValidationError{
				Code:    "EMPTY_INPUT_NAME",
				Message: "workflow has an input field with an empty name",
			})
		}
	}
	return errs
}

// validateReservedInputNames checks that no input uses an engine-reserved name.
func validateReservedInputNames(wf *Workflow) []ValidationError {
	var errs []ValidationError
	for _, inp := range wf.Inputs {
		if reservedInputNames[inp.Name] {
			errs = append(errs, ValidationError{
				Code:    "RESERVED_INPUT_NAME",
				Message: "input name is reserved by the engine: " + inp.Name,
			})
		}
	}
	return errs
}

// validateNodeConfigs checks per-type required configuration fields.
func validateNodeConfigs(wf *Workflow) []ValidationError {
	var errs []ValidationError
	for _, n := range wf.Nodes {
		switch n.Type {
		case NodeTypeLLMCall:
			if n.Config.TemplateFile == "" {
				errs = append(errs, ValidationError{
					Code:    "MISSING_REQUIRED_CONFIG",
					Message: "llm_call node " + n.ID + " must have a templateFile",
					NodeID:  n.ID,
				})
			}
		case NodeTypeAgentCall:
			if n.Config.TemplateFile == "" {
				errs = append(errs, ValidationError{
					Code:    "MISSING_REQUIRED_CONFIG",
					Message: "agent_call node " + n.ID + " must have a templateFile",
					NodeID:  n.ID,
				})
			}
		case NodeTypeScript:
			if n.Config.ScriptFile == "" {
				errs = append(errs, ValidationError{
					Code:    "MISSING_REQUIRED_CONFIG",
					Message: "script node " + n.ID + " must have a scriptFile",
					NodeID:  n.ID,
				})
			}
		case NodeTypeToolCall:
			if n.Config.Command == "" {
				errs = append(errs, ValidationError{
					Code:    "MISSING_REQUIRED_CONFIG",
					Message: "tool_call node " + n.ID + " must have a command",
					NodeID:  n.ID,
				})
			}
		case NodeTypeConditional:
			if len(n.Config.Conditions) == 0 && n.Config.DefaultOutput == "" {
				errs = append(errs, ValidationError{
					Code:    "MISSING_REQUIRED_CONFIG",
					Message: "conditional node " + n.ID + " must have at least one condition or a defaultOutput",
					NodeID:  n.ID,
				})
			}
		case NodeTypeTimerDelay:
			errs = append(errs, validateTimerDelayNode(n, wf.Edges)...)
		case NodeTypeWebhookWait:
			errs = append(errs, validateWebhookWaitNode(n, wf.Edges)...)
		case NodeTypeSpawnWorkflow:
			errs = append(errs, validateSpawnWorkflowNode(n, wf.Edges, wf.Name)...)
		case NodeTypeInteractiveLLM, NodeTypeInteractiveAgent:
			errs = append(errs, validateInteractiveNode(n)...)
		}
		errs = append(errs, validateAgentBackend(n)...)
	}
	return errs
}

// validateAgentBackend rejects an unknown Config.Backend on agent nodes
// (agent_call / interactive_agent). The backend axis selects which agent CLI
// runs the loop; an unrecognized value would mis-route (or silently fall back)
// at run time, so it is a hard, fail-closed ValidationError gated at
// workflow_validate. The check applies ONLY to agent node types — a backend on
// a chat (llm_call/interactive_llm) or any other node is an irrelevant field
// and is ignored so chat authors are not blocked. An empty value inherits and
// is always valid.
func validateAgentBackend(n Node) []ValidationError {
	switch n.Type {
	case NodeTypeAgentCall, NodeTypeInteractiveAgent:
	default:
		return nil
	}
	if IsValidAgentBackend(n.Config.Backend) {
		return nil
	}
	return []ValidationError{{
		Code:    "INVALID_AGENT_BACKEND",
		Message: "node " + n.ID + " has unknown agent backend " + n.Config.Backend + " (want claude-code, opencode, or codex)",
		NodeID:  n.ID,
	}}
}

// countOutgoingEdges counts the number of edges originating from a given node.
func countOutgoingEdges(nodeID string, edges []Edge) int {
	count := 0
	for _, e := range edges {
		if e.Source == nodeID {
			count++
		}
	}
	return count
}

// validateTimerDelayNode checks timer_delay-specific validation rules.
func validateTimerDelayNode(node Node, edges []Edge) []ValidationError {
	var errs []ValidationError

	if node.Entrypoint {
		errs = append(errs, ValidationError{
			Code:    "INVALID_TIMER_DELAY",
			Message: "timer_delay node " + node.ID + " cannot be an entrypoint",
			NodeID:  node.ID,
		})
	}

	if node.Config.DurationSeconds == "" {
		errs = append(errs, ValidationError{
			Code:    "MISSING_REQUIRED_CONFIG",
			Message: "timer_delay node " + node.ID + " must have durationSeconds",
			NodeID:  node.ID,
		})
	}

	if countOutgoingEdges(node.ID, edges) != 1 {
		errs = append(errs, ValidationError{
			Code:    "INVALID_TIMER_DELAY",
			Message: "timer_delay node " + node.ID + " must have exactly one outgoing edge",
			NodeID:  node.ID,
		})
	}

	return errs
}

// validateWebhookWaitNode checks webhook_wait-specific validation rules.
func validateWebhookWaitNode(node Node, edges []Edge) []ValidationError {
	var errs []ValidationError

	if node.Entrypoint {
		errs = append(errs, ValidationError{
			Code:    "INVALID_WEBHOOK_WAIT",
			Message: "webhook_wait node " + node.ID + " cannot be an entrypoint",
			NodeID:  node.ID,
		})
	}

	if countOutgoingEdges(node.ID, edges) != 1 {
		errs = append(errs, ValidationError{
			Code:    "INVALID_WEBHOOK_WAIT",
			Message: "webhook_wait node " + node.ID + " must have exactly one outgoing edge",
			NodeID:  node.ID,
		})
	}

	return errs
}

// validateSpawnWorkflowNode checks spawn_workflow-specific validation rules.
//
// The mode is checked FIRST and, when unrecognized, short-circuits: the port
// rules are mode-specific, so reporting them against an undefined mode would
// bury the one finding the author can act on. An empty mode is an authoring
// error, NOT an inheritance request (see IsValidSpawnMode).
func validateSpawnWorkflowNode(node Node, edges []Edge, workflowName string) []ValidationError {
	var errs []ValidationError

	if node.Entrypoint {
		errs = append(errs, ValidationError{
			Code:    "INVALID_SPAWN_WORKFLOW",
			Message: "spawn_workflow node " + node.ID + " cannot be an entrypoint",
			NodeID:  node.ID,
		})
	}

	// ONE set of contract checks, shared with the executor's defensive re-check
	// (ParseSpawnConfig). The executor cannot simply trust that this ran — an
	// instance executes from a WorkflowSnapshot that may PREDATE this validation
	// — so both sides must check, and must check identically.
	_, cfgErr := ParseSpawnConfig(node.Config)
	if errors.Is(cfgErr, ErrSpawnReservedInput) {
		errs = append(errs, ValidationError{
			Code: "SPAWN_RESERVED_INPUT",
			Message: "spawn_workflow node " + node.ID + " sets input " + strings.Join(reservedSpawnInputs(node.Config.Inputs), ", ") +
				", which the engine seeds for every run; remove it from inputs",
			NodeID: node.ID,
		})
	}
	if errors.Is(cfgErr, ErrSpawnChildWorkflowMissing) {
		errs = append(errs, ValidationError{
			Code:    "MISSING_REQUIRED_CONFIG",
			Message: "spawn_workflow node " + node.ID + " must have a childWorkflow",
			NodeID:  node.ID,
		})
	}

	// DIRECT self-spawn: the one cycle visible in a single definition, and the
	// one an author is most likely to write by accident. It is caught here, at
	// publish time, where the fix is obvious.
	//
	// This is NOT the cycle defence — it cannot be. The spawn graph is a graph of
	// workflow NAMES resolved at run time, so an INDIRECT cycle (A spawns B
	// spawns A) is invisible from inside either definition, and a workflow may be
	// renamed after publication. The runtime bound (instance.MaxSpawnDepth,
	// enforced in Engine.Spawn against the child's persisted spawn_depth) is what
	// actually contains recursion; this check just turns the obvious mistake into
	// an author-time error instead of ten wasted generations.
	if workflowName != "" && node.Config.ChildWorkflow == workflowName {
		errs = append(errs, ValidationError{
			Code: "INVALID_SPAWN_WORKFLOW",
			Message: "spawn_workflow node " + node.ID + " spawns its own workflow " +
				strconv.Quote(workflowName) + "; a run that spawns itself recurses until it hits the runtime spawn-depth limit",
			NodeID: node.ID,
		})
	}

	if errors.Is(cfgErr, ErrSpawnModeInvalid) {
		errs = append(errs, ValidationError{
			Code:    "INVALID_SPAWN_MODE",
			Message: "spawn_workflow node " + node.ID + " has an " + spawnModeProblem(cfgErr),
			NodeID:  node.ID,
		})
		return errs
	}

	// Collect this node's outgoing ports. Mirrors validateConditionalEdges'
	// (source, output) set technique.
	ports := map[string]int{}
	for _, e := range edges {
		if e.Source == node.ID {
			ports[e.Output]++
		}
	}

	switch node.Config.SpawnMode {
	case SpawnModeWaitForChild:
		// Exactly one edge per port, not merely "at least one": findNextNode
		// (domain/engine/engine.go) follows only the FIRST edge matching a
		// port and never fans out, so a second edge on success/failure would
		// silently become dead code rather than erroring at run time.
		for _, want := range []string{SpawnPortSuccess, SpawnPortFailure} {
			switch ports[want] {
			case 0:
				errs = append(errs, ValidationError{
					Code:    "MISSING_SPAWN_EDGE",
					Message: "spawn_workflow node " + node.ID + " (wait_for_child) missing edge for output: " + want,
					NodeID:  node.ID,
				})
			case 1:
				// exactly one; valid.
			default:
				errs = append(errs, ValidationError{
					Code: "INVALID_SPAWN_WORKFLOW",
					Message: "spawn_workflow node " + node.ID +
						" (wait_for_child) must have exactly one edge for output: " + want,
					NodeID: node.ID,
				})
			}
		}
		if ports[""] > 0 {
			errs = append(errs, ValidationError{
				Code: "INVALID_SPAWN_WORKFLOW",
				Message: "spawn_workflow node " + node.ID +
					" (wait_for_child) must not have an unnamed outgoing edge; route through the success and failure ports",
				NodeID: node.ID,
			})
		}
	case SpawnModeFireAndForget:
		if ports[SpawnPortSuccess] > 0 || ports[SpawnPortFailure] > 0 {
			errs = append(errs, ValidationError{
				Code: "INVALID_SPAWN_WORKFLOW",
				Message: "spawn_workflow node " + node.ID +
					" (fire_and_forget) must not wire the success or failure ports; those exist only in wait_for_child mode",
				NodeID: node.ID,
			})
		}
		if ports[""] != 1 {
			errs = append(errs, ValidationError{
				Code: "INVALID_SPAWN_WORKFLOW",
				Message: "spawn_workflow node " + node.ID +
					" (fire_and_forget) must have exactly one unnamed outgoing edge",
				NodeID: node.ID,
			})
		}
	}

	return errs
}

// spawnModeProblem extracts the mode sentence from a ParseSpawnConfig error so
// the ValidationError message stays the single-sentence form authors already
// see, without this file re-deriving the wording ParseSpawnConfig owns.
func spawnModeProblem(joined error) string {
	for _, part := range strings.Split(joined.Error(), "\n") {
		if strings.Contains(part, ErrSpawnModeInvalid.Error()) {
			return part
		}
	}
	return ErrSpawnModeInvalid.Error()
}

// validateInteractiveNode checks interactive-node-specific validation rules.
// Both orchestrationFile and templateFile are read unconditionally by the
// InteractiveExecutor; omitting either causes a runtime failure.
func validateInteractiveNode(node Node) []ValidationError {
	var errs []ValidationError

	if node.Config.OrchestrationFile == "" {
		errs = append(errs, ValidationError{
			Code:    "MISSING_REQUIRED_CONFIG",
			Message: "interactive node " + node.ID + " must have an orchestrationFile",
			NodeID:  node.ID,
		})
	}
	if node.Config.TemplateFile == "" {
		errs = append(errs, ValidationError{
			Code:    "MISSING_REQUIRED_CONFIG",
			Message: "interactive node " + node.ID + " must have a templateFile",
			NodeID:  node.ID,
		})
	}

	return errs
}

// validateErrorBehaviors checks that error behavior types are recognized values.
func validateErrorBehaviors(wf *Workflow) []ValidationError {
	var errs []ValidationError

	if !validErrorBehaviorTypes[wf.DefaultErrorBehavior.Type] {
		errs = append(errs, ValidationError{
			Code:    "INVALID_ERROR_BEHAVIOR",
			Message: "workflow defaultErrorBehavior has unknown type: " + wf.DefaultErrorBehavior.Type,
		})
	}

	for _, n := range wf.Nodes {
		if !validErrorBehaviorTypes[n.ErrorBehavior.Type] {
			errs = append(errs, ValidationError{
				Code:    "INVALID_ERROR_BEHAVIOR",
				Message: "node " + n.ID + " has unknown errorBehavior type: " + n.ErrorBehavior.Type,
				NodeID:  n.ID,
			})
		}
	}

	return errs
}

// joinIDs concatenates a slice of strings with commas.
func joinIDs(ids []string) string {
	return strings.Join(ids, ", ")
}

// gitVariablePathRE matches a dotted variable path: segments of letters,
// digits, underscores or dashes (node ids use dashes), joined by single dots.
var gitVariablePathRE = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// validateGitVariable checks the optional gitVariable is a well-formed variable
// path. It deliberately does NOT require a declared input: branch and work_dir
// are usually runtime variables set by transforms.
func validateGitVariable(wf *Workflow) []ValidationError {
	if wf.GitVariable == "" || gitVariablePathRE.MatchString(wf.GitVariable) {
		return nil
	}
	return []ValidationError{{
		Code:    "invalid_git_variable",
		Message: fmt.Sprintf("gitVariable %q is not a valid variable path (e.g. branch or node-id.field)", wf.GitVariable),
	}}
}

// reservedSpawnInputs returns the reserved names present in inputs, sorted.
func reservedSpawnInputs(inputs map[string]any) []string {
	var out []string
	for k := range inputs {
		if reservedInputNames[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
