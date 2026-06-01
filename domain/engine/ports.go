// Package engine defines the ports (interfaces) required by the workflow execution engine.
package engine

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/queue"
	"github.com/google/uuid"
)

// FileReader provides access to workflow-associated files on disk.
type FileReader interface {
	ReadFile(workflowName string, relativePath string) ([]byte, error)
	ResolvePath(workflowName string, relativePath string) (string, error)
	FileExists(workflowName string, relativePath string) bool
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
	Summary   string      // result/summary text
	Steps     []AgentStep // step events (tool calls, edits) for the trace
	Model     string
	TokensIn  int // coarse; subprocess accounting
	TokensOut int
	SessionID string
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

// EventBus defines the port for publishing and subscribing to workflow execution events.
// The unsubscribe function returned by Subscribe and SubscribeAll MUST close
// the returned channel so that consumers ranging over the channel terminate.
type EventBus interface {
	Publish(instanceID uuid.UUID, event Event)
	Subscribe(instanceID uuid.UUID) (<-chan Event, func())
	SubscribeAll() (<-chan Event, func())
}
