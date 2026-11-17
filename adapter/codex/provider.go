package codex

import (
	"context"
	"fmt"
	"log/slog"
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
// see args.go), documented in the spec's risk section.
// LunaRoute uses an isolated keyed Responses provider; every other route keeps
// the existing subscription environment without consulting Binding.RevealKey.
type Config struct {
	// Binding selects the model and, for LunaRoute only, the guarded credential
	// and hosted endpoint. Other providers retain subscription-only behavior.
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
	// CapabilityRules drives the per-call effort emission (profile-editing Phase
	// 3): the binding's resolved Effort is appended as `-c
	// model_reasoning_effort=<v>` via this ruleset. A zero-value (Empty) ruleset
	// emits nothing, so a codex adapter constructed without it behaves exactly as
	// before. Context is inapplicable to codex (no context knob) and never emits.
	CapabilityRules provider.CapabilityRules
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
	// rules drives the per-call effort emission appended to argv (profile-editing
	// Phase 3). A zero-value (Empty) ruleset emits nothing.
	rules provider.CapabilityRules

	// probeOnce memoizes the binary version probe across calls; probeErr retains
	// the verdict so a failure fails closed without re-probing.
	probeOnce sync.Once
	probeErr  error
	version   []int
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
		rules:            cfg.CapabilityRules,
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
		c.version, c.probeErr = compatibleVersion(ctx, c.runner, c.binPath, codexEnv())
	})
	return c.probeErr
}

// Provider implements engine.AgentProvider by driving `codex exec
// --dangerously-bypass-approvals-and-sandbox -m <model> --cd <WorkDir> --json`
// on a subscription or an isolated keyed LunaRoute Responses route. The mission
// prompt is delivered via stdin (never argv/logs), and a one-time compatibility
// probe rejects an incompatible binary. Each mission is one independent `exec`
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
// LunaRoute preflight → WorkDir guard → args → env/private home → memoized
// version probe → run → correlated rollout verification → cleanup. Non-LunaRoute
// launches preserve the subscription path. Empty WorkDir/model launches nothing.
// The inference key is never passed to the version subprocess or in argv.
func (p *Provider) RunAgent(ctx context.Context, mission engine.AgentMission) (result *engine.AgentResult, err error) {
	lunaroute := provider.IsLunaRoute(p.binding.Provider)
	if lunaroute {
		defer func() { err = provider.SanitizeLunaRouteError(err) }()
		if mission.Model != "" && mission.Model != p.binding.Model {
			return nil, provider.LocalLunaRouteError("model selection", ErrModelEvidence)
		}
		if err := provider.CheckLunaRouteAdapterLaunch(p.rules, p.binding, 0); err != nil {
			return nil, provider.LocalLunaRouteError("preflight", err)
		}
	}
	if mission.WorkDir == "" {
		if lunaroute {
			// Local, so the agent wrapper does not probe the gateway for it.
			return nil, provider.LocalLunaRouteError("workdir", ErrWorkDirRequired)
		}
		return nil, ErrWorkDirRequired
	}

	// Single model source: argv is built from p.binding.Model (via buildArgs), so
	// bind that ONE value here and use it for argv, every error string, AND the
	// result. mission.Model is deliberately NOT used for labels — if it ever
	// diverged from the binding, the result/errors would name a model codex never
	// ran, sending an operator chasing a phantom.
	model := p.binding.Model

	// Build args FIRST so an empty model fails closed with NO launch (never a
	// silent default-model run). buildArgsWithRules names the failure; we add the
	// model context here. The binding's resolved Effort rides argv as `-c
	// model_reasoning_effort=<v>` (profile-editing Phase 3); a default/empty effort
	// or an Empty ruleset appends nothing.
	var args, env []string
	if lunaroute {
		extra, xerr := lunaRouteExecExtra(p.binding)
		if xerr != nil {
			return nil, provider.LocalLunaRouteError("arguments", xerr)
		}
		args, err = buildExecArgs(model, mission.WorkDir, extra)
		if err != nil {
			return nil, provider.LocalLunaRouteError("arguments", err)
		}
		launch, lerr := prepareLunaRouteLaunch(ctx, p.binding)
		if lerr != nil {
			return nil, lerr
		}
		// Rollout evidence below is read before this private home is removed.
		// A removal failure is logged and never changes the run's outcome: a
		// finished run keeps its result, and an opaque launch failure must stay
		// diagnosable rather than be marked local by the cleanup error. The home
		// holds no credential (the key travels only in the process env).
		defer func() {
			if cleanupErr := launch.cleanup(); cleanupErr != nil {
				slog.Warn("codex: lunaroute private home cleanup failed; leaving it behind",
					"home", launch.home, "error", provider.SanitizeLunaRouteError(cleanupErr).Error())
			}
		}()
		env = launch.env
		if err := p.ensureLunaRouteCompatible(ctx, env); err != nil {
			return nil, err
		}
	} else {
		args, err = buildArgsWithRules(mission, p.binding, p.rules)
		if err != nil {
			return nil, fmt.Errorf("codex agent (model %q): %w", model, err)
		}
		// Subscription mode has no per-provider reveal and preserves CODEX_HOME.
		env = codexEnv()
		if err := p.ensureCompatible(ctx); err != nil {
			return nil, err
		}
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
		if p.binding.Pinned || lunaroute {
			err = claudecli.SanitizeProcessError(err)
		}
		return nil, fmt.Errorf("codex: run (model %q): %w", model, err)
	}

	// Drain stdout fully BEFORE calling wait (CommandRunner contract).
	var collected []string
	for line := range lines {
		collected = append(collected, line)
	}
	if waitErr := wait(); waitErr != nil {
		if p.binding.Pinned || lunaroute {
			waitErr = claudecli.SanitizeProcessError(waitErr)
		} else {
			claudecli.LogStderrTail("codex", waitErr)
		}
		// Keep cancellation classifiable alongside the process failure. Pinned
		// calls retain only the sanitized process error.
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("codex: mission timed out (model %q): %w (%v)", model, ctxErr, waitErr)
		}
		// A non-zero exit usually states its REAL cause on the --json stdout
		// stream (an `error`/`turn.failed` event — e.g. a quota refusal,
		// "You've hit your usage limit"), while stderr carries only incidental
		// noise that the redacted summary then presents as if it were the
		// reason. Recover that cause so a refusal is distinguishable from a
		// crash instead of collapsing to a bare "exit status 1". Best-effort:
		// when the stream states no cause the exit error stands alone, and
		// waitErr stays wrapped either way so callers keep unwrapping it.
		// A pinned run must not copy untrusted harness messages into errors;
		// they can include account credentials or tool output.
		if p.binding.Pinned || lunaroute {
			return nil, fmt.Errorf("codex: mission failed (model %q): %w", model, waitErr)
		}
		if cause := failureCause(collected); cause != "" {
			return nil, fmt.Errorf("codex: mission failed (model %q): %s: %w", model, cause, waitErr)
		}
		return nil, fmt.Errorf("codex: mission failed (model %q): %w", model, waitErr)
	}

	res, err := parseOutput(collected)
	if err != nil {
		return nil, fmt.Errorf("codex: parse output (model %q): %w", model, err)
	}
	var evidence []engine.ModelEvidence
	if p.binding.Pinned || lunaroute {
		if res.failed || !res.completed || lunaroute && !res.finalTurnCompleted {
			return nil, fmt.Errorf("%w: batch turn did not complete successfully", ErrModelEvidence)
		}
		verifier, err := newRolloutVerifier(env)
		if err != nil {
			if lunaroute {
				return nil, provider.LocalLunaRouteError("rollout evidence", err)
			}
			return nil, err
		}
		if lunaroute {
			// The final turn completed on the gateway; a private rollout that
			// cannot prove the route is a local harness fault, so it carries the
			// local marker and the diagnosis wrapper does not probe.
			evidence, err = verifier.lunaRouteAfterTurn(res.sessionID, model)
			if err != nil {
				return nil, provider.LocalLunaRouteError("rollout evidence", err)
			}
		} else if err := verifier.afterTurn(res.sessionID, model, 0); err != nil {
			return nil, err
		}
	}
	engineSteps := make([]engine.AgentStep, 0, len(res.steps))
	for _, s := range res.steps {
		engineSteps = append(engineSteps, engine.AgentStep{Kind: s.Kind, Summary: s.Summary})
	}

	return &engine.AgentResult{
		ModelEvidence: evidence,
		Summary:       res.summary,
		Steps:         engineSteps,
		Model:         model,
		TokensIn:      res.tokensIn,
		TokensOut:     res.tokensOut,
		SessionID:     res.sessionID, // codex thread id (surfaced for the trace)
	}, nil
}

