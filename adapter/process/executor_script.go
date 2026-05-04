// Package process provides os/exec-based node executors that run external
// processes. These live in the adapter layer because they perform I/O via
// the operating system process interface.
package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// SECURITY: Phase 1 threat model assumes workflow authors are trusted administrators.
// Scripts defined in workflow manifests execute with the same privileges as the Arbitron
// process. There is no sandboxing, no allow-list of commands, and no filesystem isolation.
// Before opening workflow authoring to untrusted users, a sandbox (e.g., container-based
// execution, seccomp profiles, or a dedicated unprivileged user) must be implemented.

// TODO(refactor): Extract a CommandRunner interface the moment this executor is
// touched for any reason. Both ScriptExecutor and ToolCallExecutor share identical
// process lifecycle logic (start, wait, kill-on-timeout, capture stdout/stderr).
// A shared CommandRunner would eliminate this duplication and simplify testing by
// allowing process execution to be mocked.
//
// Trigger condition: any modification to this file.

// ScriptExecutor runs external scripts via os/exec, piping the variable
// store as JSON to stdin and parsing stdout as JSON.
type ScriptExecutor struct {
	fileReader engine.FileReader
}

// NewScriptExecutor creates a ScriptExecutor that uses the given FileReader
// to resolve script paths.
func NewScriptExecutor(fr engine.FileReader) *ScriptExecutor {
	return &ScriptExecutor{fileReader: fr}
}

// Execute resolves the script path, runs it with the configured interpreter,
// pipes the variable store as JSON to stdin, and parses stdout as JSON.
func (e *ScriptExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*engine.NodeResult, error) {
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
	cmd.Dir = filepath.Dir(scriptPath) // run script from its own directory so relative paths work
	setProcGroup(cmd)

	// Pipe variables as JSON to stdin.
	snapshot := vars.Snapshot()
	stdinData, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("script %q: marshal variables: %w", node.ID, err)
	}
	cmd.Stdin = bytes.NewReader(stdinData)

	// Capture stdout and stderr with bounded writers to prevent unbounded memory use.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, limit: maxOutputBytes}
	cmd.Stderr = &limitedWriter{buf: &stderr, limit: maxOutputBytes}

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

	result := &engine.NodeResult{
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
