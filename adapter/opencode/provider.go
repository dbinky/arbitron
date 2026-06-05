package opencode

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
	// AllowAllTools enables the open-by-default allow-all permission posture
	// (operator knob (a)). It is narrowed further by a per-profile AllowedTools
	// allowlist on the binding (knob (b)). The composition root sets this true
	// for open-by-default; the zero value (false) is a closed posture so a
	// caller that has not yet wired the knob still compiles and stays safe.
	AllowAllTools bool
}

// Provider implements engine.AgentProvider by driving the modern `opencode run
// <prompt> -m <provider>/<model>` contract: per-provider auth is injected via
// the subprocess env (never argv/logs), tool/permission selection follows an
// open-by-default posture, and a one-time version probe rejects an incompatible
// binary. Each mission is one independent `run` invocation (no session resume),
// so AgentMission.SessionID is unused. See args.go, posture.go, env.go,
// env_providers.go, and version.go.
type Provider struct {
	binding          provider.ResolvedBinding
	binPath          string
	runner           claudecli.CommandRunner
	timeout          time.Duration
	maxResponseBytes int64
	allowAllTools    bool

	// probeOnce memoizes the binary version probe across missions.
	probeOnce sync.Once
	probeErr  error
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
		allowAllTools:    cfg.AllowAllTools,
	}
}

// RunAgent runs a modern `opencode run` mission and folds its output into an
// AgentResult. SessionID is never threaded (one independent run per mission).
//
// Strict ordering guarantees a fail-closed launch with no partial state:
// WorkDir guard → build args (fail-closed on unknown provider / empty model) →
// build credential env (fail-closed on RevealKey/bedrock-config) → memoized
// version probe → run. A RevealKey failure, unknown provider, or empty model
// produces ZERO subprocess launches (not even the version probe).
func (p *Provider) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	if mission.WorkDir == "" {
		return nil, ErrWorkDirRequired
	}

	// Build args FIRST so an unknown provider / empty model fails closed with NO
	// launch (never a silent tool-less run). buildArgs names the provider in the
	// error; we add the node/model context here.
	args, err := buildArgs(mission, p.binding, buildPosture(p.binding, p.allowAllTools))
	if err != nil {
		return nil, fmt.Errorf("opencode agent (provider %q, model %q): %w", p.binding.Provider, mission.Model, err)
	}

	// Build the credential env BEFORE any subprocess so a RevealKey / config
	// failure aborts before launch (no partial-env, no probe).
	env, err := opencodeEnv(ctx, p.binding)
	if err != nil {
		return nil, fmt.Errorf("opencode: build env: %w", err)
	}

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

// ensureCompatible runs the version probe at most once per Provider. A failure
// is memoized so every subsequent mission fails closed without re-probing.
func (p *Provider) ensureCompatible(ctx context.Context) error {
	p.probeOnce.Do(func() {
		p.probeErr = probeVersion(ctx, p.runner, p.binPath)
	})
	return p.probeErr
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
