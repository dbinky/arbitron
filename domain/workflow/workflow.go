// Package workflow defines the domain types for workflow definitions.
package workflow

// NodeType identifies the kind of processing a workflow node performs.
type NodeType string

const (
	NodeTypeLLMCall     NodeType = "llm_call"
	NodeTypeScript      NodeType = "script"
	NodeTypeToolCall    NodeType = "tool_call"
	NodeTypeConditional NodeType = "conditional"
	NodeTypeTransform   NodeType = "transform"
	NodeTypeTimerDelay  NodeType = "timer_delay"
	NodeTypeWebhookWait NodeType = "webhook_wait"
	NodeTypeInteractive NodeType = "interactive"
)

// Workflow is the top-level definition of a directed graph of processing nodes.
type Workflow struct {
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

// WorkflowSummary is a lightweight representation for listing workflows.
type WorkflowSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	NodeCount   int    `json:"nodeCount"`
}
