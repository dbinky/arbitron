package git

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestArea returns a WorkingArea rooted at a fresh workspace root together
// with that root. Skips only when the host has no git binary.
func newTestArea(t *testing.T) (*WorkingArea, string) {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	a, err := NewWorkingArea(root, WithRunner(NewRunner(RunnerConfig{StateDir: t.TempDir()})))
	require.NoError(t, err)
	return a, a.root
}

// mkdirIn creates <root>/<rel> and returns it (symlink-resolved like the
// adapter's own paths).
func mkdirIn(t *testing.T, root, rel string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(p, 0o755))
	resolved, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return resolved
}

// writeFile writes content at dir/rel (creating parents) with mode perm.
func writeFile(t *testing.T, dir, rel, content string, perm os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), perm))
	require.NoError(t, os.Chmod(p, perm))
}

// contentOf renders a directory as path -> description, EXCLUDING every .git
// entry: "file:<0644|0755>:<bytes>", "link:<target>", "dir" (empty dirs only).
// Two directories with equal contentOf hold identical task content.
func contentOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if strings.EqualFold(d.Name(), ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		require.NoError(t, ierr)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, lerr := os.Readlink(p)
			require.NoError(t, lerr)
			out[rel] = "link:" + target
		case d.IsDir():
			entries, rerr := os.ReadDir(p)
			require.NoError(t, rerr)
			if len(entries) == 0 {
				out[rel] = "dir"
			}
		default:
			raw, rerr := os.ReadFile(p)
			require.NoError(t, rerr)
			mode := "0644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "0755"
			}
			out[rel] = "file:" + mode + ":" + string(raw)
		}
		return nil
	}))
	return out
}

// author runs the HOST git as a workflow author would (own identity, no
// global config so a developer's signing setup cannot break the test).
func author(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=Author", "-c", "user.email=author@example.com",
		"-c", "commit.gpgSign=false", "-c", "init.defaultBranch=main", "-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	require.NoError(t, cmd.Run(), "git %v: %s", args, errb.String())
	return strings.TrimSpace(out.String())
}

// authorRepo initialises a repository at dir with one commit of files.
func authorRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	author(t, dir, "init", "-q")
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeFile(t, dir, k, files[k], 0o644)
	}
	author(t, dir, "add", "-A")
	author(t, dir, "commit", "-q", "-m", "initial")
}

// lsTree lists a commit's paths (recursively) in the area's backing store.
func lsTree(t *testing.T, a *WorkingArea, handle, rev string) []string {
	t.Helper()
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	res, err := a.git.Run(t.Context(), Command{Args: []string{"ls-tree", "-r", "-z", "--name-only", rev}, GitDir: backing})
	require.NoError(t, err)
	var paths []string
	for _, p := range strings.Split(res.Stdout, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// gitOut runs a git command against the area's backing store.
func gitOut(t *testing.T, a *WorkingArea, handle string, args ...string) string {
	t.Helper()
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	res, err := a.git.Run(t.Context(), Command{Args: args, GitDir: backing})
	require.NoError(t, err)
	return strings.TrimSpace(res.Stdout)
}

// blobAt returns the exact bytes of rev:path in the area's backing store.
func blobAt(t *testing.T, a *WorkingArea, handle, rev, path string) string {
	t.Helper()
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	res, err := a.git.Run(t.Context(), Command{Args: []string{"cat-file", "blob", rev + ":" + path}, GitDir: backing})
	require.NoError(t, err)
	return res.Stdout
}
