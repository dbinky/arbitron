// adapter/runneragent/workspace/sync.go
// Package workspace keeps a runner's local copies of run workspaces equal to
// the committed server checkpoints (design §3.3). A push only PREPARES a
// candidate; the copy's committed base moves only when the server names that
// exact candidate as committed (contracts: prepared-push receipt).
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/adapter/runneragent/transfer"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
)

var (
	// ErrOutsideWorkspaceRoot: the dispatch names a path outside the runner's
	// configured (server-equal) root; the runner never touches it.
	ErrOutsideWorkspaceRoot = errors.New("runner workspace: path is outside the configured workspace root")
	// ErrPullVerifyFailed: the server manifest is not the assigned one, or the
	// local tree does not equal it after the pull.
	ErrPullVerifyFailed = errors.New("runner workspace: pulled copy does not match the server checkpoint")
	// ErrStaleBase: the local copy is not on the job's pinned base, so its
	// diff would not apply to the base the push token is pinned to.
	ErrStaleBase = errors.New("runner workspace: local copy is not on the job's pinned base")
	// ErrReceiptMismatch: the server's prepared ref does not name the tree
	// this runner pushed.
	ErrReceiptMismatch = errors.New("runner workspace: prepared receipt does not match the pushed tree")
)

const (
	minPartBytes = 64
	partAttempts = 3
)

// SyncerDeps: WorkspaceRoot is the canonical root (equal to the server's, D2);
// LocalRoot is where copies physically live: equal to WorkspaceRoot in
// production; tests point it elsewhere so runner and server share a machine.
type SyncerDeps struct {
	WorkspaceRoot string
	LocalRoot     string
	Transfer      transfer.Transfer
	Copies        runneragent.CopyStateStore
	Clock         func() time.Time
}

type Syncer struct{ d SyncerDeps }

func NewSyncer(d SyncerDeps) (*Syncer, error) {
	if d.WorkspaceRoot == "" || d.Transfer == nil || d.Copies == nil {
		return nil, errors.New("runner workspace: WorkspaceRoot, Transfer and Copies are required")
	}
	if d.LocalRoot == "" {
		d.LocalRoot = d.WorkspaceRoot
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	d.WorkspaceRoot = filepath.Clean(d.WorkspaceRoot)
	d.LocalRoot = filepath.Clean(d.LocalRoot)
	return &Syncer{d: d}, nil
}

// LocalRoot is where local copies live (WorkspaceRoot in production).
func (s *Syncer) LocalRoot() string { return s.d.LocalRoot }

// LocalPath maps a canonical {{workspace}} path to the local copy path.
func (s *Syncer) LocalPath(workDir string) (string, error) {
	rel, err := filepath.Rel(s.d.WorkspaceRoot, filepath.Clean(workDir))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %s", ErrOutsideWorkspaceRoot, workDir)
	}
	return filepath.Join(s.d.LocalRoot, rel), nil
}

// scope checks the path and the whole scoped descriptor (Cycle 1b) before any
// transfer or local mutation; nothing is defaulted.
func (s *Syncer) scope(ws runneragent.WorkspaceJob) (string, runneragent.PullTarget, error) {
	local, err := s.LocalPath(ws.WorkDir)
	if err != nil {
		return "", runneragent.PullTarget{}, err
	}
	if err := ws.Validate(); err != nil {
		return "", runneragent.PullTarget{}, fmt.Errorf("runner workspace: %w", err)
	}
	id, err := uuid.Parse(ws.RunID)
	if err != nil {
		return "", runneragent.PullTarget{}, fmt.Errorf("runner workspace: run id: %w", err)
	}
	return local, runneragent.PullTarget{RunID: id, HomeGeneration: ws.HomeGeneration, CheckpointVersion: ws.CheckpointVersion, ManifestHash: ws.ManifestHash}, nil
}

