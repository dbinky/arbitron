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

// LLMProvider abstracts large-language-model calls for use by LLM nodes.
type LLMProvider interface {
	Call(ctx context.Context, request LLMRequest) (*LLMResponse, error)
	StreamCall(ctx context.Context, request LLMRequest) (<-chan LLMStreamChunk, error)
}

// LLMMessage represents a single message in a multi-turn LLM conversation.
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMRequest describes a single LLM invocation.
type LLMRequest struct {
	Model        string
	SystemPrompt string
	Prompt       string       // Single-turn prompt (used when Messages is empty).
	Messages     []LLMMessage // Multi-turn conversation history (takes precedence over Prompt).
	MaxTokens    int
	Temperature  float64
	OutputSchema any
	Tools        []LLMTool
}

// LLMTool describes a tool definition passed to the LLM provider.
type LLMTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

// LLMResponse holds the result of a non-streaming LLM call.
type LLMResponse struct {
	Output       any
	Conversation any
	Model        string
	TokensIn     int
	TokensOut    int
}

// LLMStreamChunk is one piece of a streaming LLM response.
type LLMStreamChunk struct {
	Token     string
	Done      bool
	Error     error
	TokensIn  int
	TokensOut int
	// ToolUse is set on the final chunk when the LLM invokes a tool instead
	// of producing a plain-text response.
	ToolUse *LLMToolUse
}

// LLMToolUse describes a tool invocation detected in a streaming response.
type LLMToolUse struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
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

	// InteractiveCompletionToolName is the tool name the LLM must call to end an
	// interactive session with structured output. Both InteractiveExecutor and
	// ConversationHandler must agree on this value — a mismatch causes silent
	// session hang (tool called but never detected).
	InteractiveCompletionToolName        = "complete_session"
	InteractiveCompletionToolDescription = "Call this tool when the conversation has reached its natural conclusion and you have the final output ready."
)

// Event represents a domain event emitted during workflow execution.
type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// EventBus defines the port for publishing and subscribing to workflow execution events.
type EventBus interface {
	Publish(instanceID uuid.UUID, event Event)
	Subscribe(instanceID uuid.UUID) (<-chan Event, func())
	SubscribeAll() (<-chan Event, func())
}
