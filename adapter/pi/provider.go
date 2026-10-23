package pi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrWorkDirRequired is returned when a mission's WorkDir is empty.
//
// SECURITY: an empty cmd.Dir means "inherit the engine launch directory" —
// which holds arbitron.db, config.yaml and other secrets. pi has no --cd flag,
// so the process cwd IS the sandbox boundary and an empty one is unacceptable.
// Same invariant as claudecli/opencode/codex.
var ErrWorkDirRequired = errors.New("pi: WorkDir is required")

// ErrTurnFailed is returned when pi's event stream reports a failed turn.
//
// This is the adapter's defining error. pi exits 0 whether the turn succeeded
// or the provider refused the request — a quota refusal, an auth failure and a
// clean run are indistinguishable by exit code. The stream's stopReason is the
// ONLY authoritative signal, so this error is what a caller matches on to tell
// "pi ran and the model refused" from "pi itself broke".
var ErrTurnFailed = errors.New("pi: turn failed")

// Config configures a pi AgentProvider.
//
// The CommandRunner seam is REUSED from adapter/claudecli rather than redefined,
// so all CLI adapters share one subprocess contract and one fake.
type Config struct {
	// Binding is the resolved backend selection. Provider selects the credential
	// env var (env.go), Model routes argv, and Effort becomes --thinking.
	Binding provider.ResolvedBinding
	// BinaryPath is the pi binary; "" resolves from PATH / standard bin dirs.
	BinaryPath string
	// Runner is the subprocess seam; nil uses the shared production os/exec
	// runner (claudecli.NewExecRunner).
	Runner claudecli.CommandRunner
	// Timeout is the default per-mission timeout; <=0 means no default.
	Timeout time.Duration
	// MaxResponseBytes caps per-request stdout bytes; 0 means no cap.
	MaxResponseBytes int64
}

// Provider implements engine.AgentProvider by driving
// `pi --print --mode json --no-session --model <model> [--thinking <level>]`:
// the mission prompt is delivered via stdin (never argv), the provider key is
// injected via the child env only (env.go), the working directory is the
// subprocess cwd, and a one-time version probe rejects an incompatible binary.
//
// Each mission is one independent run (--no-session), so
// AgentMission.SessionID is unused; pi's own session id is surfaced OUT on the
// result for the trace.
type Provider struct {
	binding          provider.ResolvedBinding
	binPath          string
	runner           claudecli.CommandRunner
	timeout          time.Duration
	maxResponseBytes int64

	// probeOnce memoizes the binary version probe across calls; probeErr retains
	// the verdict so a failure fails closed without re-probing.
	probeOnce sync.Once
	probeErr  error
	version   []int
}

var _ engine.AgentProvider = (*Provider)(nil)

// New constructs a pi Provider from cfg. An empty BinaryPath is resolved from
// PATH and common install directories. A nil Runner uses the shared production
// os/exec runner.
func New(cfg Config) *Provider {
	runner := cfg.Runner
	if runner == nil {
		runner = claudecli.NewExecRunner()
	}
	return &Provider{
		binding:          cfg.Binding,
		binPath:          resolveBinary(cfg.BinaryPath),
		runner:           runner,
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
	}
}

