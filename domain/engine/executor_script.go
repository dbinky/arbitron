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

// ScriptExecutor runs external scripts via os/exec, piping the variable
// store as JSON to stdin and parsing stdout as JSON.
type ScriptExecutor struct {
	fileReader FileReader
}

// NewScriptExecutor creates a ScriptExecutor that uses the given FileReader
// to resolve script paths.
func NewScriptExecutor(fr FileReader) *ScriptExecutor {
	return &ScriptExecutor{fileReader: fr}
}

// Execute resolves the script path, runs it with the configured interpreter,
// pipes the variable store as JSON to stdin, and parses stdout as JSON.
func (e *ScriptExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	// Resolve script path.
	scriptPath, err := e.fileReader.ResolvePath(workflowName, node.Config.ScriptFile)
	if err != nil {
		return nil, fmt.Errorf("script %q: resolve path: %w", node.ID, err)
	}

	// Resolve substitution in args.
	args := make([]string, len(node.Config.Args))
	for i, arg := range node.Config.Args {
		args[i] = variable.Resolve(arg, vars)
	}

	// Build command: interpreter + script + args.
	cmdArgs := append([]string{scriptPath}, args...)
	interpreter := node.Config.Interpreter
	if interpreter == "" {
		interpreter = "/bin/sh"
	}

	// Apply timeout if configured.
	execCtx := ctx
	var cancel context.CancelFunc
	if node.Config.TimeoutSeconds > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(node.Config.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	cmd := exec.Command(interpreter, cmdArgs...)
	setProcGroup(cmd)

	// Pipe variables as JSON to stdin.
	snapshot := vars.Snapshot()
	stdinData, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("script %q: marshal variables: %w", node.ID, err)
	}
	cmd.Stdin = bytes.NewReader(stdinData)

	// Capture stdout and stderr.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run with context-aware waiting.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("script %q: start: %w", node.ID, err)
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
		<-doneCh // wait for Wait() to return
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
		return result, fmt.Errorf("script %q: exit status %d: %s", node.ID, result.ScriptExitCode, strings.TrimSpace(stderr.String()))
	}

	// Parse stdout as JSON.
	var output any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &output); err != nil {
		return result, fmt.Errorf("script %q: stdout is not valid JSON: %w", node.ID, err)
	}

	result.Output = output
	return result, nil
}