// Pull makes the local copy equal the committed checkpoint pinned by ws and
// returns the local path. A clean copy at the same home generation and
// checkpoint is reused with no transfer. A copy whose prepared receipt names
// exactly the pinned checkpoint, and whose tree is still that candidate, is
// adopted with no transfer. Otherwise the copy is compared with the server
// manifest, only differing paths are fetched (each hash-checked), paths
// absent from the checkpoint are removed, the result is verified to equal the
// checkpoint exactly, and any uncommitted receipt is discarded.
func (s *Syncer) Pull(ctx context.Context, ws runneragent.WorkspaceJob) (string, error) {
	local, tgt, err := s.scope(ws)
	if err != nil {
		return "", err
	}
	st, ok, err := s.d.Copies.Get(ctx, tgt.RunID)
	if err != nil {
		return "", err
	}
	if ok {
		if _, err := os.Stat(local); err == nil {
			if runneragent.ReuseLocalCopy(st, tgt) {
				return local, nil
			}
			adopted, err := s.adoptPrepared(ctx, local, st, tgt)
			if err != nil || adopted {
				return local, err
			}
		}
	}

	server, err := s.d.Transfer.Manifest(ctx, ws.PullToken)
	if err != nil {
		return "", err
	}
	if err := server.Verify(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrPullVerifyFailed, err)
	}
	if server.Hash != ws.ManifestHash || server.Version != ws.CheckpointVersion {
		return "", fmt.Errorf("%w: server has v%d %s, pinned base is %s", ErrPullVerifyFailed, server.Version, server.Hash, ws.BaseRef)
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		return "", err
	}
	have, err := checkpoint.ScanEntries(ctx, local)
	if err != nil {
		// Unsupported content left by an interrupted step: start from empty.
		if err := os.RemoveAll(local); err != nil {
			return "", err
		}
		if err := os.MkdirAll(local, 0o755); err != nil {
			return "", err
		}
		have = nil
	}
	changed, deleted := runner.DiffManifests(have, server.Entries)
	if err := s.apply(ctx, ws.PullToken, local, changed, deleted); err != nil {
		return "", err
	}
	got, err := checkpoint.ScanEntries(ctx, local)
	if err != nil {
		return "", err
	}
	if !runner.SameEntries(got, server.Entries) {
		return "", fmt.Errorf("%w: local tree differs after pull", ErrPullVerifyFailed)
	}
	err = s.d.Copies.Put(ctx, runneragent.CopyState{ // Prepared is nil: an uncommitted receipt is discarded
		RunID: tgt.RunID, WorkDir: ws.WorkDir, HomeGeneration: ws.HomeGeneration,
		CheckpointVersion: server.Version, ManifestHash: server.Hash, Base: server, UpdatedAt: s.d.Clock(),
	})
	return local, err
}

// adoptPrepared promotes the local record to the committed checkpoint when the
// server committed exactly the candidate this runner pushed: same home
// generation, same version and manifest hash (the hash covers every entry),
// and the local tree is still that candidate. It reports false otherwise.
func (s *Syncer) adoptPrepared(ctx context.Context, local string, st runneragent.CopyState, tgt runneragent.PullTarget) (bool, error) {
	p := st.Prepared
	if p == nil || st.HomeGeneration != tgt.HomeGeneration ||
		p.Ref.Version != tgt.CheckpointVersion || p.Ref.ManifestHash != tgt.ManifestHash {
		return false, nil
	}
	base := runner.NewManifest(p.Ref.Version, p.Entries)
	if base.Hash != p.Ref.ManifestHash {
		return false, nil
	}
	have, err := checkpoint.ScanEntries(ctx, local)
	if err != nil || !runner.SameEntries(have, p.Entries) {
		return false, nil // edited since the push: verify and restore instead
	}
	st.CheckpointVersion, st.ManifestHash, st.Base = base.Version, base.Hash, base
	st.Dirty, st.Prepared, st.UpdatedAt = false, nil, s.d.Clock()
	return true, s.d.Copies.Put(ctx, st)
}

