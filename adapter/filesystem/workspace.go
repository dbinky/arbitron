package filesystem

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// WorkspaceManagerAdapter implements engine.WorkspaceManager by creating
// per-instance directories under basePath/instances/<instanceID>/.
type WorkspaceManagerAdapter struct {
	basePath string
}

// NewWorkspaceManager creates a WorkspaceManagerAdapter rooted at basePath.
func NewWorkspaceManager(basePath string) *WorkspaceManagerAdapter {
	return &WorkspaceManagerAdapter{basePath: basePath}
}

// instanceDir returns the directory path for an instance.
func (w *WorkspaceManagerAdapter) instanceDir(instanceID uuid.UUID) string {
	return filepath.Join(w.basePath, "instances", instanceID.String())
}

// EnsureInstanceDir creates the instance directory if needed and returns it.
func (w *WorkspaceManagerAdapter) EnsureInstanceDir(instanceID uuid.UUID) (string, error) {
	dir := w.instanceDir(instanceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("filesystem: ensure instance dir: %w", err)
	}
	return dir, nil
}

// RemoveInstanceDir deletes the instance directory; a missing dir is ignored.
func (w *WorkspaceManagerAdapter) RemoveInstanceDir(instanceID uuid.UUID) error {
	if err := os.RemoveAll(w.instanceDir(instanceID)); err != nil {
		return fmt.Errorf("filesystem: remove instance dir: %w", err)
	}
	return nil
}
