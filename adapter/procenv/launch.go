package procenv

import (
	"context"
	"os/exec"
)

// Launcher starts a fully configured *exec.Cmd (Path, Args, Dir, Env, stdio and
// cancellation already set). The caller calls the returned finish exactly once,
// after cmd.Wait returns. When err is non-nil, the command was not handed to
// the caller and finish is nil. ctx carries the runner assignment for
// supervised launches (domain/runner.AssignmentFromContext).
type Launcher interface {
	Start(ctx context.Context, cmd *exec.Cmd) (finish func(), err error)
}

// Unsupervised is the explicit launcher for read-only probes and non-workflow
// subprocesses. It only starts the command.
type Unsupervised struct{}

func (Unsupervised) Start(_ context.Context, cmd *exec.Cmd) (func(), error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