func (s *Syncer) apply(ctx context.Context, token, local string, changed []runner.ManifestEntry, deleted []string) error {
	sort.Sort(sort.Reverse(sort.StringSlice(deleted))) // children before parents
	for _, p := range deleted {
		if err := os.RemoveAll(filepath.Join(local, filepath.FromSlash(p))); err != nil {
			return err
		}
	}
	var dirs []runner.ManifestEntry
	for _, e := range changed { // sorted by path: parents first
		p := filepath.Join(local, filepath.FromSlash(e.Path))
		mode := os.FileMode(e.Mode)
		if info, err := os.Lstat(p); err == nil && (info.IsDir() != mode.IsDir() || info.Mode()&os.ModeSymlink != 0 || mode&os.ModeSymlink != 0) {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		switch {
		case mode&os.ModeSymlink != 0:
			if err := os.Symlink(e.Symlink, p); err != nil {
				return err
			}
		case mode.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			dirs = append(dirs, e)
		default:
			if err := s.fetchFile(ctx, token, e, p); err != nil {
				return err
			}
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- { // directory modes last, deepest first
		if err := os.Chmod(filepath.Join(local, filepath.FromSlash(dirs[i].Path)), os.FileMode(dirs[i].Mode).Perm()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) fetchFile(ctx context.Context, token string, e runner.ManifestEntry, dst string) error {
	rc, err := s.d.Transfer.FetchFile(ctx, token, e.Path)
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pull-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), rc); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("%w: %s content hash mismatch", ErrPullVerifyFailed, e.Path)
	}
	if err := os.Chmod(tmp.Name(), os.FileMode(e.Mode).Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// MarkDirty records that a step is about to mutate the copy.
func (s *Syncer) MarkDirty(ctx context.Context, ws runneragent.WorkspaceJob) error {
	_, tgt, err := s.scope(ws)
	if err != nil {
		return err
	}
	st, ok, err := s.d.Copies.Get(ctx, tgt.RunID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("runner workspace: no local copy for run %s", tgt.RunID)
	}
	st.Dirty, st.UpdatedAt = true, s.d.Clock()
	return s.d.Copies.Put(ctx, st)
}

// Push sends the copy's changes and deletions since its pinned base (one
// checkpoint.WriteDelta stream carrying DeltaControl{Deleted, Expected}) in
// parts of ws.PartBytes, then commits the upload. The server only PREPARES a
// candidate: Push records that exact receipt with its base and claim identity
// and keeps the copy dirty on the pinned base. It never records the candidate
// as the committed checkpoint. On any error nothing is recorded and the copy
// stays dirty, so the next pull restores the committed checkpoint.
func (s *Syncer) Push(ctx context.Context, ws runneragent.WorkspaceJob) (runner.CheckpointRef, error) {
	local, tgt, err := s.scope(ws)
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	st, ok, err := s.d.Copies.Get(ctx, tgt.RunID)
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	if !ok {
		return runner.CheckpointRef{}, fmt.Errorf("runner workspace: no local copy for run %s", tgt.RunID)
	}
	if st.CheckpointVersion != ws.CheckpointVersion || st.ManifestHash != ws.ManifestHash {
		return runner.CheckpointRef{}, fmt.Errorf("%w: copy is on v%d, job is pinned to %s", ErrStaleBase, st.CheckpointVersion, ws.BaseRef)
	}
	now, err := checkpoint.ScanEntries(ctx, local)
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	changed, deleted := runner.DiffManifests(st.Base.Entries, now)
	paths := make([]string, 0, len(changed))
	for _, e := range changed {
		paths = append(paths, e.Path)
	}
	var delta bytes.Buffer
	if err := checkpoint.WriteDelta(&delta, local, paths, runner.DeltaControl{Deleted: deleted, Expected: now}); err != nil {
		return runner.CheckpointRef{}, err
	}
	part := int(ws.PartBytes)
	if part < minPartBytes {
		part = minPartBytes
	}
	data := delta.Bytes()
	n := 0
	for off := 0; off < len(data); off += part {
		end := off + part
		if end > len(data) {
			end = len(data)
		}
		if err := s.putPart(ctx, ws.PushToken, n, data[off:end]); err != nil {
			return runner.CheckpointRef{}, err
		}
		n++
	}
	sum := sha256.Sum256(data)
	ref, err := s.d.Transfer.CommitPush(ctx, ws.PushToken, transfer.PushCommit{Parts: n, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return runner.CheckpointRef{}, err
	}
	if ref.RunID != tgt.RunID || ref.Version <= ws.CheckpointVersion || runner.NewManifest(ref.Version, now).Hash != ref.ManifestHash {
		return runner.CheckpointRef{}, fmt.Errorf("%w: got %s", ErrReceiptMismatch, ref)
	}
	st.Dirty = true
	st.Prepared = &runneragent.PreparedCopy{Ref: ref, BaseRef: ws.BaseRef, FenceKey: ws.FenceKey,
		FenceGeneration: ws.FenceGeneration, FenceToken: ws.FenceToken, Entries: now}
	st.UpdatedAt = s.d.Clock()
	if err := s.d.Copies.Put(ctx, st); err != nil {
		return runner.CheckpointRef{}, err
	}
	return ref, nil
}

// putPart retries a part on error (a part whose hash the server rejects is
// retried, design §7); the third failure fails the push.
func (s *Syncer) putPart(ctx context.Context, token string, i int, b []byte) error {
	var err error
	for attempt := 0; attempt < partAttempts; attempt++ {
		if err = s.d.Transfer.PutPart(ctx, token, i, b); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("runner workspace: push part %d: %w", i, err)
}
