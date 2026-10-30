package filesystem_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ workflow.LibraryStore = (*filesystem.LibraryStore)(nil)

// libStoreRoundTrip writes, reads, lists and deletes through the store in one
// scope and checks the on-disk location under libDir.
func libStoreRoundTrip(t *testing.T, store *filesystem.LibraryStore, scope workflow.Scope, libDir string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.WriteFile(ctx, scope, "b.md", []byte("bee")))
	require.NoError(t, store.WriteFile(ctx, scope, "a/x.sh", []byte("ex")))

	onDisk, err := os.ReadFile(filepath.Join(libDir, "a", "x.sh"))
	require.NoError(t, err)
	assert.Equal(t, "ex", string(onDisk))

	got, err := store.ReadFile(ctx, scope, "b.md")
	require.NoError(t, err)
	assert.Equal(t, "bee", string(got))

	files, err := store.ListFiles(ctx, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"a/x.sh", "b.md"}, files)

	require.NoError(t, store.DeleteFile(ctx, scope, "b.md"))
	_, err = store.ReadFile(ctx, scope, "b.md")
	assert.ErrorIs(t, err, workflow.ErrFileNotFound)
	files, err = store.ListFiles(ctx, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"a/x.sh"}, files)
}

// --- Happy path ---

func TestLibraryStore_WriteReadListDelete_Shared(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	libStoreRoundTrip(t, filesystem.NewLibraryStore(base), workflow.Shared(), sharedLib)
}

func TestLibraryStore_WriteReadListDelete_Private(t *testing.T) {
	base, privLib, sharedLib := libFixture(t)
	libStoreRoundTrip(t, filesystem.NewLibraryStore(base), workflow.Private(frOwner), privLib)
	_, err := os.Stat(sharedLib)
	assert.True(t, os.IsNotExist(err), "private writes must not touch the shared library")
}

func TestLibraryStore_ReadIsExactScope(t *testing.T) {
	// Unlike the run-time reader, the store never falls back to shared.
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "x.md", "shared")
	_, err := filesystem.NewLibraryStore(base).ReadFile(context.Background(), workflow.Private(frOwner), "x.md")
	assert.ErrorIs(t, err, workflow.ErrFileNotFound)
}

// --- Edge ---

func TestLibraryStore_FirstWriteCreatesLibWithPartitionPerms(t *testing.T) {
	base, privLib, _ := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	require.NoError(t, store.WriteFile(context.Background(), workflow.Private(frOwner), "x.md", []byte("x")))

	for _, dir := range []string{filepath.Join(base, "users"), filepath.Join(base, "users", frOwner.String())} {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), dir)
	}
	info, err := os.Lstat(privLib)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestLibraryStore_ListEmptyWhenAbsent(t *testing.T) {
	base, _, _ := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	for _, scope := range []workflow.Scope{workflow.Shared(), workflow.Private(frOwner)} {
		files, err := store.ListFiles(context.Background(), scope)
		require.NoError(t, err)
		require.NotNil(t, files)
		assert.Empty(t, files)
	}
}

func TestLibraryStore_ListSkipsEscapingSymlink(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "ok.md", "ok")
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(sharedLib, "leak.md")))
	require.NoError(t, os.Symlink(outside, filepath.Join(sharedLib, "leakdir")))
	require.NoError(t, os.Symlink(filepath.Join(sharedLib, "gone"), filepath.Join(sharedLib, "dangling.md")))
	// A link that stays inside the library is ordinary content.
	require.NoError(t, os.Symlink(filepath.Join(sharedLib, "ok.md"), filepath.Join(sharedLib, "alias.md")))

	files, err := filesystem.NewLibraryStore(base).ListFiles(context.Background(), workflow.Shared())
	require.NoError(t, err)
	assert.Equal(t, []string{"alias.md", "ok.md"}, files)
}

func TestLibraryStore_ListSkipsInternalArtifacts(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "ok.md", "ok")
	for _, rel := range []string{
		".arbitron-locks/x",
		"sub/.arbitron-layout-journal",
		"sub/.ARBITRON-TREE-COMMITS/x",
		".x.tmp-" + uuid.NewString() + "/a.md",
		"workflow.json",
		"Layout.JSON",
		".write-123.tmp",
	} {
		writeLibFile(t, sharedLib, rel, "internal")
	}
	files, err := filesystem.NewLibraryStore(base).ListFiles(context.Background(), workflow.Shared())
	require.NoError(t, err)
	assert.Equal(t, []string{"ok.md"}, files)
}

