package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
)

// Git file modes used in trees built by this package.
const (
	modeFile    = "100644"
	modeExec    = "100755"
	modeSymlink = "120000"
)

// fileEntry is one regular file or symlink of a scanned area. Rel is
// slash-separated and relative to the area root.
type fileEntry struct {
	Rel  string
	Abs  string
	Mode string
	Link string // symlink target (Mode == modeSymlink)
}

// gitEntry is one author repository metadata entry (".git" directory or
// ".git" file) found at any depth. It is NEVER task content.
type gitEntry struct {
	Rel   string
	IsDir bool
}

// areaContent is the content of a working area as git sees it: files and
// symlinks, directories that have no children at all (git cannot store them,
// so they are kept in the manifest), and author metadata locations.
type areaContent struct {
	Permissions map[string]uint32
	Files       []fileEntry
	EmptyDirs   []string
	GitEntries  []gitEntry
}

// isGitMetadataName reports whether a directory entry is repository metadata.
// git itself refuses ".git" path components case-insensitively, so a
// differently-cased ".GIT" can never be content either.
func isGitMetadataName(name string) bool {
	return strings.EqualFold(name, ".git")
}

// scanArea walks root (which must already be symlink-resolved) without
// following symlinks. skip, when non-empty, is an absolute path excluded with
// everything beneath it (the backing store when it sits inside root).
func scanArea(root, skip string) (*areaContent, error) {
	c := &areaContent{Permissions: map[string]uint32{}}
	hasChild := map[string]bool{}
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			info, err := d.Info()
			if err != nil {
				return err
			}
			c.Permissions["."] = uint32(info.Mode().Perm())
			return nil
		}
		if skip != "" && p == skip {
			hasChild[filepath.Dir(p)] = true
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isGitMetadataName(d.Name()) {
			c.GitEntries = append(c.GitEntries, gitEntry{Rel: rel, IsDir: d.IsDir()})
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		hasChild[filepath.Dir(p)] = true
		switch t := d.Type(); {
		case t.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			c.Permissions[rel] = uint32(info.Mode().Perm())
			dirs = append(dirs, p)
		case t.IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			c.Permissions[rel] = uint32(info.Mode().Perm())
			mode := modeFile
			if info.Mode().Perm()&0o111 != 0 {
				mode = modeExec
			}
			c.Files = append(c.Files, fileEntry{Rel: rel, Abs: p, Mode: mode})
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			c.Files = append(c.Files, fileEntry{Rel: rel, Abs: p, Mode: modeSymlink, Link: target})
		default:
			return fmt.Errorf("%w: %q is a %s (only regular files, directories and symlinks can be gathered)",
				engine.ErrUnsupportedSnapshotContent, rel, describeMode(t))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if !hasChild[d] {
			rel, _ := filepath.Rel(root, d)
			c.EmptyDirs = append(c.EmptyDirs, filepath.ToSlash(rel))
		}
	}
	sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Rel < c.Files[j].Rel })
	sort.Strings(c.EmptyDirs)
	sort.Slice(c.GitEntries, func(i, j int) bool { return c.GitEntries[i].Rel < c.GitEntries[j].Rel })
	return c, nil
}

