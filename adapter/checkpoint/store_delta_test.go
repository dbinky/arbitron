// adapter/checkpoint/store_delta_test.go
package checkpoint_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ runner.CheckpointStore = (*checkpoint.Store)(nil)

// ckDelta encodes a push the way the runner agent does (P3 T9).
func ckDelta(t *testing.T, root string, base []runner.ManifestEntry) ([]byte, runner.DeltaControl) {
	t.Helper()
	now, err := checkpoint.ScanEntries(context.Background(), root)
	require.NoError(t, err)
	changed, deleted := runner.DiffManifests(base, now)
	paths := make([]string, 0, len(changed))
	for _, c := range changed {
		paths = append(paths, c.Path)
	}
	ctl := runner.DeltaControl{Deleted: deleted, Expected: now}
	var buf bytes.Buffer
	require.NoError(t, checkpoint.WriteDelta(&buf, root, paths, ctl))
	return buf.Bytes(), ctl
}

func stagingEntries(t *testing.T, storeDir string) []os.DirEntry {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(storeDir, "staging"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return es
}

func TestStore_StageDeltaPreparesUnderFence(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, storeDir := newCkStore(t, cat)
	run := uuid.New()
	server := t.TempDir()
	ckWrite(t, server, "a.txt", "one", 0o644)
	ckWrite(t, server, "old.txt", "old", 0o644)
	base, baseM, err := store.Commit(ctx, run, server)
	require.NoError(t, err)
	baseTree := ckTree(t, server)

	runnerCopy := t.TempDir()
	require.NoError(t, store.Restore(ctx, base, runnerCopy))
	ckWrite(t, runnerCopy, "a.txt", "two", 0o600)
	require.NoError(t, os.Remove(filepath.Join(runnerCopy, "old.txt")))
	ckWrite(t, runnerCopy, "pkg/new.go", "package pkg", 0o644)
	delta, _ := ckDelta(t, runnerCopy, baseM.Entries)

	staged, err := store.StageDelta(ctx, run, base, bytes.NewReader(delta))
	require.NoError(t, err)
	assert.Greater(t, staged.Version, base.Version)
	assert.Equal(t, base, staged.Base)

	f, res := ckOperation(cat, run)
	_, err = store.Publish(ctx, staged, f)
	require.ErrorIs(t, err, runner.ErrInvalidCandidate, "a staged push without its session's resource never prepares")
	assert.Empty(t, stagingEntries(t, storeDir), "a refused publish discards its staging")

	staged, err = store.StageDelta(ctx, run, base, bytes.NewReader(delta))
	require.NoError(t, err)
	staged.Resource = res // assigned from the durable transfer session (P3 T7)
	stale := f
	stale.Generation++
	_, err = store.Publish(ctx, staged, stale)
	require.ErrorIs(t, err, runner.ErrFenced, "a stale job generation never prepares")
	assert.Empty(t, cat.prepared(run))

	staged, err = store.StageDelta(ctx, run, base, bytes.NewReader(delta))
	require.NoError(t, err)
	staged.Resource = res
	ref, err := store.Publish(ctx, staged, f)
	require.NoError(t, err)
	assert.Equal(t, []runner.CheckpointRef{ref}, cat.prepared(run))
	latest, _, _ := store.Latest(ctx, run)
	assert.Equal(t, base, latest, "upload alone is not success")
	_, err = store.Manifest(ctx, ref)
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound, "a prepared candidate is not served")

	require.NoError(t, cat.commit(ref), "test validated publisher")
	latest, _, _ = store.Latest(ctx, run)
	assert.Equal(t, ref, latest)
	got := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref, got))
	assert.Equal(t, ckTree(t, runnerCopy), ckTree(t, got))
	again := t.TempDir()
	require.NoError(t, store.Restore(ctx, base, again))
	assert.Equal(t, baseTree, ckTree(t, again), "staging never mutated the base")
}

