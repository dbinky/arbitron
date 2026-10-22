//go:build !windows

package filesystem_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func metadataGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// No background auto-gc/maintenance (as adapter/git/runner.go): newer git
	// detaches it after a commit and its objects/maintenance.lock races the
	// snapshot, which rightly refuses a locked repository.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Group Test", "GIT_AUTHOR_EMAIL=group@example.test", "GIT_COMMITTER_NAME=Group Test", "GIT_COMMITTER_EMAIL=group@example.test")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func metadataRepo(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o700))
	metadataGit(t, path, "init", "-b", "main")
	writeFile(t, filepath.Join(path, "tracked"), "base\n", 0o644)
	metadataGit(t, path, "add", ".")
	metadataGit(t, path, "commit", "-m", "base")
}

func assertMetadataIsolation(t *testing.T, root, parent, repoRel string) {
	t.Helper()
	repo := filepath.Join(parent, repoRel)
	writeFile(t, filepath.Join(repo, "tracked"), "staged\n", 0o644)
	metadataGit(t, repo, "add", "tracked")
	writeFile(t, filepath.Join(repo, "tracked"), "dirty\n", 0o644)
	writeFile(t, filepath.Join(repo, "untracked"), "new\n", 0o644)
	beforeStatus := metadataGit(t, repo, "status", "--porcelain")
	beforeHead := metadataGit(t, repo, "rev-parse", "HEAD")
	beforeIndex := metadataGit(t, repo, "show", ":tracked")
	common := metadataGit(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	commonBefore := listTree(t, common)
	gw := newGW(t, root)
	handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	a, b := filepath.Join(root, "task-a"), filepath.Join(root, "task-b")
	require.NoError(t, gw.ForkTask(context.Background(), handle, a))
	require.NoError(t, gw.ForkTask(context.Background(), handle, b))
	for _, area := range []string{a, b} {
		fork := filepath.Join(area, repoRel)
		assert.Equal(t, beforeStatus, metadataGit(t, fork, "status", "--porcelain"))
		assert.Equal(t, beforeHead, metadataGit(t, fork, "rev-parse", "HEAD"))
		assert.Equal(t, beforeIndex, metadataGit(t, fork, "show", ":tracked"))
		assert.Equal(t, metadataGit(t, repo, "symbolic-ref", "HEAD"), metadataGit(t, fork, "symbolic-ref", "HEAD"))
		bytes, err := os.ReadFile(filepath.Join(fork, "tracked"))
		require.NoError(t, err)
		assert.Equal(t, "dirty\n", string(bytes))
	}
	sibling := listTree(t, b)
	source := listTree(t, parent)
	snapshot := listTree(t, handle)
	metadataGit(t, filepath.Join(a, repoRel), "add", ".")
	metadataGit(t, filepath.Join(a, repoRel), "commit", "-m", "task only")
	metadataGit(t, filepath.Join(a, repoRel), "config", "group.task", "a")
	assert.NotEqual(t, beforeHead, metadataGit(t, filepath.Join(a, repoRel), "rev-parse", "HEAD"))
	assert.Equal(t, beforeHead, metadataGit(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, beforeHead, metadataGit(t, filepath.Join(b, repoRel), "rev-parse", "HEAD"))
	assert.Equal(t, source, listTree(t, parent), "task commit/config/index cannot change parent")
	assert.Equal(t, commonBefore, listTree(t, common), "external common refs, objects, config and index stay untouched")
	assert.Equal(t, sibling, listTree(t, b), "sibling metadata and content stay untouched")
	assert.Equal(t, snapshot, listTree(t, handle), "snapshot stays immutable")
}

func TestGroupGitMetadata_SampleLinkedWorktreeIsolation(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	src := filepath.Join(parent, "src")
	metadataRepo(t, src)
	metadataGit(t, src, "worktree", "add", "-b", "mission", filepath.Join(parent, "work"))
	assertMetadataIsolation(t, root, parent, "work")
}

func TestGroupGitMetadata_RepositoryForms(t *testing.T) {
	for _, form := range []string{"clone", "nested", "external-absolute", "external-relative", "submodule-pointer"} {
		t.Run(form, func(t *testing.T) {
			root := t.TempDir()
			parent := filepath.Join(root, "parent")
			repoRel := "repo"
			repo := filepath.Join(parent, repoRel)
			switch form {
			case "clone":
				source := filepath.Join(root, "source")
				metadataRepo(t, source)
				require.NoError(t, os.MkdirAll(parent, 0o700))
				metadataGit(t, parent, "clone", source, "repo")
			case "nested":
				metadataRepo(t, filepath.Join(parent, "other"))
				repoRel = "other/nested/repo"
				repo = filepath.Join(parent, repoRel)
				metadataRepo(t, repo)
			case "external-absolute", "external-relative":
				source := filepath.Join(root, "source")
				metadataRepo(t, source)
				metadataGit(t, source, "worktree", "add", "-b", "task", repo)
				if form == "external-relative" {
					gitdir := filepath.Join(source, ".git", "worktrees", "repo")
					rel, err := filepath.Rel(repo, gitdir)
					require.NoError(t, err)
					writeFile(t, filepath.Join(repo, ".git"), "gitdir: "+rel+"\n", 0o644)
				}
			case "submodule-pointer":
				metadataRepo(t, repo)
				external := filepath.Join(root, "module-metadata")
				require.NoError(t, os.Rename(filepath.Join(repo, ".git"), external))
				writeFile(t, filepath.Join(repo, ".git"), "gitdir: "+external+"\n", 0o644)
				metadataGit(t, repo, "config", "core.worktree", repo)
			}
			assertMetadataIsolation(t, root, parent, repoRel)
		})
	}
}

func TestGroupGitMetadata_SnapshotOwnsExternalObjectsAndWorktreeIndex(t *testing.T) {
	root := t.TempDir()
	source, parent := filepath.Join(root, "source"), filepath.Join(root, "parent")
	metadataRepo(t, source)
	metadataGit(t, source, "worktree", "add", "-b", "task", parent)
	writeFile(t, filepath.Join(parent, "tracked"), "worktree index\n", 0o644)
	metadataGit(t, parent, "add", "tracked")
	// The common repository index deliberately differs from the linked index.
	assert.Equal(t, "base", metadataGit(t, source, "show", ":tracked"))
	gw := newGW(t, root)
	handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(source))
	require.NoError(t, os.RemoveAll(parent))
	a := filepath.Join(root, "fork")
	require.NoError(t, gw.ForkTask(context.Background(), handle, a))
	assert.Equal(t, "worktree index", metadataGit(t, a, "show", ":tracked"))
	assert.Equal(t, "base", metadataGit(t, a, "show", "HEAD:tracked"))
	metadataGit(t, a, "commit", "-m", "private index")
	assert.Equal(t, "worktree index", metadataGit(t, a, "show", "HEAD:tracked"))
	for _, obsolete := range []string{"commondir", "gitdir", "worktrees", "objects/info/alternates"} {
		_, err := os.Lstat(filepath.Join(a, ".git", obsolete))
		assert.True(t, os.IsNotExist(err), obsolete)
	}
}

func TestGroupGitMetadata_MaterializesAlternatesWithoutGitExecutable(t *testing.T) {
	root := t.TempDir()
	source, parent := filepath.Join(root, "source"), filepath.Join(root, "parent")
	metadataRepo(t, source)
	require.NoError(t, os.MkdirAll(parent, 0o700))
	metadataGit(t, parent, "clone", "--shared", source, "repo")
	gw := newGW(t, root)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(source))
	fork := filepath.Join(root, "fork")
	require.NoError(t, gw.ForkTask(context.Background(), handle, fork))
	require.NoError(t, os.Setenv("PATH", oldPath))
	assert.Equal(t, "base", metadataGit(t, filepath.Join(fork, "repo"), "show", "HEAD:tracked"))
	metadataGit(t, filepath.Join(fork, "repo"), "fsck", "--full")
}

