package process_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix shell required")
	}
}

// Happy path: stdin is piped, cwd is honoured, stdout/stderr captured.
func TestLocalProcessRunner_StdinAndDir(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	res, err := process.NewLocalProcessRunner().RunProcess(context.Background(), engine.ProcessSpec{
		Kind:    engine.ProcessScript,
		Command: "/bin/sh",
		Args:    []string{"-c", `pwd; cat; echo oops 1>&2`},
		Dir:     dir,
		Stdin:   []byte("hello-stdin"),
	})
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Contains(t, res.Stdout, resolved)
	assert.Contains(t, res.Stdout, "hello-stdin")
	assert.Equal(t, "oops\n", res.Stderr)
	assert.Equal(t, 0, res.ExitCode)
}

// Passthrough VALUES are read on the executing machine at RunProcess time; the
// spec only ever carries the NAME.
func TestLocalProcessRunner_PassthroughValueReadOnExecutingMachine(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("RR_PASS_VALUE", "from-this-machine")
	res, err := process.NewLocalProcessRunner().RunProcess(context.Background(), engine.ProcessSpec{
		Kind:        engine.ProcessToolCall,
		Command:     "/bin/sh",
		Args:        []string{"-c", `printf '%s|%s' "$RR_PASS_VALUE" "${RR_NOT_LISTED:-absent}"`},
		Passthrough: []string{"RR_PASS_VALUE"},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-this-machine|absent", res.Stdout)
}

// NodeEnv is ALREADY resolved on the server: the runner must not re-template it.
func TestLocalProcessRunner_NodeEnvIsLiteral(t *testing.T) {
	skipOnWindows(t)
	res, err := process.NewLocalProcessRunner().RunProcess(context.Background(), engine.ProcessSpec{
		Kind:    engine.ProcessToolCall,
		Command: "/bin/sh",
		Args:    []string{"-c", `printf '%s' "$NODE_VALUE"`},
		NodeEnv: map[string]string{"NODE_VALUE": "{{literal}}"},
	})
	require.NoError(t, err)
	assert.Equal(t, "{{literal}}", res.Stdout)
}

// Failure: a node-env key colliding with a base or passthrough name is refused
// before any process starts.
func TestLocalProcessRunner_RejectsEnvCollision(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	for name, spec := range map[string]engine.ProcessSpec{
		"base":        {NodeEnv: map[string]string{"PATH": "/evil"}},
		"passthrough": {Passthrough: []string{"LLM_API_KEY"}, NodeEnv: map[string]string{"LLM_API_KEY": "spoof"}},
		"lc prefix":   {NodeEnv: map[string]string{"LC_ALL": "C"}},
	} {
		t.Run(name, func(t *testing.T) {
			spec.Kind = engine.ProcessToolCall
			spec.Command = "/usr/bin/touch"
			spec.Args = []string{marker}
			_, err := process.NewLocalProcessRunner().RunProcess(context.Background(), spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "collides")
			_, statErr := os.Stat(marker)
			assert.True(t, errors.Is(statErr, os.ErrNotExist), "no process may start on a collision")
		})
	}
}

// Error: a completed non-zero exit is NOT an execution fault.
func TestLocalProcessRunner_NonZeroExitIsResultNotError(t *testing.T) {
	skipOnWindows(t)
	res, err := process.NewLocalProcessRunner().RunProcess(context.Background(), engine.ProcessSpec{
		Kind: engine.ProcessToolCall, Command: "/bin/sh", Args: []string{"-c", "echo bad 1>&2; exit 3"},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, res.ExitCode)
	assert.Equal(t, "bad", strings.TrimSpace(res.Stderr))
}

// Edge: the timeout kills the process group and surfaces DeadlineExceeded.
func TestLocalProcessRunner_TimeoutIsExecutionFault(t *testing.T) {
	skipOnWindows(t)
	_, err := process.NewLocalProcessRunner().RunProcess(context.Background(), engine.ProcessSpec{
		Kind: engine.ProcessToolCall, Command: "/bin/sh", Args: []string{"-c", "sleep 5"}, TimeoutSecs: 1,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// Error: a missing binary is a start failure (execution fault).
func TestLocalProcessRunner_StartFailure(t *testing.T) {
	_, err := process.NewLocalProcessRunner().RunProcess(context.Background(), engine.ProcessSpec{
		Kind: engine.ProcessToolCall, Command: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start")
}
