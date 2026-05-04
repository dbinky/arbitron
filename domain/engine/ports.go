// Package engine defines the ports (interfaces) required by the workflow execution engine.
package engine

import "context"

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

// LLMRequest describes a single LLM invocation.
type LLMRequest struct {
	Model        string
	Prompt       string
	MaxTokens    int
	Temperature  float64
	OutputSchema any
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
	Token string
	Done  bool
	Error error
}
