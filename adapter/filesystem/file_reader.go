package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileReaderAdapter implements engine.FileReader by reading files from the
// local filesystem. Files are located relative to a workflow's subdirectory
// under basePath (i.e. basePath/<workflowName>/<relativePath>).
type FileReaderAdapter struct {
	basePath string
}

// NewFileReader creates a FileReaderAdapter rooted at the given directory.
func NewFileReader(basePath string) *FileReaderAdapter {
	return &FileReaderAdapter{basePath: basePath}
}

// ReadFile reads the content of a file within the named workflow's directory.
func (r *FileReaderAdapter) ReadFile(workflowName, relativePath string) ([]byte, error) {
	fullPath := filepath.Join(r.basePath, workflowName, relativePath)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("reading file %q in workflow %q: %w", relativePath, workflowName, err)
	}

	return data, nil
}

// ResolvePath returns the absolute path to a file within the named workflow's
// directory. It returns an error if the file does not exist.
func (r *FileReaderAdapter) ResolvePath(workflowName, relativePath string) (string, error) {
	fullPath := filepath.Join(r.basePath, workflowName, relativePath)

	if _, err := os.Stat(fullPath); err != nil {
		return "", fmt.Errorf("resolving path %q in workflow %q: %w", relativePath, workflowName, err)
	}

	return fullPath, nil
}

// FileExists reports whether a file exists within the named workflow's directory.
func (r *FileReaderAdapter) FileExists(workflowName, relativePath string) bool {
	fullPath := filepath.Join(r.basePath, workflowName, relativePath)
	_, err := os.Stat(fullPath)
	return err == nil
}
