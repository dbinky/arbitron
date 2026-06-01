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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("gpt-4"))
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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("gpt-4"))
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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("gpt-4"))
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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("gpt-4"))
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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("gpt-4"))
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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("gpt-4"))
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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("gpt-4"))
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

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("claude-resolved-model"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "claude-resolved-model", capturedReq.Model)
	assert.Equal(t, "test prompt", capturedReq.Prompt)
	assert.Equal(t, 1000, capturedReq.MaxTokens)
	assert.Equal(t, 0.5, capturedReq.Temperature)
	assert.Equal(t, schema, capturedReq.OutputSchema)
}

// --- GAP 2: provider-agnostic structured output --------------------------

// llmNodeWithSchema builds an llm_call node carrying an object outputSchema.
func llmNodeWithSchema() workflow.Node {
	return workflow.Node{
		ID:   "assessor",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
			OutputSchema: map[string]any{"type": "object"},
		},
	}
}

// Category 1 (happy path, claude-cli-style raw JSON text): a provider that
// returns the structured result as a RAW JSON STRING (claude-cli / azure shape)
// must, with a schema, be parsed into a map so `assessor.done` resolves
// downstream regardless of which provider produced it.
func TestLLMCallExecutor_SchemaParsesRawJSONText(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("assess")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{
				Output: `{"done": true, "notes": "focus areas satisfied"}`,
				Model:  "claude-sonnet-4",
			}, nil
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("claude-sonnet-4"))
	result, err := exec.Execute(context.Background(), llmNodeWithSchema(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	m, ok := result.Output.(map[string]any)
	require.True(t, ok, "schema output must be a map so node.field references resolve")
	assert.Equal(t, true, m["done"])
	assert.Equal(t, "focus areas satisfied", m["notes"])
}

// Category 2 (passthrough): a provider that already returns a map (openrouter
// shape, whose adapter parsed it) is passed through untouched — no double-parse.
func TestLLMCallExecutor_SchemaPassesThroughMap(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("assess")

	already := map[string]any{"done": false, "notes": "more work remains"}
	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: already, Model: "deepseek"}, nil
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("deepseek"))
	result, err := exec.Execute(context.Background(), llmNodeWithSchema(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	assert.Equal(t, already, result.Output)
}

// Category 3 (no schema, unchanged): without a schema, a JSON-string output is
// parsed and a non-JSON string is wrapped, exactly as before (regression guard
// alongside the existing without-schema tests).
func TestLLMCallExecutor_NoSchemaUnchanged(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("free text")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: "just some prose", Model: "m"}, nil
		},
	}

	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("m"))
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"response": "just some prose"}, result.Output)
}

// Category 4 (error): invalid JSON text under a schema yields a clear,
// node-attributed error rather than an unparseable string downstream.
func TestLLMCallExecutor_SchemaInvalidJSONErrors(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("assess")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: "this is not json", Model: "claude-sonnet-4"}, nil
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("claude-sonnet-4"))
	_, err := exec.Execute(context.Background(), llmNodeWithSchema(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "assessor", "the error is attributed to the node")
	assert.Contains(t, err.Error(), "structured output")
}

// Error: ChatProviderSelector.ChatProviderFor returns an error -> wrapped
// "select provider" error, no Chat call.
func TestLLMCallExecutor_SelectorError(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("some prompt")

	wantErr := errors.New("no chat adapter for provider: unknown")
	failSel := staticChatSelector{err: wantErr}

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, failSel, chatResolver("gpt-4"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "select provider")
	assert.ErrorIs(t, err, wantErr)
}

// Success: the {{workspace}} reserved variable seeds ChatRequest.WorkDir so
// the ChatProvider (claude-cli ChatAdapter) receives the instance path.
// Mirrors TestAgentCallExecutor_HappyPath's WorkDir assertion for the LLM path.
func TestLLMCallExecutor_WorkDirFromWorkspace(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("prompt")

	var capturedReq engine.ChatRequest
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			capturedReq = req
			return &engine.ChatResponse{Output: map[string]any{"ok": true}, Model: "m"}, nil
		},
	}

	vars := variable.NewStore()
	vars.SetReserved(engine.ReservedVarWorkspace, "/workspace/inst-llm")

	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
			OutputSchema: map[string]any{"type": "object"},
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("m"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "/workspace/inst-llm", capturedReq.WorkDir,
		"{{workspace}} must seed ChatRequest.WorkDir so the ChatProvider receives the instance path")
}

// Edge: an unseeded {{workspace}} yields an empty WorkDir in ChatRequest; the
// ChatProvider rejects it and the error surfaces. Mirrors the AgentCallExecutor
// MissingWorkspace test to maintain parity on the LLM path.
func TestLLMCallExecutor_MissingWorkspace(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("prompt")

	errWorkDirRequired := errors.New("work dir required")
	var sawWorkDir string
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			sawWorkDir = req.WorkDir
			if req.WorkDir == "" {
				return nil, errWorkDirRequired
			}
			return &engine.ChatResponse{Output: map[string]any{"ok": true}, Model: "m"}, nil
		},
	}

	// No workspace seeded.
	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("m"))
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Empty(t, sawWorkDir, "unseeded workspace must propagate as empty WorkDir")
	assert.ErrorIs(t, err, errWorkDirRequired)
	assert.Contains(t, err.Error(), "provider call")
}

// SECURITY: the executor must never call binding.RevealKey. Credential
// injection is the selector's/adapter's responsibility; calling RevealKey in the
// executor would expose the plaintext key to the executor's call stack (where it
// could be logged or serialised into error messages). The spyRevealResolver's
// RevealKey counter must stay at zero after a successful Execute.
func TestLLMCallExecutor_RevealKeyNotCalledByExecutor(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("prompt")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: map[string]any{"ok": true}, Model: "m"}, nil
		},
	}

	var revealCalls int
	resolver := newSpyRevealResolver("chat-default", provider.ProfileKindChat, "m", &revealCalls)

	vars := variable.NewStore()
	vars.SetReserved(engine.ReservedVarWorkspace, "/tmp")
	node := workflow.Node{
		ID:   "llm1",
		Type: workflow.NodeTypeLLMCall,
		Config: workflow.NodeConfig{
			TemplateFile: "prompt.txt",
			ModelProfile: "chat-default",
			OutputSchema: map[string]any{"type": "object"},
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), resolver)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, 0, revealCalls,
		"LLMCallExecutor must never call RevealKey; credential injection is the selector/adapter's responsibility")
}

// Category 5 (empty output edge): an empty-string output under a schema is a
// parse error (empty is not valid JSON), surfaced clearly rather than silently
// passed through.
func TestLLMCallExecutor_SchemaEmptyOutputErrors(t *testing.T) {
	fr := newMockFileReader()
	fr.files["test-wf/prompt.txt"] = []byte("assess")

	prov := &mockChatProvider{
		callFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: "", Model: "claude-sonnet-4"}, nil
		},
	}

	exec := engine.NewLLMCallExecutor(fr, chatSelector(prov), chatResolver("claude-sonnet-4"))
	_, err := exec.Execute(context.Background(), llmNodeWithSchema(), variable.NewStore(), "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "structured output")
}
