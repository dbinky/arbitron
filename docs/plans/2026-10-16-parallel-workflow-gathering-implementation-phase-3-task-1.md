# Parallel Workflow Gathering — Phase 3 Task 1: Adapter-local bounded git runner

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the new `adapter/git` package one bounded, cancellable, isolated way to run `git`, and a single actionable `ErrGitUnavailable` when the host has no git — resolving **P3-D** without touching `adapter/process`.
**Architecture:** `adapter/git/runner.go` re-implements, adapter-locally, the shape of `adapter/process/runner.go:run` (timeout via context, process-group kill, capped output) because `run`/`commandSpec`/`setProcGroup` are package-private and `adapter/process` is the script/tool executor's package, not a shared library. Every git process gets an isolated environment (procenv allowlist minus identity/locale vars, private `HOME`/`XDG_CONFIG_HOME`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`) plus command-line `-c` overrides (hooks to `/dev/null`, no autocrlf/filters/fsmonitor/signing, fixed identity), so no author or host git configuration, hook or credential helper ever runs. The git binary is resolved at EVERY use (never cached) and a missing binary is `ErrGitUnavailable`.
**Tech Stack:** Go 1.25.5 standard library (`os/exec`, `syscall`, `context`, `bytes`), `adapter/procenv`, testify; real `git` (tests skip only when the CI host has no git binary).
**Spec / Design:** `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §1 last paragraph ("`adapter/process/runner.go:run`, `commandSpec` and process-group helpers are package-private … An exported shared runner or equivalent adapter-local implementation is **P3-D**. Git host availability must be checked at use and reported"), Data Provenance row "Cancellable bounded Git subprocess execution".

**Interfaces**
- Consumes (existing code only):
  - `adapter/procenv/procenv.go:76-119` `func BaseEnv() []string` — allowlisted parent env (`baseAllowlist`, lines 29-38: PATH, HOME, USER, LOGNAME, LANG, TERM, TMPDIR, SHELL, CLAUDE_CONFIG_DIR, ANTHROPIC_BASE_URL, LC_*) with PATH augmented by `StandardBinDirs` (lines 46-55). Secrets and every `GIT_*` variable of the server process are therefore never inherited.
  - Pattern only (NOT imported; package-private): `adapter/process/runner.go:12-20` `commandSpec`, `:29-81` `run` (timeout context, `setProcGroup`, `killProcGroup` on deadline, `*exec.ExitError` → exit code), `adapter/process/procgroup_unix.go:10-22` / `procgroup_windows.go:7-15`, `adapter/process/limit.go:5-28` `limitedWriter`.
- Produces (package `git`, import path `github.com/arbitron/arbitron/adapter/git`; recommended import alias in callers `gitarea`):
  - `var ErrGitUnavailable = errors.New("git executable is not available on this host")` (contract name)
  - `var ErrOutputLimit = errors.New("git output exceeded the configured limit")`
  - `const DefaultTimeout = 5 * time.Minute`, `const DefaultMaxOutputBytes = 64 << 20`
  - `type RunnerConfig struct { GitPath string; SearchPath string; Timeout time.Duration; MaxOutputBytes int; StateDir string }`
  - `type Command struct { Args []string; Dir string; GitDir string; WorkTree string; Stdin []byte; Env []string; Timeout time.Duration }`
  - `type Result struct { Stdout string; Stderr string; ExitCode int }`
  - `type ExitError struct { Args []string; ExitCode int; Stderr string }` with `func (e *ExitError) Error() string`
  - `func IsExitCode(err error, code int) bool`
  - `type Runner struct{ /* cfg, lazily created private state dir */ }`
  - `func NewRunner(cfg RunnerConfig) *Runner`
  - `func (r *Runner) Resolve() (string, error)` — binary the next Run would use, or error wrapping `ErrGitUnavailable`
  - `func (r *Runner) Version(ctx context.Context) (string, error)`
  - `func (r *Runner) Run(ctx context.Context, c Command) (Result, error)`
  - Semantics later tasks rely on: non-zero exit ⇒ populated `Result` + `*ExitError` (so `IsExitCode(err, 1)` detects a merge conflict); timeout/cancel ⇒ whole process group SIGKILLed and an error wrapping `context.DeadlineExceeded`/`context.Canceled`; output beyond the cap ⇒ error wrapping `ErrOutputLimit` (callers never parse a truncated listing); an already-cancelled ctx never starts a process.

**Wiring & data provenance**
- Composition root: none of its own. The runner becomes live through `gitarea.NewWorkingArea` (P3-T2), which builds a default `NewRunner(RunnerConfig{StateDir: <root>/.group-backing/.runner})`; P3-T11 wires `gitarea.NewWorkingArea(cfg.Workspace.Path)` in `cmd/arbitron/main.go:initDomain` (next to `filesystem.NewWorkspaceManager(cfg.Workspace.Path)`, main.go:814) and its e2e (`cmd/arbitron/group_files_e2e_test.go`) runs real git through it.
- Producers of every input: `Command.Args/Dir/GitDir/WorkTree/Env/Stdin` — built by `adapter/git/working_area.go` / `working_area_merge.go` (P3-T2/T3) from the workspace root (`cfg.Workspace.Path`), group-run ids and task/parent directories; `RunnerConfig` — zero value (or `StateDir`) from P3-T2's `NewWorkingArea`; process environment — `adapter/procenv/procenv.go:BaseEnv`.
- Provenance probes: `TestRunner_GitDirWorkTreeAndExtraEnvAreHonoured` fails if `Command.GitDir`/`WorkTree`/`Env` are dropped; `TestRunner_MissingGit_SearchPathWithoutGit` fails if resolution ignores the configured search PATH; `TestRunner_IgnoresUserConfigHooksAndIdentity` fails if the isolated environment or `-c` overrides are not applied.

### Task 1: Bounded, isolated git runner
**Files:**
- Create: `adapter/git/runner.go`
- Create: `adapter/git/procgroup_unix.go`
- Create: `adapter/git/procgroup_windows.go`
- Test:   `adapter/git/runner_test.go` (package `git`, testify — same style as `adapter/process/runner_test.go:1-87`)

No existing file is modified. `adapter/process` stays exactly as it is (its executors keep their own runner).

Design notes (pinned by the tests):
- Isolation is two-layered. Environment: `procenv.BaseEnv()` minus `HOME`, `LANG`, `LC_*`, `CLAUDE_CONFIG_DIR`, `ANTHROPIC_BASE_URL`, plus `HOME=<state>/home`, `XDG_CONFIG_HOME=<state>/xdg`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_TERMINAL_PROMPT=0`, fixed author/committer, `LANG=C`, `LC_ALL=C` (stable messages). Command line: the `isolationConfig` `-c` pairs, which outrank every config file — including the author's own repository config (a repository-level `core.hooksPath` is overridden; the test proves it).
- `Resolve` searches `SearchPath` (default: the PATH that `BaseEnv` hands subprocesses) for an executable `git` on every call, so the error message names exactly what was searched and an operator who installs git needs no restart.
- The output cap is an ERROR, not silent truncation (unlike `adapter/process/limit.go`), because the working area parses `ls-tree`/`ls-files` output.

