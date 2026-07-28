// Package engine defines the ports (interfaces) required by the workflow execution engine.
package engine

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// FileReader provides access to workflow-associated files on disk. The owner
// selects which user's private workspace to search first; the reader falls
// back to the shared (published) space. For shared/system reads pass
// uuid.Nil to search the shared space only.
type FileReader interface {
	ReadFile(owner uuid.UUID, workflowName string, relativePath string) ([]byte, error)
	ResolvePath(owner uuid.UUID, workflowName string, relativePath string) (string, error)
	FileExists(owner uuid.UUID, workflowName string, relativePath string) bool
}

// WorkspaceManager provides per-instance working directories for agent nodes.
type WorkspaceManager interface {
	// EnsureInstanceDir creates (idempotently) the working directory for an
	// instance and returns its absolute path.
	EnsureInstanceDir(instanceID uuid.UUID) (string, error)
	// RemoveInstanceDir deletes an instance's working directory. Removing a
	// directory that does not exist is not an error.
	RemoveInstanceDir(instanceID uuid.UUID) error
	// InstanceDirStats returns the cumulative byte size and file count of
	// an instance directory, used by the engine to log sensitive-data
	// retention when a workspace is kept past instance completion (SF9 /
	// PR #8 security MEDIUM thread 3291941512). A missing directory
	// returns (0, 0, nil).
	InstanceDirStats(instanceID uuid.UUID) (bytes int64, files int, err error)
}

// ChatProvider performs reasoning/chat with no tool use and no file side
// effects. Backends: tool-suppressed claude -p (P1); Azure AI Foundry,
// OpenRouter (P3).
type ChatProvider interface {
	Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error)
	StreamChat(ctx context.Context, request ChatRequest) (<-chan ChatChunk, error)
}

// AgentProvider runs an agentic mission that mutates files inside the
// instance workspace. The agentic loop (tool calls, edits, test runs)
// happens INSIDE the harness, not inside Arbitron. Backends: claude -p with
// tools (P1); opencode (P3).
type AgentProvider interface {
	RunAgent(ctx context.Context, mission AgentMission) (*AgentResult, error)
}

// ChatProviderSelector chooses the concrete ChatProvider for a resolved
// binding. The composition root supplies an implementation (the
// adapter/provider factory); this keeps domain/engine free of concrete adapter
// imports while letting the LLMCallExecutor pick a backend per node from the
// resolved binding's Provider (claude-cli, openrouter, azure, ...).
type ChatProviderSelector interface {
	ChatProviderFor(b provider.ResolvedBinding) (ChatProvider, error)
}

// AgentProviderSelector chooses the concrete AgentProvider for a resolved
// binding. It is the agent-side mirror of ChatProviderSelector: the
// AgentCallExecutor selects claude-cli or opencode per node from the resolved
// binding's Provider, without importing either adapter package.
type AgentProviderSelector interface {
	AgentProviderFor(b provider.ResolvedBinding) (AgentProvider, error)
}

// InteractiveStreamSelector chooses the streaming ChatProvider for an
// interactive node from a resolved binding: chat bindings get the tool-suppressed
// chat stream; agent bindings get the tool-enabled (YOLO) stream.
type InteractiveStreamSelector interface {
	StreamProviderFor(b provider.ResolvedBinding) (ChatProvider, error)
}

// ChildInputRequest is everything the preparer needs to turn a parent's
// available context into startup values for a child workflow. It is a struct
// rather than a positional signature because the field set is the contract and
// is expected to grow (e.g. a per-node token budget) without breaking callers.
type ChildInputRequest struct {
	// NodeID is the spawn node being executed. It is used to attribute errors
	// and to assert the binding kind against the node type.
	NodeID string
	// ChildWorkflow is the child's name, used only for error messages and prompt
	// framing. The preparer never loads a workflow itself.
	ChildWorkflow string
	// Inputs is the CHILD's declared input contract, loaded by the caller. The
	// child's declaration is the authority; the parent never names the fields.
	Inputs []workflow.InputField
	// ParentContext is the parent run's flat variable snapshot (its inputs and
	// all prior node outputs).
	ParentContext map[string]any
	// Guidance is the optional author-supplied prose, already read by the
	// caller. Empty means no guidance was configured.
	Guidance string
	// ProfileAlias is the chat profile alias to resolve for the preparation call.
	ProfileAlias string
}

// ChildInputPreparer turns a parent's available context into startup values
// that satisfy a child workflow's declared input contract.
//
// GUARANTEES: on success, every REQUIRED input of the child is present and
// non-null. On failure, NO partial result is returned — the caller cannot
// accidentally spawn with half a payload.
//
// Because the return value is "values that satisfy the contract", the caller
// needs no knowledge of how they were obtained; a future non-LLM implementation
// (static mapping, cached result) satisfies the same port.
type ChildInputPreparer interface {
	Prepare(ctx context.Context, req ChildInputRequest) (map[string]any, error)
}

// AgentMission describes a single agentic mission to run inside the instance
// workspace.
type AgentMission struct {
	Model     string   // resolved model-id (or provider/model for opencode)
	Prompt    string   // the mission instruction
	WorkDir   string   // = the instance {{workspace}}
	ToolScope []string // allowed-tools / permission scope
	SessionID string   // optional cross-step resume; see opencode spike
	Timeout   time.Duration
}

