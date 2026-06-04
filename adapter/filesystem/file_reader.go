package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// FileReaderAdapter implements engine.FileReader by reading files from the
// local filesystem. Files are located relative to a workflow's subdirectory
// under a scope root: basePath/users/<owner>/<workflowName>/<relativePath>
// (private) with a fallback to basePath/shared/<workflowName>/<relativePath>
// (shared). When owner is uuid.Nil only the shared space is searched.
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

// candidateRoots returns the ordered scope roots a read should try for the
// given owner: the owner's private partition first (when owner is non-nil),
// then the shared space.
func (r *FileReaderAdapter) candidateRoots(owner uuid.UUID) []string {
	var roots []string
	if owner != uuid.Nil {
		roots = append(roots, filepath.Join(r.basePath, "users", owner.String()))
	}
	roots = append(roots, filepath.Join(r.basePath, "shared"))
	return roots
}

// safeJoin builds the full path under the given scope root and checks that it
// is (a) inside basePath and (b) inside the specific workflow subdirectory.
// The two-level check prevents both workspace-root escapes (e.g.
// workflowName="../../etc") and cross-workflow reads (e.g.
// relativePath="../other-wf/secret.txt").
func (r *FileReaderAdapter) safeJoin(scopeRoot, workflowName, relativePath string) (string, error) {
	wfDir := filepath.Join(scopeRoot, workflowName)
	full := filepath.Join(wfDir, relativePath)
	// Must stay inside the workspace root (catches bad workflowName).
	if !strings.HasPrefix(full, r.containmentRoot()) {
		return "", fmt.Errorf("path %q escapes workspace root", relativePath)
	}
	// Must stay inside the specific workflow directory (catches bad relativePath).
	wfRoot := filepath.Clean(wfDir) + string(os.PathSeparator)
	if !strings.HasPrefix(full, wfRoot) {
		return "", fmt.Errorf("path %q escapes workflow directory", relativePath)
	}
	return full, nil
}

// resolveExisting returns the first containment-valid candidate path that
// exists across the owner's private partition then the shared space. It
// returns the last containment error if every candidate is rejected, or
// os.ErrNotExist if none of the valid candidates exists on disk.
func (r *FileReaderAdapter) resolveExisting(owner uuid.UUID, workflowName, relativePath string) (string, error) {
	var lastErr error
	for _, root := range r.candidateRoots(owner) {
		full, err := r.safeJoin(root, workflowName, relativePath)
		if err != nil {
			lastErr = err
			continue
		}
		if _, statErr := os.Stat(full); statErr == nil {
			return full, nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", os.ErrNotExist
}

// ReadFile reads the content of a file within the named workflow's directory,
// searching the owner's private partition first then the shared space.
func (r *FileReaderAdapter) ReadFile(owner uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	full, err := r.resolveExisting(owner, workflowName, relativePath)
	if err != nil {
		return nil, fmt.Errorf("reading file %q in workflow %q: %w", relativePath, workflowName, err)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("reading file %q in workflow %q: %w", relativePath, workflowName, err)
	}
	return data, nil
}

// ResolvePath returns the absolute path to a file within the named workflow's
// directory, searching the owner's private partition first then the shared
// space. It returns an error if the file does not exist in any scope.
func (r *FileReaderAdapter) ResolvePath(owner uuid.UUID, workflowName, relativePath string) (string, error) {
	full, err := r.resolveExisting(owner, workflowName, relativePath)
	if err != nil {
		return "", fmt.Errorf("resolving path %q in workflow %q: %w", relativePath, workflowName, err)
	}
	return full, nil
}

// FileExists reports whether a file exists within the named workflow's
// directory in any of the owner's visible scopes.
func (r *FileReaderAdapter) FileExists(owner uuid.UUID, workflowName, relativePath string) bool {
	_, err := r.resolveExisting(owner, workflowName, relativePath)
	return err == nil
}
