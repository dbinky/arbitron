// Package git provides bounded, isolated git execution for parallel-group
// working areas. Runner applies timeout, cancellation, output limits and
// process-group termination to each command.
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
// repository config nor a host's config can enable hooks, signing, credential
// helpers, line-ending conversion or fsmonitor for these operations.
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
	"credential.helper=",
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
		bin, err := executablePath(r.cfg.GitPath)
		if err != nil {
			return "", fmt.Errorf("%w: configured git path %q is not an executable file: %v", ErrGitUnavailable, r.cfg.GitPath, err)
		}
		return bin, nil
	}
	search := r.cfg.SearchPath
	if search == "" {
		search = envValue(procenv.BaseEnv(), "PATH")
	}
	for _, dir := range filepath.SplitList(search) {
		if dir == "" {
			continue
		}
		bin, err := executablePath(filepath.Join(dir, "git"))
		if err == nil {
			return bin, nil
		}
	}
	return "", fmt.Errorf("%w: no \"git\" executable on search path %q; install git on the server host", ErrGitUnavailable, search)
}

// executablePath resolves relative paths before the subprocess changes directory.
// LookPath applies the platform's executable rules, including .exe on Windows.
func executablePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return exec.LookPath(abs)
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