- [x] Step 1: Write the failing tests. Create `adapter/git/runner_test.go`:

```go
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
		return syscall.Kill(pid, 0) == syscall.ESRCH
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
```

- [x] Step 2: Run them; confirm they fail to compile.

Confirmed `go test ./adapter/git/ -count=1` fails with undefined `Runner`, `NewRunner`, `RunnerConfig`, `Command`, and `ExitError` before implementation.

```bash
go test ./adapter/git/ -count=1
```
Expected:
```
# github.com/arbitron/arbitron/adapter/git [github.com/arbitron/arbitron/adapter/git.test]
adapter/git/runner_test.go:37:35: undefined: Runner
adapter/git/runner_test.go:39:9: undefined: NewRunner
adapter/git/runner_test.go:39:19: undefined: RunnerConfig
adapter/git/runner_test.go:51:57: undefined: Command
...
FAIL	github.com/arbitron/arbitron/adapter/git [build failed]
```

- [x] Step 3: Implement the process-group helpers. Create `adapter/git/procgroup_unix.go`:

```go
//go:build !windows

package git

import (
	"os/exec"
	"syscall"
)

// setProcGroup starts git in its own process group so a timeout or
// cancellation can kill git and every helper it spawned (hooks are disabled,
// but aliases, merge drivers and pack helpers are still child processes).
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup sends SIGKILL to the whole process group (negative pid).
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
```

Create `adapter/git/procgroup_windows.go`:

```go
//go:build windows

package git

import "os/exec"

// setProcGroup is a no-op on Windows.
func setProcGroup(_ *exec.Cmd) {}

// killProcGroup kills the git process directly on Windows.
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
```

- [x] Step 4: Implement the runner. Create `adapter/git/runner.go`:

The supplied runner and platform helpers pass all 15 initial tests against real git, including stdin/index provenance, isolated hooks/identity, bounded output, and descendant cancellation.

