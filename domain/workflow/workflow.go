// Package workflow defines the domain types for workflow definitions.
package workflow

import "github.com/google/uuid"

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
)

// IsValidAgentBackend reports whether b is a recognized agent backend value.
// An empty value (BackendUnset) is valid (it means "inherit"). Any other value
// must exactly match a known backend; near-misses (wrong case, typos) are
// rejected so a mis-routed agent fails closed at workflow load.
func IsValidAgentBackend(b string) bool {
	switch b {
	case BackendUnset, BackendClaudeCode, BackendOpencode, BackendCodex:
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
	Nodes                []Node        `json:"nodes"`
	Edges                []Edge        `json:"edges"`
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
