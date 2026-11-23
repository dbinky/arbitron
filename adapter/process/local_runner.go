package process

import (
	"context"

	"github.com/arbitron/arbitron/domain/engine"
)

// LocalProcessRunner executes a ProcessSpec on this machine with os/exec. It is
// the built-in runner's process path (adapter/runner/builtin) and, until Task 16
// wires the routed runner, the executors' direct runner.
type LocalProcessRunner struct{}

var _ engine.ProcessRunner = (*LocalProcessRunner)(nil)

// NewLocalProcessRunner returns the os/exec-backed ProcessRunner.
func NewLocalProcessRunner() *LocalProcessRunner { return &LocalProcessRunner{} }

// RunProcess builds the controlled environment on this machine (passthrough
// values read here) and runs the command with the spec's cwd, stdin and timeout.
func (r *LocalProcessRunner) RunProcess(ctx context.Context, spec engine.ProcessSpec) (engine.ProcessResult, error) {
	env, err := buildEnvFromResolved(spec.Passthrough, spec.NodeEnv)
	if err != nil {
		return engine.ProcessResult{}, err
	}
	res, runErr := run(ctx, commandSpec{
		Name:        spec.Command,
		Args:        spec.Args,
		Dir:         spec.Dir,
		Stdin:       spec.Stdin,
		Env:         env,
		TimeoutSecs: spec.TimeoutSecs,
	})
	return engine.ProcessResult{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode}, runErr
}
