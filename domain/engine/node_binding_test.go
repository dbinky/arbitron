package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// fakeBindingResolver resolves aliases from a fixed map; err, when set, is
// returned for every alias.
type fakeBindingResolver struct {
	bindings map[string]provider.ResolvedBinding
	err      error
}

func (f fakeBindingResolver) Resolve(_ context.Context, alias string) (provider.ResolvedBinding, error) {
	if f.err != nil {
		return provider.ResolvedBinding{}, f.err
	}
	b, ok := f.bindings[alias]
	if !ok {
		return provider.ResolvedBinding{}, provider.ErrUnknownProfile
	}
	return b, nil
}

const testDefaultBackend = "claude-code"

func codexResolver() fakeBindingResolver {
	return fakeBindingResolver{bindings: map[string]provider.ResolvedBinding{
		"coder": {
			Model: "gpt-6.1-sol", Effort: "high", Backend: "codex", Provider: "litellm",
			BaseURL: "https://secret.example", Extras: map[string]string{"k": "v"},
			RevealKey: func(context.Context) (string, error) { return "sk-secret", nil },
		},
		"chatter": {Model: "claude-sonnet-5", Effort: "medium", Backend: "codex", Provider: "claude-cli"},
		"plain":   {Model: "opus-5.5", Provider: "claude-cli"},
	}}
}

// bind calls resolveNodeBinding and drops the error; error cases assert it explicitly.
func bind(ctx context.Context, r provider.ProfileResolver, n workflow.Node, def string) NodeBinding {
	b, _ := resolveNodeBinding(ctx, r, n, def)
	return b
}

func node(t workflow.NodeType, cfg workflow.NodeConfig) workflow.Node {
	return workflow.Node{ID: "n", Type: t, Config: cfg}
}

func TestResolveNodeBinding_AgentFromProfile(t *testing.T) {
	got := bind(context.Background(), codexResolver(),
		node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "coder"}), testDefaultBackend)
	want := NodeBinding{NodeType: "agent_call", Model: "gpt-6.1-sol", Effort: "high", Backend: "codex", Provider: "litellm"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestResolveNodeBinding_InteractiveAgentLikeAgent(t *testing.T) {
	got := bind(context.Background(), codexResolver(),
		node(workflow.NodeTypeInteractiveAgent, workflow.NodeConfig{ModelProfile: "coder"}), testDefaultBackend)
	if got.Backend != "codex" || got.Model != "gpt-6.1-sol" || got.NodeType != "interactive_agent" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveNodeBinding_BackendPrecedence(t *testing.T) {
	r := codexResolver()
	ctx := context.Background()
	if got := bind(ctx, r, node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "coder", Backend: "opencode"}), testDefaultBackend); got.Backend != "opencode" {
		t.Fatalf("node backend should win: got %q", got.Backend)
	}
	if got := bind(ctx, r, node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "coder"}), testDefaultBackend); got.Backend != "codex" {
		t.Fatalf("profile backend should beat default: got %q", got.Backend)
	}
	if got := bind(ctx, r, node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "plain"}), testDefaultBackend); got.Backend != testDefaultBackend {
		t.Fatalf("empty chain should use default: got %q", got.Backend)
	}
	// interactive_agent: the stream selector never sees node.Config.Backend, so
	// the row must not either (profile > default only).
	if got := bind(ctx, r, node(workflow.NodeTypeInteractiveAgent, workflow.NodeConfig{ModelProfile: "coder", Backend: "claude-code"}), testDefaultBackend); got.Backend != "codex" {
		t.Fatalf("interactive_agent: profile should beat conflicting node backend: got %q", got.Backend)
	}
	if got := bind(ctx, r, node(workflow.NodeTypeInteractiveAgent, workflow.NodeConfig{ModelProfile: "plain", Backend: "codex"}), testDefaultBackend); got.Backend != testDefaultBackend {
		t.Fatalf("interactive_agent: node backend ignored, default applies: got %q", got.Backend)
	}
}

