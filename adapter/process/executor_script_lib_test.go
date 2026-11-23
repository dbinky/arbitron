package process_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScriptExecutor_RunsLibScript wires the real FileReaderAdapter so an
// @lib/ scriptFile resolves to the shared library and runs with its cwd inside
// the library's real path.
func TestScriptExecutor_RunsLibScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}

	base := t.TempDir()
	libDir := filepath.Join(base, "shared", workflow.LibraryFolder)
	require.NoError(t, os.MkdirAll(libDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "echo.sh"), []byte(`#!/bin/sh
echo '{"cwd":"'$(pwd)'"}'
`), 0o755))

	node := workflow.Node{
		ID:   "lib-script",
		Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile:  "@lib/echo.sh",
			Interpreter: "/bin/sh",
		},
	}

	exec := process.NewScriptExecutor(filesystem.NewFileReader(base), nil, process.NewLocalProcessRunner())
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "any-workflow")
	require.NoError(t, err)

	realLib, err := filepath.EvalSymlinks(libDir)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"cwd": realLib}, result.Output)
}
