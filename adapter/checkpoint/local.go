package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// Layout under dir (production: <cfg.Runners.CheckpointDir>/local):
//
//	<runID>/v<version:020>-<manifestHash>/manifest.json   committed or prepared copy
//	<runID>/v<version:020>-<manifestHash>/tree/...        the home's files
//	<runID>/.staging-*                                    in-progress copy (never restored)
//
// A version directory appears only by an atomic rename of a fully written and
// fsynced staging directory, so a crash leaves either nothing or a complete
// copy. Which version is the recovery base is decided by the database
// (workspace_homes.local_checkpoint), never by directory listing.
const (
	localTreeDir       = "tree"
	localManifestFile  = "manifest.json"
	localStagingPrefix = ".staging-"
)

// LocalStore implements runner.LocalCheckpointStore with plain copies on the
// server's disk for NON-PORTABLE homes (RR-17 decision B). Unlike Store it does
// no tar/gzip and no portable-Git materialization: these copies never leave
// the built-in runner.
type LocalStore struct {
	dir    string
	create func(path string, perm os.FileMode) (io.WriteCloser, error)
}

type LocalOption func(*LocalStore)

// WithLocalFileCreator replaces file creation (tests inject disk-full).
func WithLocalFileCreator(fn func(path string, perm os.FileMode) (io.WriteCloser, error)) LocalOption {
	return func(s *LocalStore) { s.create = fn }
}

