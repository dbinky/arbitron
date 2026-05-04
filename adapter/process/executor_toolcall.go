package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// SECURITY: Phase 1 threat model assumes workflow authors are trusted administrators.
// Commands defined in workflow manifests execute with the same privileges as the Arbitron
// process. There is no sandboxing, no allow-list of commands, and no filesystem isolation.
// Before opening workflow authoring to untrusted users, a sandbox must be implemented.

// TODO(refactor): Extract a CommandRunner interface the moment this executor is
// touched for any reason. Both ToolCallExecutor and ScriptExecutor share identical
// process lifecycle logic (start, wait, kill-on-timeout, capture stdout/stderr).
// A shared CommandRunner would eliminate this duplication and simplify testing by
// allowing process execution to be mocked.
//
// Trigger condition: any modification to this file.

// ToolCallExecutor runs an external command with arguments. Unlike
// ScriptExecutor, it does NOT pipe variables to stdin.
type ToolCallExecutor struct{}

// NewToolCallExecutor creates a new ToolCallExecutor.
func NewToolCallExecutor() *ToolCallExecutor {
	return &ToolCallExecutor{}
}

// Execute runs the configured command with resolved args, captures stdout
// (parsed as JSON) and stderr, and returns the result.
func (e *ToolCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*engine.NodeResult, error) {
	// Resolve substitution in args.
	args := make([]string, len(node.Config.Args))
	for i, arg := range node.Config.Args {
		args[i] = variable.Resolve(arg, vars)
	}

	// Apply timeout if configured.
	execCtx := ctx
	var cancel context.CancelFunc
	if node.Config.TimeoutSeconds > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(node.Config.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	cmd := exec.Command(node.Config.Command, args...)
	setProcGroup(cmd)

	// No stdin for tool calls.
	cmd.Stdin = nil

	// Capture stdout and stderr with bounded writers to prevent unbounded memory use.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, limit: maxOutputBytes}
	cmd.Stderr = &limitedWriter{buf: &stderr, limit: maxOutputBytes}

	// Run with context-aware waiting.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tool_call %q: start: %w", node.ID, err)
	}

	// Wait for completion or context cancellation.
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- cmd.Wait()
	}()

	var runErr error
	select {
	case <-execCtx.Done():
		killProcGroup(cmd)
		<-doneCh
		runErr = execCtx.Err()
	case runErr = <-doneCh:
	}

	result := &engine.NodeResult{
		ScriptStdout: stdout.String(),
		ScriptStderr: stderr.String(),
	}

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			result.ScriptExitCode = exitErr.ExitCode()
		}
		return result, fmt.Errorf("tool_call %q: exit status %d: %s", node.ID, result.ScriptExitCode, strings.TrimSpace(stderr.String()))
	}

	// Parse stdout as JSON.
	var output any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &output); err != nil {
		return result, fmt.Errorf("tool_call %q: stdout is not valid JSON: %w", node.ID, err)
	}

	result.Output = output
	return result, nil
}
