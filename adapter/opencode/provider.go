package opencode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// claudecli.ErrWorkDirRequired; the opencode adapter enforces it before launch.
var ErrWorkDirRequired = fmt.Errorf("opencode: WorkDir is required")

// Config configures an opencode AgentProvider.
//
// The CommandRunner seam is REUSED from adapter/claudecli rather than
// redefined, so both CLI adapters share one subprocess contract and one fake.
type Config struct {
	// Binding is the resolved backend selection: provider, base-url, model,
	// and the lazy RevealKey used to inject the credential at launch.
	Binding provider.ResolvedBinding
	// BinaryPath is the opencode binary; "" resolves from PATH / StandardBinDirs.
	BinaryPath string
	// Runner is the subprocess seam; nil uses the shared production os/exec
	// runner (claudecli.NewExecRunner).
	Runner claudecli.CommandRunner
	// Timeout is the default per-mission timeout; <=0 means no default.
	Timeout time.Duration
	// MaxResponseBytes caps per-request stdout bytes; 0 means no cap.
	MaxResponseBytes int64
}

// Provider implements engine.AgentProvider by driving the `opencode` CLI in
// single-shot headless mode (`opencode -p <prompt> -c <cwd> -q -f text`).
//
// SINGLE-SHOT CONTRACT (spike finding, spec §9 fallback): the installed
// opencode v0.0.55 exposes no headless session-resume primitive, so each
// mission is one independent invocation and AgentMission.SessionID is unused.
// MULTI-PROVIDER LIMITATION: this build has no `--model`/provider-routing flag
// and authenticates Anthropic via ANTHROPIC_API_KEY; open-weight routing is
// limited on this binary. See env_providers.go and args.go.
type Provider struct {
	binding          provider.ResolvedBinding
	binPath          string
	runner           claudecli.CommandRunner
	timeout          time.Duration
	maxResponseBytes int64
}

var _ engine.AgentProvider = (*Provider)(nil)

// New constructs an opencode Provider from cfg. An empty BinaryPath is resolved
// from PATH and common install directories. A nil Runner uses the shared
// production os/exec runner.
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

// RunAgent runs a single-shot opencode mission and folds its output into an
// AgentResult. SessionID is never threaded (single-shot contract).
func (p *Provider) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	if mission.WorkDir == "" {
		return nil, ErrWorkDirRequired
	}

	// Build the credential env FIRST so a RevealKey / unknown-provider failure
	// aborts before any subprocess launch (no partial-env launch).
	env, err := opencodeEnv(ctx, p.binding)
	if err != nil {
		return nil, fmt.Errorf("opencode: build env: %w", err)
	}

	timeout := mission.Timeout
	if timeout <= 0 {
		timeout = p.timeout
	}
	runCtx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	spec := claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             buildArgs(mission),
		Dir:              mission.WorkDir,
		Env:              env,
		MaxResponseBytes: p.maxResponseBytes,
	}

	lines, wait, err := p.runner.Run(runCtx, spec)
	if err != nil {
		return nil, fmt.Errorf("opencode: run: %w", err)
	}

	// Drain stdout fully BEFORE calling wait (CommandRunner contract).
	var collected []string
	for line := range lines {
		collected = append(collected, line)
	}
	if waitErr := wait(); waitErr != nil {
		// Prefer a context error when the run context was cancelled / timed out
		// so callers can errors.Is against context.Canceled/DeadlineExceeded.
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("opencode: mission failed: %w", waitErr)
	}

	summary, steps, tokensOut := parseOutput(collected)
	engineSteps := make([]engine.AgentStep, 0, len(steps))
	for _, s := range steps {
		engineSteps = append(engineSteps, engine.AgentStep{Kind: s.Kind, Summary: s.Summary})
	}

	return &engine.AgentResult{
		Summary:   summary,
		Steps:     engineSteps,
		Model:     mission.Model,
		TokensOut: tokensOut,
		// TokensIn left zero (coarse subprocess accounting; text mode emits no
		// structured usage). SessionID empty per the single-shot contract.
	}, nil
}

// withTimeout derives a context honouring timeout. A non-positive timeout
// yields the parent ctx unchanged with a no-op cancel. Mirrors claudecli's.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// resolveBinary returns the opencode binary path. An explicit path is returned
// unchanged; an empty path triggers a PATH lookup then procenv.StandardBinDirs.
func resolveBinary(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p, err := exec.LookPath("opencode"); err == nil {
		return p
	}
	for _, dir := range procenv.StandardBinDirs() {
		c := filepath.Join(dir, "opencode")
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "opencode"
}
