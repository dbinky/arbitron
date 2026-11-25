package process

import (
	"context"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
)

// LocalProcessRunner executes a ProcessSpec on this machine with os/exec. It is
// the built-in runner's process path (adapter/runner/builtin). Workflow
// execution uses the supervised form (P3 T13). The zero value and
// NewLocalProcessRunner are explicitly unsupervised.
type LocalProcessRunner struct{ launcher procenv.Launcher }

var _ engine.ProcessRunner = (*LocalProcessRunner)(nil)

// NewLocalProcessRunner returns the unsupervised os/exec-backed ProcessRunner.
func NewLocalProcessRunner() *LocalProcessRunner { return &LocalProcessRunner{} }

// NewSupervisedLocalProcessRunner launches every command through l (the
// built-in ProcessSupervisor). A nil launcher panics: a "supervised" runner
// that silently launched unsupervised would leave untracked writers.
func NewSupervisedLocalProcessRunner(l procenv.Launcher) *LocalProcessRunner {
	if l == nil {
		panic("process: NewSupervisedLocalProcessRunner requires a launcher")
	}
	return &LocalProcessRunner{launcher: l}
}

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
		Launcher:    r.launcher,
	})
	return engine.ProcessResult{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode}, runErr
}