func TestGroupGitMetadata_RejectsUnsafeOrIncompleteMetadata(t *testing.T) {
	for _, kind := range []string{"gitdir", "malformed-pointer", "commondir", "symlink", "alternates", "http-alternates", "alternate-cycle", "index-lock", "common-lock", "special", "include-config", "conditional-include"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			parent := filepath.Join(root, "parent")
			metadataRepo(t, parent)
			bad := filepath.Join(parent, ".git")
			switch kind {
			case "gitdir", "malformed-pointer":
				require.NoError(t, os.RemoveAll(bad))
				content := "gitdir: ../missing\n"
				if kind == "malformed-pointer" {
					content = "not a pointer\n"
				}
				writeFile(t, bad, content, 0o644)
			case "commondir":
				writeFile(t, filepath.Join(bad, "commondir"), "../missing\n", 0o644)
			case "symlink":
				secret := filepath.Join(root, "secret")
				writeFile(t, secret, "secret", 0o600)
				require.NoError(t, os.Symlink(secret, filepath.Join(bad, "refs", "leak")))
			case "alternates":
				writeFile(t, filepath.Join(bad, "objects", "info", "alternates"), "../../../../missing\n", 0o644)
			case "http-alternates":
				writeFile(t, filepath.Join(bad, "objects", "info", "http-alternates"), "https://objects.example.test\n", 0o644)
			case "alternate-cycle":
				writeFile(t, filepath.Join(bad, "objects", "info", "alternates"), "../objects\n", 0o644)
			case "index-lock":
				writeFile(t, filepath.Join(bad, "index.lock"), "held", 0o600)
			case "common-lock":
				work := filepath.Join(root, "work")
				metadataGit(t, parent, "worktree", "add", "-b", "task", work)
				writeFile(t, filepath.Join(bad, "index.lock"), "held", 0o600)
				parent = work
			case "special":
				// A directory in place of HEAD is an unambiguous metadata read failure, even as root.
				require.NoError(t, os.Remove(filepath.Join(bad, "HEAD")))
				require.NoError(t, os.Mkdir(filepath.Join(bad, "HEAD"), 0o700))
			case "include-config":
				metadataGit(t, parent, "config", "include.path", filepath.Join(root, "external-config"))
			case "conditional-include":
				// Git accepts tab whitespace between section and subsection.
				f, err := os.OpenFile(filepath.Join(bad, "config"), os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = f.WriteString("\n[includeIf\t\"gitdir:**\"]\n\tpath = /external/config\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			}
			before := listTree(t, parent)
			gw := newGW(t, root)
			handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
			require.Error(t, err)
			assert.Empty(t, handle)
			assert.ErrorIs(t, err, engine.ErrUnsupportedSnapshotContent)
			assert.Contains(t, err.Error(), ".git")
			if strings.Contains(kind, "lock") {
				assert.ErrorIs(t, err, filesystem.ErrGroupRepositoryLocked)
			}
			assert.Empty(t, snapshotEntries(t, gw), "no partial publication")
			assert.Equal(t, before, listTree(t, parent), "failure must not change source")
		})
	}
}

func TestGroupGitMetadata_ForkMetadataErrorCleansDestination(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	metadataRepo(t, parent)
	gw := newGW(t, root)
	handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	writeFile(t, filepath.Join(handle, ".git", "commondir"), "missing\n", 0o644)
	for _, existing := range []bool{false, true} {
		fork := filepath.Join(root, "fork")
		if existing {
			require.NoError(t, os.Mkdir(fork, 0o750))
		}
		err := gw.ForkTask(context.Background(), handle, fork)
		require.ErrorIs(t, err, engine.ErrUnsupportedSnapshotContent)
		if existing {
			assert.Empty(t, listTree(t, fork))
		} else {
			_, err := os.Stat(fork)
			assert.True(t, os.IsNotExist(err))
		}
	}
}

func TestGroupGitMetadata_Edges(t *testing.T) {
	for _, kind := range []string{"detached", "empty", "relative-symlink", "no-repository"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			parent := filepath.Join(root, "parent")
			if kind == "empty" {
				require.NoError(t, os.MkdirAll(parent, 0o700))
				metadataGit(t, parent, "init", "-b", "main")
			} else if kind == "no-repository" {
				writeFile(t, filepath.Join(parent, "tracked"), "base\n", 0o644)
			} else {
				metadataRepo(t, parent)
			}
			if kind == "detached" {
				metadataGit(t, parent, "checkout", "--detach")
			}
			if kind == "relative-symlink" {
				require.NoError(t, os.Symlink("tracked", filepath.Join(parent, "link")))
			}
			gw := newGW(t, root)
			handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
			require.NoError(t, err)
			a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
			require.NoError(t, gw.ForkTask(context.Background(), handle, a))
			require.NoError(t, gw.ForkTask(context.Background(), handle, b))
			if kind == "empty" {
				writeFile(t, filepath.Join(a, "new"), "first", 0o644)
				metadataGit(t, a, "add", ".")
				metadataGit(t, a, "commit", "-m", "first")
				assert.Empty(t, metadataGit(t, b, "status", "--porcelain"))
			} else if kind != "no-repository" {
				assert.Equal(t, metadataGit(t, parent, "rev-parse", "HEAD"), metadataGit(t, a, "rev-parse", "HEAD"))
				if kind == "detached" {
					assert.Equal(t, "HEAD", metadataGit(t, a, "rev-parse", "--abbrev-ref", "HEAD"))
				}
				if kind == "relative-symlink" {
					target, err := os.Readlink(filepath.Join(a, "link"))
					require.NoError(t, err)
					assert.Equal(t, "tracked", target)
				}
			} else {
				assert.Equal(t, listTree(t, parent), listTree(t, a))
			}
		})
	}
}

