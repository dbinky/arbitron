package claudecli

import (
	"context"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
)

// AgentAdapter implements engine.AgentProvider by invoking the `claude` CLI in
// headless (`--print`) mode with tools enabled: it emits
// `--dangerously-skip-permissions` and `--allowedTools <ToolScope csv>`, runs
// in the mission WorkDir, and mutates files inside the instance workspace.
type AgentAdapter struct {
	core adapterCore
}

var _ engine.AgentProvider = (*AgentAdapter)(nil)

// NewAgentAdapter constructs a tools-enabled agent adapter from cfg. An empty
// BinaryPath is resolved from PATH and common install directories. A nil
// Runner uses the production os/exec runner.
//
// SECURITY: the agent path emits --dangerously-skip-permissions, removing the
// human-in-the-loop tool gate, so the allowed-tools allowlist (mission
// ToolScope, falling back to cfg.AllowedTools) is the only bound on what the
// agent may run.
func NewAgentAdapter(cfg Config) *AgentAdapter {
	return &AgentAdapter{core: newAdapterCore(cfg)}
}

// spec maps a mission to a tools-enabled RunSpec. An empty ToolScope falls
// back to the configured full agent scope (cfg.AllowedTools).
func (a *AgentAdapter) spec(mission engine.AgentMission) RunSpec {
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
		Env:              procenv.BaseEnv(),
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

	timeout := mission.Timeout
	if timeout <= 0 {
		timeout = a.core.timeout
	}
	runCtx, cancel := procenv.WithTimeout(ctx, timeout)
	defer cancel()

	result, events, err := collectResult(runCtx, a.core.runner, a.spec(mission))
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
