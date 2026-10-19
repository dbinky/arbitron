package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalWorkspacePath_ResolvesSymlinksAndCreatesTheRoot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(real, 0o700))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(real, link))
	want, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)

	got, err := canonicalWorkspacePath(link)
	require.NoError(t, err)
	assert.Equal(t, want, got, "a symlinked workspace root is resolved once, at boot")

	got, err = canonicalWorkspacePath(filepath.Join(link, "nested", "ws"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(want, "nested", "ws"), got)
	info, err := os.Stat(got)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "a missing root is created")
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	_, err = canonicalWorkspacePath("")
	assert.Error(t, err)
}