```go
// Package git implements the parallel-group working-area port
// (engine.WorkingArea) on top of the git command-line tool. Every git process
// is started through Runner, which bounds it (timeout, cancellation, output
// cap, process-group kill) and isolates it from the host's and the author's
// git configuration.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
)

// ErrGitUnavailable reports that no usable git executable could be found.
// It is checked at USE (every Run resolves the binary), never cached, so an
// operator who installs git does not need to restart the server.
var ErrGitUnavailable = errors.New("git executable is not available on this host")

// ErrOutputLimit reports that a git command produced more output than the
// runner's cap. Output is never silently truncated for callers: a command
// whose stdout is parsed must not be parsed from a partial listing.
var ErrOutputLimit = errors.New("git output exceeded the configured limit")

const (
	// DefaultTimeout bounds one git process when RunnerConfig.Timeout is unset.
	DefaultTimeout = 5 * time.Minute
	// DefaultMaxOutputBytes caps each of stdout and stderr per process.
	DefaultMaxOutputBytes = 64 << 20 // 64 MiB: large tree listings stay parseable
)

// RunnerConfig configures a Runner. The zero value is valid.
type RunnerConfig struct {
	// GitPath is an explicit git binary. "" looks "git" up on SearchPath at
	// every use.
	GitPath string
	// SearchPath is the PATH-style list searched for "git" when GitPath is "".
	// "" uses the PATH every subprocess receives (procenv.BaseEnv: the
	// engine's PATH plus procenv.StandardBinDirs).
	SearchPath string
	// Timeout bounds one git process. <= 0 uses DefaultTimeout.
	Timeout time.Duration
	// MaxOutputBytes caps each output stream. <= 0 uses DefaultMaxOutputBytes.
	MaxOutputBytes int
	// StateDir is a private directory used as HOME and XDG_CONFIG_HOME for
	// every git process so no user-level configuration is ever read. "" creates
	// one under os.TempDir() on first use.
	StateDir string
}

// Command is one git invocation. Args are the subcommand and its arguments
// (for example {"write-tree"}); the runner prepends the isolation flags.
type Command struct {
	Args     []string
	Dir      string        // working directory; "" inherits the server's
	GitDir   string        // passed as --git-dir=<GitDir> when non-empty
	WorkTree string        // passed as --work-tree=<WorkTree> when non-empty
	Stdin    []byte        // nil = no stdin
	Env      []string      // extra "K=V" entries appended last (e.g. GIT_INDEX_FILE)
	Timeout  time.Duration // overrides RunnerConfig.Timeout when > 0
}

// Result is the captured outcome of a git process.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExitError is returned when git ran to completion with a non-zero status.
type ExitError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 2048 {
		msg = msg[:2048] + "..."
	}
	return fmt.Sprintf("git %s: exit status %d: %s", strings.Join(e.Args, " "), e.ExitCode, msg)
}

// IsExitCode reports whether err is an *ExitError with the given status.
func IsExitCode(err error, code int) bool {
	var ee *ExitError
	return errors.As(err, &ee) && ee.ExitCode == code
}

// isolationConfig is passed as "-c" pairs ahead of every subcommand. Command
// line configuration outranks every config file, so neither an author's
// repository config nor a host's config can enable hooks, signing, filters,
// line-ending conversion or fsmonitor for operations the engine performs.
var isolationConfig = []string{
	"core.hooksPath=" + os.DevNull,
	"core.autocrlf=false",
	"core.safecrlf=false",
	"core.fileMode=true",
	"core.symlinks=true",
	"core.quotePath=false",
	"core.fsmonitor=false",
	"core.untrackedCache=false",
	"core.excludesFile=" + os.DevNull,
	"core.attributesFile=" + os.DevNull,
	"commit.gpgSign=false",
	"tag.gpgSign=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"rerere.enabled=false",
	"merge.conflictStyle=merge",
	"init.defaultBranch=arbitron",
	"advice.detachedHead=false",
	"safe.directory=*",
	"protocol.file.allow=always",
	"user.name=Arbitron",
	"user.email=arbitron@localhost",
}

// Runner starts bounded, isolated git processes. Safe for concurrent use.
type Runner struct {
	cfg RunnerConfig

	stateOnce sync.Once
	stateDir  string
	stateErr  error
}

// NewRunner returns a Runner for cfg.
func NewRunner(cfg RunnerConfig) *Runner {
	return &Runner{cfg: cfg}
}

// Resolve returns the git binary that the next Run would execute, or an error
// wrapping ErrGitUnavailable.
func (r *Runner) Resolve() (string, error) {
	if r.cfg.GitPath != "" {
		info, err := os.Stat(r.cfg.GitPath)
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%w: configured git path %q is not an executable file", ErrGitUnavailable, r.cfg.GitPath)
		}
		return r.cfg.GitPath, nil
	}
	search := r.cfg.SearchPath
	if search == "" {
		search = envValue(procenv.BaseEnv(), "PATH")
	}
	for _, dir := range filepath.SplitList(search) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, "git")
		info, err := os.Stat(cand)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%w: no \"git\" executable on search path %q; install git on the server host", ErrGitUnavailable, search)
}

// Version runs "git --version" and returns its trimmed output.
func (r *Runner) Version(ctx context.Context) (string, error) {
	res, err := r.Run(ctx, Command{Args: []string{"--version"}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// Run executes one git command. A non-zero exit returns the populated Result
// together with an *ExitError. Timeout and cancellation kill the whole process
// group and return an error wrapping context.DeadlineExceeded or
// context.Canceled. Output beyond the cap returns an error wrapping
// ErrOutputLimit (Result then holds the first MaxOutputBytes of each stream).
func (r *Runner) Run(ctx context.Context, c Command) (Result, error) {
	if len(c.Args) == 0 {
		return Result{}, errors.New("git: empty command")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("git %s: %w", c.Args[0], err)
	}
	bin, err := r.Resolve()
	if err != nil {
		return Result{}, err
	}
	env, err := r.environment(c.Env)
	if err != nil {
		return Result{}, err
	}

	timeout := r.cfg.Timeout
	if c.Timeout > 0 {
		timeout = c.Timeout
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	limit := r.cfg.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := make([]string, 0, 2*len(isolationConfig)+len(c.Args)+2)
	for _, kv := range isolationConfig {
		args = append(args, "-c", kv)
	}
	if c.GitDir != "" {
		args = append(args, "--git-dir="+c.GitDir)
	}
	if c.WorkTree != "" {
		args = append(args, "--work-tree="+c.WorkTree)
	}
	args = append(args, c.Args...)

	cmd := exec.Command(bin, args...)
	cmd.Env = env
	if c.Dir != "" {
		cmd.Dir = c.Dir
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	setProcGroup(cmd)

	stdout := &cappedBuffer{limit: limit}
	stderr := &cappedBuffer{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("%w: %v", ErrGitUnavailable, err)
		}
		return Result{}, fmt.Errorf("git %s: start: %w", c.Args[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-execCtx.Done():
		killProcGroup(cmd)
		<-done
		res := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}
		return res, fmt.Errorf("git %s: %w", c.Args[0], execCtx.Err())
	case waitErr := <-done:
		res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if waitErr != nil {
			var ee *exec.ExitError
			if !errors.As(waitErr, &ee) {
				return res, fmt.Errorf("git %s: %w", c.Args[0], waitErr)
			}
			res.ExitCode = ee.ExitCode()
		}
		if stdout.overflow || stderr.overflow {
			return res, fmt.Errorf("git %s: %w (%d bytes per stream)", c.Args[0], ErrOutputLimit, limit)
		}
		if res.ExitCode != 0 {
			return res, &ExitError{Args: c.Args, ExitCode: res.ExitCode, Stderr: res.Stderr}
		}
		return res, nil
	}
}

// environment builds the isolated environment: the procenv base allowlist
// (never the engine's secrets) minus every user-identity / locale variable,
// plus a private HOME/XDG_CONFIG_HOME and the switches that disable the
// system and global config files.
func (r *Runner) environment(extra []string) ([]string, error) {
	state, err := r.state()
	if err != nil {
		return nil, err
	}
	home := filepath.Join(state, "home")
	xdg := filepath.Join(state, "xdg")
	drop := map[string]bool{
		"HOME": true, "LANG": true, "CLAUDE_CONFIG_DIR": true, "ANTHROPIC_BASE_URL": true,
	}
	base := procenv.BaseEnv()
	env := make([]string, 0, len(base)+16+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] || strings.HasPrefix(k, "LC_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+xdg,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Arbitron",
		"GIT_AUTHOR_EMAIL=arbitron@localhost",
		"GIT_COMMITTER_NAME=Arbitron",
		"GIT_COMMITTER_EMAIL=arbitron@localhost",
		"LANG=C",
		"LC_ALL=C",
	)
	env = append(env, extra...)
	return env, nil
}

func (r *Runner) state() (string, error) {
	r.stateOnce.Do(func() {
		dir := r.cfg.StateDir
		if dir == "" {
			dir, r.stateErr = os.MkdirTemp("", "arbitron-git-")
			if r.stateErr != nil {
				r.stateErr = fmt.Errorf("git: create private state dir: %w", r.stateErr)
				return
			}
		}
		for _, sub := range []string{"home", "xdg"} {
			if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
				r.stateErr = fmt.Errorf("git: create private state dir: %w", err)
				return
			}
		}
		r.stateDir = dir
	})
	return r.stateDir, r.stateErr
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// cappedBuffer keeps the first limit bytes and records whether more arrived.
// It always reports a full write so exec's copy goroutine never errors.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := b.limit - b.buf.Len()
	if room <= 0 {
		if n > 0 {
			b.overflow = true
		}
		return n, nil
	}
	if len(p) > room {
		p = p[:room]
		b.overflow = true
	}
	b.buf.Write(p)
	return n, nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
```

