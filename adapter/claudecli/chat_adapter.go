package claudecli

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// adapterCore holds the construction inputs shared by ChatAdapter and
// AgentAdapter: the resolved binary, the workspace pin root, the per-call
// timeout, and the runner seam. WorkDir validation and timeout derivation are
// methods on this type so both adapters reuse the same logic without
// duplicating it.
type adapterCore struct {
	binPath          string
	defaultModel     string
	workspaceRoot    string
	timeout          time.Duration
	allowedTools     []string
	maxResponseBytes int64
	runner           CommandRunner
	// rules drives the capability env emission (context window + effort) appended
	// to the per-call env (profile-editing Phase 3). A zero-value (Empty) ruleset
	// emits nothing.
	rules provider.CapabilityRules
}

func newAdapterCore(cfg Config) adapterCore {
	if cfg.Runner == nil {
		cfg.Runner = newExecRunner()
	}
	return adapterCore{
		binPath:          resolveBinary(cfg.BinaryPath),
		defaultModel:     cfg.DefaultModel,
		workspaceRoot:    cfg.WorkspaceRoot,
		timeout:          cfg.Timeout,
		allowedTools:     cfg.AllowedTools,
		maxResponseBytes: cfg.MaxResponseBytes,
		runner:           cfg.Runner,
		rules:            cfg.CapabilityRules,
	}
}

// validateWorkDir enforces the per-call WorkDir invariants (non-empty, pinned
// under workspaceRoot when configured). See the package-level ErrWorkDir*
// vars for the security rationale.
func (c *adapterCore) validateWorkDir(workDir string) error {
	return validateWorkDir(workDir, c.workspaceRoot)
}

// ChatAdapter implements engine.ChatProvider by invoking the `claude` CLI in
// headless (`--print`) mode with tools suppressed (`--allowedTools ""`, no
// `--dangerously-skip-permissions`), so it behaves as a plain completion with
// no file side effects.
//
// When constructed with NewChatAdapterForBinding, the binding's ContextWindow
// and Effort axes are applied to the subprocess env (profile-editing Phase 3)
// so a chat-kind profile with 200k or a non-default effort setting uses the
// correct context window / effort level at runtime. NewChatAdapter (no binding)
// produces the subscription env with no capability additions.
type ChatAdapter struct {
	core    adapterCore
	binding provider.ResolvedBinding
}

var _ engine.ChatProvider = (*ChatAdapter)(nil)

// NewChatAdapter constructs a tool-suppressed chat adapter from cfg with NO
// per-binding axes (the subscription path, or any path where the caller
// manages axis emission separately). An empty BinaryPath is resolved from PATH
// and common install directories. A zero Timeout means no timeout. A nil
// Runner uses the production os/exec runner.
//
// SECURITY: the chat path never emits --dangerously-skip-permissions and never
// forwards cfg.AllowedTools; any tool-related Config fields are intentionally
// ignored here.
func NewChatAdapter(cfg Config) *ChatAdapter {
	return &ChatAdapter{core: newAdapterCore(cfg)}
}

// NewChatAdapterForBinding constructs a tool-suppressed chat adapter that
// applies the binding's ContextWindow and Effort axes to the subprocess env
// (profile-editing Phase 3). A zero-value binding is equivalent to
// NewChatAdapter (no axis additions). The factory's claude-cli arm calls this
// so a chat-kind profile with non-default axes actually takes effect.
func NewChatAdapterForBinding(cfg Config, binding provider.ResolvedBinding) *ChatAdapter {
	return &ChatAdapter{core: newAdapterCore(cfg), binding: binding}
}

// spec builds the tool-suppressed RunSpec for a chat request.
func (a *ChatAdapter) spec(req engine.ChatRequest) RunSpec {
	args := buildArgs(req, argsPolicy{DefaultModel: a.core.defaultModel, SuppressTools: true})
	env := a.core.chatEnvWithBinding(a.binding)
	if req.SuppressTools {
		args, env = buildProbeArgs(req), probeEnv(env)
	}
	return RunSpec{
		Bin:              a.core.binPath,
		Args:             args,
		Stdin:            req.Prompt,
		Dir:              req.WorkDir,
		Env:              env,
		MaxResponseBytes: a.core.maxResponseBytes,
	}
}

// Chat runs claude to completion and returns the terminal result event.
func (a *ChatAdapter) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	if err := a.core.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

	runCtx, cancel := procenv.WithTimeout(ctx, a.core.timeout)
	defer cancel()

	wantModel, err := requiredExecutedModel(req.Model, req.RequireExecutedModel, a.binding)
	if err != nil {
		return nil, err
	}
	result, _, err := collectResult(runCtx, a.core.runner, a.spec(req), wantModel)
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
// drains the runner's lines channel and calls wait().
func (a *ChatAdapter) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if err := a.core.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

	// A pinned binding demands harness evidence whatever the request says, and
	// the request may not name another model (D14); refuse before any launch.
	wantModel, err := requiredExecutedModel(req.Model, req.RequireExecutedModel, a.binding)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := procenv.WithTimeout(ctx, a.core.timeout)

	lines, wait, err := a.core.runner.Run(runCtx, a.spec(req))
	if err != nil {
		cancel()
		if wantModel != "" {
			err = SanitizeProcessError(err)
		}
		return nil, fmt.Errorf("claudecli: stream call: %w", err)
	}

	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel()
		defer close(out)

		sendChunk := makeSendChunk(runCtx, out)

		st := streamState{wantModel: wantModel, ctx: runCtx}
		for line := range lines {
			st.Handle(line, sendChunk)
		}
		st.Finalize(wait(), sendChunk)
	}()

	return out, nil
}
