package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLLMProvider implements engine.LLMProvider for tests.
type mockLLMProvider struct {
	callFn func(ctx context.Context, req engine.LLMRequest) (*engine.LLMResponse, error)
}

func (m *mockLLMProvider) Call(ctx context.Context, req engine.LLMRequest) (*engine.LLMResponse, error) {
	return m.callFn(ctx, req)
}

func (m *mockLLMProvider) StreamCall(_ context.Context, _ engine.LLMRequest) (<-chan engine.LLMStreamChunk, error) {
	return nil, errors.New("streaming not implemented in mock")
}

func TestLLMCallExecutor_SuccessWithSchema(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Summarize: {{topic}}")

	provider := &mockLLMProvider{
		callFn: func(_ context.Context, req engine.LLMRequest) (*engine.LLMResponse, error) {
			return &engine.LLMResponse{
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
			Model:        "gpt-4",
			MaxTokens:    500,
			Temperature:  0.7,
			OutputSchema: map[string]any{"type": "object"},
		},
	}

	exec := engine.NewLLMCallExecutor(fr, provider)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"summary": "A brief summary"}, result.Output)
	assert.Equal(t, "gpt-4", result.LLMModel)
	assert.Equal(t, 10, result.LLMTokensIn)
	assert.Equal(t, 20, result.LLMTokensOut)
	assert.NotNil(t, result.LLMConversation)
}

func TestLLMCallExecutor_SuccessWithoutSchema_JSONResponse(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Generate JSON")

	provider := &mockLLMProvider{
		callFn: func(_ context.Context, _ engine.LLMRequest) (*engine.LLMResponse, error) {
			return &engine.LLMResponse{
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
			Model:        "gpt-4",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, provider)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"key": "value"}, result.Output)
}

func TestLLMCallExecutor_SuccessWithoutSchema_TextResponse(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Tell me a joke")

	provider := &mockLLMProvider{
		callFn: func(_ context.Context, _ engine.LLMRequest) (*engine.LLMResponse, error) {
			return &engine.LLMResponse{
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
			Model:        "gpt-4",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, provider)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"response": "Why did the chicken cross the road?"}, result.Output)
}

func TestLLMCallExecutor_TemplateSubstitution(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("Hello {{name}}, summarize {{topic}}")

	var capturedPrompt string
	provider := &mockLLMProvider{
		callFn: func(_ context.Context, req engine.LLMRequest) (*engine.LLMResponse, error) {
			capturedPrompt = req.Prompt
			return &engine.LLMResponse{
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
			Model:        "gpt-4",
			OutputSchema: map[string]any{"type": "object"},
		},
	}

	exec := engine.NewLLMCallExecutor(fr, provider)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "Hello Alice, summarize AI safety", capturedPrompt)
}

func TestLLMCallExecutor_ProviderError(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("some prompt")

	provider := &mockLLMProvider{
		callFn: func(_ context.Context, _ engine.LLMRequest) (*engine.LLMResponse, error) {
			return nil, errors.New("rate limit exceeded")
		},
	}

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			Model:        "gpt-4",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, provider)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit")
}

func TestLLMCallExecutor_FileNotFound(t *testing.T) {
	fr := newMockFileReader()
	// No file registered.

	provider := &mockLLMProvider{
		callFn: func(_ context.Context, _ engine.LLMRequest) (*engine.LLMResponse, error) {
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
			Model:        "gpt-4",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, provider)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read template")
}

func TestLLMCallExecutor_RequestFields(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("test prompt")

	var capturedReq engine.LLMRequest
	provider := &mockLLMProvider{
		callFn: func(_ context.Context, req engine.LLMRequest) (*engine.LLMResponse, error) {
			capturedReq = req
			return &engine.LLMResponse{
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
			Model:        "gpt-4",
			MaxTokens:    1000,
			Temperature:  0.5,
			OutputSchema: schema,
		},
	}

	exec := engine.NewLLMCallExecutor(fr, provider)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "gpt-4", capturedReq.Model)
	assert.Equal(t, "test prompt", capturedReq.Prompt)
	assert.Equal(t, 1000, capturedReq.MaxTokens)
	assert.Equal(t, 0.5, capturedReq.Temperature)
	assert.Equal(t, schema, capturedReq.OutputSchema)
}
