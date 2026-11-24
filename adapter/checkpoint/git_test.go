// adapter/checkpoint/git_test.go
package checkpoint_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ckGitEnv is an isolated, offline git environment: no lazy fetch, no
// transport, no user/system config.
func ckGitEnv(home, lazyFetch string) []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME="+home, "GIT_NO_LAZY_FETCH="+lazyFetch)
}

func ckGitTry(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "protocol.allow=never"}, args...)...)
	cmd.Dir = dir
	cmd.Env = ckGitEnv(dir, "1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ckGit runs git offline in dir and requires success.
func ckGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := ckGitTry(t, dir, args...)
	require.NoError(t, err, "git %v: %s", args, out)
	return out
}

// ckGitOnline is used ONLY by fixtures that build clones from a local bare
// repository; the code under test never runs it.
func ckGitOnline(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = ckGitEnv(dir, "0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func ckRepo(t *testing.T, dir string) string {
	t.Helper()
	_, err := exec.LookPath("git")
	require.NoError(t, err, "git is required on the server PATH for checkpointing repositories")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	ckGit(t, dir, "init", "-q", "-b", "main")
	ckWrite(t, dir, "README.md", "hello", 0o644)
	ckWrite(t, dir, "tool.sh", "#!/bin/sh\n", 0o755)
	ckGit(t, dir, "add", ".")
	ckGit(t, dir, "commit", "-q", "-m", "init")
	return ckGit(t, dir, "rev-parse", "HEAD")
}

// ckDigest hashes every path, mode and byte under root (metadata byte-for-byte check).
func ckDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	var lines []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		info, err := os.Lstat(p)
		require.NoError(t, err)
		rel, _ := filepath.Rel(root, p)
		line := rel + " " + info.Mode().String()
		switch {
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			sum := sha256.Sum256(b)
			line += " " + hex.EncodeToString(sum[:])
		case info.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			line += " ->" + l
		}
		lines = append(lines, line)
		return nil
	}))
	sort.Strings(lines)
	io.WriteString(h, strings.Join(lines, "\n"))
	return hex.EncodeToString(h.Sum(nil))
}

// ckBaseline bootstraps ws as run's committed baseline.
func ckBaseline(t *testing.T, store *checkpoint.Store, run uuid.UUID, ws string) runner.CheckpointRef {
	t.Helper()
	ref, _, err := store.Commit(context.Background(), run, ws)
	require.NoError(t, err)
	return ref
}

