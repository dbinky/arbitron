package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireGit skips ONLY when the CI host has no git binary at all. Behaviour
// without git is asserted separately (TestRunner_MissingGit_*), with a search
// path that is known to be empty.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not installed on this host")
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process-group and shell-alias assertions are unix-only")
	}
}

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	return NewRunner(RunnerConfig{StateDir: t.TempDir()})
}

func TestRunner_VersionHappyPath(t *testing.T) {
	requireGit(t)
	v, err := newTestRunner(t).Version(context.Background())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(v, "git version "), v)
}

func TestRunner_StdinAndStdout(t *testing.T) {
	requireGit(t)
	res, err := newTestRunner(t).Run(context.Background(), Command{
		Args:  []string{"hash-object", "--stdin"},
		Stdin: []byte("hello\n"),
	})
	require.NoError(t, err)
	assert.Equal(t, "ce013625030ba8dba906f756967f9e9ca394464a", strings.TrimSpace(res.Stdout))
	assert.Equal(t, 0, res.ExitCode)
}

func TestRunner_GitDirWorkTreeAndExtraEnvAreHonoured(t *testing.T) {
	requireGit(t)
	r := newTestRunner(t)
	ctx := context.Background()
	gitDir := filepath.Join(t.TempDir(), "backing.git")
	work := t.TempDir()
	_, err := r.Run(ctx, Command{Args: []string{"init", "--bare", "-q", gitDir}})
	require.NoError(t, err)

	res, err := r.Run(ctx, Command{Args: []string{"rev-parse", "--absolute-git-dir"}, GitDir: gitDir})
	require.NoError(t, err)
	wantGitDir, _ := filepath.EvalSymlinks(gitDir)
	gotGitDir, _ := filepath.EvalSymlinks(strings.TrimSpace(res.Stdout))
	assert.Equal(t, wantGitDir, gotGitDir)

	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644))
	index := filepath.Join(t.TempDir(), "idx")
	_, err = r.Run(ctx, Command{
		Args: []string{"add", "a.txt"}, Dir: work, GitDir: gitDir, WorkTree: work,
		Env: []string{"GIT_INDEX_FILE=" + index},
	})
	require.NoError(t, err)
	_, statErr := os.Stat(index)
	require.NoError(t, statErr, "GIT_INDEX_FILE from Command.Env must be used")
	res, err = r.Run(ctx, Command{
		Args: []string{"ls-files"}, Dir: work, GitDir: gitDir, WorkTree: work,
		Env: []string{"GIT_INDEX_FILE=" + index},
	})
	require.NoError(t, err)
	assert.Equal(t, "a.txt", strings.TrimSpace(res.Stdout))
}

func TestRunner_NonZeroExitIsExitError(t *testing.T) {
	requireGit(t)
	res, err := newTestRunner(t).Run(context.Background(), Command{
		Args: []string{"rev-parse", "--verify", "refs/heads/does-not-exist"},
		Dir:  t.TempDir(),
	})
	require.Error(t, err)
	var ee *ExitError
	require.True(t, errors.As(err, &ee), "got %T: %v", err, err)
	assert.NotZero(t, ee.ExitCode)
	assert.Equal(t, ee.ExitCode, res.ExitCode)
	assert.True(t, IsExitCode(err, ee.ExitCode))
	assert.False(t, IsExitCode(err, ee.ExitCode+1))
	assert.Contains(t, err.Error(), "rev-parse")
}