func TestStore_PartialUploadNeverApplied(t *testing.T) {
	ctx := context.Background()
	var full atomic.Bool
	cat := newMemCatalog()
	store, storeDir := newCkStore(t, cat, checkpoint.WithFileCreator(func(p string, perm os.FileMode) (io.WriteCloser, error) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			return nil, err
		}
		if full.Load() {
			return &enospcWriter{f: f, left: 16}, nil
		}
		return f, nil
	}))
	run := uuid.New()
	server := t.TempDir()
	ckWrite(t, server, "a.txt", "one", 0o644)
	base, baseM, err := store.Commit(ctx, run, server)
	require.NoError(t, err)
	runnerCopy := t.TempDir()
	require.NoError(t, store.Restore(ctx, base, runnerCopy))
	ckWrite(t, runnerCopy, "big.bin", strings.Repeat("z", 1<<16), 0o644)
	delta, ctl := ckDelta(t, runnerCopy, baseM.Entries)

	_, err = store.StageDelta(ctx, run, base, bytes.NewReader(delta[:len(delta)/2]))
	require.Error(t, err, "connection killed mid-transfer: truncated stream")

	_, err = store.StageDelta(ctx, run, base, io.MultiReader(bytes.NewReader(delta), strings.NewReader("extra part")))
	require.ErrorIs(t, err, runner.ErrCorruptCheckpoint, "unexpected trailing part")

	bad := ctl
	bad.Expected = append([]runner.ManifestEntry(nil), ctl.Expected...)
	for i := range bad.Expected {
		if bad.Expected[i].Path == "big.bin" {
			bad.Expected[i].SHA256 = strings.Repeat("0", 64)
		}
	}
	var badBuf bytes.Buffer
	require.NoError(t, checkpoint.WriteDelta(&badBuf, runnerCopy, []string{"big.bin"}, bad))
	_, err = store.StageDelta(ctx, run, base, &badBuf)
	require.ErrorIs(t, err, runner.ErrManifestMismatch, "declared hash differs from the bytes")

	var slip bytes.Buffer
	gz := gzip.NewWriter(&slip)
	tw := tar.NewWriter(gz)
	tarFile(t, tw, runner.DeltaControlPath, `{"deleted":[],"expected":[]}`)
	tarFile(t, tw, "../escape.txt", "x")
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	_, err = store.StageDelta(ctx, run, base, &slip)
	require.Error(t, err, "tar slip in a push")

	full.Store(true)
	_, err = store.StageDelta(ctx, run, base, bytes.NewReader(delta))
	require.ErrorIs(t, err, syscall.ENOSPC, "disk full while applying the push")
	full.Store(false)

	latest, _, _ := store.Latest(ctx, run)
	assert.Equal(t, base, latest, "the committed checkpoint never moved")
	assert.Empty(t, cat.prepared(run), "nothing was prepared")
	assert.Empty(t, stagingEntries(t, storeDir), "failed staging leaves nothing behind")
}

func TestStore_ReadFileServesCommittedContentOnly(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "dir/a.txt", "committed", 0o644)
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(ws, "escape")))
	ref, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	ckWrite(t, ws, "dir/a.txt", "dirty edit after commit", 0o644)

	rc, err := store.ReadFile(ctx, ref, "dir/a.txt")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	assert.Equal(t, "committed", string(b), "served from the immutable snapshot, never the work dir")

	for _, bad := range []string{"../x", "/etc/hosts", "escape", "dir", "missing", ""} {
		_, err := store.ReadFile(ctx, ref, bad)
		assert.Error(t, err, bad)
	}

	f, res := ckOperation(cat, run)
	prepared, _, err := store.StageBuiltIn(ctx, run, ws, f, res)
	require.NoError(t, err)
	_, err = store.ReadFile(ctx, prepared, "dir/a.txt")
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound, "prepared candidates are never served")
}

func TestStore_TwoValidatedCommitsHaveDistinctManifests(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a.txt", "same", 0o644)
	_, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	f, res := ckOperation(cat, run)

	r2, m2, err := store.StageBuiltIn(ctx, run, ws, f, res)
	require.NoError(t, err)
	require.NoError(t, cat.commit(r2))
	r3, m3, err := store.StageBuiltIn(ctx, run, ws, f, res)
	require.NoError(t, err)
	latest, _, _ := store.Latest(ctx, run)
	assert.Equal(t, r2, latest, "the second step is not progress until validated")
	require.NoError(t, cat.commit(r3))
	latest, _, _ = store.Latest(ctx, run)
	assert.Equal(t, r3, latest)
	assert.NotEqual(t, r2.ManifestHash, r3.ManifestHash)
	assert.True(t, runner.SameEntries(m2.Entries, m3.Entries), "content is identical")
}

// syncRecorder records the identity (inode) of every file fsynced through it.
type syncRecorder struct {
	*os.File
	mu     *sync.Mutex
	synced *[]os.FileInfo
}

