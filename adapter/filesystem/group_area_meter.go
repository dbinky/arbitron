package filesystem

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
)

// GroupAreaMeter implements engine.GroupAreaMeter: an upper bound on the bytes
// one task fork of a working area materializes. That is every regular file under
// the area (at any depth, without following symlinks; ignored, untracked and
// in-area ".git" content all count), PLUS the repository metadata the fork copier
// pulls from OUTSIDE the area: a `.git` pointer file's external gitdir and common
// dir (a git worktree, Arbitron's normal instance layout, keeps its objects in
// the source clone), and every object directory borrowed through alternates.
// It resolves repositories through the same helpers as CopyGroupGitMetadata, so
// the launch byte budget cannot be passed by an area whose forks copy more.
type GroupAreaMeter struct{}

// AreaBytes returns the bytes one fork of dir would copy.
func (GroupAreaMeter) AreaBytes(ctx context.Context, dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Name() == ".git" && (d.IsDir() || d.Type().IsRegular()) {
			n, err := externalRepositoryBytes(ctx, path, d.IsDir())
			if err != nil {
				return fmt.Errorf("repository %s: %w", path, err)
			}
			total += n
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("filesystem: measure working area %s: %w", dir, err)
	}
	return total, nil
}

// externalRepositoryBytes counts what a fork copies for one `.git` entry beyond
// the area walk itself: for a pointer file, the external gitdir and common dir;
// for either form, every alternate object directory. An in-area `.git`
// directory's own files are already counted by the area walk.
func externalRepositoryBytes(ctx context.Context, entry string, inArea bool) (int64, error) {
	gitdir, common, err := resolveGroupRepository(entry)
	if err != nil {
		return 0, err
	}
	var total int64
	if !inArea {
		dirs := []string{common}
		if gitdir != common {
			dirs = append(dirs, gitdir)
		}
		for _, d := range dirs {
			n, err := treeBytes(ctx, d)
			if err != nil {
				return 0, err
			}
			total += n
		}
	}
	alternates, err := alternateObjectDirs(ctx, filepath.Join(common, "objects"), map[string]bool{})
	if err != nil {
		return 0, err
	}
	for _, a := range alternates {
		n, err := treeBytes(ctx, a)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// treeBytes sums regular-file sizes under dir without following symlinks. It
// over-counts slightly (the copier skips worktrees/, modules/ and info caches),
// which is the safe direction for a budget.
func treeBytes(ctx context.Context, dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
