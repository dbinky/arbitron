package process_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockFileReader implements engine.FileReader for tests.
type mockFileReader struct {
	files map[string][]byte
	paths map[string]string
}

func newMockFileReader() *mockFileReader {
	return &mockFileReader{
		files: make(map[string][]byte),
		paths: make(map[string]string),
	}
}

func (m *mockFileReader) ReadFile(_ uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	key := workflowName + "/" + relativePath
	data, ok := m.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (m *mockFileReader) ResolvePath(_ uuid.UUID, workflowName, relativePath string) (string, error) {
	key := workflowName + "/" + relativePath
	p, ok := m.paths[key]
	if !ok {
		return "", os.ErrNotExist
	}
	return p, nil
}

func (m *mockFileReader) FileExists(_ uuid.UUID, workflowName, relativePath string) bool {
	key := workflowName + "/" + relativePath
	_, ok := m.paths[key]
	return ok
}

func writeTempScript(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	err := os.WriteFile(path, []byte(content), 0o755)
	require.NoError(t, err)
	return path
}

func TestScriptExecutor_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	scriptPath := writeTempScript(t, `#!/bin/sh
echo '{"result": "ok"}'
`)

	fr := newMockFileReader()
	fr.paths["test-wf/run.sh"] = scriptPath

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "script1",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:  "run.sh",
			Interpreter: "/bin/sh",
		},
	}

	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"result": "ok"}, result.Output)
	assert.Equal(t, 0, result.ScriptExitCode)
}

func TestScriptExecutor_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	scriptPath := writeTempScript(t, `#!/bin/sh
echo "something went wrong" >&2
exit 1
`)

	fr := newMockFileReader()
	fr.paths["test-wf/fail.sh"] = scriptPath

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "script1",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:  "fail.sh",
			Interpreter: "/bin/sh",
		},
	}

	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit")
	assert.Equal(t, 1, result.ScriptExitCode, "exit code must be populated even on error")
	assert.Equal(t, "something went wrong", strings.TrimSpace(result.ScriptStderr))
}

func TestScriptExecutor_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	scriptPath := writeTempScript(t, `#!/bin/sh
sleep 30
echo '{"result": "late"}'
`)

	fr := newMockFileReader()
	fr.paths["test-wf/slow.sh"] = scriptPath

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "script1",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:     "slow.sh",
			Interpreter:    "/bin/sh",
			TimeoutSeconds: 1,
		},
	}

	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	start := time.Now()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadline")
	assert.Less(t, elapsed, 5*time.Second)
}

func TestScriptExecutor_NonJSONStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	scriptPath := writeTempScript(t, `#!/bin/sh
echo "not json"
`)

	fr := newMockFileReader()
	fr.paths["test-wf/text.sh"] = scriptPath

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "script1",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:  "text.sh",
			Interpreter: "/bin/sh",
		},
	}

	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON")
}

func TestScriptExecutor_SubstitutionInArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	scriptPath := writeTempScript(t, `#!/bin/sh
echo "{\"arg\": \"$1\"}"
`)

	fr := newMockFileReader()
	fr.paths["test-wf/args.sh"] = scriptPath

	vars := variable.NewStore()
	vars.SetInput("name", "Alice")

	node := workflow.Node{
		ID:   "script1",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:  "args.sh",
			Interpreter: "/bin/sh",
			Args:        []string{"{{name}}"},
		},
	}

	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"arg": "Alice"}, result.Output)
}

func TestScriptExecutor_StdinVariables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	// Script reads stdin and echoes a field from it
	scriptPath := writeTempScript(t, `#!/bin/sh
# Read stdin and extract the "greeting" field using basic tools
INPUT=$(cat)
# Use Python to parse JSON if available, otherwise use a simpler approach
echo "$INPUT"
`)

	fr := newMockFileReader()
	fr.paths["test-wf/stdin.sh"] = scriptPath

	vars := variable.NewStore()
	vars.SetInput("greeting", "hello")

	node := workflow.Node{
		ID:   "script1",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:  "stdin.sh",
			Interpreter: "/bin/sh",
		},
	}

	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	// The script echoes the full JSON from stdin
	output, ok := result.Output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "hello", output["greeting"])
}

func TestScriptExecutor_FileNotFound(t *testing.T) {
	fr := newMockFileReader()

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "script1",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:  "missing.sh",
			Interpreter: "/bin/sh",
		},
	}

	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
}

func TestScriptExecutor_PerNodeEnvAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	scriptPath := writeTempScript(t, `#!/bin/sh
echo "{\"model\": \"$LLM_MODEL\"}"
`)
	fr := newMockFileReader()
	fr.paths["test-wf/e.sh"] = scriptPath
	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("m", "opus"))
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile: "e.sh", Interpreter: "/bin/sh",
			Env: map[string]string{"LLM_MODEL": "{{m}}"},
		},
	}
	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"model": "opus"}, result.Output)
}

func TestScriptExecutor_PassthroughSecretAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	t.Setenv("LLM_API_KEY", "sk-xyz")
	scriptPath := writeTempScript(t, `#!/bin/sh
echo "{\"k\": \"$LLM_API_KEY\"}"
`)
	fr := newMockFileReader()
	fr.paths["test-wf/k.sh"] = scriptPath
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{ScriptFile: "k.sh", Interpreter: "/bin/sh"},
	}
	exec := process.NewScriptExecutor(fr, []string{"LLM_API_KEY"}, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "sk-xyz"}, result.Output)
}

func TestScriptExecutor_EnvCollisionFailsNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	scriptPath := writeTempScript(t, "#!/bin/sh\necho '{}'\n")
	fr := newMockFileReader()
	fr.paths["test-wf/c.sh"] = scriptPath
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile: "c.sh", Interpreter: "/bin/sh",
			Env: map[string]string{"PATH": "/evil"},
		},
	}
	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	_, err := exec.Execute(context.Background(), node, variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}

func TestScriptExecutor_SecretNotLeakedWithoutPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	t.Setenv("MY_SECRET", "leak")
	scriptPath := writeTempScript(t, `#!/bin/sh
echo "{\"s\": \"${MY_SECRET:-absent}\"}"
`)
	fr := newMockFileReader()
	fr.paths["test-wf/leak.sh"] = scriptPath
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{ScriptFile: "leak.sh", Interpreter: "/bin/sh"},
	}
	exec := process.NewScriptExecutor(fr, nil, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"s": "absent"}, result.Output)
}
