package filesystem_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// libFixture lays out a workspace with both library roots and returns the base
// path plus the private and shared _lib directories. The _lib folders are not
// created; tests create what they need.
func libFixture(t *testing.T) (base, privLib, sharedLib string) {
	t.Helper()
	base = t.TempDir()
	privLib = filepath.Join(base, "users", frOwner.String(), workflow.LibraryFolder)
	sharedLib = filepath.Join(base, "shared", workflow.LibraryFolder)
	return base, privLib, sharedLib
}

func writeLibFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	return full
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

// assertLibInvalid checks that all three FileReader entry points reject p with
// ErrInvalidLibPath for the given owner.
func assertLibInvalid(t *testing.T, reader *filesystem.FileReaderAdapter, owner uuid.UUID, p string) {
	t.Helper()
	data, err := reader.ReadFile(owner, "my-workflow", p)
	require.Error(t, err, "ReadFile(%q)", p)
	assert.Nil(t, data)
	assert.True(t, errors.Is(err, workflow.ErrInvalidLibPath), "ReadFile(%q): %v", p, err)
	_, err = reader.ResolvePath(owner, "my-workflow", p)
	require.Error(t, err, "ResolvePath(%q)", p)
	assert.True(t, errors.Is(err, workflow.ErrInvalidLibPath), "ResolvePath(%q): %v", p, err)
	assert.False(t, reader.FileExists(owner, "my-workflow", p), "FileExists(%q)", p)
}

// --- Happy path ---

func TestFileReaderLib_PrivateWins(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	writeLibFile(t, privLib, "x.md", "private")
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@lib/x.md")
	require.NoError(t, err)
	assert.Equal(t, "private", string(data))
}

func TestFileReaderLib_FallsThroughToShared(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	writeLibFile(t, privLib, "other.md", "private other")
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@lib/x.md")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(data))
}

func TestFileReaderLib_NilOwnerSharedOnly(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	writeLibFile(t, privLib, "x.md", "private")
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(uuid.Nil, "my-workflow", "@lib/x.md")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(data))
}

func TestFileReaderLib_ResolvePathIsRealPathInsideLib(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	full := writeLibFile(t, sharedLib, "scripts/run.sh", "#!/bin/sh\n")
	reader := filesystem.NewFileReader(base)

	got, err := reader.ResolvePath(frOwner, "my-workflow", "@lib/scripts/run.sh")
	require.NoError(t, err)
	assert.Equal(t, realPath(t, full), got)
	assert.True(t, filepath.IsAbs(got))
}

func TestFileReaderLib_FileExistsSharedOnly(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	assert.True(t, reader.FileExists(frOwner, "my-workflow", "@lib/x.md"))
	assert.True(t, reader.FileExists(uuid.Nil, "my-workflow", "@lib/x.md"))
	assert.False(t, reader.FileExists(frOwner, "my-workflow", "@lib/missing.md"))
}

// --- Edge ---

func TestFileReaderLib_MissingPrivateLibDirFallsThrough(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	// The owner's partition exists but holds no _lib folder.
	require.NoError(t, os.MkdirAll(filepath.Dir(privLib), 0o755))
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@lib/x.md")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(data))
}

func TestFileReaderLib_WorkflowNameIrrelevant(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	for _, wf := range []string{"a", "b", "does-not-exist"} {
		data, err := reader.ReadFile(frOwner, wf, "@lib/x.md")
		require.NoError(t, err, wf)
		assert.Equal(t, "shared", string(data), wf)
	}
}

