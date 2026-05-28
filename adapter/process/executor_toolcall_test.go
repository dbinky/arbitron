package process_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTempCommand(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte(content), 0o755)
	require.NoError(t, err)
	return path
}

func TestToolCallExecutor_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	cmdPath := writeTempCommand(t, "tool.sh", `#!/bin/sh
echo '{"status": "done"}'
`)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: cmdPath,
			Args:    []string{},
		},
	}

	exec := process.NewToolCallExecutor(nil)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"status": "done"}, result.Output)
	assert.Equal(t, 0, result.ScriptExitCode)
}

func TestToolCallExecutor_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	cmdPath := writeTempCommand(t, "fail.sh", `#!/bin/sh
echo "error details" >&2
exit 2
`)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: cmdPath,
		},
	}

	exec := process.NewToolCallExecutor(nil)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit")
}

func TestToolCallExecutor_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	cmdPath := writeTempCommand(t, "slow.sh", `#!/bin/sh
sleep 30
echo '{"late": true}'
`)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command:        cmdPath,
			TimeoutSeconds: 1,
		},
	}

	exec := process.NewToolCallExecutor(nil)
	start := time.Now()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 5*time.Second)
}

func TestToolCallExecutor_NonJSONStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	cmdPath := writeTempCommand(t, "text.sh", `#!/bin/sh
echo "plain text output"
`)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: cmdPath,
		},
	}

	exec := process.NewToolCallExecutor(nil)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON")
}

func TestToolCallExecutor_SubstitutionInArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	cmdPath := writeTempCommand(t, "echo_arg.sh", `#!/bin/sh
echo "{\"name\": \"$1\"}"
`)

	vars := variable.NewStore()
	vars.SetInput("user", "Bob")

	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: cmdPath,
			Args:    []string{"{{user}}"},
		},
	}

	exec := process.NewToolCallExecutor(nil)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"name": "Bob"}, result.Output)
}

func TestToolCallExecutor_NoStdinPiped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	// Script tries to read stdin - should get empty
	cmdPath := writeTempCommand(t, "read_stdin.sh", `#!/bin/sh
INPUT=$(cat)
if [ -z "$INPUT" ]; then
  echo '{"stdin": "empty"}'
else
  echo '{"stdin": "has_data"}'
fi
`)

	vars := variable.NewStore()
	vars.SetInput("data", "should_not_be_piped")

	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: cmdPath,
		},
	}

	exec := process.NewToolCallExecutor(nil)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"stdin": "empty"}, result.Output)
}

func TestToolCallExecutor_CommandNotFound(t *testing.T) {
	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: "/nonexistent/command/that/does/not/exist",
		},
	}

	exec := process.NewToolCallExecutor(nil)
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "start")
}

func TestToolCallExecutor_StderrCaptured(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	cmdPath := writeTempCommand(t, "stderr.sh", `#!/bin/sh
echo "warning message" >&2
echo '{"ok": true}'
`)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "tool1",
		Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: cmdPath,
		},
	}

	exec := process.NewToolCallExecutor(nil)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Contains(t, result.ScriptStderr, "warning message")
}

func TestToolCallExecutor_PassthroughSecretAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	t.Setenv("LLM_API_KEY", "sk-tc")
	node := workflow.Node{
		ID: "t1", Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: "/bin/sh",
			Args:    []string{"-c", `echo "{\"k\": \"$LLM_API_KEY\"}"`},
		},
	}
	exec := process.NewToolCallExecutor([]string{"LLM_API_KEY"})
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "sk-tc"}, result.Output)
}

func TestToolCallExecutor_SecretNotLeaked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	t.Setenv("MY_SECRET", "leak")
	node := workflow.Node{
		ID: "t1", Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: "/bin/sh",
			Args:    []string{"-c", `echo "{\"s\": \"${MY_SECRET:-absent}\"}"`},
		},
	}
	exec := process.NewToolCallExecutor(nil)
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"s": "absent"}, result.Output)
}

func TestToolCallExecutor_EnvCollisionFailsNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	node := workflow.Node{
		ID: "t1", Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: "/bin/sh",
			Args:    []string{"-c", "echo '{}'"},
			Env:     map[string]string{"PATH": "/evil"},
		},
	}
	exec := process.NewToolCallExecutor(nil)
	_, err := exec.Execute(context.Background(), node, variable.NewStore(), "wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}
