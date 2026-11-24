// adapter/checkpoint/store_test.go
package checkpoint_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_RejectsSymlinkAliasedStore(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	require.NoError(t, os.Mkdir(storeDir, 0o700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(storeDir, alias))
	store := checkpoint.NewStore(storeDir, checkpoint.WithCatalog(cat))
	run := uuid.New()
	_, _, err := store.Commit(ctx, run, alias)
	require.Error(t, err)
	_, ok, err := store.Latest(ctx, run)
	require.NoError(t, err)
	assert.False(t, ok, "overlapping workspace must never publish a baseline")
}

func ckWrite(t *testing.T, root, rel, body string, perm os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), perm))
	require.NoError(t, os.Chmod(p, perm))
}

// ckTree renders a directory as sorted "path mode [content|->link]" lines.
func ckTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			out = append(out, rel+" link ->"+l)
		case info.IsDir():
			out = append(out, fmt.Sprintf("%s dir %o", rel, info.Mode().Perm()))
		default:
			b, _ := os.ReadFile(p)
			out = append(out, fmt.Sprintf("%s %o %s", rel, info.Mode().Perm(), b))
		}
		return nil
	}))
	sort.Strings(out)
	return out
}

func newCkStore(t *testing.T, cat runner.CheckpointCatalog, opts ...checkpoint.Option) (*checkpoint.Store, string) {
	t.Helper()
	dir := t.TempDir()
	return checkpoint.NewStore(dir, append([]checkpoint.Option{checkpoint.WithCatalog(cat)}, opts...)...), dir
}

// ckOperation returns an exact job fence and run resource claim, activated in cat.
func ckOperation(cat *memCatalog, run uuid.UUID) (runner.Fence, engine.ResourceFenceRef) {
	f := runner.Fence{JobID: uuid.New(), RunnerID: runner.BuiltInRunnerID, Generation: 1}
	r := engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: string(runner.BuiltInRunnerID), Generation: 1, Token: uuid.New()}
	cat.activate(f, r)
	return f, r
}

func TestStore_RoundTripChmodDeletionSymlink(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "run.sh", "#!/bin/sh\necho hi\n", 0o755)
	ckWrite(t, ws, "gone.txt", "bye", 0o644)
	ckWrite(t, ws, "sub/keep.txt", "keep", 0o600)
	require.NoError(t, os.Mkdir(filepath.Join(ws, "empty"), 0o750))
	require.NoError(t, os.Symlink("run.sh", filepath.Join(ws, "link")))
	v1Tree := ckTree(t, ws)

	ref1, m1, err := store.Commit(ctx, run, ws) // bootstrap baseline
	require.NoError(t, err)
	assert.Equal(t, m1.Hash, ref1.ManifestHash)

	require.NoError(t, os.Chmod(filepath.Join(ws, "run.sh"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(ws, "gone.txt")))
	require.NoError(t, os.Remove(filepath.Join(ws, "link")))
	require.NoError(t, os.Symlink("sub/keep.txt", filepath.Join(ws, "link")))
	v2Tree := ckTree(t, ws)
	f, res := ckOperation(cat, run)
	ref2, _, err := store.StageBuiltIn(ctx, run, ws, f, res)
	require.NoError(t, err)
	assert.Greater(t, ref2.Version, ref1.Version)
	latest, _, _ := store.Latest(ctx, run)
	assert.Equal(t, ref1, latest, "a prepared candidate is not progress")
	require.NoError(t, cat.commit(ref2), "test validated publisher")

	dst := t.TempDir()
	ckWrite(t, dst, "stale.txt", "must be wiped", 0o644)
	require.NoError(t, store.Restore(ctx, ref2, dst))
	assert.Equal(t, v2Tree, ckTree(t, dst), "restore replaces dst with v2 exactly")

	dst1 := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref1, dst1))
	assert.Equal(t, v1Tree, ckTree(t, dst1), "the previous committed checkpoint is still restorable")

	latest, ok, err := store.Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ref2, latest)
	got, err := store.Manifest(ctx, ref1)
	require.NoError(t, err)
	assert.Equal(t, m1, got)
}

func TestStore_RejectsUnsupportedFileTypes(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "ok.txt", "x", 0o644)
	require.NoError(t, syscall.Mkfifo(filepath.Join(ws, "pipe"), 0o600))
	_, _, err := store.Commit(ctx, run, ws)
	require.ErrorIs(t, err, runner.ErrUnsupportedFileType)
	assert.Contains(t, err.Error(), "pipe")
	f, res := ckOperation(cat, run)
	_, _, err = store.StageBuiltIn(ctx, run, ws, f, res)
	require.ErrorIs(t, err, runner.ErrUnsupportedFileType)
	_, ok, err := store.Latest(ctx, run)
	require.NoError(t, err)
	assert.False(t, ok, "nothing committed")
	assert.Empty(t, cat.prepared(run), "nothing prepared")
}

