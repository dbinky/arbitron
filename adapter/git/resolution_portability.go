package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// DefaultResolutionMaxBytes bounds one exported resolution copy.
const DefaultResolutionMaxBytes int64 = 2 << 30

var (
	// ErrResolutionPortability marks a refused export/import: foreign or cyclic
	// alternates, symlinks or path escapes, an untrusted source or destination.
	ErrResolutionPortability = errors.New("git working area: resolution staging is not portable")
	// ErrResolutionTooLarge marks a copy over the configured byte bound.
	ErrResolutionTooLarge = errors.New("git working area: resolution copy exceeds the size bound")
)

const portableRootRefs = "refs/arbitron/portable"

// ResolutionResourceRoot is <workspace root>/.runner-resources/resolutions.
// A remote runner uses the same relative path under its own resource root.
func (a *WorkingArea) ResolutionResourceRoot() string {
	return filepath.Join(a.root, ".runner-resources", "resolutions")
}

func (a *WorkingArea) maxResolutionBytes() int64 {
	if a.resolutionMaxBytes > 0 {
		return a.resolutionMaxBytes
	}
	return DefaultResolutionMaxBytes
}

// ExportResolution makes dstDir a STANDALONE copy of a gather staging area:
// working files, permissions, empty dirs and the complete .git state (index
// with every conflict stage, HEAD/refs, MERGE_HEAD/MERGE_MSG/ORIG_HEAD and the
// full object closure, without alternates). The source .git is only read.
// The copy is built beside dstDir and renamed into place; any failure
// publishes nothing.
func (a *WorkingArea) ExportResolution(ctx context.Context, stagingDir, dstDir string) error {
	if a.portableGit == nil {
		return fmt.Errorf("%w: no portable git materializer configured", ErrResolutionPortability)
	}
	src, err := a.trustedStaging(stagingDir)
	if err != nil {
		return err
	}
	dst, err := a.resolutionDst(dstDir)
	if err != nil {
		return err
	}
	if err := a.checkAlternates(filepath.Join(src, ".git", "objects")); err != nil {
		return err
	}
	tmp := dst + ".partial-" + uuid.NewString()
	defer os.RemoveAll(tmp)
	if err := copyResolutionTree(src, tmp, a.maxResolutionBytes(), true); err != nil {
		return err
	}
	if err := sealStaging(tmp); err != nil {
		return err
	}
	if err := pinResolutionRoots(tmp); err != nil {
		return err
	}
	if err := a.portableGit(ctx, src, tmp); err != nil {
		return fmt.Errorf("git working area: export resolution: materialize objects: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(tmp, ".git", filepath.FromSlash(portableRootRefs))); err != nil {
		return err
	}
	// Materialization can expand a small alternate-backed repository substantially.
	var expanded int64
	if err := filepath.WalkDir(tmp, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			expanded += info.Size()
			if expanded > a.maxResolutionBytes() {
				return ErrResolutionTooLarge
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := a.verifyStandalone(ctx, tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// ImportResolution restores a committed standalone copy (srcDir) into the
// trusted gather staging area: objects are added (content-addressed, never
// rewritten), index/HEAD/refs/merge state and the work tree are replaced, and
// the staging repository keeps its own alternates/config and is re-sealed.
func (a *WorkingArea) ImportResolution(ctx context.Context, srcDir, stagingDir string) error {
	staging, err := a.trustedStaging(stagingDir)
	if err != nil {
		return err
	}
	src, err := resolveDir(srcDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrResolutionPortability, err)
	}
	if _, err := os.Lstat(filepath.Join(src, ".git", "objects", "info", "alternates")); err == nil {
		return fmt.Errorf("%w: imported copy still has alternates", ErrResolutionPortability)
	}
	if err := a.verifyStandalone(ctx, src); err != nil {
		return err
	}
	if err := copyResolutionObjects(filepath.Join(src, ".git", "objects"), filepath.Join(staging, ".git", "objects")); err != nil {
		return err
	}
	gitDir := filepath.Join(staging, ".git")
	for _, name := range []string{"MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "ORIG_HEAD"} {
		if err := os.RemoveAll(filepath.Join(gitDir, name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(src, ".git"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch e.Name() {
		case "objects", "config", "hooks", "info", "config.worktree", "commondir":
			continue
		}
		if err := os.RemoveAll(filepath.Join(gitDir, e.Name())); err != nil {
			return err
		}
		if err := copyResolutionTree(filepath.Join(src, ".git", e.Name()), filepath.Join(gitDir, e.Name()), a.maxResolutionBytes(), false); err != nil {
			return err
		}
	}
	if err := replaceWorkTree(src, staging, a.maxResolutionBytes()); err != nil {
		return err
	}
	if err := sealStaging(staging); err != nil {
		return err
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"rev-parse", "--verify", "HEAD"}, Dir: staging}); err != nil {
		return fmt.Errorf("git working area: import resolution: %w", err)
	}
	return nil
}

// trustedStaging accepts only a gather staging work tree of this area.
func (a *WorkingArea) trustedStaging(dir string) (string, error) {
	p, err := resolveDir(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrResolutionPortability, err)
	}
	rel, err := filepath.Rel(a.backing, p)
	if err != nil || strings.Contains(rel, string(filepath.Separator)) || rel == "." || strings.HasPrefix(rel, "..") || !strings.Contains(rel, "-gather-") {
		return "", fmt.Errorf("%w: %s is not a gather staging area", ErrResolutionPortability, dir)
	}
	info, err := os.Lstat(filepath.Join(p, ".git"))
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %s has no staging repository", ErrResolutionPortability, dir)
	}
	return p, nil
}

// resolutionDst accepts only <ResolutionResourceRoot>/<uuid>.
func (a *WorkingArea) resolutionDst(dir string) (string, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", fmt.Errorf("%w: destination %q must be a clean absolute path", ErrResolutionPortability, dir)
	}
	if filepath.Dir(dir) != a.ResolutionResourceRoot() {
		return "", fmt.Errorf("%w: destination %q is outside the resource root", ErrResolutionPortability, dir)
	}
	if _, err := uuid.Parse(filepath.Base(dir)); err != nil {
		return "", fmt.Errorf("%w: destination %q is not named by a resource id", ErrResolutionPortability, dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil || resolved != filepath.Dir(dir) {
		return "", fmt.Errorf("%w: resource root traverses a symlink", ErrResolutionPortability)
	}
	return dir, nil
}

// checkAlternates follows the alternates chain from objectsDir: every entry
// must be a real directory inside this area's backing root, and no store may
// be visited twice (cycle).
func (a *WorkingArea) checkAlternates(objectsDir string) error {
	seen := map[string]bool{}
	var walk func(dir string) error
	walk = func(dir string) error {
		if seen[dir] {
			return fmt.Errorf("%w: alternate cycle through %s", ErrResolutionPortability, dir)
		}
		seen[dir] = true
		f, err := os.Open(filepath.Join(dir, "info", "alternates"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !filepath.IsAbs(line) || filepath.Clean(line) != line {
				return fmt.Errorf("%w: alternate %q is not a clean absolute path", ErrResolutionPortability, line)
			}
			rel, err := filepath.Rel(a.backing, line)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("%w: alternate %q is outside the backing root", ErrResolutionPortability, line)
			}
			info, err := os.Lstat(line)
			if err != nil || !info.IsDir() {
				return fmt.Errorf("%w: alternate %q is not a directory", ErrResolutionPortability, line)
			}
			if err := walk(line); err != nil {
				return err
			}
		}
		return sc.Err()
	}
	return walk(objectsDir)
}

// pinResolutionRoots adds MERGE_HEAD, ORIG_HEAD and every index-entry object
// (all conflict stages) as temporary refs of the COPY, so P3 T2's closure
// (refs + HEAD + index) includes them. Written as files: the objects are not
// in the copy yet, so update-ref would refuse them.
func pinResolutionRoots(dir string) error {
	gitDir := filepath.Join(dir, ".git")
	shas := map[string]bool{}
	for _, name := range []string{"MERGE_HEAD", "ORIG_HEAD"} {
		raw, err := os.ReadFile(filepath.Join(gitDir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, l := range strings.Fields(string(raw)) {
			shas[l] = true
		}
	}
	idx, err := readIndexObjects(filepath.Join(gitDir, "index"))
	if err != nil {
		return err
	}
	for _, s := range idx {
		shas[s] = true
	}
	refDir := filepath.Join(gitDir, filepath.FromSlash(portableRootRefs))
	if err := os.MkdirAll(refDir, 0o700); err != nil {
		return err
	}
	for s := range shas {
		if len(s) != 40 && len(s) != 64 {
			return fmt.Errorf("%w: malformed object id %q", ErrResolutionPortability, s)
		}
		if err := os.WriteFile(filepath.Join(refDir, s), []byte(s+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// verifyStandalone proves dir needs nothing outside itself: HEAD, MERGE_HEAD,
// every index object and connectivity resolve with alternates disabled.
func (a *WorkingArea) verifyStandalone(ctx context.Context, dir string) error {
	noAlt := []string{"GIT_ALTERNATE_OBJECT_DIRECTORIES="}
	run := func(args ...string) (string, error) {
		res, err := a.git.Run(ctx, Command{Args: args, Dir: dir, Env: noAlt})
		return res.Stdout, err
	}
	if _, err := run("rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return fmt.Errorf("git working area: standalone HEAD: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err == nil {
		if _, err := run("cat-file", "-e", "MERGE_HEAD^{commit}"); err != nil {
			return fmt.Errorf("git working area: standalone MERGE_HEAD: %w", err)
		}
	}
	idx, err := readIndexObjects(filepath.Join(dir, ".git", "index"))
	if err != nil {
		return err
	}
	for _, s := range idx {
		if _, err := run("cat-file", "-e", s); err != nil {
			return fmt.Errorf("git working area: standalone index object %s: %w", s, err)
		}
	}
	if _, err := run("fsck", "--connectivity-only", "--no-dangling", "--no-progress"); err != nil {
		return fmt.Errorf("git working area: standalone connectivity: %w", err)
	}
	return nil
}

// readIndexObjects returns every index entry's object ID (all stages), using
// a throwaway git reader rather than parsing the binary index format.
func readIndexObjects(index string) ([]string, error) {
	if _, err := os.Stat(index); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return listIndexObjects(index)
}

// copyResolutionTree copies src to dst preserving modes, empty dirs and work
// tree symlinks. withGit=true copies the .git directory too, except hooks and
// objects/info/alternates; any symlink inside .git is refused. Expanded bytes
// are bounded.
func copyResolutionTree(src, dst string, max int64, withGit bool) error {
	var total int64
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		relSlash := filepath.ToSlash(rel)
		inGit := relSlash == ".git" || strings.HasPrefix(relSlash, ".git/")
		if inGit && !withGit {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relSlash == ".git/hooks" || relSlash == ".git/objects/info/alternates" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			if inGit || isGitDirOf(src, p) {
				return fmt.Errorf("%w: symlink %s inside repository metadata", ErrResolutionPortability, relSlash)
			}
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			total += info.Size()
			if total > max {
				return fmt.Errorf("%w: more than %d bytes", ErrResolutionTooLarge, max)
			}
			return copyRegular(p, target, info.Mode().Perm())
		default:
			return fmt.Errorf("%w: unsupported file type at %s", ErrResolutionPortability, relSlash)
		}
	})
}

func isGitDirOf(root, p string) bool { return filepath.Base(p) == ".git" && filepath.Dir(p) == root }

func copyRegular(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// copyResolutionObjects adds every object file (loose or pack) of src to dst
// when absent; existing content-addressed files are never rewritten.
func copyResolutionObjects(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "info" {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink in imported objects", ErrResolutionPortability)
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		target := filepath.Join(dst, rel)
		if _, err := os.Lstat(target); err == nil {
			return nil
		}
		return copyRegular(p, target, 0o444)
	})
}

// replaceWorkTree makes staging's work tree (everything but .git) equal src's.
func replaceWorkTree(src, staging string, max int64) error {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(staging, e.Name())); err != nil {
			return err
		}
	}
	return copyResolutionTree(src, staging, max, false)
}

// WithPortableGit injects the single portable-object materializer (P3 T2).
func WithPortableGit(fn func(ctx context.Context, stagingDir, dstDir string) error) Option {
	return func(a *WorkingArea) error {
		if fn == nil {
			return errors.New("git working area: portable git materializer is required")
		}
		a.portableGit = fn
		return nil
	}
}

// WithResolutionMaxBytes bounds the expanded bytes of one resolution copy
// (<= 0 restores DefaultResolutionMaxBytes).
func WithResolutionMaxBytes(n int64) Option {
	return func(a *WorkingArea) error { a.resolutionMaxBytes = n; return nil }
}

// listIndexObjects lists the object ids of every entry of an index file by
// running "git ls-files -s" with GIT_INDEX_FILE in an empty scratch repo
// (ls-files -s reads only the index; no objects are needed).
func listIndexObjects(index string) ([]string, error) {
	scratch, err := os.MkdirTemp("", "arbitron-index-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	r := NewRunner(RunnerConfig{StateDir: filepath.Join(scratch, ".state")})
	ctx := context.Background()
	if _, err := r.Run(ctx, Command{Args: []string{"init", "-q", "--template=", scratch}}); err != nil {
		return nil, err
	}
	res, err := r.Run(ctx, Command{Args: []string{"ls-files", "-s"}, Dir: scratch, Env: []string{"GIT_INDEX_FILE=" + index}})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			out = append(out, f[1])
		}
	}
	return out, nil
}
