package checkpoint_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lsFile struct {
	body string
	mode os.FileMode
	link string // non-empty: a symlink to this target
}

func lsWrite(t *testing.T, root string, files map[string]lsFile, emptyDirs ...string) {
	t.Helper()
	for rel, f := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		if f.link != "" {
			require.NoError(t, os.Symlink(f.link, p))
			continue
		}
		require.NoError(t, os.WriteFile(p, []byte(f.body), f.mode))
		require.NoError(t, os.Chmod(p, f.mode))
	}
	for _, d := range emptyDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o700))
	}
}

// lsRead returns rel -> "body|mode" or "-> target" for every non-directory entry.
func lsRead(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		require.NoError(t, err)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			require.NoError(t, err)
			out[filepath.ToSlash(rel)] = "-> " + l
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			out[filepath.ToSlash(rel)] = string(b) + "|" + info.Mode().Perm().String()
		}
		return nil
	}))
	return out
}

func lsVersions(t *testing.T, dir string, run uuid.UUID) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(dir, run.String()))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func lsFixture() map[string]lsFile {
	return map[string]lsFile{
		"a.txt":                             {body: "alpha", mode: 0o644},
		"bin/run.sh":                        {body: "#!/bin/sh\necho hi\n", mode: 0o755},
		"secret.txt":                        {body: "s", mode: 0o600},
		"repo/.git/HEAD":                    {body: "ref: refs/heads/main\n", mode: 0o644},
		"repo/.git/objects/info/alternates": {body: "/srv/origin.git/objects\n", mode: 0o644},
		"repo/.git/config":                  {body: "[extensions]\n\tpartialClone = origin\n", mode: 0o644},
		"link":                              {link: "a.txt"},
	}
}

// Happy path: the copy round-trips bytes, modes, symlinks (not followed),
// promisor Git metadata verbatim and empty directories.
func TestLocalStore_RoundTripPreservesTheHomeVerbatim(t *testing.T) {
	ctx := context.Background()
	store := checkpoint.NewLocalStore(t.TempDir())
	ws := filepath.Join(t.TempDir(), "ws")
	lsWrite(t, ws, lsFixture(), "empty")
	want := lsRead(t, ws)
	run := uuid.New()

	ref, err := store.StageLocal(ctx, run, ws, 1)
	require.NoError(t, err)
	assert.Equal(t, run, ref.RunID)
	assert.Equal(t, int64(1), ref.Version)
	assert.NotEmpty(t, ref.ManifestHash)

	// the failed/next step dirties the copy
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a.txt"), []byte("half-written"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(ws, "bin", "run.sh")))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "junk.txt"), []byte("x"), 0o600))

	require.NoError(t, store.RestoreLocal(ctx, ref, ws))
	assert.Equal(t, want, lsRead(t, ws), "SINK: the restored home is byte- and mode-identical to the checkpoint")
	assert.DirExists(t, filepath.Join(ws, "empty"))
	siblings, err := os.ReadDir(filepath.Dir(ws))
	require.NoError(t, err)
	assert.Len(t, siblings, 1, "no restore staging or swapped-out copy is left next to the workspace")
}

// Identical bytes staged twice at one version are the same prepared checkpoint.
func TestLocalStore_IdenticalRestageIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := checkpoint.NewLocalStore(dir)
	ws := t.TempDir()
	lsWrite(t, ws, lsFixture())
	run := uuid.New()
	a, err := store.StageLocal(ctx, run, ws, 1)
	require.NoError(t, err)
	b, err := store.StageLocal(ctx, run, ws, 1)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.Len(t, lsVersions(t, dir, run), 1)
}

// Failure: the copy fails part-way (disk full). No new version exists, no
// staging is left behind and the previous checkpoint still restores.
func TestLocalStore_CopyFailureLeavesNoNewVersionAndKeepsThePrevious(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ws := t.TempDir()
	lsWrite(t, ws, lsFixture())
	run := uuid.New()
	v1, err := checkpoint.NewLocalStore(dir).StageLocal(ctx, run, ws, 1)
	require.NoError(t, err)
	before := lsVersions(t, dir, run)

	n := 0
	full := checkpoint.NewLocalStore(dir, checkpoint.WithLocalFileCreator(func(p string, perm os.FileMode) (io.WriteCloser, error) {
		n++
		if n > 2 {
			return nil, syscall.ENOSPC
		}
		return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	}))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a.txt"), []byte("step two"), 0o644))
	_, err = full.StageLocal(ctx, run, ws, 2)
	require.ErrorIs(t, err, syscall.ENOSPC)
	assert.Equal(t, before, lsVersions(t, dir, run), "no v2 and no staging leftover")

	dst := filepath.Join(t.TempDir(), "restored")
	require.NoError(t, checkpoint.NewLocalStore(dir).RestoreLocal(ctx, v1, dst))
	assert.Equal(t, "alpha|-rw-r--r--", lsRead(t, dst)["a.txt"], "SINK: v1 is intact")
}