// The author's (or host's) global config, hooks and identity must never apply
// to engine-run git commands.
func TestRunner_IgnoresUserConfigHooksAndIdentity(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	evilHome := t.TempDir()
	hooks := filepath.Join(evilHome, "hooks")
	marker := filepath.Join(evilHome, "hook-ran")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
		[]byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evilHome, ".gitconfig"), []byte(
		"[user]\n\tname = Evil\n\temail = evil@example.com\n[core]\n\thooksPath = "+hooks+"\n"), 0o644))
	t.Setenv("HOME", evilHome)
	t.Setenv("GIT_DIR", filepath.Join(evilHome, "not-a-repo"))
	t.Setenv("GIT_AUTHOR_NAME", "Evil")

	r := newTestRunner(t)
	repo := t.TempDir()
	_, err := r.Run(ctx, Command{Args: []string{"init", "-q", repo}})
	require.NoError(t, err)
	// A repository-level hooksPath (what an author repo could carry) must be
	// overridden too.
	_, err = r.Run(ctx, Command{Args: []string{"config", "core.hooksPath", hooks}, Dir: repo})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644))
	_, err = r.Run(ctx, Command{Args: []string{"add", "f"}, Dir: repo})
	require.NoError(t, err)
	_, err = r.Run(ctx, Command{Args: []string{"commit", "-q", "-m", "m"}, Dir: repo})
	require.NoError(t, err)

	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "pre-commit hook must not run")
	res, err := r.Run(ctx, Command{Args: []string{"log", "-1", "--format=%an <%ae>|%cn"}, Dir: repo})
	require.NoError(t, err)
	assert.Equal(t, "Arbitron <arbitron@localhost>|Arbitron", strings.TrimSpace(res.Stdout))
}

func TestRunner_IgnoresRepositoryCredentialHelper(t *testing.T) {
	requireGit(t)
	skipOnWindows(t)
	r := newTestRunner(t)
	ctx := context.Background()
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "credential-helper-ran")
	_, err := r.Run(ctx, Command{Args: []string{"init", "-q", repo}})
	require.NoError(t, err)
	helper := "!f() { touch '" + marker + "'; printf 'username=evil\\npassword=secret\\n'; }; f"
	_, err = r.Run(ctx, Command{Args: []string{"config", "credential.helper", helper}, Dir: repo})
	require.NoError(t, err)

	_, err = r.Run(ctx, Command{
		Args: []string{"credential", "fill"}, Dir: repo,
		Stdin: []byte("protocol=https\nhost=example.invalid\n\n"),
	})
	require.Error(t, err, "credential prompting is disabled and no helper may supply credentials")
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "repository credential helper must not execute")
}

func TestRunner_MissingGit_ExplicitPath(t *testing.T) {
	r := NewRunner(RunnerConfig{GitPath: filepath.Join(t.TempDir(), "no-git-here"), StateDir: t.TempDir()})
	_, err := r.Run(context.Background(), Command{Args: []string{"--version"}})
	require.ErrorIs(t, err, ErrGitUnavailable)
	assert.Contains(t, err.Error(), "no-git-here")
	_, err = r.Version(context.Background())
	require.ErrorIs(t, err, ErrGitUnavailable)
}

func TestRunner_MissingGit_SearchPathWithoutGit(t *testing.T) {
	empty := t.TempDir()
	r := NewRunner(RunnerConfig{SearchPath: empty, StateDir: t.TempDir()})
	_, err := r.Resolve()
	require.ErrorIs(t, err, ErrGitUnavailable)
	_, err = r.Run(context.Background(), Command{Args: []string{"--version"}})
	require.ErrorIs(t, err, ErrGitUnavailable)
	assert.Contains(t, err.Error(), empty, "the error names the searched path so an operator can act")
}

// Availability is checked at USE: a binary that appears later is found
// without rebuilding the runner.
func TestRunner_ResolveIsNotCached(t *testing.T) {
	requireGit(t)
	skipOnWindows(t)
	real, err := exec.LookPath("git")
	require.NoError(t, err)
	dir := t.TempDir()
	r := NewRunner(RunnerConfig{SearchPath: dir, StateDir: t.TempDir()})
	_, err = r.Resolve()
	require.ErrorIs(t, err, ErrGitUnavailable)
	require.NoError(t, os.Symlink(real, filepath.Join(dir, "git")))
	got, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "git"), got)
}

func TestRunner_RelativeBinaryIsIndependentOfCommandDirectory(t *testing.T) {
	requireGit(t)
	skipOnWindows(t)
	real, err := exec.LookPath("git")
	require.NoError(t, err)
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("bin", 0o755))
	require.NoError(t, os.Symlink(real, filepath.Join("bin", "git")))
	for _, cfg := range []RunnerConfig{
		{SearchPath: "bin", StateDir: t.TempDir()},
		{GitPath: filepath.Join("bin", "git"), StateDir: t.TempDir()},
	} {
		r := NewRunner(cfg)
		resolved, err := r.Resolve()
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(resolved), "resolution must survive a change of subprocess directory")
		res, err := r.Run(context.Background(), Command{Args: []string{"--version"}, Dir: t.TempDir()})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(res.Stdout, "git version "))
	}
}