func TestGroupGitMetadata_WorktreeConfigIsNormalized(t *testing.T) {
	root := t.TempDir()
	source, parent := filepath.Join(root, "source"), filepath.Join(root, "parent")
	metadataRepo(t, source)
	metadataGit(t, source, "worktree", "add", "-b", "task", parent)
	metadataGit(t, source, "config", "extensions.worktreeConfig", "true")
	metadataGit(t, parent, "config", "--worktree", "core.worktree", parent)
	metadataGit(t, parent, "config", "--worktree", "group.local", "inherited")
	gw := newGW(t, root)
	handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	fork := filepath.Join(root, "fork")
	require.NoError(t, gw.ForkTask(context.Background(), handle, fork))
	assert.Equal(t, "inherited", metadataGit(t, fork, "config", "group.local"))
	actual := metadataGit(t, fork, "rev-parse", "--show-toplevel")
	realFork, err := filepath.EvalSymlinks(fork)
	require.NoError(t, err)
	assert.Equal(t, realFork, actual)
	// Repeated copying must not accumulate normalized sections or settings.
	config, err := os.ReadFile(filepath.Join(handle, ".git", "config"))
	require.NoError(t, err)
	forkConfig, err := os.ReadFile(filepath.Join(fork, ".git", "config"))
	require.NoError(t, err)
	assert.Equal(t, string(config), string(forkConfig))
}

