package process_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingProcessRunner records every spec and answers with a canned result.
type recordingProcessRunner struct {
	mu    sync.Mutex
	specs []engine.ProcessSpec
	res   engine.ProcessResult
	err   error
}

func (r *recordingProcessRunner) RunProcess(_ context.Context, spec engine.ProcessSpec) (engine.ProcessResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	return r.res, r.err
}

func (r *recordingProcessRunner) calls() []engine.ProcessSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.ProcessSpec(nil), r.specs...)
}

func TestScriptExecutor_BuildsResolvedSpecForPort(t *testing.T) {
	fr := newMockFileReader()
	scriptPath := filepath.Join(t.TempDir(), "scripts", "run.sh")
	fr.paths["wf/scripts/run.sh"] = scriptPath
	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("model", "opus"))
	vars.SetReserved(engine.ReservedVarWorkspace, "/ws/run-1")

	pr := &recordingProcessRunner{res: engine.ProcessResult{Stdout: `{"ok":true}`}}
	exec := process.NewScriptExecutor(fr, []string{"LLM_API_KEY"}, pr)
	res, err := exec.Execute(context.Background(), workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile: "scripts/run.sh", Args: []string{"--m={{model}}"},
			Env: map[string]string{"LLM_MODEL": "{{model}}"}, TimeoutSeconds: 7,
		},
	}, vars, "wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"ok": true}, res.Output)

	calls := pr.calls()
	require.Len(t, calls, 1)
	spec := calls[0]
	assert.Equal(t, engine.ProcessScript, spec.Kind)
	assert.Equal(t, "/bin/sh", spec.Command, "default interpreter preserved")
	assert.Equal(t, []string{scriptPath, "--m=opus"}, spec.Args)
	assert.Equal(t, filepath.Dir(scriptPath), spec.Dir, "script cwd stays the script directory")
	assert.Equal(t, map[string]string{"LLM_MODEL": "opus"}, spec.NodeEnv, "templates resolved on the server")
	assert.Equal(t, []string{"LLM_API_KEY"}, spec.Passthrough, "names only")
	assert.Equal(t, 7, spec.TimeoutSecs)
	assert.Equal(t, "/ws/run-1", spec.WorkDir)
	assert.Nil(t, spec.Bundle)
	var stdin map[string]any
	require.NoError(t, json.Unmarshal(spec.Stdin, &stdin))
	assert.Equal(t, "opus", stdin["model"])
}

func TestToolCallExecutor_BuildsResolvedSpecForPort(t *testing.T) {
	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("target", "repo"))
	pr := &recordingProcessRunner{res: engine.ProcessResult{Stdout: `{"done":1}`}}
	exec := process.NewToolCallExecutor(nil, pr)
	_, err := exec.Execute(context.Background(), workflow.Node{
		ID: "t1", Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{Command: "git", Args: []string{"status", "{{target}}"}, Env: map[string]string{"X": "{{target}}"}},
	}, vars, "wf")
	require.NoError(t, err)
	spec := pr.calls()[0]
	assert.Equal(t, engine.ProcessToolCall, spec.Kind)
	assert.Equal(t, "git", spec.Command)
	assert.Equal(t, []string{"status", "repo"}, spec.Args)
	assert.Equal(t, "", spec.Dir, "Phase 1 keeps the inherited cwd for tool_call")
	assert.Nil(t, spec.Stdin)
	assert.Equal(t, map[string]string{"X": "repo"}, spec.NodeEnv)
	assert.Equal(t, "", spec.WorkDir)
}

// Failure: a collision is refused on the server BEFORE the port is called, so a
// doomed step never reaches placement or consumes a slot.
func TestExecutors_RejectEnvCollisionBeforeDispatch(t *testing.T) {
	pr := &recordingProcessRunner{}
	tool := process.NewToolCallExecutor([]string{"LLM_API_KEY"}, pr)
	_, err := tool.Execute(context.Background(), workflow.Node{ID: "t1", Config: workflow.NodeConfig{
		Command: "true", Env: map[string]string{"LLM_API_KEY": "spoof"},
	}}, variable.NewStore(), "wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")

	fr := newMockFileReader()
	fr.paths["wf/run.sh"] = "/tmp/run.sh"
	script := process.NewScriptExecutor(fr, nil, pr)
	_, err = script.Execute(context.Background(), workflow.Node{ID: "s1", Config: workflow.NodeConfig{
		ScriptFile: "run.sh", Env: map[string]string{"PATH": "/evil"},
	}}, variable.NewStore(), "wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
	assert.Empty(t, pr.calls(), "the port must not be called on a collision")
}

// Error: port execution faults and non-zero exits keep today's messages and
// partial output.
func TestExecutors_PortFaultAndNonZeroExit(t *testing.T) {
	fault := &recordingProcessRunner{res: engine.ProcessResult{Stdout: "partial"}, err: context.DeadlineExceeded}
	tool := process.NewToolCallExecutor(nil, fault)
	res, err := tool.Execute(context.Background(), workflow.Node{ID: "t1", Config: workflow.NodeConfig{Command: "x"}}, variable.NewStore(), "wf")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Equal(t, "partial", res.ScriptStdout)

	nonzero := &recordingProcessRunner{res: engine.ProcessResult{Stderr: "boom\n", ExitCode: 2}}
	tool = process.NewToolCallExecutor(nil, nonzero)
	res, err = tool.Execute(context.Background(), workflow.Node{ID: "t1", Config: workflow.NodeConfig{Command: "x"}}, variable.NewStore(), "wf")
	require.Error(t, err)
	assert.Equal(t, `tool_call "t1": exit status 2: boom`, err.Error())
	assert.Equal(t, 2, res.ScriptExitCode)
}

// Edge: a nil port panics at construction (fail closed; never a silent local run).
func TestExecutors_NilProcessRunnerPanics(t *testing.T) {
	assert.Panics(t, func() { process.NewScriptExecutor(newMockFileReader(), nil, nil) })
	assert.Panics(t, func() { process.NewToolCallExecutor(nil, nil) })
}