func TestStore_GitDirectoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	ws := t.TempDir()
	head := ckRepo(t, filepath.Join(ws, "repo"))
	ckWrite(t, filepath.Join(ws, "repo"), "dirty.txt", "untracked", 0o644)
	ref := ckBaseline(t, store, uuid.New(), ws)
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref, dst))
	repo := filepath.Join(dst, "repo")
	assert.Equal(t, head, ckGit(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, "?? dirty.txt", ckGit(t, repo, "status", "--porcelain"))
	fi, err := os.Stat(filepath.Join(repo, "tool.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm(), "executable bit survives")
}

func TestStore_LinkedWorktreeRestoresWithoutOriginalRepo(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	mainRepo := filepath.Join(t.TempDir(), "main")
	ckRepo(t, mainRepo)
	ws := t.TempDir()
	ckGit(t, mainRepo, "worktree", "add", "-q", "-b", "feat", filepath.Join(ws, "repo"))
	wt := filepath.Join(ws, "repo")
	ckWrite(t, wt, "wip.txt", "uncommitted", 0o644)
	ckWrite(t, wt, "README.md", "edited", 0o644)
	ckGit(t, wt, "commit", "-q", "-am", "feat work")
	ckWrite(t, wt, "README.md", "dirty again", 0o644)
	head := ckGit(t, wt, "rev-parse", "HEAD")
	fi, err := os.Lstat(filepath.Join(wt, ".git"))
	require.NoError(t, err)
	require.True(t, fi.Mode().IsRegular(), "precondition: linked worktree has a .git FILE")
	before := ckDigest(t, filepath.Join(mainRepo, ".git"))
	beforeFile := ckDigest(t, filepath.Join(wt, ".git"))

	ref := ckBaseline(t, store, uuid.New(), ws)
	m, err := store.Manifest(ctx, ref)
	require.NoError(t, err)
	var gitEntry *runner.ManifestEntry
	for i := range m.Entries {
		if m.Entries[i].Path == "repo/.git" {
			gitEntry = &m.Entries[i]
		}
	}
	require.NotNil(t, gitEntry)
	assert.True(t, os.FileMode(gitEntry.Mode).IsDir(), "the .git file became a self-contained directory")
	assert.Equal(t, before, ckDigest(t, filepath.Join(mainRepo, ".git")), "the user's repository is byte-for-byte unchanged")
	assert.Equal(t, beforeFile, ckDigest(t, filepath.Join(wt, ".git")))

	require.NoError(t, os.RemoveAll(filepath.Dir(mainRepo))) // the original repository is gone
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref, dst))
	repo := filepath.Join(dst, "repo")
	assert.Equal(t, head, ckGit(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, "feat", ckGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	status := ckGit(t, repo, "status", "--porcelain")
	assert.Contains(t, status, "?? wip.txt")
	assert.Contains(t, status, "M README.md")
	ckGit(t, repo, "fsck", "--connectivity-only", "--no-dangling")
	ckGit(t, repo, "log", "--oneline", "main")
}

func TestStore_AlternatesMaterializedInPrivateCopy(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	src := filepath.Join(t.TempDir(), "src")
	head := ckRepo(t, src)
	ws := t.TempDir()
	shared := filepath.Join(ws, "shared")
	ckGitOnline(t, ws, "clone", "-q", "--shared", src, shared)
	ckWrite(t, shared, "more.txt", "more", 0o644)
	ckGit(t, shared, "add", ".")
	ckGit(t, shared, "commit", "-q", "-m", "local")
	local := ckGit(t, shared, "rev-parse", "HEAD")
	alt := filepath.Join(shared, ".git", "objects", "info", "alternates")
	_, err := os.Stat(alt)
	require.NoError(t, err, "precondition: the clone borrows objects")
	before := ckDigest(t, filepath.Join(shared, ".git"))

	ref := ckBaseline(t, store, uuid.New(), ws)
	assert.Equal(t, before, ckDigest(t, filepath.Join(shared, ".git")), "the user's repository is never repacked or rewritten")

	require.NoError(t, os.RemoveAll(src))
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref, dst))
	repo := filepath.Join(dst, "shared")
	_, err = os.Stat(filepath.Join(repo, ".git", "objects", "info", "alternates"))
	assert.True(t, os.IsNotExist(err), "the private copy has no alternates")
	assert.Equal(t, local, ckGit(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, "commit", ckGit(t, repo, "cat-file", "-t", head), "borrowed history is now local")
	ckGit(t, repo, "fsck", "--connectivity-only", "--no-dangling")
	assert.Equal(t, "", ckGit(t, repo, "status", "--porcelain"))
}

func TestStore_UnmergedIndexAndMergeHeadRoundTrip(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	mainRepo := filepath.Join(t.TempDir(), "main")
	ckRepo(t, mainRepo)
	ckGit(t, mainRepo, "checkout", "-q", "-b", "theirs")
	ckWrite(t, mainRepo, "README.md", "theirs", 0o644)
	ckGit(t, mainRepo, "commit", "-q", "-am", "theirs")
	theirs := ckGit(t, mainRepo, "rev-parse", "HEAD")
	ckGit(t, mainRepo, "checkout", "-q", "main")
	ws := t.TempDir()
	wt := filepath.Join(ws, "repo")
	ckGit(t, mainRepo, "worktree", "add", "-q", "-b", "ours", wt)
	ckWrite(t, wt, "README.md", "ours", 0o644)
	ckGit(t, wt, "commit", "-q", "-am", "ours")
	_, err := ckGitTry(t, wt, "merge", "theirs")
	require.Error(t, err, "precondition: the merge conflicts")
	stages := ckGit(t, wt, "ls-files", "-u")
	require.Len(t, strings.Split(stages, "\n"), 3)
	before := ckDigest(t, filepath.Join(mainRepo, ".git"))

	ref := ckBaseline(t, store, uuid.New(), ws)
	assert.Equal(t, before, ckDigest(t, filepath.Join(mainRepo, ".git")))
	require.NoError(t, os.RemoveAll(filepath.Dir(mainRepo)))
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref, dst))
	repo := filepath.Join(dst, "repo")
	assert.Equal(t, stages, ckGit(t, repo, "ls-files", "-u"), "all three index stages survive")
	assert.Equal(t, theirs, ckGit(t, repo, "rev-parse", "MERGE_HEAD"))
	for _, line := range strings.Split(stages, "\n") {
		oid := strings.Fields(line)[1]
		assert.Equal(t, "blob", ckGit(t, repo, "cat-file", "-t", oid), "stage object %s is local", oid)
	}
	assert.Contains(t, ckGit(t, repo, "status", "--porcelain"), "UU README.md")
}