func (s syncRecorder) Sync() error {
	if err := s.File.Sync(); err != nil {
		return err
	}
	fi, err := s.File.Stat()
	if err != nil {
		return err
	}
	s.mu.Lock()
	*s.synced = append(*s.synced, fi)
	s.mu.Unlock()
	return nil
}

func TestStore_ArtifactSyncedBeforeCandidateRegistered(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var synced []os.FileInfo
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat, checkpoint.WithFileCreator(func(p string, perm os.FileMode) (io.WriteCloser, error) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			return nil, err
		}
		return syncRecorder{File: f, mu: &mu, synced: &synced}, nil
	}))
	var registered []runner.CheckpointCandidate
	cat.onPublish = func(c runner.CheckpointCandidate) {
		mu.Lock()
		defer mu.Unlock()
		archive, err := os.Stat(c.ArchivePath)
		require.NoError(t, err, "the registered artifact exists")
		sameInode := false
		for _, fi := range synced {
			sameInode = sameInode || os.SameFile(fi, archive)
		}
		assert.True(t, sameInode, "archive %s was fsynced before registration", c.ArchivePath)
		registered = append(registered, c)
	}
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	base, baseM, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	f, res := ckOperation(cat, run)
	_, _, err = store.StageBuiltIn(ctx, run, ws, f, res)
	require.NoError(t, err)
	runnerCopy := t.TempDir()
	require.NoError(t, store.Restore(ctx, base, runnerCopy))
	ckWrite(t, runnerCopy, "b", "b", 0o644)
	delta, _ := ckDelta(t, runnerCopy, baseM.Entries)
	staged, err := store.StageDelta(ctx, run, base, bytes.NewReader(delta))
	require.NoError(t, err)
	staged.Resource = res
	_, err = store.Publish(ctx, staged, f)
	require.NoError(t, err)
	assert.Len(t, registered, 2, "built-in and pushed candidates both registered after fsync")
}

func TestStore_NoCatalogFailsClosedForPushes(t *testing.T) {
	ctx := context.Background()
	store := checkpoint.NewStore(t.TempDir())
	_, err := store.StageDelta(ctx, uuid.New(), runner.CheckpointRef{}, strings.NewReader(""))
	assert.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
	_, err = store.Publish(ctx, runner.StagedCheckpoint{}, runner.Fence{})
	assert.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
}

func TestStore_DiscardRefusesForeignDirectories(t *testing.T) {
	ctx := context.Background()
	store, _ := newCkStore(t, newMemCatalog())
	victim := t.TempDir()
	ckWrite(t, victim, "keep", "keep", 0o644)
	require.Error(t, store.Discard(ctx, runner.StagedCheckpoint{Dir: victim, ArchivePath: filepath.Join(victim, "archive.tar.gz")}))
	_, err := os.Stat(filepath.Join(victim, "keep"))
	require.NoError(t, err, "Discard never removes a directory the store does not own")
}

func TestStore_StageResourceRegistersNothing(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a.txt", "baseline", 0o644)
	base, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	ckWrite(t, ws, "a.txt", "group mutation", 0o644)
	ckWrite(t, ws, "b/new.txt", "added", 0o600)
	wantTree := ckTree(t, ws)

	staged, err := store.StageResource(ctx, run, ws, base)
	require.NoError(t, err)
	assert.Equal(t, base, staged.Base)
	assert.Greater(t, staged.Version, base.Version)
	require.NoError(t, staged.Manifest.Verify())
	assert.Equal(t, staged.Version, staged.Manifest.Version)
	assert.Empty(t, staged.Dir, "no staging directory: the artifact is already immutable")
	_, err = os.Stat(staged.ArchivePath)
	require.NoError(t, err, "the artifact bytes are on disk")

	assert.Empty(t, cat.prepared(run), "no catalog row is registered")
	latest, _, err := store.Latest(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, base, latest, "Latest is unchanged")
	ref := runner.CheckpointRef{RunID: run, Version: staged.Version, ManifestHash: staged.Manifest.Hash}
	_, err = store.Manifest(ctx, ref)
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound, "not served before P3 T13 commits it")

	committed, err := cat.commitResource(staged) // stand-in for ResourceCommitRepo.CommitResource
	require.NoError(t, err)
	assert.Equal(t, ref, committed)
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, committed, dst))
	assert.Equal(t, wantTree, ckTree(t, dst))

	_, err = store.StageResource(ctx, run, ws, runner.CheckpointRef{RunID: uuid.New(), Version: 1, ManifestHash: strings.Repeat("a", 64)})
	require.ErrorIs(t, err, runner.ErrCheckpointConflict, "a base from another run")
}