func TestFileReaderLib_PrivateOnlyWithNilOwnerNotFound(t *testing.T) {
	base, privLib, _ := libFixture(t)
	writeLibFile(t, privLib, "x.md", "private")
	reader := filesystem.NewFileReader(base)

	_, err := reader.ReadFile(uuid.Nil, "my-workflow", "@lib/x.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist), "got %v", err)
	_, err = reader.ResolvePath(uuid.Nil, "my-workflow", "@lib/x.md")
	assert.True(t, errors.Is(err, os.ErrNotExist), "got %v", err)
	assert.False(t, reader.FileExists(uuid.Nil, "my-workflow", "@lib/x.md"))
}

func TestFileReaderLib_SymlinkInsideLibAllowed(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	target := writeLibFile(t, sharedLib, "real/target.md", "target bytes")
	require.NoError(t, os.Symlink("real/target.md", filepath.Join(sharedLib, "alias.md")))
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@lib/alias.md")
	require.NoError(t, err)
	assert.Equal(t, "target bytes", string(data))

	got, err := reader.ResolvePath(frOwner, "my-workflow", "@lib/alias.md")
	require.NoError(t, err)
	assert.Equal(t, realPath(t, target), got)
	assert.True(t, reader.FileExists(frOwner, "my-workflow", "@lib/alias.md"))
}

// --- Error/failure ---

func TestFileReaderLib_MalformedPathsRejected(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "x", "shared")
	reader := filesystem.NewFileReader(base)

	for _, p := range []string{
		"@lib",
		"@lib/",
		"@lib//x",
		"@lib/a//x",
		"@lib/../x",
		"@lib/a/../x",
		"@lib/./x",
		"@lib/a/./x",
		"@lib/a/",
		"@lib/a\\x",
		"@lib/a\x00x",
		"@lib/workflow.json",
		"@lib/Layout.JSON",
		"@lib/workflow.json/x",
	} {
		t.Run(p, func(t *testing.T) { assertLibInvalid(t, reader, frOwner, p) })
	}
}

func TestFileReaderLib_InternalArtifactNamesRejected(t *testing.T) {
	// The adapter's internal-artifact names are never library content, even
	// when such a file is physically present under _lib.
	base, _, sharedLib := libFixture(t)
	reader := filesystem.NewFileReader(base)

	for _, rel := range []string{
		".arbitron-locks/x",
		"sub/.arbitron-layout-journal",
		"sub/.ARBITRON-TREE-COMMITS/x",
		".x.tmp-" + uuid.NewString() + "/a.md",
	} {
		writeLibFile(t, sharedLib, rel, "internal")
		t.Run(rel, func(t *testing.T) { assertLibInvalid(t, reader, frOwner, "@lib/"+rel) })
	}
}

func TestFileReaderLib_EscapingSymlinkErrorsNoFallthrough(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	secret := writeLibFile(t, filepath.Join(base, "shared", "wf"), "secret", "secret bytes")
	require.FileExists(t, secret)
	require.NoError(t, os.MkdirAll(privLib, 0o755))
	// users/<o>/_lib/x.md -> ../../../shared/wf/secret
	require.NoError(t, os.Symlink("../../../shared/wf/secret", filepath.Join(privLib, "x.md")))
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@lib/x.md")
	require.Error(t, err)
	assert.Nil(t, data)
	assert.True(t, errors.Is(err, workflow.ErrInvalidLibPath), "got %v", err)
	assertLibInvalid(t, reader, frOwner, "@lib/x.md")
}

func TestFileReaderLib_EscapingDirSymlinkErrors(t *testing.T) {
	// An intermediate directory link that leaves the library is caught too.
	base, _, sharedLib := libFixture(t)
	outside := t.TempDir()
	writeLibFile(t, outside, "y.md", "outside")
	require.NoError(t, os.MkdirAll(sharedLib, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(sharedLib, "sub")))
	reader := filesystem.NewFileReader(base)

	assertLibInvalid(t, reader, frOwner, "@lib/sub/y.md")
}

func TestFileReaderLib_AbsoluteSymlinkOutsideErrors(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	require.NoError(t, os.MkdirAll(sharedLib, 0o755))
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(sharedLib, "hosts")))
	reader := filesystem.NewFileReader(base)

	assertLibInvalid(t, reader, frOwner, "@lib/hosts")
}

func TestFileReaderLib_SymlinkedLibRootErrors(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	elsewhere := t.TempDir()
	writeLibFile(t, elsewhere, "x.md", "elsewhere")
	require.NoError(t, os.MkdirAll(filepath.Dir(privLib), 0o755))
	require.NoError(t, os.Symlink(elsewhere, privLib))
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	// Fails closed: neither the link target nor the shared copy is served.
	assertLibInvalid(t, reader, frOwner, "@lib/x.md")
}

func TestFileReaderLib_LibRootNotDirectoryErrors(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(privLib), 0o755))
	require.NoError(t, os.WriteFile(privLib, []byte("not a dir"), 0o644))
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	assertLibInvalid(t, reader, frOwner, "@lib/x.md")
}

