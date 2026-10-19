package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/instance"
)

// ErrGroupRepositoryLocked means a snapshot cannot safely capture the author's
// repository while a Git writer holds a lock. Retry after that writer finishes.
var ErrGroupRepositoryLocked = errors.New("author git repository is locked")

// CopyGroupGitMetadata reproduces each repository as a private standalone .git
// directory. Ordinary content must already have been copied to destinationRoot.
// Linked worktrees and submodules are flattened, so neither the snapshot nor a
// later fork retains a dependency on the original gitdir, common dir or objects.
// No Git executable is used and no source metadata is changed.
func CopyGroupGitMetadata(ctx context.Context, sourceRoot, destinationRoot string) error {
	for _, root := range []string{sourceRoot, destinationRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return unsupported(root, "metadata root must be a clean absolute directory")
		}
		info, err := os.Stat(root)
		if err != nil {
			return metadataError(root, err)
		}
		if !info.IsDir() {
			return unsupported(root, "metadata root is not a directory")
		}
	}
	source, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return metadataError(sourceRoot, err)
	}
	destination, err := filepath.EvalSymlinks(destinationRoot)
	if err != nil {
		return metadataError(destinationRoot, err)
	}
	if within(source, destination) || within(destination, source) {
		return unsupported(destinationRoot, "metadata source and destination overlap")
	}
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return metadataError(path, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Name() != ".git" {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if err := copyGroupRepository(ctx, path, target); err != nil {
			return metadataError(path, err)
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}

func metadataError(path string, err error) error {
	return fmt.Errorf("%w: %q: %w", instance.ErrUnsupportedSnapshotContent, path, err)
}

func copyGroupRepository(ctx context.Context, entry, target string) error {
	gitdir, common, err := resolveGroupRepository(entry)
	if err != nil {
		return err
	}
	head, err := readMetadataFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) == "" {
		return fmt.Errorf("empty HEAD")
	}
	for _, dir := range []string{gitdir, common} {
		if _, err := os.Lstat(filepath.Join(dir, "index.lock")); err == nil {
			return fmt.Errorf("%w: %s", ErrGroupRepositoryLocked, filepath.Join(dir, "index.lock"))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// Destination directories may have inherited read-only permission bits from
	// the ordinary copier. Restore those bits even if metadata construction fails.
	parentInfo, err := os.Stat(filepath.Dir(target))
	if err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(target), parentInfo.Mode().Perm()|0o700); err != nil {
		return err
	}
	defer os.Chmod(filepath.Dir(target), parentInfo.Mode().Perm())
	if err := os.Mkdir(target, snapshotDirMode); err != nil {
		return err
	}
	if err := copyMetadataTree(ctx, common, target, true); err != nil {
		return err
	}
	if gitdir != common {
		// A missing linked-worktree index means an empty index, NOT the common
		// worktree's index. Worktree-local HEAD/index/logs then overlay common data.
		if err := os.Remove(filepath.Join(target, "index")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := copyMetadataTree(ctx, gitdir, target, true); err != nil {
			return err
		}
	}
	if err := copyAlternateObjects(ctx, filepath.Join(common, "objects"), filepath.Join(target, "objects"), map[string]bool{}); err != nil {
		return err
	}
	if err := normalizeGroupConfig(target); err != nil {
		return err
	}
	dirInfo, err := os.Stat(gitdir)
	if err != nil {
		return err
	}
	return os.Chmod(target, dirInfo.Mode().Perm())
}

// metadataPath resolves gitdir/commondir paths relative to the file's directory.
// A symlink at the metadata root is rejected; the copier checks every entry
// below it too, so no writable symlink is retained in the destination.
// resolveGroupRepository resolves a `.git` entry to the gitdir that holds its
// per-worktree state and the common dir that holds its objects and refs. An
// in-area `.git` directory is both; a `gitdir:` pointer file (a git worktree,
// Arbitron's normal instance layout) points OUTSIDE the area. The fork copier
// and GroupAreaMeter both resolve through here so they cannot disagree about
// what a fork materializes.
func resolveGroupRepository(entry string) (gitdir, common string, err error) {
	info, err := os.Lstat(entry)
	if err != nil {
		return "", "", err
	}
	gitdir = entry
	switch {
	case info.IsDir():
	case info.Mode().IsRegular():
		b, err := os.ReadFile(entry)
		if err != nil {
			return "", "", err
		}
		text := strings.TrimSpace(string(b))
		if !strings.HasPrefix(text, "gitdir: ") {
			return "", "", fmt.Errorf("malformed gitdir pointer")
		}
		gitdir, err = metadataPath(filepath.Dir(entry), strings.TrimSpace(strings.TrimPrefix(text, "gitdir: ")))
		if err != nil {
			return "", "", err
		}
	default:
		return "", "", fmt.Errorf("unsupported .git entry (%s); replace metadata symlinks with real files", info.Mode())
	}
	common = gitdir
	if b, err := readMetadataFile(filepath.Join(gitdir, "commondir")); err == nil {
		common, err = metadataPath(gitdir, strings.TrimSpace(string(b)))
		if err != nil {
			return "", "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	return gitdir, common, nil
}

// alternateObjectDirs lists, depth-first in copy order, every object directory a
// repository borrows through objects/info/alternates (transitively). HTTP
// alternates and cycles are errors: a fork cannot materialize them.
func alternateObjectDirs(ctx context.Context, objects string, visiting map[string]bool) ([]string, error) {
	real, err := metadataPath(filepath.Dir(objects), filepath.Base(objects))
	if err != nil {
		return nil, err
	}
	if visiting[real] {
		return nil, fmt.Errorf("cyclic object alternates at %s", objects)
	}
	visiting[real] = true
	defer delete(visiting, real)
	if _, err := os.Lstat(filepath.Join(real, "info", "http-alternates")); err == nil {
		return nil, fmt.Errorf("cannot materialize HTTP object alternates at %s", real)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b, err := readMetadataFile(filepath.Join(real, "info", "alternates"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if line == "" {
			continue
		}
		alternate, err := metadataPath(real, line)
		if err != nil {
			return nil, err
		}
		nested, err := alternateObjectDirs(ctx, alternate, visiting)
		if err != nil {
			return nil, err
		}
		dirs = append(append(dirs, alternate), nested...)
	}
	return dirs, nil
}

func metadataPath(base, value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("malformed metadata path %q", value)
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve metadata %s: %w", path, err)
	}
	// Allow platform aliases in ancestors, but not a symlink at the metadata root.
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("metadata path %s is not a real directory", path)
	}
	return real, nil
}

func readMetadataFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("metadata %s is not a regular file", path)
	}
	return os.ReadFile(path)
}

// copyMetadataTree merges independent file bytes, never symlinks or hard links.
// Back-pointers and registrations describe the original layout and are obsolete
// once every working tree has its own standalone repository.
func copyMetadataTree(ctx context.Context, src, dst string, repository bool) error {
	var dirs []dirMode
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		if repository && (rel == "worktrees" || rel == "modules" || rel == "commondir" || rel == "gitdir") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == "objects/info/http-alternates" || slashRel == "info/http-alternates" {
			return fmt.Errorf("cannot materialize HTTP object alternates at %s", path)
		}
		if slashRel == "objects/info/alternates" || slashRel == "info/alternates" {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			return fmt.Errorf("%w: %s", ErrGroupRepositoryLocked, path)
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, snapshotDirMode); err != nil {
				return err
			}
			if err := os.Chmod(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{path: target, perm: info.Mode().Perm()})
			return nil
		case info.Mode().IsRegular():
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return copyFileContent(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("metadata %s has unsupported type %s; materialize metadata symlinks before launching", path, info.Mode().Type())
		}
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].perm); err != nil {
			return err
		}
	}
	return nil
}