func TestLibraryStore_OverwriteReplacesContent(t *testing.T) {
	base, _, _ := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	require.NoError(t, store.WriteFile(ctx, workflow.Shared(), "x.md", []byte("first, longer")))
	require.NoError(t, store.WriteFile(ctx, workflow.Shared(), "x.md", []byte("second")))
	got, err := store.ReadFile(ctx, workflow.Shared(), "x.md")
	require.NoError(t, err)
	assert.Equal(t, "second", string(got))
}

// --- Error/failure ---

var invalidLibRels = []string{
	"",
	"/abs",
	"..",
	"../x",
	"a/../x",
	"./x",
	"a/./x",
	"a/",
	"a//b",
	"a\\x",
	"a\x00x",
}

func TestLibraryStore_InvalidRelRejected(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "x", "x")
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	for _, rel := range invalidLibRels {
		t.Run(rel, func(t *testing.T) {
			_, err := store.ReadFile(ctx, workflow.Shared(), rel)
			assert.ErrorIs(t, err, workflow.ErrInvalidLibPath, "read")
			assert.ErrorIs(t, store.WriteFile(ctx, workflow.Shared(), rel, []byte("x")), workflow.ErrInvalidLibPath, "write")
			assert.ErrorIs(t, store.DeleteFile(ctx, workflow.Shared(), rel), workflow.ErrInvalidLibPath, "delete")
		})
	}
	_, err := os.Stat(filepath.Join(base, "x"))
	assert.True(t, os.IsNotExist(err), "no write may land outside the library")
}

func TestLibraryStore_WriteRejectsControlCharacters(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	for _, rel := range []string{"a\x01b", "a\nb.md", "sub/x\tz", "x\x1f", "x\x7f"} {
		err := store.WriteFile(context.Background(), workflow.Shared(), rel, []byte("x"))
		assert.ErrorIs(t, err, workflow.ErrInvalidLibPath, "%q", rel)
	}
	_, err := os.Stat(sharedLib)
	assert.True(t, os.IsNotExist(err), "a rejected write must not create the library")
}

func TestLibraryStore_InternalArtifactNamesRejected(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	for _, rel := range []string{
		".arbitron-locks/x",
		"sub/.arbitron-layout-journal",
		"sub/.ARBITRON-TREE-COMMITS/x",
		".x.tmp-" + uuid.NewString() + "/a.md",
	} {
		writeLibFile(t, sharedLib, rel, "internal")
		t.Run(rel, func(t *testing.T) {
			_, err := store.ReadFile(ctx, workflow.Shared(), rel)
			assert.ErrorIs(t, err, workflow.ErrInvalidLibPath, "read")
			assert.ErrorIs(t, store.WriteFile(ctx, workflow.Shared(), rel, []byte("x")), workflow.ErrInvalidLibPath, "write")
			assert.ErrorIs(t, store.DeleteFile(ctx, workflow.Shared(), rel), workflow.ErrInvalidLibPath, "delete")
			onDisk, err := os.ReadFile(filepath.Join(sharedLib, filepath.FromSlash(rel)))
			require.NoError(t, err)
			assert.Equal(t, "internal", string(onDisk), "artifact must be untouched")
		})
	}
}

func TestLibraryStore_DeleteMissing_ErrFileNotFound(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	// Absent library folder, then absent file in a present folder.
	assert.ErrorIs(t, store.DeleteFile(ctx, workflow.Shared(), "x.md"), workflow.ErrFileNotFound)
	writeLibFile(t, sharedLib, "other.md", "o")
	assert.ErrorIs(t, store.DeleteFile(ctx, workflow.Shared(), "x.md"), workflow.ErrFileNotFound)
	assert.ErrorIs(t, store.DeleteFile(ctx, workflow.Shared(), "sub/x.md"), workflow.ErrFileNotFound)
}