func TestFileReaderLib_DanglingSymlinkErrors(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	require.NoError(t, os.MkdirAll(privLib, 0o755))
	require.NoError(t, os.Symlink("gone.md", filepath.Join(privLib, "x.md")))
	writeLibFile(t, sharedLib, "x.md", "shared")
	reader := filesystem.NewFileReader(base)

	assertLibInvalid(t, reader, frOwner, "@lib/x.md")
	_, err := reader.ReadFile(frOwner, "my-workflow", "@lib/x.md")
	assert.False(t, errors.Is(err, os.ErrNotExist), "dangling link must not read as absent: %v", err)
}

// --- Boundary ---

func TestFileReaderLib_DeepNesting(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	full := writeLibFile(t, sharedLib, "a/b/c/d.md", "deep")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@lib/a/b/c/d.md")
	require.NoError(t, err)
	assert.Equal(t, "deep", string(data))
	got, err := reader.ResolvePath(frOwner, "my-workflow", "@lib/a/b/c/d.md")
	require.NoError(t, err)
	assert.Equal(t, realPath(t, full), got)
}

func TestFileReaderLib_CaseVariantPrefixIsWorkflowPath(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "x", "lib")
	wfDir := filepath.Join(base, "users", frOwner.String(), "my-workflow")
	writeLibFile(t, wfDir, "@LIB/x", "workflow-local")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@LIB/x")
	require.NoError(t, err)
	assert.Equal(t, "workflow-local", string(data))
	got, err := reader.ResolvePath(frOwner, "my-workflow", "@LIB/x")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(wfDir, "@LIB", "x"), got)
}

func TestFileReaderLib_NonLibPathsUnchanged(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "x", "lib")
	wfDir := filepath.Join(base, "users", frOwner.String(), "my-workflow")
	writeLibFile(t, wfDir, "prompt.md", "local")
	writeLibFile(t, filepath.Join(base, "users", frOwner.String(), "other-wf"), "x", "other")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "prompt.md")
	require.NoError(t, err)
	assert.Equal(t, "local", string(data))
	got, err := reader.ResolvePath(frOwner, "my-workflow", "prompt.md")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(wfDir, "prompt.md"), got, "non-lib paths keep the unresolved join")

	_, err = reader.ReadFile(frOwner, "my-workflow", "../other-wf/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workflow directory")
	assert.False(t, errors.Is(err, workflow.ErrInvalidLibPath))
}

func TestFileReaderLib_DanglingDirSymlinkNoFallthrough(t *testing.T) {
	// Lstat follows intermediate components, so a dangling directory link
	// reads as a missing leaf; it must still fail closed.
	base, privLib, sharedLib := libFixture(t)
	require.NoError(t, os.MkdirAll(privLib, 0o755))
	require.NoError(t, os.Symlink("gone", filepath.Join(privLib, "sub")))
	writeLibFile(t, sharedLib, "sub/x.md", "shared")
	reader := filesystem.NewFileReader(base)

	assertLibInvalid(t, reader, frOwner, "@lib/sub/x.md")
}

func TestFileReaderLib_EscapingDirSymlinkMissingLeafNoFallthrough(t *testing.T) {
	// A missing leaf under an escaping directory link must not fall through
	// to shared, nor leak outside existence through FileExists.
	base, privLib, sharedLib := libFixture(t)
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(privLib, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(privLib, "sub")))
	writeLibFile(t, sharedLib, "sub/x", "shared")
	reader := filesystem.NewFileReader(base)

	assertLibInvalid(t, reader, frOwner, "@lib/sub/x")
	assertLibInvalid(t, reader, frOwner, "@lib/sub/deeper/x")
}

func TestFileReaderLib_InsideDirSymlinkMissingLeafFallsThrough(t *testing.T) {
	// A directory link that stays inside the library is an ordinary miss.
	base, privLib, sharedLib := libFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(privLib, "real"), 0o755))
	require.NoError(t, os.Symlink("real", filepath.Join(privLib, "sub")))
	writeLibFile(t, sharedLib, "sub/x.md", "shared")
	reader := filesystem.NewFileReader(base)

	data, err := reader.ReadFile(frOwner, "my-workflow", "@lib/sub/x.md")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(data))
}

func TestFileReaderLib_FileAncestorIsInvalid(t *testing.T) {
	// A path through a regular file fails closed and classifies as invalid.
	base, privLib, sharedLib := libFixture(t)
	writeLibFile(t, privLib, "a.md", "file")
	writeLibFile(t, sharedLib, "a.md/x", "shared")
	reader := filesystem.NewFileReader(base)

	assertLibInvalid(t, reader, frOwner, "@lib/a.md/x")
}
