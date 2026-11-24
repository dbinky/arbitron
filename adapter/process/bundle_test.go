// adapter/process/bundle_test.go
package process_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), mode))
}

func scriptFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\n. ./helper.sh\n", 0o755)
	writeFile(t, filepath.Join(dir, "helper.sh"), "echo '{\"ok\":true}'\n", 0o644)
	writeFile(t, filepath.Join(dir, "data", "cfg.json"), `{"a":1}`, 0o600)
	return dir
}

func TestBuildScriptBundle_IncludesSiblingsAndNestedFiles(t *testing.T) {
	dir := scriptFolder(t)
	ref, data, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, "run.sh", ref.Entrypoint, "entrypoint is relative to the bundle root")
	assert.Len(t, ref.Hash, 64)

	dst := t.TempDir()
	require.NoError(t, process.ExtractScriptBundle(bytes.NewReader(data), dst))
	for _, rel := range []string{"run.sh", "helper.sh", "data/cfg.json"} {
		want, err := os.ReadFile(filepath.Join(dir, rel))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(dst, rel))
		require.NoError(t, err, "bundle must contain sibling %s", rel)
		assert.Equal(t, want, got)
	}
	info, err := os.Stat(filepath.Join(dst, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "executable bit survives")
	info, err = os.Stat(filepath.Join(dst, "data", "cfg.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestBuildScriptBundle_IsDeterministicAcrossMtimes(t *testing.T) {
	dir := scriptFolder(t)
	ref1, data1, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.NoError(t, err)
	later := time.Now().Add(2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "helper.sh"), later, later))
	ref2, data2, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, ref1, ref2)
	assert.Equal(t, data1, data2)
}

func TestBuildScriptBundle_ContentChangeChangesHash(t *testing.T) {
	dir := scriptFolder(t)
	ref1, _, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.NoError(t, err)
	writeFile(t, filepath.Join(dir, "helper.sh"), "echo '{\"ok\":false}'\n", 0o644)
	ref2, _, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.NoError(t, err)
	assert.NotEqual(t, ref1.Hash, ref2.Hash)
}

func TestBuildScriptBundle_AllowsSymlinkInsideFolder(t *testing.T) {
	dir := scriptFolder(t)
	require.NoError(t, os.Symlink("data/cfg.json", filepath.Join(dir, "cfg-link.json")))
	_, data, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.NoError(t, err)
	dst := t.TempDir()
	require.NoError(t, process.ExtractScriptBundle(bytes.NewReader(data), dst))
	target, err := os.Readlink(filepath.Join(dst, "cfg-link.json"))
	require.NoError(t, err)
	assert.Equal(t, "data/cfg.json", target)
}
func TestBuildScriptBundle_RejectsEscapingAndAbsoluteSymlinks(t *testing.T) {
	for name, target := range map[string]string{"escaping": "../outside.txt", "absolute": "/etc/hosts"} {
		t.Run(name, func(t *testing.T) {
			dir := scriptFolder(t)
			require.NoError(t, os.Symlink(target, filepath.Join(dir, "leak")))
			_, _, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
			require.ErrorIs(t, err, process.ErrBundleOutsideReference)
			assert.Contains(t, err.Error(), "leak")
		})
	}
}

func TestBuildScriptBundle_RejectsSpecialFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs on windows")
	}
	dir := scriptFolder(t)
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600))
	_, _, err := process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.ErrorIs(t, err, process.ErrBundleUnsupportedFile)
	assert.Contains(t, err.Error(), "pipe")
}
func TestBuildScriptBundle_RejectsRelativeOrMissingEntrypoint(t *testing.T) {
	_, _, err := process.BuildScriptBundle("relative/run.sh")
	require.Error(t, err)
	_, _, err = process.BuildScriptBundle(filepath.Join(t.TempDir(), "absent.sh"))
	require.Error(t, err)
}

// The @lib scope folder as resolved by FileReader.ResolvePath is the bundle
// root, so a lib script's siblings travel with it.
func TestBuildScriptBundle_LibScriptBundlesLibSiblings(t *testing.T) {
	base := t.TempDir()
	libTools := filepath.Join(base, "shared", workflow.LibraryFolder, "tools")
	writeFile(t, filepath.Join(libTools, "run.sh"), "#!/bin/sh\n. ./common.sh\n", 0o755)
	writeFile(t, filepath.Join(libTools, "common.sh"), "echo '{\"lib\":true}'\n", 0o644)

	resolved, err := filesystem.NewFileReader(base).ResolvePath(uuid.New(), "any-workflow", "@lib/tools/run.sh")
	require.NoError(t, err)
	ref, data, err := process.BuildScriptBundle(resolved)
	require.NoError(t, err)
	assert.Equal(t, "run.sh", ref.Entrypoint)
	dst := t.TempDir()
	require.NoError(t, process.ExtractScriptBundle(bytes.NewReader(data), dst))
	_, err = os.Stat(filepath.Join(dst, "common.sh"))
	require.NoError(t, err, "the lib sibling must be in the bundle")
}

func TestExtractScriptBundle_RejectsTraversalEntries(t *testing.T) {
	for name, hdr := range map[string]*tar.Header{
		"dotdot":        {Name: "../evil.sh", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		"absolute":      {Name: "/tmp/evil.sh", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		"escaping link": {Name: "l", Linkname: "../../etc/passwd", Typeflag: tar.TypeSymlink},
		"absolute link": {Name: "l", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink},
		"unsupported":   {Name: "dev", Typeflag: tar.TypeChar},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			tw := tar.NewWriter(zw)
			require.NoError(t, tw.WriteHeader(hdr))
			if hdr.Typeflag == tar.TypeReg {
				_, err := tw.Write([]byte("x"))
				require.NoError(t, err)
			}
			require.NoError(t, tw.Close())
			require.NoError(t, zw.Close())
			err := process.ExtractScriptBundle(bytes.NewReader(buf.Bytes()), t.TempDir())
			require.ErrorIs(t, err, process.ErrBundleUnsafeEntry)
		})
	}
}

func TestBuildScriptBundle_RejectsOversizedFolder(t *testing.T) {
	dir := scriptFolder(t)
	f, err := os.Create(filepath.Join(dir, "large"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(128<<20+1))
	require.NoError(t, f.Close())
	_, _, err = process.BuildScriptBundle(filepath.Join(dir, "run.sh"))
	require.ErrorIs(t, err, process.ErrBundleTooLarge)
}

func TestExtractScriptBundle_ConfinesWritesWithExistingSymlink(t *testing.T) {
	dst, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dst, "escape")))
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "escape/written", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	require.ErrorIs(t, process.ExtractScriptBundle(bytes.NewReader(buf.Bytes()), dst), process.ErrBundleUnsafeEntry)
	_, err = os.Stat(filepath.Join(outside, "written"))
	require.True(t, os.IsNotExist(err))
}

func TestExtractScriptBundle_RejectsOversizedContent(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "large", Mode: 0600, Size: 128<<20 + 1, Typeflag: tar.TypeReg}))
	// The header alone must refuse before allocating or reading the content.
	require.NoError(t, zw.Close())
	require.ErrorIs(t, process.ExtractScriptBundle(bytes.NewReader(buf.Bytes()), t.TempDir()), process.ErrBundleUnsafeEntry)
}