- [x] Step 5: Run the tests, the race detector, gofmt and vet.

Completed all specified checks: 18 tests pass against real git (no skips), the focused race suite passes, and formatting/vet/diff checks are clean. Neighboring `adapter/process` and `adapter/procenv` tests pass unchanged. The Windows test binary cross-compiles; Windows runtime execution was not available on this macOS host.

Self-review completed in order:
1. **Spec compliance:** kept the runner-only slice and unchanged process adapter. A new credential-helper sink regression failed because repository configuration executed a helper; adding the empty command-line `credential.helper` override made it green. A relative-binary regression failed when `Command.Dir` changed; absolute, platform-aware executable resolution fixed both configured paths and search-path entries. Replaced the Unix-only `syscall.Kill` test call with the portable process API after the Windows compile check failed. No new configuration/API was added. The planned live consumer and composition-root sink remain P3-T2/P3-T11, as specified in this task's provenance section.
2. **Anti-green-theater:** real git consumes stdin, git-dir, work-tree and the custom index environment, with assertions on the resulting hash/index/files and real hook/helper marker files. Temporarily dropping `Command.Env` made the provenance test fail at the missing custom index sink; restored the input and reran green. No mutation remains. Timeout tests assert the spawned descendant is gone, not merely that Run returned.
3. **Code quality:** reviewed neighboring process/procenv patterns and the platform helpers; clarified package/config comments to describe actual responsibilities. Reran the full focused suite and race detector after review, plus gofmt, vet, Windows cross-compilation and neighboring package tests; all pass.

