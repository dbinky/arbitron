package process

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipWin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell not supported on Windows")
	}
}

func TestRun_SuccessCapturesStdout(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{Name: "/bin/sh", Args: []string{"-c", "echo hi"}})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, res.Stdout, "hi")
}

func TestRun_NonZeroExitIsNotRunError(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{Name: "/bin/sh", Args: []string{"-c", "echo oops >&2; exit 3"}})
	require.NoError(t, err) // completed-but-failed is NOT a run error
	assert.Equal(t, 3, res.ExitCode)
	assert.Contains(t, res.Stderr, "oops")
}

func TestRun_StartFailureIsRunError(t *testing.T) {
	_, err := run(context.Background(), commandSpec{Name: "/nonexistent/binary-xyz"})
	require.Error(t, err)
}

func TestRun_TimeoutIsRunErrorAndFast(t *testing.T) {
	skipWin(t)
	start := time.Now()
	_, err := run(context.Background(), commandSpec{Name: "/bin/sh", Args: []string{"-c", "sleep 30"}, TimeoutSecs: 1})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestRun_PassesEnv(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{
		Name: "/bin/sh", Args: []string{"-c", "echo $FOO"}, Env: []string{"FOO=bar"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.Stdout, "bar")
}

func TestRun_PassesStdin(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{
		Name: "/bin/sh", Args: []string{"-c", "cat"}, Stdin: []byte("piped"),
	})
	require.NoError(t, err)
	assert.Contains(t, res.Stdout, "piped")
}

func TestRun_Dir(t *testing.T) {
	skipWin(t)
	dir := t.TempDir()
	res, err := run(context.Background(), commandSpec{
		Name: "/bin/sh", Args: []string{"-c", "pwd"}, Dir: dir,
	})
	require.NoError(t, err)
	// On macOS the /tmp <-> /private/tmp symlink means pwd and t.TempDir() may
	// disagree on the absolute path prefix. The unique basename is stable in
	// either direction, so assert on that to keep the test reliable.
	assert.Contains(t, res.Stdout, filepath.Base(dir))
}

func TestRun_AlreadyCancelledCtxIsRunError(t *testing.T) {
	skipWin(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := run(ctx, commandSpec{Name: "/bin/sh", Args: []string{"-c", "echo hi"}})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}
