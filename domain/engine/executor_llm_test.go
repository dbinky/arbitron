package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChatProvider implements engine.ChatProvider for tests.
type mockChatProvider struct {
	callFn func(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)
}

func (m *mockChatProvider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	return m.callFn(ctx, req)
}

func (m *mockChatProvider) StreamChat(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return nil, errors.New("streaming not implemented in mock")
}

// chatResolver builds a resolver with a single chat profile aliased "chat-default"
// mapping to the given model.
func chatResolver(model string) *fakeResolver {
	return newFakeResolver(provider.Profile{
		Alias: "chat-default",
		Kind:  provider.ProfileKindChat,
		Model: model,
	})
}

// Happy path: resolves a chat profile -> ChatRequest.Model = prof.Model; calls
// Chat; returns structured output with model + tokens.
func TestLLMCallExecutor_SuccessWithSchema(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Summarize: {{topic}}")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{
				Output:       map[string]any{"summary": "A brief summary"},
				Conversation: []map[string]any{{"role": "user", "content": req.Prompt}},
				Model:        "gpt-4",
				TokensIn:     10,
				TokensOut:    20,
			}, nil
		},
	}

	vars := variable.NewStore()
	vars.SetInput("topic", "quantum computing")

	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
			MaxTokens:    500,
			Temperature:  0.7,
			OutputSchema: map[string]any{"type": "object"},
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("gpt-4"))
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"summary": "A brief summary"}, result.Output)
	assert.Equal(t, "gpt-4", result.LLMModel)
	assert.Equal(t, 10, result.LLMTokensIn)
	assert.Equal(t, 20, result.LLMTokensOut)
	assert.NotNil(t, result.LLMConversation)
}

// Success: structured vs text both handled; resolved model threads to the request.
func TestLLMCallExecutor_SuccessWithoutSchema_JSONResponse(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Generate JSON")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{
				Output:   `{"key": "value"}`,
				Model:    "gpt-4",
				TokensIn: 5,
			}, nil
		},
	}

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("gpt-4"))
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"key": "value"}, result.Output)
}

// Edge case: non-JSON text with no schema is wrapped in {"response": ...}.
func TestLLMCallExecutor_SuccessWithoutSchema_TextResponse(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Tell me a joke")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{
				Output:   "Why did the chicken cross the road?",
				Model:    "gpt-4",
				TokensIn: 5,
			}, nil
		},
	}

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("gpt-4"))
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"response": "Why did the chicken cross the road?"}, result.Output)
}

// Happy path: {{variable}} substitution feeds the prompt.
func TestLLMCallExecutor_TemplateSubstitution(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Hello {{name}}, summarize {{topic}}")

	var capturedPrompt string
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			capturedPrompt = req.Prompt
			return &engine.ChatResponse{
				Output: map[string]any{"ok": true},
				Model:  "gpt-4",
			}, nil
		},
	}

	vars := variable.NewStore()
	vars.SetInput("name", "Alice")
	vars.SetInput("topic", "AI safety")

	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
			OutputSchema: map[string]any{"type": "object"},
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("gpt-4"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "Hello Alice, summarize AI safety", capturedPrompt)
}

// Error: ChatProvider.Chat error -> wrapped provider call error.
func TestLLMCallExecutor_ProviderError(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("some prompt")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return nil, errors.New("rate limit exceeded")
		},
	}

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("gpt-4"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider call")
	assert.Contains(t, err.Error(), "rate limit")
}

// Error: resolver miss -> wrapped resolve profile error, no provider call.
func TestLLMCallExecutor_ResolverMiss(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("some prompt")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			t.Fatal("provider should not be called on resolver miss")
			return nil, nil
		},
	}

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "no-such-profile",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("gpt-4"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve profile")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// Failure: template read error -> wrapped error, no provider call.
func TestLLMCallExecutor_FileNotFound(t *testing.T) {
	fr := newMockFileReader()
	// No file registered.

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			t.Fatal("provider should not be called")
			return nil, nil
		},
	}

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "missing.txt",
			ModelProfile: "chat-default",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("gpt-4"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read template")
}

// Success: the resolved model and other request fields thread into ChatRequest.
func TestLLMCallExecutor_RequestFields(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("test prompt")

	var capturedReq engine.ChatRequest
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			capturedReq = req
			return &engine.ChatResponse{
				Output: map[string]any{"ok": true},
				Model:  "gpt-4",
			}, nil
		},
	}

	vars := variable.NewStore()
	schema := map[string]any{"type": "object", "properties": map[string]any{}}

	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
			MaxTokens:    1000,
			Temperature:  0.5,
			OutputSchema: schema,
		},
	}

	exec := engine.NewLLMCallExecutor(fr, prov, chatResolver("claude-resolved-model"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "claude-resolved-model", capturedReq.Model)
	assert.Equal(t, "test prompt", capturedReq.Prompt)
	assert.Equal(t, 1000, capturedReq.MaxTokens)
	assert.Equal(t, 0.5, capturedReq.Temperature)
	assert.Equal(t, schema, capturedReq.OutputSchema)
}