func TestResolveNodeBinding_ChatNodesHaveNoBackend(t *testing.T) {
	for _, nt := range []workflow.NodeType{workflow.NodeTypeLLMCall, workflow.NodeTypeInteractiveLLM} {
		got := bind(context.Background(), codexResolver(),
			node(nt, workflow.NodeConfig{ModelProfile: "chatter"}), testDefaultBackend)
		want := NodeBinding{NodeType: string(nt), Model: "claude-sonnet-5", Effort: "medium", Provider: "claude-cli"}
		if got != want {
			t.Fatalf("%s: got %+v, want %+v", nt, got, want)
		}
	}
}

func TestResolveNodeBinding_LegacyModelWithoutProfile(t *testing.T) {
	got := bind(context.Background(), codexResolver(),
		node(workflow.NodeTypeAgentCall, workflow.NodeConfig{Model: "opus"}), testDefaultBackend)
	want := NodeBinding{NodeType: "agent_call", Model: "opus", Backend: testDefaultBackend}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestResolveNodeBinding_UnknownProfile(t *testing.T) {
	got := bind(context.Background(), codexResolver(),
		node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "deleted", Model: "m"}), testDefaultBackend)
	want := NodeBinding{NodeType: "agent_call", Model: "m", Backend: testDefaultBackend}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestResolveNodeBinding_ResolverErrorDegrades(t *testing.T) {
	r := fakeBindingResolver{err: errors.New("db down")}
	got := bind(context.Background(), r,
		node(workflow.NodeTypeLLMCall, workflow.NodeConfig{ModelProfile: "x", Model: "m"}), testDefaultBackend)
	want := NodeBinding{NodeType: "llm_call", Model: "m"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestResolveNodeBinding_NonModelNodesOnlyType(t *testing.T) {
	for _, nt := range []workflow.NodeType{
		workflow.NodeTypeScript, workflow.NodeTypeTransform, workflow.NodeTypeConditional,
		workflow.NodeTypeTimerDelay, workflow.NodeTypeWebhookWait, workflow.NodeTypeToolCall,
		workflow.NodeTypeSpawnWorkflow,
	} {
		got := bind(context.Background(), codexResolver(),
			node(nt, workflow.NodeConfig{ModelProfile: "coder", Model: "m", Backend: "codex"}), testDefaultBackend)
		if got != (NodeBinding{NodeType: string(nt)}) {
			t.Fatalf("%s: got %+v", nt, got)
		}
	}
}

func TestResolveNodeBinding_NoopResolverDegrades(t *testing.T) {
	got := bind(context.Background(), NoopProfileResolver{},
		node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "coder"}), testDefaultBackend)
	want := NodeBinding{NodeType: "agent_call", Backend: testDefaultBackend}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNoopProfileResolver_ReturnsUnknownProfile(t *testing.T) {
	_, err := NoopProfileResolver{}.Resolve(context.Background(), "any")
	if !errors.Is(err, provider.ErrUnknownProfile) {
		t.Fatalf("got %v, want ErrUnknownProfile", err)
	}
}

func TestResolveNodeBinding_ReturnsResolveError(t *testing.T) {
	_, err := resolveNodeBinding(context.Background(), codexResolver(),
		node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "deleted"}), testDefaultBackend)
	if !errors.Is(err, provider.ErrUnknownProfile) {
		t.Fatalf("got %v, want wrapped ErrUnknownProfile", err)
	}
	if _, err := resolveNodeBinding(context.Background(), codexResolver(),
		node(workflow.NodeTypeAgentCall, workflow.NodeConfig{ModelProfile: "coder"}), testDefaultBackend); err != nil {
		t.Fatalf("resolvable profile: unexpected error %v", err)
	}
	if _, err := resolveNodeBinding(context.Background(), fakeBindingResolver{err: errors.New("x")},
		node(workflow.NodeTypeScript, workflow.NodeConfig{ModelProfile: "coder"}), testDefaultBackend); err != nil {
		t.Fatalf("non-model node never resolves: unexpected error %v", err)
	}
}