func TestStore_StageBuiltInRequiresExactEvidence(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	_, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	f, res := ckOperation(cat, run)
	_, _, err = store.StageBuiltIn(ctx, run, ws, runner.Fence{}, res)
	require.ErrorIs(t, err, runner.ErrInvalidCandidate, "a zero job fence is never a wildcard")
	_, _, err = store.StageBuiltIn(ctx, run, ws, f, engine.ResourceFenceRef{})
	require.ErrorIs(t, err, runner.ErrInvalidCandidate, "a zero resource is never a wildcard")
	stale := f
	stale.Generation = 7
	_, _, err = store.StageBuiltIn(ctx, run, ws, stale, res)
	require.ErrorIs(t, err, runner.ErrFenced)
	assert.Empty(t, cat.prepared(run))
}

func TestStore_ConcurrentStageBuiltInAllocatesDistinctVersions(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	base, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	f, res := ckOperation(cat, run)
	var wg sync.WaitGroup
	refs := make([]runner.CheckpointRef, 8)
	errs := make([]error, 8)
	for i := range refs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refs[i], _, errs[i] = store.StageBuiltIn(ctx, run, ws, f, res)
		}(i)
	}
	wg.Wait()
	seen := map[int64]bool{}
	for i := range refs {
		require.NoError(t, errs[i])
		assert.Greater(t, refs[i].Version, base.Version)
		assert.False(t, seen[refs[i].Version], "version %d allocated twice", refs[i].Version)
		seen[refs[i].Version] = true
	}
	assert.Len(t, cat.prepared(run), 8)
}

func TestStore_NoCatalogFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := checkpoint.NewStore(t.TempDir())
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	run := uuid.New()
	_, _, err := store.Commit(ctx, run, ws)
	assert.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
	_, _, err = store.StageBuiltIn(ctx, run, ws, runner.Fence{JobID: uuid.New(), RunnerID: "builtin", Generation: 1},
		engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: "builtin", Generation: 1, Token: uuid.New()})
	assert.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
	_, _, err = store.Latest(ctx, run)
	assert.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
	ref := runner.CheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("a", 64)}
	assert.ErrorIs(t, store.Restore(ctx, ref, t.TempDir()), runner.ErrNoCheckpointCatalog)
	_, err = store.Manifest(ctx, ref)
	assert.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
	assert.ErrorIs(t, store.Delete(ctx, run), runner.ErrNoCheckpointCatalog)
}

// rewriteArchive replaces a stored artifact's bytes (simulating corruption or
// a hostile archive reaching extraction).
func rewriteArchive(t *testing.T, storeDir string, ref runner.CheckpointRef, build func(tw *tar.Writer)) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	build(tw)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	p := filepath.Join(storeDir, "runs", ref.RunID.String(), fmt.Sprintf("v%010d.tar.gz", ref.Version))
	require.NoError(t, os.Chmod(p, 0o600))
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o600))
}

func tarFile(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}))
	_, err := tw.Write([]byte(body))
	require.NoError(t, err)
}

func TestStore_RestoreRejectsTarSlip(t *testing.T) {
	ctx := context.Background()
	for name, build := range map[string]func(*testing.T, *tar.Writer){
		"parent traversal": func(t *testing.T, tw *tar.Writer) { tarFile(t, tw, "../escape.txt", "x") },
		"absolute path":    func(t *testing.T, tw *tar.Writer) { tarFile(t, tw, "/tmp/escape.txt", "x") },
		"through symlink": func(t *testing.T, tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777}))
			tarFile(t, tw, "l/escape.txt", "x")
		},
		"device": func(t *testing.T, tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: "dev", Typeflag: tar.TypeChar, Mode: 0o600}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			cat := newMemCatalog()
			store, storeDir := newCkStore(t, cat)
			run := uuid.New()
			ws := t.TempDir()
			ckWrite(t, ws, "a", "a", 0o644)
			ref, _, err := store.Commit(ctx, run, ws)
			require.NoError(t, err)
			rewriteArchive(t, storeDir, ref, func(tw *tar.Writer) { build(t, tw) })
			parent := t.TempDir()
			dst := filepath.Join(parent, "ws")
			ckWrite(t, dst, "prior.txt", "prior", 0o644)
			before := ckTree(t, parent)
			require.Error(t, store.Restore(ctx, ref, dst))
			assert.Equal(t, before, ckTree(t, parent), "a refused restore leaves the destination and its parent untouched")
			_, err = os.Stat(filepath.Join(filepath.Dir(parent), "escape.txt"))
			assert.True(t, os.IsNotExist(err))
		})
	}
}