func TestLibraryStore_ReadMissing_ErrFileNotFound(t *testing.T) {
	base, _, _ := libFixture(t)
	_, err := filesystem.NewLibraryStore(base).ReadFile(context.Background(), workflow.Shared(), "x.md")
	assert.ErrorIs(t, err, workflow.ErrFileNotFound)
}

func TestLibraryStore_ReadEscapingSymlinkErrors(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644))
	require.NoError(t, os.MkdirAll(sharedLib, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(sharedLib, "leak.md")))
	require.NoError(t, os.Symlink(outside, filepath.Join(sharedLib, "dir")))

	store := filesystem.NewLibraryStore(base)
	for _, rel := range []string{"leak.md", "dir/secret"} {
		data, err := store.ReadFile(context.Background(), workflow.Shared(), rel)
		assert.ErrorIs(t, err, workflow.ErrInvalidLibPath, rel)
		assert.Nil(t, data)
	}
}

func TestLibraryStore_DeleteThroughEscapingSymlinkRefused(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("s"), 0o644))
	require.NoError(t, os.MkdirAll(sharedLib, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(sharedLib, "dir")))

	err := filesystem.NewLibraryStore(base).DeleteFile(context.Background(), workflow.Shared(), "dir/secret")
	assert.ErrorIs(t, err, workflow.ErrInvalidLibPath)
	_, err = os.Stat(secret)
	assert.NoError(t, err, "file outside the library must survive")
}

func TestLibraryStore_WriteRefusesSymlinks(t *testing.T) {
	// The store never writes through a link: not at the target, not at any
	// ancestor, and not when _lib itself is a link, even one that resolves
	// inside the library.
	cases := map[string]func(t *testing.T, sharedLib, outside string) string{
		"target links outside": func(t *testing.T, sharedLib, outside string) string {
			require.NoError(t, os.MkdirAll(sharedLib, 0o755))
			require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(sharedLib, "x.md")))
			return "x.md"
		},
		"target links inside": func(t *testing.T, sharedLib, outside string) string {
			writeLibFile(t, sharedLib, "real.md", "real")
			require.NoError(t, os.Symlink(filepath.Join(sharedLib, "real.md"), filepath.Join(sharedLib, "x.md")))
			return "x.md"
		},
		"ancestor links outside": func(t *testing.T, sharedLib, outside string) string {
			require.NoError(t, os.MkdirAll(sharedLib, 0o755))
			require.NoError(t, os.Symlink(outside, filepath.Join(sharedLib, "dir")))
			return "dir/secret"
		},
		"ancestor links inside": func(t *testing.T, sharedLib, outside string) string {
			require.NoError(t, os.MkdirAll(filepath.Join(sharedLib, "realdir"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(sharedLib, "realdir"), filepath.Join(sharedLib, "dir")))
			return "dir/x.md"
		},
		"library folder is a link": func(t *testing.T, sharedLib, outside string) string {
			require.NoError(t, os.MkdirAll(filepath.Dir(sharedLib), 0o755))
			require.NoError(t, os.Symlink(outside, sharedLib))
			return "secret"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			base, _, sharedLib := libFixture(t)
			outside := t.TempDir()
			secret := filepath.Join(outside, "secret")
			require.NoError(t, os.WriteFile(secret, []byte("s"), 0o644))
			rel := setup(t, sharedLib, outside)

			err := filesystem.NewLibraryStore(base).WriteFile(context.Background(), workflow.Shared(), rel, []byte("pwned"))
			assert.ErrorIs(t, err, workflow.ErrInvalidLibPath)
			got, err := os.ReadFile(secret)
			require.NoError(t, err)
			assert.Equal(t, "s", string(got))
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			assert.Len(t, entries, 1, "nothing may be created outside the library")
			if real, err := os.ReadFile(filepath.Join(sharedLib, "real.md")); err == nil {
				assert.Equal(t, "real", string(real))
			}
		})
	}
}

func TestLibraryStore_WriteThroughFileAncestorRejected(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	writeLibFile(t, sharedLib, "a.md", "a")
	err := filesystem.NewLibraryStore(base).WriteFile(context.Background(), workflow.Shared(), "a.md/x", []byte("x"))
	assert.ErrorIs(t, err, workflow.ErrInvalidLibPath)
}

