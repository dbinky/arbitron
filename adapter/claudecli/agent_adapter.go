package claudecli

import (
	"context"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// AgentAdapter implements engine.AgentProvider by invoking the `claude` CLI in
// headless (`--print`) mode with tools enabled: it emits
// `--dangerously-skip-permissions` and `--allowedTools <ToolScope csv>`, runs
// in the mission WorkDir, and mutates files inside the instance workspace.
//
// org-readiness Phase 2: the adapter optionally carries a per-binding
// ResolvedBinding so the agent run can target the org gateway (or any
// configured provider) — ANTHROPIC_BASE_URL + key are injected into the
// subprocess env PER CALL from the binding (see agentEnv). A zero-value binding
// (the default, NewAgentAdapter) is the subscription path: no endpoint, no key.
type AgentAdapter struct {
	core    adapterCore
	binding provider.ResolvedBinding
}

var _ engine.AgentProvider = (*AgentAdapter)(nil)

// NewAgentAdapter constructs a tools-enabled agent adapter from cfg with NO
// per-binding credential injection (the subscription path). An empty BinaryPath
// is resolved from PATH and common install directories. A nil Runner uses the
// production os/exec runner.
//
// SECURITY: the agent path emits --dangerously-skip-permissions, removing the
// human-in-the-loop tool gate, so the allowed-tools allowlist (mission
// ToolScope, falling back to cfg.AllowedTools) is the only bound on what the
// agent may run.
func NewAgentAdapter(cfg Config) *AgentAdapter {
	return &AgentAdapter{core: newAdapterCore(cfg)}
}

// NewAgentAdapterForBinding constructs a tools-enabled agent adapter that
// injects the binding's endpoint + key (ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY)
// into the subprocess env PER CALL (org-readiness Phase 2). A zero-value binding
// is equivalent to NewAgentAdapter (the subscription path). The factory's
// claude-code arm calls this so a claude-code backend can target the org gateway
// or any configured provider, not just the local subscription.
func NewAgentAdapterForBinding(cfg Config, binding provider.ResolvedBinding) *AgentAdapter {
	return &AgentAdapter{core: newAdapterCore(cfg), binding: binding}
}

// spec maps a mission to a tools-enabled RunSpec, with the per-call env supplied
// by the caller (RunAgent builds it via agentEnv after revealing the key, so an
// env-build failure aborts before launch — no partial-env). An empty ToolScope
// falls back to the configured full agent scope (cfg.AllowedTools).
func (a *AgentAdapter) spec(mission engine.AgentMission, env []string) RunSpec {
	scope := mission.ToolScope
	if len(scope) == 0 {
		scope = a.core.allowedTools
	}
	req := engine.ChatRequest{
		Model:     mission.Model,
		SessionID: mission.SessionID,
	}
	return RunSpec{
		Bin: a.core.binPath,
		Args: buildArgs(req, argsPolicy{
			DefaultModel:    a.core.defaultModel,
			SkipPermissions: true,
			AllowedTools:    scope,
		}),
		Stdin:            mission.Prompt,
		Dir:              mission.WorkDir,
		Env:              env,
		MaxResponseBytes: a.core.maxResponseBytes,
	}
}

// RunAgent runs the mission to completion (non-streaming). It folds the
// intermediate transcript events into AgentResult.Steps and captures the
// terminal result event's summary, token counts, and session id.
func (a *AgentAdapter) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	if err := a.core.validateWorkDir(mission.WorkDir); err != nil {
		return nil, err
	}

	// Build the per-call env BEFORE any subprocess so a RevealKey / collision
	// failure aborts the run with NO partial-env launch (fail-closed). A
	// zero-value binding yields the subscription env (no endpoint/key); the
	// capability rules append the binding's resolved context-window + effort env
	// on top (profile-editing Phase 3) — a default/empty axis adds nothing.
	env, err := agentEnvWithRules(ctx, a.binding, a.core.rules)
	if err != nil {
		return nil, err
	}

	timeout := mission.Timeout
	if timeout <= 0 {
		timeout = a.core.timeout
	}
	runCtx, cancel := procenv.WithTimeout(ctx, timeout)
	defer cancel()

	result, events, err := collectResult(runCtx, a.core.runner, a.spec(mission, env))
	if err != nil {
		return nil, err
	}

	steps := make([]engine.AgentStep, 0, len(events))
	for _, ev := range events {
		step, ok := foldStep(ev)
		if !ok {
			continue
		}
		steps = append(steps, step)
	}

	return &engine.AgentResult{
		Summary:   result.Text,
		Steps:     steps,
		Model:     mission.Model,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
		SessionID: result.SessionID,
	}, nil
}

// foldStep maps a parsed transcript event to an AgentStep for the trace.
// Assistant turns carry their text as the step summary; user (tool-result)
// turns become tool_call steps. The ok return is false for non-step events
// (e.g. the system init event), which are dropped from the trace.
func foldStep(ev streamEvent) (engine.AgentStep, bool) {
	switch ev.Kind {
	case eventAssistant:
		return engine.AgentStep{Kind: "assistant", Summary: ev.Text}, true
	case eventUser:
		return engine.AgentStep{Kind: "tool_call"}, true
	default:
		return engine.AgentStep{}, false
	}
}
