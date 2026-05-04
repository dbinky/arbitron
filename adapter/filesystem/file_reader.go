package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// containmentRoot returns the absolute root that file paths must stay within.
// Using filepath.Clean + separator ensures the prefix check works on all OSes.
func (r *FileReaderAdapter) containmentRoot() string {
	return filepath.Clean(r.basePath) + string(os.PathSeparator)
}

// safeJoin builds the full path and checks that it is inside basePath.
func (r *FileReaderAdapter) safeJoin(workflowName, relativePath string) (string, error) {
	full := filepath.Join(r.basePath, workflowName, relativePath)
	if !strings.HasPrefix(full, r.containmentRoot()) {
		return "", fmt.Errorf("path %q escapes workspace root", relativePath)
	}
	return full, nil
}

// ReadFile reads the content of a file within the named workflow's directory.
func (r *FileReaderAdapter) ReadFile(workflowName, relativePath string) ([]byte, error) {
	fullPath, err := r.safeJoin(workflowName, relativePath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("reading file %q in workflow %q: %w", relativePath, workflowName, err)
	}

	return data, nil
}

// ResolvePath returns the absolute path to a file within the named workflow's
// directory. It returns an error if the file does not exist.
func (r *FileReaderAdapter) ResolvePath(workflowName, relativePath string) (string, error) {
	fullPath, err := r.safeJoin(workflowName, relativePath)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(fullPath); err != nil {
		return "", fmt.Errorf("resolving path %q in workflow %q: %w", relativePath, workflowName, err)
	}

	return fullPath, nil
}

// FileExists reports whether a file exists within the named workflow's directory.
func (r *FileReaderAdapter) FileExists(workflowName, relativePath string) bool {
	fullPath, err := r.safeJoin(workflowName, relativePath)
	if err != nil {
		return false
	}
	_, err = os.Stat(fullPath)
	return err == nil
}