// Edge: a crash in the middle of a copy (process killed, no deferred
// cleanup) leaves a staging directory. The previous checkpoint is untouched
// and restorable, and the next prune reclaims the leftover.
func TestLocalStore_InterruptedStageLeavesThePreviousCheckpointIntact(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := checkpoint.NewLocalStore(dir)
	ws := t.TempDir()
	lsWrite(t, ws, lsFixture())
	run := uuid.New()
	v1, err := store.StageLocal(ctx, run, ws, 1)
	require.NoError(t, err)

	crashed := filepath.Join(dir, run.String(), ".staging-crashed", "tree")
	require.NoError(t, os.MkdirAll(crashed, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(crashed, "a.txt"), []byte("hal"), 0o600))

	dst := filepath.Join(t.TempDir(), "restored")
	require.NoError(t, store.RestoreLocal(ctx, v1, dst))
	assert.Equal(t, "alpha|-rw-r--r--", lsRead(t, dst)["a.txt"])

	require.NoError(t, store.PruneLocal(ctx, run, v1))
	assert.Len(t, lsVersions(t, dir, run), 1, "the staging leftover is gone; v1 remains")
	require.NoError(t, store.RestoreLocal(ctx, v1, dst))
}

// Retention: pruning with the committed ref keeps exactly that version.
func TestLocalStore_PruneKeepsOnlyTheCommittedVersion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := checkpoint.NewLocalStore(dir)
	ws := t.TempDir()
	run := uuid.New()
	var refs []runner.LocalCheckpointRef
	for v := int64(1); v <= 3; v++ {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "n.txt"), []byte(strings.Repeat("x", int(v))), 0o600))
		ref, err := store.StageLocal(ctx, run, ws, v)
		require.NoError(t, err)
		refs = append(refs, ref)
	}
	require.Len(t, lsVersions(t, dir, run), 3)

	require.NoError(t, store.PruneLocal(ctx, run, refs[2]))
	assert.Len(t, lsVersions(t, dir, run), 1)
	require.NoError(t, store.RestoreLocal(ctx, refs[2], filepath.Join(t.TempDir(), "r")))
	err := store.RestoreLocal(ctx, refs[1], filepath.Join(t.TempDir(), "r"))
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound)

	require.NoError(t, store.PruneLocal(ctx, uuid.New(), runner.LocalCheckpointRef{}), "an unknown run prunes nothing")
	require.NoError(t, store.Delete(ctx, run))
	assert.Empty(t, lsVersions(t, dir, run))
}

// Error: tampered checkpoint bytes are detected; the target is not touched.
func TestLocalStore_RestoreRejectsACorruptCheckpointAndLeavesTheTargetAlone(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := checkpoint.NewLocalStore(dir)
	ws := t.TempDir()
	lsWrite(t, ws, lsFixture())
	run := uuid.New()
	ref, err := store.StageLocal(ctx, run, ws, 1)
	require.NoError(t, err)
	matches, err := filepath.Glob(filepath.Join(dir, run.String(), "v*", "tree", "a.txt"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.NoError(t, os.WriteFile(matches[0], []byte("tampered"), 0o644))

	dst := filepath.Join(t.TempDir(), "ws")
	lsWrite(t, dst, map[string]lsFile{"keep.txt": {body: "current", mode: 0o600}})
	err = store.RestoreLocal(ctx, ref, dst)
	require.ErrorIs(t, err, runner.ErrCorruptCheckpoint)
	assert.Equal(t, map[string]string{"keep.txt": "current|-rw-------"}, lsRead(t, dst))
	siblings, _ := os.ReadDir(filepath.Dir(dst))
	assert.Len(t, siblings, 1, "restore staging is removed")
}

// Error: FIFOs/sockets/devices are not copied silently.
func TestLocalStore_RejectsUnsupportedFileTypes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ws := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(ws, "pipe"), 0o600))
	run := uuid.New()
	_, err := checkpoint.NewLocalStore(dir).StageLocal(ctx, run, ws, 1)
	require.ErrorIs(t, err, runner.ErrUnsupportedFileType)
	assert.Empty(t, lsVersions(t, dir, run))
}

func TestLocalStore_RestoreOfAMissingVersionIsNotFound(t *testing.T) {
	store := checkpoint.NewLocalStore(t.TempDir())
	ref := runner.LocalCheckpointRef{RunID: uuid.New(), Version: 4, ManifestHash: strings.Repeat("c", 64)}
	err := store.RestoreLocal(context.Background(), ref, filepath.Join(t.TempDir(), "ws"))
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound)
	_, err = store.StageLocal(context.Background(), uuid.Nil, t.TempDir(), 1)
	require.Error(t, err)
}
