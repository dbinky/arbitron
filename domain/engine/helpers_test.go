package engine_test

import (
	"fmt"
	"os"

	"github.com/arbitron/arbitron/domain/provider"
)

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

func (r *fakeResolver) Resolve(alias string) (provider.Profile, error) {
	p, ok := r.byAlias[alias]
	if !ok {
		return provider.Profile{}, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, alias)
	}
	return p, nil
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
