package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ToolCallExecutor runs an external command with arguments. Unlike
// ScriptExecutor, it does NOT pipe variables to stdin.
type ToolCallExecutor struct{}

// NewToolCallExecutor creates a new ToolCallExecutor.
func NewToolCallExecutor() *ToolCallExecutor {
	return &ToolCallExecutor{}
}

// Execute runs the configured command with resolved args, captures stdout
// (parsed as JSON) and stderr, and returns the result.
func (e *ToolCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*NodeResult, error) {
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

	// Capture stdout and stderr.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

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

	result := &NodeResult{
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
