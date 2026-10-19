package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

var (
	_ engine.GatherPreflighter = (*WorkingArea)(nil)
	_ engine.SnapshotInventory = (*WorkingArea)(nil)
)

// PreflightGather verifies, before any task of a results_and_files group
// starts, that the gather can work: git is available to this server (checked
// at use, never cached), parentDir is an existing directory, and the optional
// repository hint repoRel (slash-separated, relative to parentDir; "" = none)
// resolves — after symlinks — to a directory INSIDE the parent's area that is
// not a git repository locked by another git process. A plain directory hint
// is accepted: the hint never limits what is captured or gathered.
func (a *WorkingArea) PreflightGather(ctx context.Context, parentDir, repoRel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := a.git.Resolve(); err != nil {
		return fmt.Errorf("git working area: gatherMode results_and_files gathers files with git: %w", err)
	}
	parent, err := resolveDir(parentDir)
	if err != nil {
		return fmt.Errorf("git working area: the parent's working area cannot be gathered into: %w", err)
	}
	if repoRel == "" {
		return nil
	}
	if filepath.IsAbs(repoRel) || strings.HasPrefix(repoRel, `\`) {
		return fmt.Errorf("%w: %q must be relative to the parent's working area", engine.ErrGroupRepoPathInvalid, repoRel)
	}
	for _, seg := range strings.Split(filepath.ToSlash(repoRel), "/") {
		if seg == ".." {
			return fmt.Errorf("%w: %q climbs out of the parent's working area", engine.ErrGroupRepoPathInvalid, repoRel)
		}
	}
	target, err := filepath.EvalSymlinks(filepath.Join(parent, filepath.FromSlash(repoRel)))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %q does not exist in the parent's working area %s", engine.ErrGroupRepoPathInvalid, repoRel, parent)
	}
	if err != nil {
		return fmt.Errorf("%w: %q: %v", engine.ErrGroupRepoPathInvalid, repoRel, err)
	}
	rel, err := filepath.Rel(parent, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q resolves to %s, outside the parent's working area %s", engine.ErrGroupRepoPathInvalid, repoRel, target, parent)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", engine.ErrGroupRepoPathInvalid, repoRel, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", engine.ErrGroupRepoPathInvalid, repoRel)
	}
	lock := filepath.Join(target, ".git", "index.lock")
	if _, err := os.Stat(lock); err == nil {
		return fmt.Errorf("git working area: repoPath %q: %w; finish or abort the git operation running there, or remove the stale %s",
			repoRel, ErrRepositoryLocked, lock)
	}
	return nil
}

// ListSnapshots lists every group-start snapshot this area still holds: the
// "<groupRunID>.git" backing repositories under root/.group-backing. Staging
// areas, the runner's private state and anything that is not a UUID-named
// directory are ignored. Sorted by handle.
func (a *WorkingArea) ListSnapshots(ctx context.Context) ([]engine.SnapshotInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(a.backing)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("git working area: list snapshots: %w", err)
	}
	var out []engine.SnapshotInfo
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasSuffix(name, ".git") {
			continue
		}
		id, err := uuid.Parse(strings.TrimSuffix(name, ".git"))
		if err != nil || id == uuid.Nil {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue // released while listing
		}
		if err != nil {
			return nil, fmt.Errorf("git working area: list snapshots: %w", err)
		}
		out = append(out, engine.SnapshotInfo{GroupRunID: id, Handle: id.String(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

// HasSnapshot reports whether handle's backing repository still exists. A
// handle that is not a group-run id is an error (it would be used in a path).
func (a *WorkingArea) HasSnapshot(ctx context.Context, handle string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dir, err := a.BackingDir(handle)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("git working area: inspect snapshot %s: %w", handle, err)
	}
	return info.IsDir(), nil
}
