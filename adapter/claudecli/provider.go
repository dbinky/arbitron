package claudecli

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrWorkDirRequired is returned by Chat/StreamChat when req.WorkDir is empty.
// SECURITY: an empty Dir on cmd.Exec means "inherit parent cwd" — i.e., the
// engine launch directory containing arbitron.db, config.yaml, and other
// secrets. PR #8 security MEDIUM thread 3291944671 item 1.
var ErrWorkDirRequired = fmt.Errorf("claudecli: WorkDir is required")

// ErrWorkDirEscapesRoot is returned when req.WorkDir resolves outside the
// configured workspace root. SECURITY: PR #8 thread 3291944671 item 2.
var ErrWorkDirEscapesRoot = fmt.Errorf("claudecli: WorkDir is outside the configured workspace root")

// Config configures a claude-cli Provider.
//
// SECURITY:
//   - SkipPermissions defaults to false; setting true emits
//     `--dangerously-skip-permissions`, removing the human-in-the-loop tool
//     gate. Only flip in deployments that trust every prompt input.
//   - AllowedTools is forwarded as `--allowedTools` (comma-joined). When
//     SkipPermissions is false this should be configured because headless
//     `--print` mode cannot prompt the operator for tool approvals.
//   - WorkspaceRoot pins request WorkDir to a subtree of this absolute path.
//     An empty WorkspaceRoot disables the pin check (test convenience only).
//   - MaxResponseBytes caps the per-request total stdout bytes; 0 means no
//     cap (test convenience only).
type Config struct {
	BinaryPath       string
	DefaultModel     string
	WorkspaceRoot    string
	Timeout          time.Duration
	AllowedTools     []string
	SkipPermissions  bool
	MaxResponseBytes int64
	Runner           CommandRunner
	// CapabilityRules drives the per-call context-window + effort env emission
	// (profile-editing Phase 3): the agent run appends the binding's resolved
	// axes to its subprocess env via this ruleset. A zero-value (Empty) ruleset
	// emits nothing, so adapters constructed without it (back-compat / tests)
	// behave exactly as before.
	CapabilityRules provider.CapabilityRules
}

// Provider implements engine.ChatProvider by invoking the `claude` CLI in
// headless (`--print`) mode.
//
// Deprecated: Provider is a thin pre-split shim retained only until Task 6
// rewires cmd/arbitron/main.go onto NewChatAdapter / NewAgentAdapter. New code
// should construct a ChatAdapter (tool-suppressed chat) or AgentAdapter
// (tools-enabled mission) instead.
type Provider struct {
	binPath          string
	defaultModel     string
	workspaceRoot    string
	timeout          time.Duration
	allowedTools     []string
	skipPermissions  bool
	maxResponseBytes int64
	runner           CommandRunner
}

var _ engine.ChatProvider = (*Provider)(nil)

// New constructs a claude-cli Provider from cfg. An empty BinaryPath is
// resolved from PATH and common install directories. A zero Timeout means no
// timeout. A nil Runner uses the production os/exec runner.
//
// SECURITY: when SkipPermissions=false and AllowedTools is empty, a slog.Warn
// is emitted because headless `--print` mode cannot answer interactive
// permission prompts — the agent will fail at its first tool call. Operators
// wanting permissionful runs should configure an AllowedTools allowlist.
func New(cfg Config) *Provider {
	if cfg.Runner == nil {
		cfg.Runner = newExecRunner()
	}
	if !cfg.SkipPermissions && len(cfg.AllowedTools) == 0 {
		slog.Warn(
			"claudecli: SkipPermissions=false with empty AllowedTools; agent runs will fail at the first tool call because --print mode cannot prompt for approvals",
		)
	}
	return &Provider{
		binPath:          resolveBinary(cfg.BinaryPath),
		defaultModel:     cfg.DefaultModel,
		workspaceRoot:    cfg.WorkspaceRoot,
		timeout:          cfg.Timeout,
		allowedTools:     cfg.AllowedTools,
		skipPermissions:  cfg.SkipPermissions,
		maxResponseBytes: cfg.MaxResponseBytes,
		runner:           cfg.Runner,
	}
}

// spec builds the RunSpec for a request.
func (p *Provider) spec(req engine.ChatRequest) RunSpec {
	return RunSpec{
		Bin: p.binPath,
		Args: buildArgs(req, argsPolicy{
			DefaultModel:    p.defaultModel,
			SkipPermissions: p.skipPermissions,
			AllowedTools:    p.allowedTools,
		}),
		Stdin:            req.Prompt,
		Dir:              req.WorkDir,
		Env:              procenv.BaseEnv(),
		MaxResponseBytes: p.maxResponseBytes,
	}
}

// validateWorkDir delegates to the shared per-call WorkDir invariant check.
func (p *Provider) validateWorkDir(workDir string) error {
	return validateWorkDir(workDir, p.workspaceRoot)
}

// Chat runs claude to completion and returns the terminal result event.
func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	if err := p.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

	runCtx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()

	wantModel, err := requiredExecutedModel(req.Model, req.RequireExecutedModel, provider.ResolvedBinding{})
	if err != nil {
		return nil, err
	}
	result, _, err := collectResult(runCtx, p.runner, p.spec(req), wantModel)
	if err != nil {
		return nil, err
	}

	return &engine.ChatResponse{
		Output:    result.Text,
		SessionID: result.SessionID,
		Model:     req.Model,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
	}, nil
}

// StreamChat runs claude and streams assistant text as it arrives. The final
// chunk has Done=true and carries the session id and token counts. Any failure
// is reported as a chunk with a non-nil Error. The internal goroutine always
// drains the runner's lines channel and calls wait(), honouring the
// CommandRunner contract even when an error is encountered mid-stream.
func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if err := p.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

	runCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	lines, wait, err := p.runner.Run(runCtx, p.spec(req))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudecli: stream call: %w", err)
	}

	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel()
		defer close(out)

		// sendChunk: producer-side guard (MF9). Non-blocking send via
		// runCtx so the goroutine returns if the consumer cancels mid-
		// stream. See PR #8 thread 3291921373.
		sendChunk := makeSendChunk(runCtx, out)

		// streamState (SF3) holds per-line bookkeeping outside this closure
		// so Handle / Finalize are individually testable.
		st := streamState{wantModel: pinnedModel(req), ctx: runCtx}
		for line := range lines {
			st.Handle(line, sendChunk)
		}
		st.Finalize(wait(), sendChunk)
	}()

	return out, nil
}