func TestStore_RejectsDuplicateAndOversizeArchiveEntries(t *testing.T) {
	ctx := context.Background()
	limits := checkpoint.Limits{MaxEntries: 4, MaxBytes: 64, MaxPathBytes: 32}
	for name, build := range map[string]func(*testing.T, *tar.Writer){
		"duplicate": func(t *testing.T, tw *tar.Writer) { tarFile(t, tw, "a", "a"); tarFile(t, tw, "a", "a") },
		"oversize":  func(t *testing.T, tw *tar.Writer) { tarFile(t, tw, "a", strings.Repeat("z", 65)) },
		"many entries": func(t *testing.T, tw *tar.Writer) {
			for i := 0; i < 5; i++ {
				tarFile(t, tw, fmt.Sprintf("f%d", i), "")
			}
		},
		"long path":   func(t *testing.T, tw *tar.Writer) { tarFile(t, tw, strings.Repeat("p", 33), "") },
		"extra entry": func(t *testing.T, tw *tar.Writer) { tarFile(t, tw, "a", "a"); tarFile(t, tw, "b", "b") },
		"tampered":    func(t *testing.T, tw *tar.Writer) { tarFile(t, tw, "a", "b") },
	} {
		t.Run(name, func(t *testing.T) {
			cat := newMemCatalog()
			store, storeDir := newCkStore(t, cat, checkpoint.WithLimits(limits))
			run := uuid.New()
			ws := t.TempDir()
			ckWrite(t, ws, "a", "a", 0o644)
			ref, _, err := store.Commit(ctx, run, ws)
			require.NoError(t, err)
			rewriteArchive(t, storeDir, ref, func(tw *tar.Writer) { build(t, tw) })
			dst := t.TempDir()
			err = store.Restore(ctx, ref, dst)
			require.ErrorIs(t, err, runner.ErrCorruptCheckpoint)
			assert.Empty(t, ckTree(t, dst))
		})
	}
	t.Run("trailing data", func(t *testing.T) {
		cat := newMemCatalog()
		store, storeDir := newCkStore(t, cat)
		run := uuid.New()
		ws := t.TempDir()
		ckWrite(t, ws, "a", "a", 0o644)
		ref, _, err := store.Commit(ctx, run, ws)
		require.NoError(t, err)
		p := filepath.Join(storeDir, "runs", run.String(), fmt.Sprintf("v%010d.tar.gz", ref.Version))
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		require.NoError(t, err)
		_, err = f.Write([]byte("trailing part"))
		require.NoError(t, err)
		require.NoError(t, f.Close())
		require.ErrorIs(t, store.Restore(ctx, ref, t.TempDir()), runner.ErrCorruptCheckpoint)
	})
}

// enospcWriter simulates a full disk after `left` bytes.
type enospcWriter struct {
	f    *os.File
	left int
}

func (w *enospcWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		n, _ := w.f.Write(p[:w.left])
		w.left = 0
		return n, syscall.ENOSPC
	}
	n, err := w.f.Write(p)
	w.left -= n
	return n, err
}
func (w *enospcWriter) Close() error { return w.f.Close() }

func TestStore_DiskFullDuringApplyKeepsPreviousCheckpoint(t *testing.T) {
	ctx := context.Background()
	var full atomic.Bool
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat, checkpoint.WithFileCreator(func(p string, perm os.FileMode) (io.WriteCloser, error) {
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
	ws := t.TempDir()
	ckWrite(t, ws, "a.txt", "v1 content", 0o644)
	v1Tree := ckTree(t, ws)
	ref1, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)

	full.Store(true)
	ckWrite(t, ws, "a.txt", strings.Repeat("v2 ", 4096), 0o644)
	f, res := ckOperation(cat, run)
	_, _, err = store.StageBuiltIn(ctx, run, ws, f, res)
	require.ErrorIs(t, err, syscall.ENOSPC)
	assert.Empty(t, cat.prepared(run), "no candidate is registered for a partial artifact")

	full.Store(false)
	latest, ok, err := store.Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ref1, latest)
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, latest, dst))
	assert.Equal(t, v1Tree, ckTree(t, dst))
}

func TestStore_DeleteRemovesRunArtifacts(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, storeDir := newCkStore(t, cat)
	run, other := uuid.New(), uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	_, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	otherRef, _, err := store.Commit(ctx, other, ws)
	require.NoError(t, err)
	require.NoError(t, store.Delete(ctx, run))
	_, ok, err := store.Latest(ctx, run)
	require.NoError(t, err)
	assert.False(t, ok)
	_, err = os.Stat(filepath.Join(storeDir, "runs", run.String()))
	assert.True(t, os.IsNotExist(err))
	require.NoError(t, store.Restore(ctx, otherRef, t.TempDir()), "another run is untouched")
}