```bash
go test ./adapter/git/ -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go test ./adapter/git/ -count=1 -race
gofmt -l adapter/git/
go vet ./adapter/git/
```
Expected (every test listed passes; `gofmt -l` and `go vet` print nothing):
```
--- PASS: TestRunner_VersionHappyPath (0.0Xs)
--- PASS: TestRunner_StdinAndStdout (0.0Xs)
--- PASS: TestRunner_GitDirWorkTreeAndExtraEnvAreHonoured (0.1Xs)
--- PASS: TestRunner_NonZeroExitIsExitError (0.0Xs)
--- PASS: TestRunner_IgnoresUserConfigHooksAndIdentity (0.1Xs)
--- PASS: TestRunner_MissingGit_ExplicitPath (0.00s)
--- PASS: TestRunner_MissingGit_SearchPathWithoutGit (0.00s)
--- PASS: TestRunner_ResolveIsNotCached (0.00s)
--- PASS: TestRunner_TimeoutKillsProcessGroup (0.3Xs)
--- PASS: TestRunner_CommandTimeoutOverridesConfig (0.2Xs)
--- PASS: TestRunner_CancelKillsProcess (0.2Xs)
--- PASS: TestRunner_AlreadyCancelledContextDoesNotStart (0.00s)
--- PASS: TestRunner_OutputCapIsAnError (0.1Xs)
--- PASS: TestRunner_EmptyCommandRejected (0.00s)
--- PASS: TestCappedBuffer_ReportsFullWritesAndOverflow (0.00s)
ok  	github.com/arbitron/arbitron/adapter/git	1.XXXs
ok  	github.com/arbitron/arbitron/adapter/git	2.XXXs
```
(On a host without git, the git-dependent tests report `--- SKIP` with "git binary not installed on this host"; `TestRunner_MissingGit_*`, `TestRunner_AlreadyCancelledContextDoesNotStart`, `TestRunner_EmptyCommandRejected` and `TestCappedBuffer_*` still run and pass.)

- [x] Step 6: Commit.

Committed the runner, platform helpers, tests and this task's TDD/verification record using the task's commit title.

```bash
git add adapter/git/runner.go adapter/git/procgroup_unix.go adapter/git/procgroup_windows.go adapter/git/runner_test.go
git commit -m "feat(git): adapter-local bounded, isolated git runner with ErrGitUnavailable" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
