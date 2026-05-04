package engine_test

import "os"

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
