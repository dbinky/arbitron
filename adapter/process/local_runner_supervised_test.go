package process_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type launcherCtxKey struct{}

type recordingLauncher struct {
	mu       sync.Mutex
	ctxValue any
	started  []string
	finished int
}

func (l *recordingLauncher) Start(ctx context.Context, cmd *exec.Cmd) (func(), error) {
	l.mu.Lock()
	l.ctxValue, l.started = ctx.Value(launcherCtxKey{}), append(l.started, cmd.Path)
	l.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() { l.mu.Lock(); l.finished++; l.mu.Unlock() }, nil
}

type refusingLauncher struct{ err error }

func (r refusingLauncher) Start(context.Context, *exec.Cmd) (func(), error) { return nil, r.err }

func TestLocalProcessRunner_SupervisedLaunchGoesThroughTheLauncher(t *testing.T) {
	l := &recordingLauncher{}
	ctx := context.WithValue(context.Background(), launcherCtxKey{}, "assignment")
	res, err := process.NewSupervisedLocalProcessRunner(l).RunProcess(ctx, engine.ProcessSpec{Command: "/bin/sh", Args: []string{"-c", "echo hi; exit 3"}})
	require.NoError(t, err)
	assert.Equal(t, "hi\n", res.Stdout)
	assert.Equal(t, 3, res.ExitCode)
	l.mu.Lock()
	defer l.mu.Unlock()
	assert.Equal(t, "assignment", l.ctxValue, "the launcher receives the caller's context, which carries the assignment")
	assert.Equal(t, []string{"/bin/sh"}, l.started)
	assert.Equal(t, 1, l.finished, "finish runs exactly once, after Wait")
}

func TestLocalProcessRunner_LauncherRefusalStartsNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	refuse := errors.New("no assignment")
	_, err := process.NewSupervisedLocalProcessRunner(refusingLauncher{err: refuse}).RunProcess(context.Background(),
		engine.ProcessSpec{Command: "/bin/sh", Args: []string{"-c", `touch "$0"`, marker}})
	require.ErrorIs(t, err, refuse)
	_, statErr := os.Stat(marker)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestNewSupervisedLocalProcessRunner_RequiresALauncher(t *testing.T) {
	assert.Panics(t, func() { process.NewSupervisedLocalProcessRunner(nil) })
}
