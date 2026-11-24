// adapter/checkpoint/git.go
package checkpoint

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/runner"
)

type gitFunc = func(ctx context.Context, args ...string) ([]byte, error)

// noNetwork is prepended to every git invocation: no transport may run, so a
// promisor lazy fetch or credential prompt cannot happen even on a git that
// predates GIT_NO_LAZY_FETCH.
var noNetwork = []string{"-c", "protocol.allow=never", "-c", "credential.helper=", "-c", "core.fsmonitor=false"}

// scrubbedGitVars are inherited variables that would redirect git away from
// the directories this package names explicitly.
var scrubbedGitVars = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS"}

// gitEnv returns base without redirecting variables, plus lazy-fetch,
// prompt and system-config suppression.
func gitEnv(base []string) []string {
	out := make([]string, 0, len(base)+5)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, v := range scrubbedGitVars {
			if name == v {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_ASKPASS=", "SSH_ASKPASS=")
}

func runGit(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return out, nil
}

func gitAt(gitDir string, args ...string) []string {
	return append(append(append([]string{}, noNetwork...), "--git-dir", gitDir), args...)
}

// MaterializePortableGit writes a self-contained `.git` directory for the
// repository whose work tree is srcDir into dstDir/.git (replacing any there).
// It never writes beneath srcDir or its git directories: object closure is
// computed and packed in a private copy that temporarily borrows the source
// objects as an alternate, then the alternate is removed and the closure is
// re-verified with lazy fetch disabled. Required objects that are absent
// locally fail with ErrUnsupportedGit; for a partial clone the error also wraps
// ErrPromisorObjectsMissing. P4 T11 reuses this helper for resolution copies.
func MaterializePortableGit(ctx context.Context, srcDir, dstDir string) error {
	return materializePortableGit(ctx, runGit, srcDir, dstDir)
}

func materializePortableGit(ctx context.Context, git gitFunc, srcDir, dstDir string) error {
	srcAbs, err := filepath.Abs(srcDir)
	if err != nil {
		return err
	}
	dstAbs, err := filepath.Abs(dstDir)
	if err != nil {
		return err
	}
	gitdir, common, err := resolveGitDirs(filepath.Join(srcAbs, ".git"))
	if err != nil {
		return fmt.Errorf("%w: %v", runner.ErrUnsupportedGit, err)
	}
	if dstAbs == srcAbs || within(gitdir, dstAbs) || within(common, dstAbs) || within(filepath.Join(srcAbs, ".git"), dstAbs) {
		return fmt.Errorf("%w: destination %s overlaps the source repository", runner.ErrUnsupportedGit, dstDir)
	}
	if err := os.MkdirAll(dstAbs, 0o700); err != nil {
		return err
	}
	work, err := os.MkdirTemp(dstAbs, ".git-materialize-")
	if err != nil {
		return err
	}
	defer forceRemoveAll(work)
	out := filepath.Join(work, ".git")
	if err := copyGitMeta(common, out, map[string]bool{"objects": true, "worktrees": true}); err != nil {
		return fmt.Errorf("%w: copy common dir: %v", runner.ErrUnsupportedGit, err)
	}
	if gitdir != common {
		if err := copyGitMeta(gitdir, out, map[string]bool{"commondir": true, "gitdir": true, "locked": true}); err != nil {
			return fmt.Errorf("%w: copy worktree dir: %v", runner.ErrUnsupportedGit, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(out, "objects", "pack"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(out, "objects", "info"), 0o755); err != nil {
		return err
	}
	alternates := filepath.Join(out, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(common, "objects")+"\n"), 0o600); err != nil {
		return err
	}
	cfg := filepath.Join(out, "config")
	if _, err := git(ctx, append(append([]string{}, noNetwork...), "config", "--file", cfg, "core.bare", "false")...); err != nil {
		return fmt.Errorf("%w: %v", runner.ErrUnsupportedGit, err)
	}
	// Exit status 5 (key not set) is expected for most repositories.
	_, _ = git(ctx, append(append([]string{}, noNetwork...), "config", "--file", cfg, "--unset-all", "core.worktree")...)

	extra, err := pseudoHeadRoots(out)
	if err != nil {
		return fmt.Errorf("%w: %v", runner.ErrUnsupportedGit, err)
	}
	if missing, err := missingObjects(ctx, git, out, extra); err != nil {
		return fmt.Errorf("%w: closure: %v", runner.ErrUnsupportedGit, err)
	} else if len(missing) > 0 {
		if isPartialClone(ctx, git, out, common) {
			return fmt.Errorf("%w: %w: %d required objects are absent locally (first %s); no network fetch is attempted",
				runner.ErrUnsupportedGit, runner.ErrPromisorObjectsMissing, len(missing), missing[0])
		}
		return fmt.Errorf("%w: repository is missing %d required objects (first %s)", runner.ErrUnsupportedGit, len(missing), missing[0])
	}
	// Pin pseudo-heads with temporary refs so --all packs their closure too.
	for i, oid := range extra {
		if _, err := git(ctx, gitAt(out, "update-ref", fmt.Sprintf("refs/arbitron-materialize/root-%d", i), oid)...); err != nil {
			return fmt.Errorf("%w: %v", runner.ErrUnsupportedGit, err)
		}
	}
	if _, err := git(ctx, gitAt(out, "pack-objects", "-q", "--all", "--reflog", "--indexed-objects",
		filepath.Join(out, "objects", "pack", "pack"))...); err != nil {
		return fmt.Errorf("%w: pack closure: %v", runner.ErrUnsupportedGit, err)
	}
	for i := range extra {
		if _, err := git(ctx, gitAt(out, "update-ref", "-d", fmt.Sprintf("refs/arbitron-materialize/root-%d", i))...); err != nil {
			return fmt.Errorf("%w: %v", runner.ErrUnsupportedGit, err)
		}
	}
	_ = os.RemoveAll(filepath.Join(out, "refs", "arbitron-materialize"))
	if err := os.Remove(alternates); err != nil {
		return err
	}
	if missing, err := missingObjects(ctx, git, out, extra); err != nil || len(missing) > 0 {
		return fmt.Errorf("%w: materialized copy is not self-contained (missing %v, %v)", runner.ErrUnsupportedGit, missing, err)
	}
	final := filepath.Join(dstAbs, ".git")
	forceRemoveAll(final)
	return os.Rename(out, final)
}

// resolveGitDirs returns the repository's own git dir and its common dir.
func resolveGitDirs(dotGit string) (gitdir, common string, err error) {
	fi, err := os.Lstat(dotGit)
	if err != nil {
		return "", "", err
	}
	switch {
	case fi.IsDir():
		if gitdir, err = filepath.EvalSymlinks(dotGit); err != nil {
			return "", "", err
		}
	case fi.Mode().IsRegular():
		if gitdir, err = readGitFile(dotGit); err != nil {
			return "", "", err
		}
	default:
		return "", "", fmt.Errorf("%s is neither a directory nor a gitdir file", dotGit)
	}
	common = gitdir
	if b, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		if common, err = filepath.EvalSymlinks(filepath.Clean(c)); err != nil {
			return "", "", err
		}
	}
	return gitdir, common, nil
}

// needsMaterialize reports whether a `.git` directory borrows objects
// (alternates) or is a partial clone, so a verbatim copy would not be portable.
func (s *Store) needsMaterialize(ctx context.Context, dotGit string) (bool, error) {
	gitdir, common, err := resolveGitDirs(dotGit)
	if err != nil {
		return false, fmt.Errorf("%w: %v", runner.ErrUnsupportedGit, err)
	}
	if gitdir != common {
		return true, nil
	}
	if _, err := os.Stat(filepath.Join(common, "objects", "info", "alternates")); err == nil {
		return true, nil
	}
	return isPartialClone(ctx, s.git, gitdir, common), nil
}

func isPartialClone(ctx context.Context, git gitFunc, gitDir, common string) bool {
	if ps, _ := filepath.Glob(filepath.Join(common, "objects", "pack", "*.promisor")); len(ps) > 0 {
		return true
	}
	out, _ := git(ctx, gitAt(gitDir, "config", "--get-regexp", `^(extensions\.partialclone|remote\..*\.promisor)$`)...)
	return strings.TrimSpace(string(out)) != ""
}

// pseudoHeadRoots lists object IDs named by in-progress operation heads that
// --all does not reach (merge/cherry-pick/revert/rebase state).
func pseudoHeadRoots(gitDir string) ([]string, error) {
	var oids []string
	for _, name := range []string{"ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "AUTO_MERGE"} {
		f, err := os.Open(filepath.Join(gitDir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) > 0 && isHexOID(fields[0]) {
				oids = append(oids, fields[0])
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return oids, nil
}

func isHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// missingObjects lists required objects (refs, reflogs, HEAD, every index
// stage and the pseudo-heads) that the repository cannot read locally.
func missingObjects(ctx context.Context, git gitFunc, gitDir string, extra []string) ([]string, error) {
	args := gitAt(gitDir, "rev-list", "--objects", "--missing=print", "--all", "--reflog", "--indexed-objects")
	out, err := git(ctx, append(args, extra...)...)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "?") {
			missing = append(missing, strings.TrimPrefix(line, "?"))
		}
	}
	return missing, nil
}

// copyGitMeta copies a git directory's metadata (refs, logs, config, index,
// HEADs, info, hooks…) skipping top-level names in skipTop, lock files and
// runtime sockets/fifos.
func copyGitMeta(src, dst string, skipTop map[string]bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if skipTop[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			return os.MkdirAll(target, mode.Perm()|0o700)
		case mode.IsRegular():
			return copyGitFile(p, target, mode.Perm())
		case mode&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return os.Symlink(l, target)
		default:
			return nil // sockets/fifos (fsmonitor) are runtime artefacts
		}
	})
}

func copyGitFile(src, dst string, perm fs.FileMode) error {
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}