func TestLibraryStore_PrivateScopeRequiresOwner(t *testing.T) {
	base, _, _ := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	scope := workflow.Private(uuid.Nil)
	assert.Error(t, store.WriteFile(ctx, scope, "x.md", []byte("x")))
	_, err := store.ReadFile(ctx, scope, "x.md")
	assert.Error(t, err)
	_, err = store.ListFiles(ctx, scope)
	assert.Error(t, err)
	assert.Error(t, store.DeleteFile(ctx, scope, "x.md"))
	_, err = os.Stat(filepath.Join(base, "users"))
	assert.True(t, os.IsNotExist(err))
}

func TestLibraryStore_CanceledContext(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := filesystem.NewLibraryStore(base).WriteFile(ctx, workflow.Shared(), "x.md", []byte("x"))
	assert.ErrorIs(t, err, context.Canceled)
	_, statErr := os.Stat(filepath.Join(sharedLib, "x.md"))
	assert.True(t, os.IsNotExist(statErr))
}

// --- Boundary ---

func TestLibraryStore_NestedDirsCreated(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	require.NoError(t, store.WriteFile(ctx, workflow.Shared(), "a/b/c.sh", []byte("#!/bin/sh\n")))
	info, err := os.Stat(filepath.Join(sharedLib, "a", "b"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	files, err := store.ListFiles(ctx, workflow.Shared())
	require.NoError(t, err)
	assert.Equal(t, []string{"a/b/c.sh"}, files)
}

func TestLibraryStore_ReservedFirstComponentRejected(t *testing.T) {
	base, _, sharedLib := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	for _, rel := range []string{"workflow.json", "layout.json", "Workflow.JSON", "layout.json/x"} {
		t.Run(rel, func(t *testing.T) {
			assert.ErrorIs(t, store.WriteFile(ctx, workflow.Shared(), rel, []byte("x")), workflow.ErrInvalidLibPath, "write")
			_, err := store.ReadFile(ctx, workflow.Shared(), rel)
			assert.ErrorIs(t, err, workflow.ErrInvalidLibPath, "read")
			assert.ErrorIs(t, store.DeleteFile(ctx, workflow.Shared(), rel), workflow.ErrInvalidLibPath, "delete")
		})
	}
	// Deeper than the first component the names are ordinary content.
	require.NoError(t, store.WriteFile(ctx, workflow.Shared(), "sub/workflow.json", []byte("{}")))
	_, err := os.Stat(filepath.Join(sharedLib, "sub", "workflow.json"))
	assert.NoError(t, err)
}

func TestLibraryStore_EmptyContentWritten(t *testing.T) {
	base, _, _ := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	ctx := context.Background()
	require.NoError(t, store.WriteFile(ctx, workflow.Shared(), "empty.md", nil))
	got, err := store.ReadFile(ctx, workflow.Shared(), "empty.md")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// --- Integration ---

func TestLibraryStore_ConcurrentRewriteNeverTorn(t *testing.T) {
	base, _, _ := libFixture(t)
	store := filesystem.NewLibraryStore(base)
	reader := filesystem.NewFileReader(base)
	ctx := context.Background()

	const size = 1 << 20
	allA := bytes.Repeat([]byte("A"), size)
	allB := bytes.Repeat([]byte("B"), size)
	require.NoError(t, store.WriteFile(ctx, workflow.Shared(), "x", allA))

	done := make(chan struct{})
	var wg sync.WaitGroup
	var writeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < 200; i++ {
			content := allA
			if i%2 == 1 {
				content = allB
			}
			if err := store.WriteFile(ctx, workflow.Shared(), "x", content); err != nil {
				writeErr = err
				return
			}
		}
	}()

	reads := 0
	for {
		select {
		case <-done:
			wg.Wait()
			require.NoError(t, writeErr)
			require.Positive(t, reads)
			return
		default:
		}
		data, err := reader.ReadFile(uuid.Nil, "wf", "@lib/x")
		require.NoError(t, err)
		require.Len(t, data, size)
		if !bytes.Equal(data, allA) && !bytes.Equal(data, allB) {
			t.Fatalf("read %d saw a torn file", reads)
		}
		reads++
	}
}
