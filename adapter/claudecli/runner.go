package claudecli

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// maxLineBytes bounds a single stream-json line (tool results can be large).
const maxLineBytes = 4 * 1024 * 1024

// RunSpec describes a subprocess invocation.
type RunSpec struct {
	Bin   string
	Args  []string
	Stdin string
	Dir   string
	Env   []string
}

// CommandRunner starts a subprocess and streams its stdout line by line.
// The lines channel is closed when stdout reaches EOF; wait then blocks until
// the process exits and returns its exit error (nil on success).
//
// Contract: callers MUST drain the lines channel to completion (until it is
// closed) before calling wait. Draining establishes the happens-before edge
// that lets wait observe any stdout read error.
type CommandRunner interface {
	Run(ctx context.Context, spec RunSpec) (lines <-chan string, wait func() error, err error)
}

// execRunner is the production CommandRunner backed by os/exec.
type execRunner struct{}

// newExecRunner constructs the os/exec-backed CommandRunner.
func newExecRunner() *execRunner { return &execRunner{} }

// Run starts the process described by spec.
func (r *execRunner) Run(ctx context.Context, spec RunSpec) (<-chan string, func() error, error) {
	cmd := exec.CommandContext(ctx, spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("claudecli: start %q: %w", spec.Bin, err)
	}

	lines := make(chan string)
	var scanErr error
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		scanErr = scanner.Err()
	}()

	wait := func() error {
		waitErr := cmd.Wait()
		if scanErr != nil {
			return fmt.Errorf("claudecli: read stdout: %w", scanErr)
		}
		return waitErr
	}
	return lines, wait, nil
}
