package claudecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type launcherTestKey struct{}

type countingLauncher struct {
	ctxSeen           any
	started, finished int
}

func (l *countingLauncher) Start(ctx context.Context, cmd *exec.Cmd) (func(), error) {
	l.ctxSeen = ctx.Value(launcherTestKey{})
	l.started++
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() { l.finished++ }, nil
}

type refusingTestLauncher struct{ err error }

func (r refusingTestLauncher) Start(context.Context, *exec.Cmd) (func(), error) { return nil, r.err }

func TestSupervisedExecRunner_LaunchesThroughTheLauncher(t *testing.T) {
	l := &countingLauncher{}
	ctx := context.WithValue(context.Background(), launcherTestKey{}, "assignment")
	lines, wait, err := NewSupervisedExecRunner(l).Run(ctx, RunSpec{Bin: "/bin/sh", Args: []string{"-c", "echo one"}})
	require.NoError(t, err)
	var got []string
	for line := range lines {
		got = append(got, line)
	}
	require.NoError(t, wait())
	assert.Equal(t, []string{"one"}, got)
	assert.Equal(t, "assignment", l.ctxSeen, "the harness launch context reaches the launcher")
	assert.Equal(t, []int{1, 1}, []int{l.started, l.finished}, "finish runs once, after Wait")
}

func TestSupervisedExecRunner_LauncherRefusalIsReturned(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	refuse := errors.New("no assignment")
	_, _, err := NewSupervisedExecRunner(refusingTestLauncher{err: refuse}).Run(context.Background(),
		RunSpec{Bin: "/bin/sh", Args: []string{"-c", `touch "$0"`, marker}})
	require.ErrorIs(t, err, refuse)
	_, statErr := os.Stat(marker)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestNewSupervisedExecRunner_RequiresALauncher(t *testing.T) {
	assert.Panics(t, func() { NewSupervisedExecRunner(nil) })
}