// ProbeExecutedModel is the tool-restricted exact-model check of this agent route
// (D4): one `codex exec` turn through the same binary, subscription env and
// model/effort argv a real mission uses, under the read-only sandbox instead of
// the bypass posture, with workDir as cwd. A model absent from the account's
// models_cache.json is refused before launch (D4). It requires an explicit model and
// returns the binding's model and the codex thread id only when that thread's
// own stored rollout records exactly that model for every new turn. Errors never carry codex
// output.
func (p *Provider) ProbeExecutedModel(ctx context.Context, _ string, workDir string) (string, string, error) {
	ctx, release := context.WithTimeout(procenv.WithProbeResponseLimit(ctx), procenv.ProbeTimeout)
	defer release()
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if workDir == "" {
		return "", "", ErrWorkDirRequired
	}
	if provider.IsLunaRoute(p.binding.Provider) {
		return "", "", fmt.Errorf("%w: LunaRoute codex routes are pinned-only and have no usability probe", ErrModelEvidence)
	}
	model := p.binding.Model
	args, err := buildProbeArgs(p.binding, p.rules, workDir)
	if err != nil {
		return "", "", err
	}
	env := codexEnv()
	// D4: probe only a model the account lists, before any launch.
	if err := accountListsModel(env, model); err != nil {
		return "", "", err
	}
	if err := p.ensureCompatible(ctx); err != nil {
		return "", "", err
	}
	runCtx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()
	override, err := p.probeMCPOverride(runCtx, workDir)
	if err != nil {
		return "", "", err
	}
	args = append(args, "-c", override)
	lines, wait, err := p.runner.Run(runCtx, claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             args,
		Stdin:            probeIdentityPrompt,
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
	if res.failed || !res.completed {
		return "", "", fmt.Errorf("%w: probe turn did not complete successfully", ErrModelEvidence)
	}
	verifier, err := newRolloutVerifier(env)
	if err != nil {
		return "", "", err
	}
	if err := verifier.afterTurn(res.sessionID, model, 0); err != nil {
		return "", "", err
	}
	return model, res.sessionID, nil
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