func TestRunner_ResolvePlatformExecutable(t *testing.T) {
	dir := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(binary, []byte("only resolution is tested"), 0o755))
	for _, cfg := range []RunnerConfig{{SearchPath: dir}, {GitPath: binary}} {
		resolved, err := NewRunner(cfg).Resolve()
		require.NoError(t, err)
		assert.Equal(t, binary, resolved)
	}
}

func TestRunner_TimeoutKillsProcessGroup(t *testing.T) {
	requireGit(t)
	skipOnWindows(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := NewRunner(RunnerConfig{StateDir: t.TempDir(), Timeout: 300 * time.Millisecond})
	start := time.Now()
	_, err := r.Run(context.Background(), Command{
		// A shell alias makes git spawn a grandchild (sleep) that only a
		// process-group kill can reach.
		Args: []string{"-c", "alias.hang=!sleep 30 & echo $! > '" + pidFile + "'; wait", "hang"},
		Dir:  t.TempDir(),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 10*time.Second)

	raw, readErr := os.ReadFile(pidFile)
	require.NoError(t, readErr)
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, convErr)
	require.Eventually(t, func() bool {
		process, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		defer process.Release()
		err = process.Signal(syscall.Signal(0))
		return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
	}, 5*time.Second, 50*time.Millisecond, "grandchild sleep must be killed with the group")
}

func TestRunner_CommandTimeoutOverridesConfig(t *testing.T) {
	requireGit(t)
	skipOnWindows(t)
	r := NewRunner(RunnerConfig{StateDir: t.TempDir(), Timeout: time.Hour})
	start := time.Now()
	_, err := r.Run(context.Background(), Command{
		Args:    []string{"-c", "alias.hang=!sleep 30", "hang"},
		Dir:     t.TempDir(),
		Timeout: 200 * time.Millisecond,
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestRunner_CancelKillsProcess(t *testing.T) {
	requireGit(t)
	skipOnWindows(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := newTestRunner(t).Run(ctx, Command{
		Args: []string{"-c", "alias.hang=!sleep 30", "hang"},
		Dir:  t.TempDir(),
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestRunner_AlreadyCancelledContextDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// No requireGit: a cancelled context must win even before resolution.
	_, err := NewRunner(RunnerConfig{SearchPath: t.TempDir()}).Run(ctx, Command{Args: []string{"--version"}})
	require.ErrorIs(t, err, context.Canceled)
}

func TestRunner_OutputCapIsAnError(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	repo := t.TempDir()
	r := NewRunner(RunnerConfig{StateDir: t.TempDir(), MaxOutputBytes: 1024})
	_, err := r.Run(ctx, Command{Args: []string{"init", "-q", repo}})
	require.NoError(t, err)
	big := strings.Repeat("0123456789abcdef", 512) // 8 KiB
	res, err := r.Run(ctx, Command{Args: []string{"hash-object", "-w", "--stdin"}, Dir: repo, Stdin: []byte(big)})
	require.NoError(t, err)
	sha := strings.TrimSpace(res.Stdout)

	res, err = r.Run(ctx, Command{Args: []string{"cat-file", "blob", sha}, Dir: repo})
	require.ErrorIs(t, err, ErrOutputLimit)
	assert.Len(t, res.Stdout, 1024, "the captured head is kept for diagnostics")
}

func TestRunner_EmptyCommandRejected(t *testing.T) {
	_, err := newTestRunner(t).Run(context.Background(), Command{})
	require.Error(t, err)
}

func TestCappedBuffer_ReportsFullWritesAndOverflow(t *testing.T) {
	b := &cappedBuffer{limit: 4}
	n, err := b.Write([]byte("ab"))
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.False(t, b.overflow)
	n, err = b.Write([]byte("cdef"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.True(t, b.overflow)
	assert.Equal(t, "abcd", b.String())
}