// Materialize recursive alternates into the private object store rather than
// retaining an external object dependency.
func copyAlternateObjects(ctx context.Context, objects, target string, visiting map[string]bool) error {
	dirs, err := alternateObjectDirs(ctx, objects, visiting)
	if err != nil {
		return err
	}
	for _, alternate := range dirs {
		if err := mergeAlternateTree(ctx, alternate, target); err != nil {
			return err
		}
	}
	return nil
}

func mergeAlternateTree(ctx context.Context, src, dst string) error {
	// Alternate info files (commit graphs, packs hints, alternates) are optional
	// caches, not objects. Copy only the loose/packed object namespace.
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == "info" {
			continue
		}
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		if !entry.IsDir() {
			return fmt.Errorf("unexpected object entry %s", from)
		}
		if err := os.MkdirAll(to, snapshotDirMode); err != nil {
			return err
		}
		if err := copyMetadataTree(ctx, from, to, false); err != nil {
			return err
		}
	}
	return nil
}

// Normalize the location-dependent config without invoking Git. Includes and
// continuations can conceal external location settings; refuse those forms
// explicitly rather than copying a configuration whose isolation is unproven.
func normalizeGroupConfig(gitdir string) error {
	var configs []string
	hasBare := false
	for _, name := range []string{"config", "config.worktree"} {
		b, err := readMetadataFile(filepath.Join(gitdir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		section := ""
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasSuffix(trimmed, "\\") {
				return fmt.Errorf("unsupported config continuation in %s", name)
			}
			if strings.HasPrefix(trimmed, "[") {
				if !strings.HasSuffix(trimmed, "]") {
					return fmt.Errorf("malformed config section in %s", name)
				}
				section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
				sectionName := strings.FieldsFunc(section, func(r rune) bool { return r == ' ' || r == '\t' || r == '.' })
				if len(sectionName) > 0 && (sectionName[0] == "include" || sectionName[0] == "includeif") {
					return fmt.Errorf("external config includes are unsupported in %s", name)
				}
			}
			key := strings.ToLower(strings.TrimSpace(strings.SplitN(trimmed, "=", 2)[0]))
			if section == "core" && key == "worktree" || section == "extensions" && key == "worktreeconfig" {
				continue
			}
			if section == "core" && key == "bare" {
				line = "\tbare = false"
				hasBare = true
			}
			configs = append(configs, line)
		}
	}
	if len(configs) == 0 {
		return nil
	}
	if !hasBare {
		configs = append(configs, "[core]", "\tbare = false", "")
	}
	if err := os.Remove(filepath.Join(gitdir, "config")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(filepath.Join(gitdir, "config"), []byte(strings.Join(configs, "\n")), 0o600); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(gitdir, "config.worktree")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