func NewLocalStore(dir string, opts ...LocalOption) *LocalStore {
	s := &LocalStore{dir: dir, create: createSyncedFile}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ runner.LocalCheckpointStore = (*LocalStore)(nil)

type syncedFile struct{ *os.File }

func (f syncedFile) Close() error {
	if err := f.File.Sync(); err != nil {
		_ = f.File.Close()
		return err
	}
	return f.File.Close()
}

func createSyncedFile(path string, perm os.FileMode) (io.WriteCloser, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	return syncedFile{f}, nil
}

func (s *LocalStore) runDir(runID uuid.UUID) string { return filepath.Join(s.dir, runID.String()) }

func localVersionDir(ref runner.LocalCheckpointRef) string {
	return fmt.Sprintf("v%020d-%s", ref.Version, ref.ManifestHash)
}

func (s *LocalStore) StageLocal(ctx context.Context, runID uuid.UUID, srcDir string, version int64) (runner.LocalCheckpointRef, error) {
	if runID == uuid.Nil || version < 1 {
		return runner.LocalCheckpointRef{}, fmt.Errorf("checkpoint: local stage of run %s version %d: invalid identity", runID, version)
	}
	runDir := s.runDir(runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return runner.LocalCheckpointRef{}, fmt.Errorf("checkpoint: local stage of run %s: %w", runID, err)
	}
	staging, err := os.MkdirTemp(runDir, localStagingPrefix)
	if err != nil {
		return runner.LocalCheckpointRef{}, fmt.Errorf("checkpoint: local stage of run %s: %w", runID, err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	entries, err := copyTree(ctx, srcDir, filepath.Join(staging, localTreeDir), s.create)
	if err != nil {
		return runner.LocalCheckpointRef{}, fmt.Errorf("checkpoint: local copy of run %s: %w", runID, err)
	}
	m := runner.NewManifest(version, entries)
	if err := writeLocalManifest(filepath.Join(staging, localManifestFile), m, s.create); err != nil {
		return runner.LocalCheckpointRef{}, fmt.Errorf("checkpoint: local manifest of run %s: %w", runID, err)
	}
	if err := syncDir(staging); err != nil {
		return runner.LocalCheckpointRef{}, err
	}
	ref := runner.LocalCheckpointRef{RunID: runID, Version: version, ManifestHash: m.Hash}
	final := filepath.Join(runDir, localVersionDir(ref))
	if _, err := os.Stat(final); err == nil {
		return ref, nil // identical bytes at this version already prepared: idempotent retry
	}
	if err := os.Rename(staging, final); err != nil {
		return runner.LocalCheckpointRef{}, fmt.Errorf("checkpoint: publish local copy of run %s: %w", runID, err)
	}
	published = true
	if err := syncDir(runDir); err != nil {
		return runner.LocalCheckpointRef{}, err
	}
	return ref, nil
}

func (s *LocalStore) RestoreLocal(ctx context.Context, ref runner.LocalCheckpointRef, dstDir string) error {
	if ref.IsZero() {
		return fmt.Errorf("checkpoint: restore of an empty local ref: %w", runner.ErrCheckpointNotFound)
	}
	versionDir := filepath.Join(s.runDir(ref.RunID), localVersionDir(ref))
	m, err := readLocalManifest(filepath.Join(versionDir, localManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checkpoint: local %s: %w", ref, runner.ErrCheckpointNotFound)
	}
	if err != nil {
		return fmt.Errorf("checkpoint: local %s: %w: %w", ref, runner.ErrCorruptCheckpoint, err)
	}
	if m.Version != ref.Version || m.Hash != ref.ManifestHash || m.Verify() != nil {
		return fmt.Errorf("checkpoint: local %s: manifest mismatch: %w", ref, runner.ErrCorruptCheckpoint)
	}
	parent := filepath.Dir(dstDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(dstDir)+".restore-")
	if err != nil {
		return err
	}
	swapped := false
	defer func() {
		if !swapped {
			_ = os.RemoveAll(staging)
		}
	}()
	entries, err := copyTree(ctx, filepath.Join(versionDir, localTreeDir), staging, s.create)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checkpoint: local %s: tree missing: %w", ref, runner.ErrCorruptCheckpoint)
	}
	if err != nil {
		return fmt.Errorf("checkpoint: restore local %s: %w", ref, err)
	}
	if got := runner.NewManifest(ref.Version, entries); got.Hash != ref.ManifestHash {
		return fmt.Errorf("checkpoint: local %s: restored bytes differ from the manifest: %w", ref, runner.ErrCorruptCheckpoint)
	}
	// Swap at the ownership boundary: the caller holds the run's resource claim
	// and the old writer is stopped (P3 T10), so nothing writes dstDir now.
	old := ""
	if _, err := os.Lstat(dstDir); err == nil {
		old = staging + ".old"
		if err := os.Rename(dstDir, old); err != nil {
			return fmt.Errorf("checkpoint: restore local %s: %w", ref, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(staging, dstDir); err != nil {
		if old != "" {
			_ = os.Rename(old, dstDir)
		}
		return fmt.Errorf("checkpoint: restore local %s: %w", ref, err)
	}
	swapped = true
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return syncDir(parent)
}

func (s *LocalStore) PruneLocal(_ context.Context, runID uuid.UUID, keep runner.LocalCheckpointRef) error {
	if !keep.IsZero() && keep.RunID != runID {
		return fmt.Errorf("checkpoint: prune run %s keeping %s: run mismatch", runID, keep)
	}
	runDir := s.runDir(runID)
	entries, err := os.ReadDir(runDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	keepName := ""
	if !keep.IsZero() {
		keepName = localVersionDir(keep)
	}
	var errs []error
	for _, e := range entries {
		if e.Name() == keepName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(runDir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *LocalStore) Delete(_ context.Context, runID uuid.UUID) error {
	return os.RemoveAll(s.runDir(runID))
}

// copyTree copies src into dst without following symlinks, fsyncing every file
// and directory, and returns the manifest entries of the regular files and
// symlinks it copied. Directories keep their permission bits plus owner rwx
// (so retention can always delete them); file modes are exact.
func copyTree(ctx context.Context, src, dst string, create func(string, os.FileMode) (io.WriteCloser, error)) ([]runner.ManifestEntry, error) {
	var entries []runner.ManifestEntry
	var dirs []string
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(target, mode.Perm()|0o700); err != nil {
				return err
			}
			dirs = append(dirs, target)
			return nil
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			e, err := runner.EntryFromInfo(slashRel, info, "", link)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			entries = append(entries, e)
			return nil
		case mode.IsRegular():
			sum, err := copyRegular(path, target, mode.Perm(), create)
			if err != nil {
				return err
			}
			e, err := runner.EntryFromInfo(slashRel, info, sum, "")
			if err != nil {
				return err
			}
			entries = append(entries, e)
			return nil
		default:
			return fmt.Errorf("%s: %w", slashRel, runner.ErrUnsupportedFileType)
		}
	})
	if err != nil {
		return nil, err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(dirs[i]); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func copyRegular(src, dst string, perm fs.FileMode, create func(string, os.FileMode) (io.WriteCloser, error)) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := create(dst, perm)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(dst, perm); err != nil { // creation is umask-filtered
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeLocalManifest(path string, m runner.Manifest, create func(string, os.FileMode) (io.WriteCloser, error)) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	w, err := create(path, 0o600)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func readLocalManifest(path string) (runner.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return runner.Manifest{}, err
	}
	var m runner.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return runner.Manifest{}, err
	}
	return m, nil
}
