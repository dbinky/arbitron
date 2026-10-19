// Package workflow defines the domain types for workflow definitions.
package workflow

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
)

// NodeType identifies the kind of processing a workflow node performs.
type NodeType string

const (
	NodeTypeLLMCall          NodeType = "llm_call"
	NodeTypeScript           NodeType = "script"
	NodeTypeToolCall         NodeType = "tool_call"
	NodeTypeConditional      NodeType = "conditional"
	NodeTypeTransform        NodeType = "transform"
	NodeTypeTimerDelay       NodeType = "timer_delay"
	NodeTypeWebhookWait      NodeType = "webhook_wait"
	NodeTypeInteractiveLLM   NodeType = "interactive_llm"
	NodeTypeInteractiveAgent NodeType = "interactive_agent"
	NodeTypeAgentCall        NodeType = "agent_call"
	// NodeTypeSpawnWorkflow starts a CHILD workflow instance from within a
	// parent workflow. It is ONE node type with two modes (see SpawnMode*);
	// there is deliberately no separate node type per mode.
	NodeTypeSpawnWorkflow NodeType = "spawn_workflow"
	// NodeTypeParallelGroup fans out a fixed, ordered set of tasks (parallel
	// branches or awaited child workflows) and gathers them before continuing
	// through its success or failure port. Its configuration lives in
	// NodeConfig.Group; see group.go.
	NodeTypeParallelGroup NodeType = "parallel_group"
)

// IsInteractive reports whether t is one of the human-in-the-loop node types.
func IsInteractive(t NodeType) bool {
	return t == NodeTypeInteractiveLLM || t == NodeTypeInteractiveAgent
}

// Agent backend axis (org-readiness Phase 2). The backend selects which agent
// CLI runs an agentic loop, INDEPENDENT of the provider (the connection that
// supplies endpoint/key/model). It is chosen per agent node (agent_call /
// interactive_agent); an unset ("") value inherits profile→deployment default.
// These constants are the single source of truth for the allowed vocabulary,
// shared by validation (this package) and the adapter factory's backend
// dispatch. The allowed set is exact-match lowercase.
const (
	// BackendUnset inherits the backend (node → profile → deployment default).
	BackendUnset = ""
	// BackendClaudeCode selects the claude-cli agent backend.
	BackendClaudeCode = "claude-code"
	// BackendOpencode selects the opencode agent backend.
	BackendOpencode = "opencode"
	// BackendCodex selects the codex agent backend (`codex exec`, run on the
	// ChatGPT subscription with no API key).
	BackendCodex = "codex"
	// BackendPi selects the pi agent backend (`pi --print --mode json`), a
	// minimal harness whose small system prompt and four-tool surface cost a
	// fraction of a vendor CLI's per-request context. Unlike codex it is
	// multi-provider and key-authenticated, so a binding's provider selects the
	// credential.
	BackendPi = "pi"
)

// IsValidAgentBackend reports whether b is a recognized agent backend value.
// An empty value (BackendUnset) is valid (it means "inherit"). Any other value
// must exactly match a known backend; near-misses (wrong case, typos) are
// rejected so a mis-routed agent fails closed at workflow load.
func IsValidAgentBackend(b string) bool {
	switch b {
	case BackendUnset, BackendClaudeCode, BackendOpencode, BackendCodex, BackendPi:
		return true
	default:
		return false
	}
}

// Spawn mode axis. A spawn_workflow node either starts the child and continues
// (fire-and-forget) or suspends the parent until the child finishes
// (wait-for-child). These constants are the single source of truth for the
// allowed vocabulary, shared by validation (this package), the spawn executor,
// and the persisted link on the child instance. The allowed set is exact-match
// lowercase.
const (
	// SpawnModeFireAndForget starts the child and continues the parent along a
	// single unnamed outgoing edge. Child outcomes never affect the parent.
	SpawnModeFireAndForget = "fire_and_forget"
	// SpawnModeWaitForChild suspends the parent until the child reaches a
	// terminal state, then resumes it through the success or failure port.
	SpawnModeWaitForChild = "wait_for_child"
)

