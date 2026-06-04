package engine_test

import (
	"context"
	"fmt"
	"os"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// staticChatSelector returns the same ChatProvider for every binding. It lets
// executor tests inject a single fake through the new ChatProviderSelector seam
// without depending on the concrete adapter/provider factory.
type staticChatSelector struct {
	provider engine.ChatProvider
	err      error
}

func (s staticChatSelector) ChatProviderFor(provider.ResolvedBinding) (engine.ChatProvider, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.provider, nil
}

var _ engine.ChatProviderSelector = staticChatSelector{}

// chatSelector wraps a ChatProvider in a ChatProviderSelector for tests.
func chatSelector(p engine.ChatProvider) engine.ChatProviderSelector {
	return staticChatSelector{provider: p}
}

// staticAgentSelector returns the same AgentProvider for every binding.
type staticAgentSelector struct {
	provider engine.AgentProvider
	err      error
}

func (s staticAgentSelector) AgentProviderFor(provider.ResolvedBinding) (engine.AgentProvider, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.provider, nil
}

var _ engine.AgentProviderSelector = staticAgentSelector{}

// agentSelector wraps an AgentProvider in an AgentProviderSelector for tests.
func agentSelector(p engine.AgentProvider) engine.AgentProviderSelector {
	return staticAgentSelector{provider: p}
}

// fakeStreamSelector implements engine.InteractiveStreamSelector for tests. It
// records the binding it was asked to route and returns chatProv for chat-kind
// bindings and agentProv for agent-kind bindings, mirroring the real
// adapter/provider InteractiveStreamSelector routing. A non-nil err short-circuits.
type fakeStreamSelector struct {
	chatProv  engine.ChatProvider
	agentProv engine.ChatProvider
	err       error
	gotKind   provider.ProfileKind
	gotModel  string
}

func (s *fakeStreamSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	s.gotKind = b.Kind
	s.gotModel = b.Model
	if s.err != nil {
		return nil, s.err
	}
	if b.Kind == provider.ProfileKindAgent {
		return s.agentProv, nil
	}
	return s.chatProv, nil
}

var _ engine.InteractiveStreamSelector = (*fakeStreamSelector)(nil)

// streamSelector wraps a single ChatProvider as a chat-kind InteractiveStreamSelector.
func streamSelector(p engine.ChatProvider) *fakeStreamSelector {
	return &fakeStreamSelector{chatProv: p, agentProv: p}
}

// fakeResolver is an in-memory provider.ProfileResolver for engine tests. A
// miss returns provider.ErrUnknownProfile (wrapped), mirroring the real
// StaticProfileResolver.
type fakeResolver struct {
	byAlias map[string]provider.Profile
}

func newFakeResolver(profiles ...provider.Profile) *fakeResolver {
	m := make(map[string]provider.Profile, len(profiles))
	for _, p := range profiles {
		m[p.Alias] = p
	}
	return &fakeResolver{byAlias: m}
}

// newFakeResolverWithTools is newFakeResolver; the name documents at the call
// site that the profile's AllowedTools are expected to flow into the binding.
func newFakeResolverWithTools(profiles ...provider.Profile) *fakeResolver {
	return newFakeResolver(profiles...)
}

func (r *fakeResolver) Resolve(_ context.Context, alias string) (provider.ResolvedBinding, error) {
	p, ok := r.byAlias[alias]
	if !ok {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, alias)
	}
	return provider.ResolvedBinding{
		Kind:         p.Kind,
		Model:        p.Model,
		AllowedTools: p.AllowedTools,
		RevealKey: func(context.Context) (string, error) {
			return "", nil
		},
	}, nil
}

var _ provider.ProfileResolver = (*fakeResolver)(nil)

// spyRevealResolver is a ProfileResolver whose bindings carry a RevealKey that
// increments a counter. Executor security tests use this to assert that the
// executor itself never calls RevealKey — credential injection is the
// selector/adapter's responsibility, not the executor's.
type spyRevealResolver struct {
	alias       string
	kind        provider.ProfileKind
	model       string
	revealCalls *int
}

func newSpyRevealResolver(alias string, kind provider.ProfileKind, model string, calls *int) *spyRevealResolver {
	return &spyRevealResolver{alias: alias, kind: kind, model: model, revealCalls: calls}
}

func (r *spyRevealResolver) Resolve(_ context.Context, alias string) (provider.ResolvedBinding, error) {
	if alias != r.alias {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, alias)
	}
	return provider.ResolvedBinding{
		Kind:  r.kind,
		Model: r.model,
		RevealKey: func(context.Context) (string, error) {
			*r.revealCalls++
			return "", nil
		},
	}, nil
}

var _ provider.ProfileResolver = (*spyRevealResolver)(nil)

// mockFileReader implements engine.FileReader for tests.
type mockFileReader struct {
	files map[string][]byte
	paths map[string]string
}

func newMockFileReader() *mockFileReader {
	return &mockFileReader{
		files: make(map[string][]byte),
		paths: make(map[string]string),
	}
}

func (m *mockFileReader) ReadFile(_ uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	key := workflowName + "/" + relativePath
	data, ok := m.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (m *mockFileReader) ResolvePath(_ uuid.UUID, workflowName, relativePath string) (string, error) {
	key := workflowName + "/" + relativePath
	p, ok := m.paths[key]
	if !ok {
		return "", os.ErrNotExist
	}
	return p, nil
}

func (m *mockFileReader) FileExists(_ uuid.UUID, workflowName, relativePath string) bool {
	key := workflowName + "/" + relativePath
	_, ok := m.paths[key]
	return ok
}
