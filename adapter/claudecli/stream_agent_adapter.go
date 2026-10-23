package claudecli

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// StreamAgentAdapter implements engine.ChatProvider by invoking the `claude`
// CLI in headless (`--print`) streaming mode with tools enabled: it emits
// `--dangerously-skip-permissions` and forwards the configured AllowedTools
// (or a per-request ToolScope), so the model may execute tools inside the
// instance workspace. Unlike ChatAdapter, no tool-suppression flag is emitted.
//
// This adapter is intended for the interactive_agent node, which requires a
// streaming, tool-enabled chat interface so the engine can relay partial output
// while the agent executes tool calls.
type StreamAgentAdapter struct {
	core    adapterCore
	binding provider.ResolvedBinding
}

var _ engine.ChatProvider = (*StreamAgentAdapter)(nil)

// NewStreamAgentAdapter constructs a tool-enabled streaming chat adapter from
// cfg. An empty BinaryPath is resolved from PATH and common install
// directories. A zero Timeout means no timeout. A nil Runner uses the
// production os/exec runner.
//
// SECURITY: the stream-agent path always emits --dangerously-skip-permissions,
// removing the human-in-the-loop tool gate. The allowed-tools allowlist
// (cfg.AllowedTools) is the only bound on what the model may run.
func NewStreamAgentAdapter(cfg Config) *StreamAgentAdapter {
	return NewStreamAgentAdapterForBinding(cfg, provider.ResolvedBinding{})
}

// NewStreamAgentAdapterForBinding applies the current turn's connection and axes
// through the same credential environment as the batch agent adapter.
func NewStreamAgentAdapterForBinding(cfg Config, binding provider.ResolvedBinding) *StreamAgentAdapter {
	return &StreamAgentAdapter{core: newAdapterCore(cfg), binding: binding}
}

// spec builds the tool-enabled RunSpec for a chat request. The argsPolicy sets
// SkipPermissions=true and forwards AllowedTools from the adapter's configured
// allowedTools, mirroring AgentAdapter.spec's fallback behaviour.
// SuppressTools is set only for a request that asks for a tool-free turn.
func (a *StreamAgentAdapter) spec(req engine.ChatRequest, env []string) RunSpec {
	// Probe restrictions override both the requested and configured tool scope.
	if req.SuppressTools {
		return RunSpec{
			Bin:              a.core.binPath,
			Args:             buildProbeArgs(req),
			Stdin:            req.Prompt,
			Dir:              req.WorkDir,
			Env:              probeEnv(env),
			MaxResponseBytes: a.core.maxResponseBytes,
		}
	}
	// Per-request tool scope precedence (PR #18 item 1b): a non-empty
	// req.ToolScope (the resolved profile's per-profile AllowedTools) overrides
	// the adapter's global cfg.LLM.AllowedTools, so an interactive_agent node
	// runs against its profile's explicit minimal allowlist. An empty ToolScope
	// falls back to the adapter's configured scope (which, under buildArgs'
	// fail-closed default, is the empty set when unset).
	scope := req.ToolScope
	if len(scope) == 0 {
		scope = a.core.allowedTools
	}
	return RunSpec{
		Bin: a.core.binPath,
		Args: buildArgs(req, argsPolicy{
			DefaultModel:    a.core.defaultModel,
			SkipPermissions: true,
			AllowedTools:    scope,
		}),
		Stdin:            req.Prompt,
		Dir:              req.WorkDir,
		Env:              env,
		MaxResponseBytes: a.core.maxResponseBytes,
	}
}

// Chat runs claude to completion and returns the terminal result event.
func (a *StreamAgentAdapter) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	if err := a.core.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

	env, err := agentEnvWithRules(ctx, a.binding, a.core.rules)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := procenv.WithTimeout(ctx, a.core.timeout)
	defer cancel()

	wantModel, err := requiredExecutedModel(req.Model, req.RequireExecutedModel, a.binding)
	if err != nil {
		return nil, err
	}
	result, _, err := collectResult(runCtx, a.core.runner, a.spec(req, env), wantModel)
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
//
// Tool frames (assistant messages with tool_use content blocks, user messages
// carrying tool_result) are tolerated: the streamState loop ignores non-text
// content blocks in assistant messages (assistantText skips them) and treats
// user/tool-result events as no-ops in the switch, so text accumulation and
// the Done chunk are unaffected by tool round-trips.
func (a *StreamAgentAdapter) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if err := a.core.validateWorkDir(req.WorkDir); err != nil {
		return nil, err
	}

	env, err := agentEnvWithRules(ctx, a.binding, a.core.rules)
	if err != nil {
		return nil, err
	}
	// A pinned binding demands harness evidence whatever the request says, and
	// the request may not name another model (D14); refuse before any launch.
	wantModel, err := requiredExecutedModel(req.Model, req.RequireExecutedModel, a.binding)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := procenv.WithTimeout(ctx, a.core.timeout)

	lines, wait, err := a.core.runner.Run(runCtx, a.spec(req, env))
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

		st := streamState{wantModel: wantModel}
		for line := range lines {
			st.Handle(line, sendChunk)
		}
		st.Finalize(wait(), sendChunk)
	}()

	return out, nil
}
