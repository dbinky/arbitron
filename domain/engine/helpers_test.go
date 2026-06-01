package engine_test

import (
	"context"
	"fmt"
	"os"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
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

func (r *fakeResolver) Resolve(alias string) (provider.ResolvedBinding, error) {
	p, ok := r.byAlias[alias]
	if !ok {
		return provider.ResolvedBinding{}, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, alias)
	}
	return provider.ResolvedBinding{
		Kind:   p.Kind,
		Model:  p.Model,
		Params: p.Params,
		RevealKey: func(context.Context) (string, error) {
			return "", nil
		},
	}, nil
}

var _ provider.ProfileResolver = (*fakeResolver)(nil)

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

func (m *mockFileReader) ReadFile(workflowName, relativePath string) ([]byte, error) {
	key := workflowName + "/" + relativePath
	data, ok := m.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (m *mockFileReader) ResolvePath(workflowName, relativePath string) (string, error) {
	key := workflowName + "/" + relativePath
	p, ok := m.paths[key]
	if !ok {
		return "", os.ErrNotExist
	}
	return p, nil
}

func (m *mockFileReader) FileExists(workflowName, relativePath string) bool {
	key := workflowName + "/" + relativePath
	_, ok := m.paths[key]
	return ok
}
