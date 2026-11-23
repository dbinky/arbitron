package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// physicalTempDir resolves t.TempDir() through symlinks: on macOS it lives
// under /var -> /private/var, which VerifyWorkspaceRoot correctly refuses.
func physicalTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return d
}

func TestVerifyWorkspaceRootAdvertisesAVerifiedRoot(t *testing.T) {
	root := filepath.Join(physicalTempDir(t), "opt", "arbitron", "workspaces")
	require.NoError(t, os.MkdirAll(root, 0o750))
	got, reason := VerifyWorkspaceRoot(root + "/")
	assert.Equal(t, root, got)
	assert.Empty(t, reason)
}

func TestVerifyWorkspaceRootRefuses(t *testing.T) {
	base := physicalTempDir(t)
	target := filepath.Join(base, "real")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "ws"), 0o750))
	require.NoError(t, os.Symlink(target, filepath.Join(base, "link")))
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	ro := filepath.Join(base, "ro")
	require.NoError(t, os.MkdirAll(ro, 0o500))
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	for name, tc := range map[string]struct{ root, want string }{
		"empty":               {"", "absolute"},
		"relative":            {"ws", "absolute"},
		"missing":             {filepath.Join(base, "missing"), "does not exist"},
		"not a directory":     {file, "not a directory"},
		"symlinked component": {filepath.Join(base, "link", "ws"), "symlink"},
		"symlink is the root": {filepath.Join(base, "link"), "symlink"},
		"not writable":        {ro, "writable"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "not writable" && os.Geteuid() == 0 {
				t.Skip("root can write any directory")
			}
			got, reason := VerifyWorkspaceRoot(tc.root)
			assert.Empty(t, got, "an unverified root is never advertised")
			assert.Contains(t, reason, tc.want)
		})
	}
}