// Spawn output ports. Both are REQUIRED on a wait-for-child node and both are
// REJECTED on a fire-and-forget node (see validateSpawnWorkflowNode).
const (
	SpawnPortSuccess = "success"
	SpawnPortFailure = "failure"
)

// SpawnConfig is the VALIDATED spawn contract of one spawn_workflow node: the
// two fields every consumer needs, already checked.
type SpawnConfig struct {
	ChildWorkflow string
	Mode          string
}

// Errors ParseSpawnConfig reports. They are sentinels so the validator can map
// each to its own ValidationError code while the executor can wrap them
// verbatim — one set of checks, two presentations.
var (
	// ErrSpawnChildWorkflowMissing: no child workflow was configured.
	ErrSpawnChildWorkflowMissing = errors.New("childWorkflow is not configured")
	// ErrSpawnModeInvalid: the spawnMode is empty or unrecognized.
	ErrSpawnModeInvalid = errors.New("unrecognized spawnMode")
)

// ParseSpawnConfig extracts and checks a spawn node's contract.
//
// It is the ONE place the two string checks live. They were duplicated between
// author-time validation and the executor's defensive re-check — which is not
// redundant belt-and-braces but a real divergence risk, because an instance
// executes from a WorkflowSnapshot that may PREDATE the validation that would
// have caught the problem, so the executor genuinely must re-check and must
// re-check the SAME way.
//
// EVERY problem is reported, joined, rather than the first: an author fixing a
// spawn node should see both a missing childWorkflow and a bad mode in one
// pass. Callers that need per-problem handling use errors.Is on the sentinels.
func ParseSpawnConfig(cfg NodeConfig) (SpawnConfig, error) {
	var problems []error
	if cfg.ChildWorkflow == "" {
		problems = append(problems, ErrSpawnChildWorkflowMissing)
	}
	if !IsValidSpawnMode(cfg.SpawnMode) {
		problems = append(problems, fmt.Errorf("%w %s (must be exactly %s or %s)",
			ErrSpawnModeInvalid, strconv.Quote(cfg.SpawnMode),
			strconv.Quote(SpawnModeFireAndForget), strconv.Quote(SpawnModeWaitForChild)))
	}
	if len(problems) > 0 {
		return SpawnConfig{}, errors.Join(problems...)
	}
	return SpawnConfig{ChildWorkflow: cfg.ChildWorkflow, Mode: cfg.SpawnMode}, nil
}

// IsValidSpawnMode reports whether m is a recognized spawn mode.
//
// Unlike IsValidAgentBackend, the EMPTY value is INVALID. There is no sensible
// inherited default: the two modes have opposite control-flow consequences (the
// parent continues vs. the parent suspends and requires two connected ports),
// so an unset mode is an authoring error, not an inheritance request. An
// unrecognized value must fail closed at workflow load rather than mis-route or
// silently fall back at run time.
func IsValidSpawnMode(m string) bool {
	switch m {
	case SpawnModeFireAndForget, SpawnModeWaitForChild:
		return true
	default:
		return false
	}
}

// Workflow is the top-level definition of a directed graph of processing nodes.
type Workflow struct {
	// ID is the workflow's stable, durable identity (persisted in workflow.json).
	// The Name is the human-facing label and may change; the ID never does. It is
	// omitted from JSON when zero so legacy id-less definitions stay valid until
	// backfilled.
	ID                   uuid.UUID     `json:"id,omitempty"`
	Name                 string        `json:"name"`
	Description          string        `json:"description"`
	Author               string        `json:"author"`
	DefaultErrorBehavior ErrorBehavior `json:"defaultErrorBehavior"`
	Inputs               []InputField  `json:"inputs"`
	// GitVariable optionally names the variable (dotted path allowed) holding
	// the run's branch/worktree for the dashboard Git column. Empty applies the
	// conventional fallback: branch, pr_branch, then work_dir's last segment.
	GitVariable string `json:"gitVariable,omitempty"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
}

// InputField describes a required or optional input parameter for a workflow.
type InputField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required"`
}

