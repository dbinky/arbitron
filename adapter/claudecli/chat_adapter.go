package claudecli

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
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
type ChatAdapter struct {
	core adapterCore
}

var _ engine.ChatProvider = (*ChatAdapter)(nil)

// NewChatAdapter constructs a tool-suppressed chat adapter from cfg. An empty
// BinaryPath is resolved from PATH and common install directories. A zero
// Timeout means no timeout. A nil Runner uses the production os/exec runner.
//
// SECURITY: the chat path never emits --dangerously-skip-permissions and never
// forwards cfg.AllowedTools; any tool-related Config fields are intentionally
// ignored here.
func NewChatAdapter(cfg Config) *ChatAdapter {
	return &ChatAdapter{core: newAdapterCore(cfg)}
}

// spec builds the tool-suppressed RunSpec for a chat request.
func (a *ChatAdapter) spec(req engine.ChatRequest) RunSpec {
	return RunSpec{
		Bin: a.core.binPath,
		Args: buildArgs(req, argsPolicy{
			DefaultModel:  a.core.defaultModel,
			SuppressTools: true,
		}),
		Stdin:            req.Prompt,
		Dir:              req.WorkDir,
		Env:              procenv.BaseEnv(),
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

	result, _, err := collectResult(runCtx, a.core.runner, a.spec(req))
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

	runCtx, cancel := procenv.WithTimeout(ctx, a.core.timeout)

	lines, wait, err := a.core.runner.Run(runCtx, a.spec(req))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudecli: stream call: %w", err)
	}

	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel()
		defer close(out)

		sendChunk := makeSendChunk(runCtx, out)

		var st streamState
		for line := range lines {
			st.Handle(line, sendChunk)
		}
		st.Finalize(wait(), sendChunk)
	}()

	return out, nil
}