// AgentResult holds the outcome of an agentic mission.
type AgentResult struct {
	Summary string      // result/summary text
	Steps   []AgentStep // step events (tool calls, edits) for the trace
	Model   string
	// DroppedTools are requested tools the backend could not map to a native
	// tool and silently trimmed from the effective allowlist. Surfaced so the
	// (security-relevant) narrowing of the tool posture is observable in the
	// trace, not only in backend logs. Nil/empty when nothing was dropped.
	DroppedTools []string
	TokensIn     int // coarse; subprocess accounting
	TokensOut    int
	SessionID    string
}

// AgentStep is a single step event emitted during an agentic mission.
type AgentStep struct {
	Kind    string // "tool_call" | "edit" | "assistant" | "result"
	Summary string
}

// ChatRequest describes a single LLM invocation.
//
// History: the multi-turn Messages/Tools/LLMMessage/LLMTool/LLMToolUse types
// were removed in the PR #8 cleanup (Should Fix #8 / thread 3291907744). The
// current claude-cli provider does not consume them — resumed turns use
// SessionID + a Prompt, not a Messages array — and the Anthropic Messages-API
// adapter ships as a stub. Reintroduce only if/when a real Messages-API
// adapter lands.
type ChatRequest struct {
	Model        string
	SystemPrompt string
	Prompt       string
	MaxTokens    int
	Temperature  float64
	OutputSchema any
	// WorkDir is the working directory for subprocess-backed providers
	// (e.g. claude-cli). Empty means the provider's default.
	WorkDir string
	// SessionID, when non-empty, resumes an existing provider session
	// (claude-cli: `--resume`).
	SessionID string
	// ToolScope is the per-request tool allowlist for tool-enabled (agent)
	// streaming providers (PR #18 item 1b). When non-empty, the agent adapter
	// prefers it over its global cfg.LLM.AllowedTools so an interactive_agent
	// node runs against its profile's explicit minimal allowlist. Empty means
	// "use the adapter's global scope". Ignored by tool-suppressed chat
	// providers, which never emit tools.
	ToolScope []string
}

// ChatResponse holds the result of a non-streaming LLM call.
type ChatResponse struct {
	Output       any
	Conversation any
	Model        string
	TokensIn     int
	TokensOut    int
	// SessionID identifies the provider session for resumption. Empty for
	// providers without session support.
	SessionID string
}

// ChatChunk is one piece of a streaming LLM response.
type ChatChunk struct {
	Token     string
	Done      bool
	Error     error
	TokensIn  int
	TokensOut int
	// SessionID is set on the terminal chunk for providers with session
	// support, so callers can persist it for resumption.
	SessionID string
}

// QueueManager defines the queue operations the engine needs.
type QueueManager interface {
	Enqueue(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (started bool, err error)
	NotifyCapacityFreed(ctx context.Context)
}

// Timer type constants imported from the queue package (single source of truth).
// Re-exported here for convenience so executor callers don't need to import queue directly.
const (
	TimerTypeDelay   = queue.TimerTypeDelay
	TimerTypeWebhook = queue.TimerTypeWebhook
)

// TimerScheduler defines the timer scheduling operations the engine needs.
type TimerScheduler interface {
	Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string)
	Cancel(instanceID uuid.UUID)
}

// WebhookRegistrar defines the port for registering/deactivating webhook endpoints.
// The adapter's WebhookRegistry implements this interface.
type WebhookRegistrar interface {
	Register(token string, instanceID uuid.UUID, nodeID string, secretHash *string)
	Deactivate(token string)
}

// Event type constants for domain events emitted during workflow execution.
const (
	// Node lifecycle events.
	EventTypeNodeStarted   = "node.started"
	EventTypeNodeCompleted = "node.completed"
	EventTypeNodeFailed    = "node.failed"

	// Instance lifecycle events.
	EventTypeInstanceCompleted = "instance.completed"
	EventTypeInstanceFailed    = "instance.failed"
	EventTypeInstanceCancelled = "instance.cancelled"

	// Interactive session events.
	EventTypeInteractiveMessage = "interactive.message"
	EventTypeInteractiveToken   = "interactive.token"
	EventTypeInteractiveEnded   = "interactive.ended"
	EventTypeInteractiveHistory = "interactive.history"
	EventTypeInteractiveError   = "interactive.error"
)

// Event represents a domain event emitted during workflow execution.
type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// ScopedEvent pairs an event with the instance it was published for, so
// SubscribeAll consumers (the system stream) can authorize delivery per event
// by instance ownership. The per-instance Subscribe channel stays a bare Event
// (its instance is already the subscription key), so the on-the-wire Event JSON
// is unchanged.
type ScopedEvent struct {
	InstanceID uuid.UUID
	Event      Event
}

// EventBus defines the port for publishing and subscribing to workflow execution events.
// The unsubscribe function returned by Subscribe and SubscribeAll MUST close
// the returned channel so that consumers ranging over the channel terminate.
type EventBus interface {
	Publish(instanceID uuid.UUID, event Event)
	Subscribe(instanceID uuid.UUID) (<-chan Event, func())
	// SubscribeAll yields ScopedEvent so consumers can authorize each event by
	// the instance it belongs to.
	SubscribeAll() (<-chan ScopedEvent, func())
}
