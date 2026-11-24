// Package process provides os/exec-based node executors that run external
// processes. These live in the adapter layer because they perform I/O via
// the operating system process interface.
package process

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// SECURITY: Phase 1 threat model assumes workflow authors are trusted administrators.
// Scripts defined in workflow manifests execute with the same privileges as the Arbitron
// process. There is no sandboxing, no allow-list of commands, and no filesystem isolation.
// Before opening workflow authoring to untrusted users, a sandbox (e.g., container-based
// execution, seccomp profiles, or a dedicated unprivileged user) must be implemented.
//
// The subprocess environment is strictly controlled: only the base allowlist, operator-
// approved passthrough vars, and per-node env keys are forwarded (see buildEnvFromResolved). The
// engine's full environment is NOT inherited, preventing accidental secret leakage.

// ScriptExecutor runs external scripts through the process port, piping the variable store
// as JSON to stdin and parsing stdout as JSON.
type ScriptExecutor struct {
	fileReader  engine.FileReader
	passthrough []string
	runner      engine.ProcessRunner
	bundles     runner.BundleStore
}

// NewScriptExecutor creates a ScriptExecutor. envPassthrough names the engine
// env vars (e.g. LLM_API_KEY) that script nodes may inherit beyond the base
// allowlist. pr executes the resolved process specification; nil panics.
func NewScriptExecutor(fr engine.FileReader, envPassthrough []string, pr engine.ProcessRunner) *ScriptExecutor {
	if pr == nil {
		panic("process: NewScriptExecutor requires a non-nil engine.ProcessRunner")
	}
	return &ScriptExecutor{fileReader: fr, passthrough: envPassthrough, runner: pr}
}

// SetBundleStore enables bundling for explicitly portable scripts.
func (e *ScriptExecutor) SetBundleStore(b runner.BundleStore) { e.bundles = b }

// Execute resolves the script path, runs it with the configured interpreter,
// pipes the variable store as JSON to stdin, and parses stdout as JSON.
func (e *ScriptExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*engine.NodeResult, error) {
	scriptPath, err := e.fileReader.ResolvePath(engine.OwnerFromContext(ctx), workflowName, node.Config.ScriptFile)
	if err != nil {
		return nil, fmt.Errorf("script %q: resolve path: %w", node.ID, err)
	}

	args := make([]string, len(node.Config.Args))
	for i, arg := range node.Config.Args {
		args[i] = variable.Resolve(arg, vars)
	}

	interpreter := node.Config.Interpreter
	if interpreter == "" {
		interpreter = "/bin/sh"
	}

	if err := validateNodeEnvNames(e.passthrough, node.Config.Env); err != nil {
		return nil, fmt.Errorf("script %q: %w", node.ID, err)
	}

	stdinData, err := json.Marshal(vars.Snapshot())
	if err != nil {
		return nil, fmt.Errorf("script %q: marshal variables: %w", node.ID, err)
	}

	workDir, _ := engine.WorkDirFromVars(vars)
	spec := engine.ProcessSpec{
		Kind:        engine.ProcessScript,
		Command:     interpreter,
		Args:        append([]string{scriptPath}, args...),
		Dir:         filepath.Dir(scriptPath),
		Stdin:       stdinData,
		NodeEnv:     resolveNodeEnv(node.Config.Env, vars),
		Passthrough: append([]string(nil), e.passthrough...),
		TimeoutSecs: node.Config.TimeoutSeconds,
		WorkDir:     workDir,
		Portable:    node.Config.Portable,
		Requires:    append([]string(nil), node.Config.Requires...),
	}
	if e.bundles != nil && node.Config.Portable {
		ref, data, err := BuildScriptBundle(scriptPath)
		if err != nil {
			return nil, fmt.Errorf("script %q: portable bundle: %w", node.ID, err)
		}
		if err := e.bundles.Put(ctx, ref, data); err != nil {
			return nil, fmt.Errorf("script %q: store bundle: %w", node.ID, err)
		}
		spec.Bundle = &ref
		spec.Args[0] = "./" + ref.Entrypoint
	}
	res, runErr := e.runner.RunProcess(ctx, spec)

	result := &engine.NodeResult{
		ScriptStdout:   res.Stdout,
		ScriptStderr:   res.Stderr,
		ScriptExitCode: res.ExitCode,
	}
	if runErr != nil {
		return result, fmt.Errorf("script %q: %w", node.ID, runErr)
	}
	if res.ExitCode != 0 {
		return result, fmt.Errorf("script %q: exit status %d: %s", node.ID, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	var output any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &output); err != nil {
		return result, fmt.Errorf("script %q: stdout is not valid JSON: %w", node.ID, err)
	}
	result.Output = output
	return result, nil
}