// resolveBinary returns explicit when set, else finds `pi` on PATH, else falls
// back to common install locations (npm global bin, Homebrew), else the bare
// name so os/exec produces a clear "not found".
func resolveBinary(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p, err := exec.LookPath(binaryName); err == nil {
		return p
	}
	for _, dir := range procenv.StandardBinDirs() {
		candidate := filepath.Join(dir, binaryName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	return binaryName
}

// binaryName is the pi executable name.
const binaryName = "pi"

// ensureCompatible runs the version probe at most once. A failure is memoized
// so every subsequent call fails closed without re-probing.
//
// SECURITY: the probe runs with the allowlist-filtered base env (no credential),
// so a bare `pi --version` never inherits the engine's full parent env.
func (p *Provider) ensureCompatible(ctx context.Context) error {
	p.probeOnce.Do(func() {
		p.version, p.probeErr = probeVersion(ctx, p.runner, p.binPath, procenv.BaseEnv())
	})
	return p.probeErr
}

// RunAgent runs one pi mission and folds its JSON event stream into an
// AgentResult.
//
// Strict ordering guarantees a fail-closed launch with no partial state:
// WorkDir guard → build args (fail-closed on empty model / bad effort) → build
// credential env (fail-closed on unknown provider / reveal failure) → memoized
// version probe → run. Any of those failing produces ZERO subprocess launches.
//
// THE CRITICAL DIFFERENCE from the other CLI adapters: pi exits 0 even when the
// turn failed, so a zero exit is NOT success. The stream's verdict decides, and
// a failed turn becomes ErrTurnFailed. Pinned errors omit the untrusted
// provider message, which may contain credentials.
func (p *Provider) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	if mission.WorkDir == "" {
		return nil, ErrWorkDirRequired
	}

	// Single model source: argv is built from p.binding.Model, so bind that ONE
	// value here and use it for argv, every error string AND the result.
	// mission.Model is deliberately unused for labels — if it ever diverged, the
	// result and errors would name a model pi never ran.
	model := p.binding.Model

	args, err := buildArgs(p.binding)
	if err != nil {
		return nil, fmt.Errorf("pi agent (model %q): %w", model, err)
	}

	// Build the credential env BEFORE any subprocess: an unknown provider or a
	// reveal failure must not reach a launch.
	env, err := piEnv(ctx, p.binding)
	if err != nil {
		return nil, fmt.Errorf("pi agent (model %q): %w", model, err)
	}

	if err := p.ensureCompatible(ctx); err != nil {
		return nil, err
	}

	timeout := mission.Timeout
	if timeout <= 0 {
		timeout = p.timeout
	}
	runCtx, cancel := procenv.WithTimeout(ctx, timeout)
	defer cancel()

	lines, wait, err := p.runner.Run(runCtx, claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             args,
		Stdin:            mission.Prompt, // prompt via stdin — never argv (SECURITY)
		Dir:              mission.WorkDir,
		Env:              env,
		MaxResponseBytes: p.maxResponseBytes,
	})
	if err != nil {
		if p.binding.Pinned {
			err = claudecli.SanitizeProcessError(err)
		}
		return nil, fmt.Errorf("pi: run (model %q): %w", model, err)
	}

	// Drain stdout fully BEFORE calling wait (CommandRunner contract).
	var collected []string
	for line := range lines {
		collected = append(collected, line)
	}
	waitErr := wait()

	// Fold FIRST, even on a non-zero exit: the stream is where pi states what
	// actually happened, and a bare exit status explains nothing on its own.
	// A parse failure is only reported when the process exited cleanly —
	// otherwise the exit error is the more useful headline and garbled stdout
	// is a symptom, not the cause.
	res, parseErr := parseOutput(collected)

	if waitErr != nil {
		// A pinned run must not copy untrusted harness stderr into errors or
		// logs; it can carry account credentials. errors.Is still classifies.
		if p.binding.Pinned {
			waitErr = claudecli.SanitizeProcessError(waitErr)
		} else {
			claudecli.LogStderrTail("pi", waitErr)
		}
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("pi: mission timed out (model %q): %w (%v)", model, ctxErr, waitErr)
		}
		if !p.binding.Pinned && parseErr == nil && res.failureMsg != "" {
			return nil, fmt.Errorf("pi: mission failed (model %q): %s: %w", model, res.failureMsg, waitErr)
		}
		return nil, fmt.Errorf("pi: mission failed (model %q): %w", model, waitErr)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("pi: parse output (model %q): %w", model, parseErr)
	}

	// The exit code said success; the stream is the authority on whether the
	// turn actually did. Preserve the refusal verdict, but never include an
	// untrusted provider message in a pinned execution error.
	if res.failed {
		if p.binding.Pinned {
			return nil, fmt.Errorf("%w (model %q)", ErrTurnFailed, model)
		}
		if res.failureMsg != "" {
			return nil, fmt.Errorf("%w (model %q): %s", ErrTurnFailed, model, res.failureMsg)
		}
		return nil, fmt.Errorf("%w (model %q): pi reported a failed turn with no message", ErrTurnFailed, model)
	}

	if p.binding.Pinned {
		if err := res.verifyExecutedModel(p.binding); err != nil {
			return nil, err
		}
	}

	steps := make([]engine.AgentStep, 0, len(res.steps))
	for _, s := range res.steps {
		steps = append(steps, engine.AgentStep{Kind: s.Kind, Summary: s.Summary})
	}
	return &engine.AgentResult{
		Summary:   res.summary,
		Steps:     steps,
		Model:     model,
		TokensIn:  res.tokensIn,
		TokensOut: res.tokensOut,
		SessionID: res.sessionID,
	}, nil
}

// ProbeExecutedModel is the tool-disabled exact-model check of this agent route
// (D5): one turn through the same binary, credential env and --model/--thinking
// argv a real mission uses, plus the verified tool/extension/update restrictions
// in testdata/probe-restrictions.md, with prompt on stdin
// and workDir as cwd. It requires an explicit model and a pi whose tool-free
// flags are verified, and returns the binding's model and pi's session id only
// when every assistant message_end names the expected provider and model.
// Errors never carry pi output.
func (p *Provider) ProbeExecutedModel(ctx context.Context, prompt, workDir string) (string, string, error) {
	if workDir == "" {
		return "", "", ErrWorkDirRequired
	}
	args, err := buildArgs(p.binding)
	if err != nil {
		return "", "", err
	}
	args = append(args, noToolsFlag, noMCPFlag, noExtensionsFlag, offlineFlag)
	env, err := piEnv(ctx, p.binding)
	if err != nil {
		return "", "", fmt.Errorf("%w: credential env", ErrModelEvidence)
	}
	if err := p.ensureCompatible(ctx); err != nil {
		// Version stdout and runner errors can contain account/config secrets.
		// Discovery exposes only a static verdict, as it does for turn failures.
		return "", "", fmt.Errorf("%w: version check", ErrModelEvidence)
	}
	if compareVersion(p.version, mustParseVersion(minPiToolFreeVersion)) < 0 {
		return "", "", fmt.Errorf("%w: tool-free probe requires >= %s", ErrIncompatiblePi, minPiToolFreeVersion)
	}

	runCtx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()
	lines, wait, err := p.runner.Run(runCtx, claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             args,
		Stdin:            prompt,
		Dir:              workDir,
		Env:              env,
		MaxResponseBytes: p.maxResponseBytes,
	})
	if err != nil {
		return "", "", fmt.Errorf("%w: launch", ErrModelEvidence)
	}
	var collected []string
	for line := range lines {
		collected = append(collected, line)
	}
	if wait() != nil {
		return "", "", fmt.Errorf("%w: exit", ErrModelEvidence)
	}
	res, err := parseOutput(collected)
	if err != nil {
		return "", "", fmt.Errorf("%w: parse", ErrModelEvidence)
	}
	if res.failed {
		return "", "", ErrTurnFailed
	}
	if err := res.verifyExecutedModel(p.binding); err != nil {
		return "", "", err
	}
	// pi's json mode writes its session header even under --no-session.
	if res.sessionID == "" {
		return "", "", fmt.Errorf("%w: session", ErrModelEvidence)
	}
	return p.binding.Model, res.sessionID, nil
}