// NodePosition stores the x/y coordinates of a node on the canvas.
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node is a single processing step in the workflow graph.
type Node struct {
	ID            string        `json:"id"`
	Type          NodeType      `json:"type"`
	Config        NodeConfig    `json:"config"`
	Label         string        `json:"label"`
	Description   string        `json:"description"`
	ErrorBehavior ErrorBehavior `json:"errorBehavior,omitempty"`
	Entrypoint    bool          `json:"entrypoint,omitempty"`
	Position      *NodePosition `json:"position,omitempty"`
}

// NodeConfig holds the type-specific configuration for a node.
type NodeConfig struct {
	TemplateFile       string            `json:"templateFile,omitempty"`
	Model              string            `json:"model,omitempty"`
	ModelProfile       string            `json:"model_profile,omitempty"` // alias of a profile (chat for llm_call, agent for agent_call)
	Backend            string            `json:"backend,omitempty"`       // agent backend axis: "" | "claude-code" | "opencode" | "codex" (agent_call/interactive_agent only)
	ToolScope          []string          `json:"toolScope,omitempty"`     // agent_call: allowed-tools list (default: full agent scope)
	ChildWorkflow      string            `json:"childWorkflow,omitempty"` // spawn_workflow: the child workflow's name
	SpawnMode          string            `json:"spawnMode,omitempty"`     // spawn_workflow: "fire_and_forget" | "wait_for_child"
	OutputSchema       any               `json:"outputSchema,omitempty"`
	MaxTokens          int               `json:"maxTokens,omitempty"`
	Temperature        float64           `json:"temperature,omitempty"`
	SystemPrompt       string            `json:"systemPrompt,omitempty"`
	OrchestrationFile  string            `json:"orchestrationFile,omitempty"`
	ScriptFile         string            `json:"scriptFile,omitempty"`
	Interpreter        string            `json:"interpreter,omitempty"`
	Command            string            `json:"command,omitempty"`
	Args               []string          `json:"args,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	TimeoutSeconds     int               `json:"timeoutSeconds,omitempty"`
	Conditions         []Condition       `json:"conditions,omitempty"`
	DefaultOutput      string            `json:"defaultOutput,omitempty"`
	Operations         []Operation       `json:"operations,omitempty"`
	DurationSeconds    string            `json:"durationSeconds,omitempty"`
	Secret             string            `json:"secret,omitempty"`
	WaitTimeoutSeconds string            `json:"waitTimeoutSeconds,omitempty"`
	// Group is the parallel_group configuration (parallel_group only). Read it
	// through ParseGroupConfig, which checks it and returns a deep copy.
	Group *GroupConfig `json:"group,omitempty"`
}

// Condition defines a branching rule in a conditional node.
type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
	Output   string `json:"output"`
}

// Operation defines a data transformation step in a transform node.
type Operation struct {
	Variable  string `json:"variable"`
	Operation string `json:"operation"`
	Value     any    `json:"value,omitempty"`
}

// Edge connects two nodes in the workflow graph.
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Output string `json:"output,omitempty"`
	Label  string `json:"label,omitempty"`
}

// ErrorBehavior specifies how errors are handled at the workflow or node level.
type ErrorBehavior struct {
	Type              string `json:"type,omitempty"`
	Retries           int    `json:"retries,omitempty"`
	RetryDelaySeconds int    `json:"retryDelaySeconds,omitempty"`
	DefaultOutput     any    `json:"defaultOutput,omitempty"`
	ErrorNodeID       string `json:"errorNodeId,omitempty"`
}

// Scope labels distinguish where a listed workflow lives.
const (
	ScopeLabelPrivate = "private"
	ScopeLabelShared  = "shared"
)

// WorkflowSummary is a lightweight representation for listing workflows.
type WorkflowSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	NodeCount   int    `json:"nodeCount"`
	// Scope is "private" or "shared"; lets the dashboard/MCP distinguish.
	Scope string `json:"scope,omitempty"`
	// Publisher is the publishing user's id for shared entries; empty for private.
	Publisher string `json:"publisher,omitempty"`
	// Visibility is "department" or "public" for shared entries; empty for private.
	Visibility string `json:"visibility,omitempty"`
	// Department is the owning department for department-visibility shared entries.
	Department string `json:"department,omitempty"`
}