// scanContainedArea is scanArea for content entering the group from an
// untrusted direction (the parent's baseline, a task's output): every symlink
// must satisfy the filesystem adapter's containment policy, so a link to an
// absolute path or out of the area is never recorded, forked or merged.
func scanContainedArea(root, skip string) (*areaContent, error) {
	c, err := scanArea(root, skip)
	if err != nil {
		return nil, err
	}
	for _, f := range c.Files {
		if f.Mode != modeSymlink {
			continue
		}
		if err := filesystem.CheckAreaSymlink(root, root, f.Abs, f.Rel, f.Link); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func describeMode(m fs.FileMode) string {
	switch {
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	default:
		return "special file"
	}
}

// stdinPathSafe reports whether hash-object --stdin-paths can read rel
// verbatim: it splits on LF, strips a trailing CR and C-unquotes a leading '"'.
func stdinPathSafe(rel string) bool {
	return !strings.ContainsAny(rel, "\n\r") && !strings.HasPrefix(rel, `"`)
}

// hashFiles writes every entry's blob into gitDir and returns the object ids
// in entry order, using ONE hash-object process. Symlink targets and files
// whose names cannot travel over --stdin-paths are staged as temporary files
// first. --no-filters stores bytes exactly as they are on disk.
func (a *WorkingArea) hashFiles(ctx context.Context, gitDir, root, tmpRoot string, files []fileEntry) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	tmp, err := os.MkdirTemp(tmpRoot, "blobs-")
	if err != nil {
		return nil, fmt.Errorf("stage blobs: %w", err)
	}
	defer os.RemoveAll(tmp)

	var list bytes.Buffer
	for i, f := range files {
		p := f.Rel
		switch {
		case f.Mode == modeSymlink:
			p = filepath.Join(tmp, fmt.Sprintf("%d", i))
			if err := os.WriteFile(p, []byte(f.Link), 0o600); err != nil {
				return nil, fmt.Errorf("stage symlink %q: %w", f.Rel, err)
			}
		case !stdinPathSafe(f.Rel):
			p = filepath.Join(tmp, fmt.Sprintf("%d", i))
			if err := copyFileContent(f.Abs, p); err != nil {
				return nil, fmt.Errorf("stage file %q: %w", f.Rel, err)
			}
		}
		list.WriteString(p)
		list.WriteByte('\n')
	}
	res, err := a.git.Run(ctx, Command{
		Args:   []string{"hash-object", "-w", "--no-filters", "--stdin-paths"},
		Dir:    root,
		GitDir: gitDir,
		Stdin:  list.Bytes(),
	})
	if err != nil {
		return nil, fmt.Errorf("hash working-area files: %w", err)
	}
	shas := strings.Fields(res.Stdout)
	if len(shas) != len(files) {
		return nil, fmt.Errorf("hash working-area files: got %d object ids for %d files", len(shas), len(files))
	}
	return shas, nil
}

func copyFileContent(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// commitContent records c (scanned from root) as a commit in gitDir whose
// only parent is parent ("" = root commit) and returns the commit id. The
// index is private to this call, so concurrent finalizations never collide.
func (a *WorkingArea) commitContent(ctx context.Context, gitDir, root string, c *areaContent, parent, message string) (string, error) {
	tmpRoot := filepath.Join(gitDir, "arbitron", "tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return "", fmt.Errorf("prepare backing tmp: %w", err)
	}
	shas, err := a.hashFiles(ctx, gitDir, root, tmpRoot, c.Files)
	if err != nil {
		return "", err
	}
	idxDir, err := os.MkdirTemp(tmpRoot, "index-")
	if err != nil {
		return "", fmt.Errorf("prepare index: %w", err)
	}
	defer os.RemoveAll(idxDir)
	indexEnv := []string{"GIT_INDEX_FILE=" + filepath.Join(idxDir, "index")}

	var info bytes.Buffer
	for i, f := range c.Files {
		fmt.Fprintf(&info, "%s %s\t%s\x00", f.Mode, shas[i], f.Rel)
	}
	if _, err := a.git.Run(ctx, Command{
		Args: []string{"update-index", "-z", "--index-info"}, GitDir: gitDir, Env: indexEnv, Stdin: info.Bytes(),
	}); err != nil {
		return "", fmt.Errorf("build tree index: %w", err)
	}
	res, err := a.git.Run(ctx, Command{Args: []string{"write-tree"}, GitDir: gitDir, Env: indexEnv})
	if err != nil {
		return "", fmt.Errorf("write tree: %w", err)
	}
	tree := strings.TrimSpace(res.Stdout)
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	res, err = a.git.Run(ctx, Command{Args: args, GitDir: gitDir})
	if err != nil {
		return "", fmt.Errorf("commit tree: %w", err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// checkoutCommit writes every file of commit into dir using a private index.
// It does not delete files the commit lacks; callers must prepare the directory.
func (a *WorkingArea) checkoutCommit(ctx context.Context, gitDir, tmpRoot, commit, dir string) error {
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return fmt.Errorf("prepare tmp: %w", err)
	}
	idxDir, err := os.MkdirTemp(tmpRoot, "index-")
	if err != nil {
		return fmt.Errorf("prepare index: %w", err)
	}
	defer os.RemoveAll(idxDir)
	indexEnv := []string{"GIT_INDEX_FILE=" + filepath.Join(idxDir, "index")}
	if _, err := a.git.Run(ctx, Command{Args: []string{"read-tree", commit}, GitDir: gitDir, Env: indexEnv}); err != nil {
		return fmt.Errorf("read tree %s: %w", commit, err)
	}
	if _, err := a.git.Run(ctx, Command{
		Args: []string{"checkout-index", "-a", "-f"}, GitDir: gitDir, WorkTree: dir, Dir: dir, Env: indexEnv,
	}); err != nil {
		return fmt.Errorf("check out %s: %w", commit, err)
	}
	return nil
}

// manifest is the side record kept for every commit this package creates:
// what git cannot represent (empty directories) and where author metadata
// lived (baseline snapshots only).
type manifest struct {
	Permissions map[string]uint32 `json:"permissions"`
	Root        string            `json:"root,omitempty"` // area path the commit was scanned from
	EmptyDirs   []string          `json:"emptyDirs"`
	Metadata    []metadataEntry   `json:"metadata,omitempty"`
}

// metadataEntry describes one author ".git" entry of a baseline snapshot and
// how ForkTask reproduces it independently in every task copy.
type metadataEntry struct {
	Path string `json:"path"` // area-relative metadata-tree root (".")
	// Kind is "metadata-tree", an immutable area-relative metadata layout
	// materialized by the shared filesystem helper. No pointer targets the parent.
	Kind string `json:"kind"`
	Copy string `json:"copy"` // immutable tree location relative to the backing dir
}

func manifestPath(gitDir, commit string) string {
	return filepath.Join(gitDir, "arbitron", "manifests", commit+".json")
}

func writeManifest(gitDir, commit string, m manifest) error {
	if m.EmptyDirs == nil {
		m.EmptyDirs = []string{}
	}
	p := manifestPath(gitDir, commit)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// readManifest requires the side record: Git alone cannot reproduce the area's
// ordinary permission bits. Losing this input must never silently change modes.
func readManifest(gitDir, commit string) (manifest, error) {
	raw, err := os.ReadFile(manifestPath(gitDir, commit))
	if err != nil {
		return manifest{}, fmt.Errorf("read working-area manifest for %s: %w", commit, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("corrupt manifest for %s: %w", commit, err)
	}
	if err := validatePermissions(m.Permissions); err != nil {
		return manifest{}, fmt.Errorf("invalid permissions manifest for %s: %w", commit, err)
	}
	return m, nil
}

// createEmptyDirs recreates the manifest's empty directories under dir.
func createEmptyDirs(dir string, rels []string) error {
	for _, rel := range rels {
		if err := validateAreaPath(rel); err != nil {
			return err
		}
		if err := checkAreaAncestors(dir, rel); err != nil {
			return err
		}
		if info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil && !info.IsDir() {
			return fmt.Errorf("empty directory %q is not a directory", rel)
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
			return fmt.Errorf("recreate empty directory %q: %w", rel, err)
		}
	}
	return nil
}

// validateAreaPath accepts only canonical area-relative paths, excluding all
// metadata components. Manifests must not redirect restoration outside the area.
func validateAreaPath(rel string) error {
	native := filepath.FromSlash(rel)
	if !fs.ValidPath(rel) || strings.ContainsRune(rel, 0) ||
		filepath.IsAbs(native) || filepath.ToSlash(filepath.Clean(native)) != rel {
		return fmt.Errorf("invalid working-area manifest path %q", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if isGitMetadataName(part) {
			return fmt.Errorf("metadata path %q is not ordinary area content", rel)
		}
	}
	return nil
}

func validatePermissions(permissions map[string]uint32) error {
	if _, ok := permissions["."]; !ok {
		return fmt.Errorf("permissions must include the working-area root")
	}
	for rel, mode := range permissions {
		if err := validateAreaPath(rel); err != nil {
			return err
		}
		if mode > 0o777 {
			return fmt.Errorf("invalid permission bits %#o for %q", mode, rel)
		}
	}
	return nil
}

// checkAreaAncestors never follows an intermediate symlink. Missing ancestors
// are allowed for empty-directory creation, but existing ones must be directories.
func checkAreaAncestors(root, rel string) error {
	for _, ancestor := range slashAncestors(rel) {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(ancestor)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("manifest path %q has non-directory ancestor %q", rel, ancestor)
		}
	}
	return nil
}

// restoreAreaPermissions restores files first, deepest directories next, and
// root last. All content must have been reproduced before read-only modes apply.
func restoreAreaPermissions(ctx context.Context, root string, permissions map[string]uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePermissions(permissions); err != nil {
		return err
	}
	content, err := scanArea(root, "")
	if err != nil {
		return fmt.Errorf("inspect fork permissions: %w", err)
	}
	if len(content.Permissions) != len(permissions) {
		return fmt.Errorf("incomplete permissions manifest: got %d entries for %d files/directories", len(permissions), len(content.Permissions))
	}
	var files, dirs []string
	for rel := range permissions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := content.Permissions[rel]; !ok {
			return fmt.Errorf("permissions path %q is missing or not an ordinary file/directory", rel)
		}
		if err := checkAreaAncestors(root, rel); err != nil {
			return err
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, rel)
		} else if info.Mode().IsRegular() {
			files = append(files, rel)
		} else {
			return fmt.Errorf("refusing to chmod non-ordinary path %q", rel)
		}
	}
	sort.Strings(files)
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i] == "." {
			return false
		}
		if dirs[j] == "." {
			return true
		}
		return strings.Count(dirs[i], "/") > strings.Count(dirs[j], "/")
	})
	for _, rel := range append(files, dirs...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(root, filepath.FromSlash(rel)), os.FileMode(permissions[rel])); err != nil {
			return fmt.Errorf("restore permissions on %q: %w", rel, err)
		}
	}
	return nil
}

// within reports whether p is root or lies beneath it (both cleaned, absolute).
func within(root, p string) bool {
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// slashAncestors returns every proper ancestor directory of a slash path,
// nearest first ("a/b/c" -> "a/b", "a").
func slashAncestors(rel string) []string {
	var out []string
	for d := path.Dir(rel); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		out = append(out, d)
	}
	return out
}
