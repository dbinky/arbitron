package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time checks: FileReaderAdapter satisfies both engine.FileReader and
// workflow.FileChecker (the domain-local narrowed subset used for validation).
var _ engine.FileReader = (*filesystem.FileReaderAdapter)(nil)
var _ workflow.FileChecker = (*filesystem.FileReaderAdapter)(nil)

func setupFileReaderTestdata(t *testing.T) (string, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "arbitron-filereader-*")
	require.NoError(t, err)

	// Create a workflow directory with a file inside
	wfDir := filepath.Join(tmpDir, "my-workflow")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "prompt.txt"), []byte("Hello, {{name}}!"), 0o644))

	cleanup := func() { os.RemoveAll(tmpDir) }
	return tmpDir, cleanup
}

func TestFileReader_ReadFile_Success(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile("my-workflow", "prompt.txt")
	require.NoError(t, err)
	assert.Equal(t, "Hello, {{name}}!", string(data))
}

func TestFileReader_ReadFile_NotFound(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile("my-workflow", "missing.txt")
	assert.Nil(t, data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing.txt")
}

func TestFileReader_ResolvePath_Success(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	resolved, err := reader.ResolvePath("my-workflow", "prompt.txt")
	require.NoError(t, err)

	expected := filepath.Join(base, "my-workflow", "prompt.txt")
	assert.Equal(t, expected, resolved)
}

func TestFileReader_ResolvePath_NotFound(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	resolved, err := reader.ResolvePath("my-workflow", "nope.txt")
	assert.Empty(t, resolved)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nope.txt")
}

func TestFileReader_FileExists_True(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	assert.True(t, reader.FileExists("my-workflow", "prompt.txt"))
}

func TestFileReader_FileExists_False(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	assert.False(t, reader.FileExists("my-workflow", "nonexistent.txt"))
}

func TestFileReader_PathTraversal_ReadFileRejected(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	// Place a sensitive file one level above basePath.
	secret := filepath.Join(filepath.Dir(base), "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o644))
	defer os.Remove(secret)

	reader := filesystem.NewFileReader(base)

	_, err := reader.ReadFile("my-workflow", "../../secret.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace root")
}

func TestFileReader_PathTraversal_ResolvePathRejected(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	_, err := reader.ResolvePath("my-workflow", "../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace root")
}

func TestFileReader_PathTraversal_FileExistsRejected(t *testing.T) {
	base, cleanup := setupFileReaderTestdata(t)
	defer cleanup()

	reader := filesystem.NewFileReader(base)

	assert.False(t, reader.FileExists("my-workflow", "../../secret.txt"),
		"path traversal via FileExists must return false, not expose the file")
}
