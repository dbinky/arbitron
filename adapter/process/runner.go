package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// commandSpec describes a subprocess to run.
type commandSpec struct {
	Name        string // interpreter or command
	Args        []string
	Dir         string   // cwd; "" inherits
	Stdin       []byte   // nil = no stdin
	Env         []string // final "k=v" list; nil inherits the parent env
	TimeoutSecs int      // 0 = no timeout
}

// commandResult is the outcome of a completed (or attempted) subprocess.
type commandResult struct {
	Stdout   string
	Stderr   string // bounded tail (maxOutputBytes)
	ExitCode int
}

// run starts the process and waits with an optional timeout, killing the
// process group on deadline. A completed-but-non-zero exit is reported via
// commandResult.ExitCode with a nil error. run returns a non-nil error only for
// execution faults (start failure, timeout/cancel, or a non-exit Wait error).
func run(ctx context.Context, spec commandSpec) (commandResult, error) {
	execCtx := ctx
	if spec.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSecs)*time.Second)
		defer cancel()
	}

	cmd := exec.Command(spec.Name, spec.Args...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	setProcGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, limit: maxOutputBytes}
	cmd.Stderr = &limitedWriter{buf: &stderr, limit: maxOutputBytes}

	if err := cmd.Start(); err != nil {
		return commandResult{}, fmt.Errorf("start: %w", err)
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	select {
	case <-execCtx.Done():
		killProcGroup(cmd)
		<-doneCh
		return commandResult{Stdout: stdout.String(), Stderr: stderr.String()}, execCtx.Err()
	case waitErr := <-doneCh:
		res := commandResult{Stdout: stdout.String(), Stderr: stderr.String()}
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				res.ExitCode = exitErr.ExitCode()
				return res, nil
			}
			return res, waitErr
		}
		return res, nil
	}
}
