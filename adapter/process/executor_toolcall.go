package process

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// SECURITY: Phase 1 threat model assumes workflow authors are trusted administrators.
// Commands defined in workflow manifests execute with the same privileges as the Arbitron
// process. There is no sandboxing, no allow-list of commands, and no filesystem isolation.
// Before opening workflow authoring to untrusted users, a sandbox must be implemented.
//
// The subprocess environment is strictly controlled: only the base allowlist, operator-
// approved passthrough vars, and per-node env keys are forwarded (see buildEnvFromResolved). The
// engine's full environment is NOT inherited, preventing accidental secret leakage.

// ToolCallExecutor runs an external command with arguments. Unlike
// ScriptExecutor it does NOT pipe variables to stdin.
type ToolCallExecutor struct {
	passthrough []string
	runner      engine.ProcessRunner
}

// NewToolCallExecutor creates a ToolCallExecutor. envPassthrough names the
// engine env vars a tool_call node may inherit beyond the base allowlist;
// pr executes the resolved process specification; nil panics.
func NewToolCallExecutor(envPassthrough []string, pr engine.ProcessRunner) *ToolCallExecutor {
	if pr == nil {
		panic("process: NewToolCallExecutor requires a non-nil engine.ProcessRunner")
	}
	return &ToolCallExecutor{passthrough: envPassthrough, runner: pr}
}

// Execute runs the configured command with resolved args under a controlled
// environment through the process port, captures stdout (parsed as JSON) and stderr, and
// fails the node on a non-zero exit.
func (e *ToolCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*engine.NodeResult, error) {
	args := make([]string, len(node.Config.Args))
	for i, arg := range node.Config.Args {
		args[i] = variable.Resolve(arg, vars)
	}

	if err := validateNodeEnvNames(e.passthrough, node.Config.Env); err != nil {
		return nil, fmt.Errorf("tool_call %q: %w", node.ID, err)
	}

	workDir, _ := engine.WorkDirFromVars(vars)
	res, runErr := e.runner.RunProcess(ctx, engine.ProcessSpec{
		Kind:        engine.ProcessToolCall,
		Command:     node.Config.Command,
		Dir:         workDir,
		Args:        args,
		NodeEnv:     resolveNodeEnv(node.Config.Env, vars),
		Passthrough: append([]string(nil), e.passthrough...),
		TimeoutSecs: node.Config.TimeoutSeconds,
		WorkDir:     workDir,
		Portable:    node.Config.Portable,
		Requires:    append([]string(nil), node.Config.Requires...),
	})

	result := &engine.NodeResult{
		ScriptStdout:   res.Stdout,
		ScriptStderr:   res.Stderr,
		ScriptExitCode: res.ExitCode,
	}
	if runErr != nil {
		return result, fmt.Errorf("tool_call %q: %w", node.ID, runErr)
	}
	if res.ExitCode != 0 {
		return result, fmt.Errorf("tool_call %q: exit status %d: %s", node.ID, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	var output any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &output); err != nil {
		return result, fmt.Errorf("tool_call %q: stdout is not valid JSON: %w", node.ID, err)
	}
	result.Output = output
	return result, nil
}
