package codex

import (
	"context"
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
// which holds arbitron.db, config.yaml, and other secrets. Same invariant as
// claudecli.ErrWorkDirRequired and opencode.ErrWorkDirRequired; the codex
// adapter enforces it before launch.
var ErrWorkDirRequired = fmt.Errorf("codex: WorkDir is required")

// Config configures a codex AgentProvider.
//
// The CommandRunner seam is REUSED from adapter/claudecli rather than redefined,
// so all CLI adapters share one subprocess contract and one fake.
//
// Unlike opencode.Config there is NO AllowAllTools knob: `codex exec` runs under
// the fixed v1 fully-open posture (--dangerously-bypass-approvals-and-sandbox,
// see args.go), documented in the spec's risk section. And unlike opencode there
// is no per-provider credential to inject — codex is subscription-only (env.go),
// so Binding.RevealKey is never consulted.
type Config struct {
	// Binding is the resolved backend selection. Only Binding.Model is consumed
	// (the codex model id); the codex path is subscription-only, so RevealKey /
	// BaseURL / provider-keyed env are deliberately NOT used (env.go).
	Binding provider.ResolvedBinding
	// BinaryPath is the codex binary; "" resolves from PATH / StandardBinDirs.
	BinaryPath string
	// Runner is the subprocess seam; nil uses the shared production os/exec runner
	// (claudecli.NewExecRunner).
	Runner claudecli.CommandRunner
	// Timeout is the default per-mission timeout; <=0 means no default.
	Timeout time.Duration
	// MaxResponseBytes caps per-request stdout bytes; 0 means no cap.
	MaxResponseBytes int64
}

// core holds the construction inputs and version-probe memo shared by the batch
// Provider and the streaming StreamProvider: the resolved binding/binary, the
// runner seam, the per-call timeout, the stdout cap, and the once-only
// compatibility probe. Both adapters share ONE Config, one launch prelude, and
// one probe seam, so they embed this rather than hand-syncing seven identical
// fields plus a byte-for-byte ensureCompatible.
//
// This mirrors the claudecli precedent (adapter/claudecli/chat_adapter.go's
// adapterCore, embedded in ChatAdapter/AgentAdapter/StreamAgentAdapter): the
// shared lifecycle lives in one struct + constructor, not in parallel copies.
type core struct {
	binding          provider.ResolvedBinding
	binPath          string
	runner           claudecli.CommandRunner
	timeout          time.Duration
	maxResponseBytes int64

	// probeOnce memoizes the binary version probe across calls; probeErr retains
	// the verdict so a failure fails closed without re-probing.
	probeOnce sync.Once
	probeErr  error
}

// newCore builds the shared core from cfg. An empty BinaryPath is resolved from
// PATH and common install directories. A nil Runner uses the shared production
// os/exec runner. It is the SINGLE construction seam for both adapters.
func newCore(cfg Config) core {
	runner := cfg.Runner
	if runner == nil {
		runner = claudecli.NewExecRunner()
	}
	return core{
		binding:          cfg.Binding,
		binPath:          resolveBinary(cfg.BinaryPath),
		runner:           runner,
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
	}
}

// ensureCompatible runs the version probe at most once per core. A failure is
// memoized so every subsequent call fails closed without re-probing.
//
// SECURITY: it passes the subscription env (no API key) so the bare `codex
// --version` probe never inherits the engine's full parent env — the SAME no-key
// env codexEnv() builds for the real exec launch.
func (c *core) ensureCompatible(ctx context.Context) error {
	c.probeOnce.Do(func() {
		c.probeErr = probeVersion(ctx, c.runner, c.binPath, codexEnv())
	})
	return c.probeErr
}

// Provider implements engine.AgentProvider by driving `codex exec
// --dangerously-bypass-approvals-and-sandbox -m <model> --cd <WorkDir> --json`
// on the ChatGPT subscription: the mission prompt is delivered via stdin (never
// argv/logs), there is NO API key (the subscription auth.json under HOME /
// CODEX_HOME is codex's only credential surface), and a one-time version probe
// rejects an incompatible binary. Each mission is one independent `exec`
// invocation (no session resume), so AgentMission.SessionID is unused. See
// args.go, env.go, parse.go, and version.go.
type Provider struct {
	core
}

var _ engine.AgentProvider = (*Provider)(nil)

// New constructs a codex Provider from cfg. An empty BinaryPath is resolved from
// PATH and common install directories. A nil Runner uses the shared production
// os/exec runner.
func New(cfg Config) *Provider {
	return &Provider{core: newCore(cfg)}
}

// RunAgent runs a `codex exec` mission and folds its JSON event stream into an
// AgentResult. SessionID is never threaded in (one independent exec per
// mission); the codex thread id is surfaced OUT on the result for the trace.
//
// Strict ordering guarantees a fail-closed launch with no partial state:
// WorkDir guard → build args (fail-closed on empty model) → build subscription
// env → memoized version probe → run. An empty WorkDir or empty model produces
// ZERO subprocess launches (not even the version probe).
//
// SECURITY: the prompt is delivered via RunSpec.Stdin, never argv; the env
// carries NO OPENAI_API_KEY (env.go) — codex authenticates via its on-disk
// subscription credential.
func (p *Provider) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	if mission.WorkDir == "" {
		return nil, ErrWorkDirRequired
	}

	// Single model source: argv is built from p.binding.Model (via buildArgs), so
	// bind that ONE value here and use it for argv, every error string, AND the
	// result. mission.Model is deliberately NOT used for labels — if it ever
	// diverged from the binding, the result/errors would name a model codex never
	// ran, sending an operator chasing a phantom.
	model := p.binding.Model

	// Build args FIRST so an empty model fails closed with NO launch (never a
	// silent default-model run). buildArgs names the failure; we add the model
	// context here.
	args, err := buildArgs(mission, p.binding)
	if err != nil {
		return nil, fmt.Errorf("codex agent (model %q): %w", model, err)
	}

	// Build the subscription env BEFORE any subprocess. There is no key to reveal
	// (codex is subscription-only), so this cannot fail — but it is still built
	// before the probe so the launch ordering matches the other CLI adapters.
	env := codexEnv()

	// Version probe (memoized): an incompatible/missing binary fails closed here,
	// before the run launches.
	if err := p.ensureCompatible(ctx); err != nil {
		return nil, err
	}

	timeout := mission.Timeout
	if timeout <= 0 {
		timeout = p.timeout
	}
	runCtx, cancel := procenv.WithTimeout(ctx, timeout)
	defer cancel()

	spec := claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             args,
		Stdin:            mission.Prompt, // prompt via stdin — never argv (SECURITY)
		Dir:              mission.WorkDir,
		Env:              env,
		MaxResponseBytes: p.maxResponseBytes,
	}

	lines, wait, err := p.runner.Run(runCtx, spec)
	if err != nil {
		return nil, fmt.Errorf("codex: run (model %q): %w", model, err)
	}

	// Drain stdout fully BEFORE calling wait (CommandRunner contract).
	var collected []string
	for line := range lines {
		collected = append(collected, line)
	}
	if waitErr := wait(); waitErr != nil {
		// Prefer a context error when the run context was cancelled / timed out so
		// callers can errors.Is against context.Canceled/DeadlineExceeded.
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("codex: mission failed (model %q): %w", model, waitErr)
	}

	res, err := parseOutput(collected)
	if err != nil {
		return nil, fmt.Errorf("codex: parse output (model %q): %w", model, err)
	}
	engineSteps := make([]engine.AgentStep, 0, len(res.steps))
	for _, s := range res.steps {
		engineSteps = append(engineSteps, engine.AgentStep{Kind: s.Kind, Summary: s.Summary})
	}

	return &engine.AgentResult{
		Summary:   res.summary,
		Steps:     engineSteps,
		Model:     model,
		TokensIn:  res.tokensIn,
		TokensOut: res.tokensOut,
		SessionID: res.sessionID, // codex thread id (surfaced for the trace)
	}, nil
}

// resolveBinary returns the codex binary path. An explicit path is returned
// unchanged; an empty path triggers a PATH lookup then procenv.StandardBinDirs.
func resolveBinary(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p, err := exec.LookPath("codex"); err == nil {
		return p
	}
	for _, dir := range procenv.StandardBinDirs() {
		c := filepath.Join(dir, "codex")
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "codex"
}