func TestStore_StageResourcePropagatesWalkErrors(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	base, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	require.NoError(t, syscall.Mkfifo(filepath.Join(ws, "pipe"), 0o600))
	_, err = store.StageResource(ctx, run, ws, base)
	require.ErrorIs(t, err, runner.ErrUnsupportedFileType)
	latest, _, _ := store.Latest(ctx, run)
	assert.Equal(t, base, latest)
	_, err = checkpoint.NewStore(t.TempDir()).StageResource(ctx, run, ws, base)
	require.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
}

func TestStore_PreparedForDelegatesToTheCatalog(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	_, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	f, res := ckOperation(cat, run)
	ref, _, err := store.StageBuiltIn(ctx, run, ws, f, res)
	require.NoError(t, err)

	got, ok, err := store.PreparedFor(ctx, run, f, res)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ref, got)
	other := f
	other.Generation++
	_, ok, err = store.PreparedFor(ctx, run, other, res)
	require.NoError(t, err)
	assert.False(t, ok)
	require.NoError(t, cat.commit(ref))
	_, ok, err = store.PreparedFor(ctx, run, f, res)
	require.NoError(t, err)
	assert.False(t, ok, "a committed candidate is not prepared")
	_, _, err = checkpoint.NewStore(t.TempDir()).PreparedFor(ctx, run, f, res)
	require.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
}

// Decided (user, 2026-11-23): link targets are kept as written (virtualenvs
// need absolute links) and no write, delete or read ever goes through a link.
func TestStore_AbsoluteSymlinkIsTextNeverTraversed(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	outside := t.TempDir()
	ckWrite(t, outside, "victim.txt", "must survive", 0o644)
	outsideTree := ckTree(t, outside)
	ws := t.TempDir()
	ckWrite(t, ws, "app.py", "print(1)", 0o644)
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "venv"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "venv", "python")))
	base, baseM, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)

	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, base, dst))
	link, err := os.Readlink(filepath.Join(dst, "venv", "python"))
	require.NoError(t, err)
	assert.Equal(t, outside, link, "the absolute target survives as text")
	require.NoError(t, store.Restore(ctx, base, dst), "re-restoring wipes dst without following the link")
	assert.Equal(t, outsideTree, ckTree(t, outside))

	for _, p := range []string{"venv/python", "venv/python/victim.txt"} {
		_, err := store.ReadFile(ctx, base, p)
		assert.Error(t, err, "ReadFile never dereferences %s", p)
	}

	// A push that deletes or writes beneath the link is refused; deleting the
	// link itself removes only the link.
	runnerCopy := t.TempDir()
	require.NoError(t, store.Restore(ctx, base, runnerCopy))
	var del bytes.Buffer
	require.NoError(t, checkpoint.WriteDelta(&del, runnerCopy, nil,
		runner.DeltaControl{Deleted: []string{"venv/python/victim.txt"}, Expected: baseM.Entries}))
	_, err = store.StageDelta(ctx, run, base, &del)
	require.ErrorIs(t, err, runner.ErrCorruptCheckpoint, "delete through")
	var through bytes.Buffer
	gz := gzip.NewWriter(&through)
	tw := tar.NewWriter(gz)
	tarFile(t, tw, runner.DeltaControlPath, `{"deleted":[],"expected":[]}`)
	tarFile(t, tw, "venv/python/planted.txt", "x")
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	_, err = store.StageDelta(ctx, run, base, &through)
	require.ErrorIs(t, err, runner.ErrCorruptCheckpoint, "write through")
	_, err = os.Stat(filepath.Join(outside, "planted.txt"))
	assert.True(t, os.IsNotExist(err))

	require.NoError(t, os.Remove(filepath.Join(runnerCopy, "venv", "python")))
	delta, _ := ckDelta(t, runnerCopy, baseM.Entries)
	staged, err := store.StageDelta(ctx, run, base, bytes.NewReader(delta))
	require.NoError(t, err, "deleting the link itself is an ordinary deletion")
	require.NoError(t, store.Discard(ctx, staged))

	require.NoError(t, store.Delete(ctx, run))
	assert.Equal(t, outsideTree, ckTree(t, outside), "no restore, read, push or delete touched the link target")
}