// recordingGit wraps the real git so tests can assert what the store asked for.
type recordingGit struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *recordingGit) run(ctx context.Context, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.mu.Unlock()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, errors.New(err.Error() + ": " + string(ee.Stderr))
		}
	}
	return out, err
}

func (r *recordingGit) assertOffline(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.calls)
	for _, c := range r.calls {
		joined := strings.Join(c, " ")
		assert.Contains(t, joined, "protocol.allow=never", "every git call disables transports: %s", joined)
		for _, a := range c {
			assert.NotContains(t, []string{"fetch", "pull", "clone", "remote", "fetch-pack", "backfill"}, a, "no network command: %s", joined)
		}
	}
}

// ckPartialClone builds a --filter=blob:none clone of a local bare repository
// whose history holds a blob that is not checked out, so it is missing locally.
func ckPartialClone(t *testing.T, ws string) (clone, missingBlob string) {
	t.Helper()
	base := t.TempDir()
	seed := filepath.Join(base, "seed")
	ckRepo(t, seed)
	ckWrite(t, seed, "old.txt", "only in history", 0o644)
	ckGit(t, seed, "add", ".")
	ckGit(t, seed, "commit", "-q", "-m", "old")
	missingBlob = ckGit(t, seed, "rev-parse", "HEAD:old.txt")
	ckGit(t, seed, "rm", "-q", "old.txt")
	ckGit(t, seed, "commit", "-q", "-m", "drop old")
	bare := filepath.Join(base, "origin.git")
	ckGitOnline(t, base, "clone", "-q", "--bare", seed, bare)
	ckGit(t, bare, "config", "uploadpack.allowFilter", "true")
	clone = filepath.Join(ws, "repo")
	ckGitOnline(t, ws, "clone", "-q", "--filter=blob:none", "file://"+bare, clone)
	_, err := ckGitTry(t, clone, "cat-file", "-e", missingBlob)
	require.Error(t, err, "precondition: the historical blob is missing locally")
	return clone, missingBlob
}

func TestStore_MissingPromisorObjectReturnsPromisorErrorWithoutFetch(t *testing.T) {
	ctx := context.Background()
	rec := &recordingGit{}
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat, checkpoint.WithGit(rec.run))
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "notes.txt", "baseline", 0o644)
	base := ckBaseline(t, store, run, ws) // a committed pointer that must survive
	clone, missing := ckPartialClone(t, ws)
	before := ckDigest(t, filepath.Join(clone, ".git"))

	f, res := ckOperation(cat, run)
	_, _, err := store.StageBuiltIn(ctx, run, ws, f, res)
	require.ErrorIs(t, err, runner.ErrUnsupportedGit)
	require.ErrorIs(t, err, runner.ErrPromisorObjectsMissing)
	_, _, err = store.Commit(ctx, uuid.New(), ws)
	require.ErrorIs(t, err, runner.ErrPromisorObjectsMissing, "bootstrap propagates it unchanged")

	assert.Empty(t, cat.prepared(run), "no candidate registered")
	latest, _, _ := store.Latest(ctx, run)
	assert.Equal(t, base, latest, "the committed pointer is unchanged")
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, base, dst))
	assert.Equal(t, []string{"notes.txt 644 baseline"}, ckTree(t, dst), "prior committed files unchanged")
	_, err = ckGitTry(t, clone, "cat-file", "-e", missing)
	assert.Error(t, err, "nothing fetched the missing object")
	assert.Equal(t, before, ckDigest(t, filepath.Join(clone, ".git")), "the user's repository is unchanged")
	rec.assertOffline(t)
}

