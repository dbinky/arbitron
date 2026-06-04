package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time check: FileReaderAdapter satisfies engine.FileReader. The
// adapter is now owner-aware, so it no longer satisfies the owner-blind
// workflow.FileChecker directly — validation binds the owner via a wrapper.
var _ engine.FileReader = (*filesystem.FileReaderAdapter)(nil)

// frOwner is the private-workspace owner used across file-reader tests.
var frOwner = uuid.MustParse("22222222-2222-2222-2222-222222222222")

// setupFileReaderTestdata creates a workflow with a prompt file under the
// owner's private partition (basePath/users/<owner>/my-workflow/) and returns
// the basePath.
func setupFileReaderTestdata(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()

	wfDir := filepath.Join(tmpDir, "users", frOwner.String(), "my-workflow")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "prompt.txt"), []byte("Hello, {{name}}!"), 0o644))

	return tmpDir
}

func TestFileReader_ReadFile_Success(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "prompt.txt")
	require.NoError(t, err)
	assert.Equal(t, "Hello, {{name}}!", string(data))
}

func TestFileReader_ReadFile_NotFound(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "missing.txt")
	assert.Nil(t, data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing.txt")
}

func TestFileReader_ReadFile_SharedFallback(t *testing.T) {
	// A file present only under shared/<wf> resolves when the private copy is
	// absent (owner has no private partition).
	base := t.TempDir()
	sharedDir := filepath.Join(base, "shared", "pub-wf")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "prompt.txt"), []byte("shared"), 0o644))

	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "pub-wf", "prompt.txt")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(data))
}

func TestFileReader_ReadFile_PrefersPrivateOverShared(t *testing.T) {
	base := t.TempDir()

	privDir := filepath.Join(base, "users", frOwner.String(), "wf")
	require.NoError(t, os.MkdirAll(privDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(privDir, "f.txt"), []byte("private"), 0o644))

	sharedDir := filepath.Join(base, "shared", "wf")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "f.txt"), []byte("shared"), 0o644))

	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "wf", "f.txt")
	require.NoError(t, err)
	assert.Equal(t, "private", string(data))
}

func TestFileReader_ReadFile_NilOwnerSharedOnly(t *testing.T) {
	base := t.TempDir()

	// Private copy exists for frOwner; shared copy exists too.
	privDir := filepath.Join(base, "users", frOwner.String(), "wf")
	require.NoError(t, os.MkdirAll(privDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(privDir, "f.txt"), []byte("private"), 0o644))
	sharedDir := filepath.Join(base, "shared", "wf")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "f.txt"), []byte("shared"), 0o644))

	reader := filesystem.NewFileReader(base)

	// uuid.Nil searches the shared space only and must never reach a private partition.
	data, err := reader.ReadFile(uuid.Nil, "wf", "f.txt")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(data))
}

func TestFileReader_ResolvePath_Success(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	resolved, err := reader.ResolvePath(frOwner, "my-workflow", "prompt.txt")
	require.NoError(t, err)

	expected := filepath.Join(base, "users", frOwner.String(), "my-workflow", "prompt.txt")
	assert.Equal(t, expected, resolved)
}

func TestFileReader_ResolvePath_NotFound(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	resolved, err := reader.ResolvePath(frOwner, "my-workflow", "nope.txt")
	assert.Empty(t, resolved)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nope.txt")
}

func TestFileReader_FileExists_True(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	assert.True(t, reader.FileExists(frOwner, "my-workflow", "prompt.txt"))
}

func TestFileReader_FileExists_False(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	assert.False(t, reader.FileExists(frOwner, "my-workflow", "nonexistent.txt"))
}

func TestFileReader_PathTraversal_ReadFileRejected(t *testing.T) {
	base := setupFileReaderTestdata(t)

	// Place a sensitive file one level above basePath.
	secret := filepath.Join(filepath.Dir(base), "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o644))
	defer os.Remove(secret)

	reader := filesystem.NewFileReader(base)

	_, err := reader.ReadFile(frOwner, "my-workflow", "../../../../secret.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace root")
}

func TestFileReader_PathTraversal_ResolvePathRejected(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	_, err := reader.ResolvePath(frOwner, "my-workflow", "../../../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace root")
}

func TestFileReader_PathTraversal_FileExistsRejected(t *testing.T) {
	base := setupFileReaderTestdata(t)
	reader := filesystem.NewFileReader(base)

	assert.False(t, reader.FileExists(frOwner, "my-workflow", "../../../../secret.txt"),
		"path traversal via FileExists must return false, not expose the file")
}

func TestFileReader_PathTraversal_CrossWorkflowRejected(t *testing.T) {
	// Edge case: relativePath uses ".." to escape the workflow directory but
	// stay within the scope root — e.g. "../other-wf/secret.txt". The tighter
	// per-workflow-directory check prevents cross-workflow reads.
	base := setupFileReaderTestdata(t)

	// Place a secret in a sibling workflow directory under the same partition.
	otherDir := filepath.Join(base, "users", frOwner.String(), "other-wf")
	require.NoError(t, os.MkdirAll(otherDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "secret.txt"), []byte("sensitive"), 0o644))

	reader := filesystem.NewFileReader(base)

	_, err := reader.ReadFile(frOwner, "my-workflow", "../other-wf/secret.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workflow directory")
}