// Cancel only after the metadata writer has produced HEAD, not during the
// preceding ordinary-content copy. This drives both production rollback paths.
type cancelAfterMetadata struct {
	context.Context
	cancel context.CancelFunc
	head   string
}

func (c *cancelAfterMetadata) Err() error {
	if _, err := os.Stat(c.head); err == nil {
		c.cancel()
	}
	return c.Context.Err()
}

func TestGroupGitMetadata_CancelledMetadataCopyRollsBack(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	metadataRepo(t, parent)
	gw := newGW(t, root)
	runID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := gw.SnapshotParent(&cancelAfterMetadata{Context: ctx, cancel: cancel, head: filepath.Join(gw.SnapshotsDir(), ".tmp-"+runID.String(), ".git", "HEAD")}, runID, parent)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, snapshotEntries(t, gw))
	handle, err := gw.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	for _, existing := range []bool{false, true} {
		fork := filepath.Join(root, "fork")
		if existing {
			require.NoError(t, os.Mkdir(fork, 0o750))
		}
		ctx, cancel := context.WithCancel(context.Background())
		err := gw.ForkTask(&cancelAfterMetadata{Context: ctx, cancel: cancel, head: filepath.Join(fork, ".git", "HEAD")}, handle, fork)
		cancel()
		require.ErrorIs(t, err, context.Canceled)
		if existing {
			assert.Empty(t, listTree(t, fork))
		} else {
			_, err := os.Stat(fork)
			assert.True(t, os.IsNotExist(err))
		}
	}
}

func TestGroupGitMetadata_CancelBetweenMetadataFiles(t *testing.T) {
	root := t.TempDir()
	source, dest := filepath.Join(root, "source"), filepath.Join(root, "destination")
	metadataRepo(t, source)
	require.NoError(t, os.MkdirAll(dest, 0o700))
	before := listTree(t, source)
	require.ErrorIs(t, filesystem.CopyGroupGitMetadata(copyingContext(), source, dest), context.Canceled)
	assert.Equal(t, before, listTree(t, source))
}