func TestStore_PromisorObjectsPresentAfterRefetchStagesNormally(t *testing.T) {
	ctx := context.Background()
	rec := &recordingGit{}
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat, checkpoint.WithGit(rec.run))
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "notes.txt", "baseline", 0o644)
	base := ckBaseline(t, store, run, ws)
	clone, missing := ckPartialClone(t, ws)
	// The operator makes the objects local (what "git fetch --refetch" achieves);
	// the fixture, not the store, performs this lazy fetch.
	ckGitOnline(t, clone, "cat-file", "-p", missing)
	ckGit(t, clone, "cat-file", "-e", missing)

	f, res := ckOperation(cat, run)
	ref, _, err := store.StageBuiltIn(ctx, run, ws, f, res)
	require.NoError(t, err)
	assert.Equal(t, []runner.CheckpointRef{ref}, cat.prepared(run), "a normal candidate")
	require.NoError(t, cat.commit(ref))
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref, dst))
	repo := filepath.Join(dst, "repo")
	assert.Equal(t, "blob", ckGit(t, repo, "cat-file", "-t", missing), "the restored copy holds the object without the remote")
	ckGit(t, repo, "fsck", "--connectivity-only", "--no-dangling")
	assert.NotEqual(t, base, ref)
	rec.assertOffline(t)
}

func TestMaterializePortableGit_RefusesSourceOverlap(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	ckRepo(t, repo)
	before := ckDigest(t, filepath.Join(repo, ".git"))
	require.ErrorIs(t, checkpoint.MaterializePortableGit(ctx, repo, repo), runner.ErrUnsupportedGit)
	require.ErrorIs(t, checkpoint.MaterializePortableGit(ctx, repo, filepath.Join(repo, ".git", "copy")), runner.ErrUnsupportedGit)
	assert.Equal(t, before, ckDigest(t, filepath.Join(repo, ".git")), "the source repository is never written")

	dst := t.TempDir()
	require.NoError(t, checkpoint.MaterializePortableGit(ctx, repo, dst))
	require.NoError(t, os.RemoveAll(repo))
	assert.Equal(t, "commit", ckGit(t, dst, "--git-dir", filepath.Join(dst, ".git"), "cat-file", "-t", "HEAD"))
}

// Group workspaces (P3 T13) may be linked worktrees: StageResource
// materializes them exactly like StageBuiltIn.
func TestStore_StageResourceMaterializesLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "notes.txt", "baseline", 0o644)
	base := ckBaseline(t, store, run, ws)
	mainRepo := filepath.Join(t.TempDir(), "main")
	ckRepo(t, mainRepo)
	wt := filepath.Join(ws, "branch")
	ckGit(t, mainRepo, "worktree", "add", "-q", "-b", "task-1", wt)
	ckWrite(t, wt, "result.txt", "group task output", 0o644)
	head := ckGit(t, wt, "rev-parse", "HEAD")
	before := ckDigest(t, filepath.Join(mainRepo, ".git"))

	staged, err := store.StageResource(ctx, run, ws, base)
	require.NoError(t, err)
	assert.Empty(t, cat.prepared(run), "no catalog row")
	assert.Equal(t, before, ckDigest(t, filepath.Join(mainRepo, ".git")), "the user's repository is unchanged")
	ref, err := cat.commitResource(staged) // stand-in for P3 T13 ResourceCommitRepo.CommitResource
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(filepath.Dir(mainRepo))) // the original repository is gone
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, ref, dst))
	repo := filepath.Join(dst, "branch")
	assert.Equal(t, head, ckGit(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, "task-1", ckGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Contains(t, ckGit(t, repo, "status", "--porcelain"), "?? result.txt")
	ckGit(t, repo, "fsck", "--connectivity-only", "--no-dangling")
}

func TestStore_StageResourcePropagatesPromisorError(t *testing.T) {
	ctx := context.Background()
	rec := &recordingGit{}
	cat := newMemCatalog()
	store, _ := newCkStore(t, cat, checkpoint.WithGit(rec.run))
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "notes.txt", "baseline", 0o644)
	base := ckBaseline(t, store, run, ws)
	clone, missing := ckPartialClone(t, ws)

	_, err := store.StageResource(ctx, run, ws, base)
	require.ErrorIs(t, err, runner.ErrUnsupportedGit)
	require.ErrorIs(t, err, runner.ErrPromisorObjectsMissing, "RR-08 signal propagates unchanged")
	latest, _, _ := store.Latest(ctx, run)
	assert.Equal(t, base, latest)
	_, err = ckGitTry(t, clone, "cat-file", "-e", missing)
	assert.Error(t, err, "nothing fetched the missing object")
	rec.assertOffline(t)
}
