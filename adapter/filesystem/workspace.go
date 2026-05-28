package filesystem

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// WorkspaceManagerAdapter implements engine.WorkspaceManager by creating
// per-instance directories under basePath/instances/<instanceID>/.
//
// SECURITY: directories are created with mode 0o700 (owner-only). The agent
// running under --dangerously-skip-permissions can drop secrets, tool
// outputs, dumped .env files, etc. into these dirs — they must not be world-
// or group-readable. PR #8 thread 3291943659. basePath is also resolved to
// absolute at construction (PR #8 thread 3291907750) so any downstream pin
// check has a canonical root.
type WorkspaceManagerAdapter struct {
	basePath string
}

// instanceDirMode is the permission applied to both the parent instances/
// directory and each per-instance directory.
const instanceDirMode os.FileMode = 0o700

// NewWorkspaceManager creates a WorkspaceManagerAdapter rooted at basePath.
// basePath must be non-empty; it is resolved to an absolute path.
func NewWorkspaceManager(basePath string) (*WorkspaceManagerAdapter, error) {
	if basePath == "" {
		return nil, fmt.Errorf("filesystem: workspace basePath is required")
	}
	abs, err := filepath.Abs(basePath)
	if err != nil {
		return nil, fmt.Errorf("filesystem: resolve workspace basePath: %w", err)
	}
	return &WorkspaceManagerAdapter{basePath: abs}, nil
}

// BasePath returns the absolute workspace root.
func (w *WorkspaceManagerAdapter) BasePath() string { return w.basePath }

// instanceDir returns the directory path for an instance.
func (w *WorkspaceManagerAdapter) instanceDir(instanceID uuid.UUID) string {
	return filepath.Join(w.basePath, "instances", instanceID.String())
}

// EnsureInstanceDir creates the instance directory if needed and returns it.
// Both the per-instance directory and the parent instances/ directory are
// created (and chmodded) with mode 0o700.
func (w *WorkspaceManagerAdapter) EnsureInstanceDir(instanceID uuid.UUID) (string, error) {
	dir := w.instanceDir(instanceID)
	if err := os.MkdirAll(dir, instanceDirMode); err != nil {
		return "", fmt.Errorf("filesystem: ensure instance dir: %w", err)
	}
	// MkdirAll honours the umask, which can relax the mode below 0o700.
	// Explicitly chmod both the parent instances/ dir and the per-instance
	// dir so the on-disk perms always match the declared invariant.
	parent := filepath.Dir(dir)
	if err := os.Chmod(parent, instanceDirMode); err != nil {
		return "", fmt.Errorf("filesystem: tighten parent perms: %w", err)
	}
	if err := os.Chmod(dir, instanceDirMode); err != nil {
		return "", fmt.Errorf("filesystem: tighten instance perms: %w", err)
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
