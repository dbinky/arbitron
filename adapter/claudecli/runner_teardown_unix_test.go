//go:build !windows

package claudecli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing group cancellation leaves the pipe-holding child alive; closing only
// the parent's pipes must not be mistaken for terminating the harness tree.
func TestExecRunnerBoundsDescendantTeardown(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "output_limit", "parent_exit", "detached_pipes", "detached_stdout"} {
		t.Run(mode, func(t *testing.T) {
			detached := strings.HasPrefix(mode, "detached_")
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" || mode == "parent_exit" || detached {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			}
			defer cancel()
			script := `sleep 10 & printf '%s\n' "$!"; wait`
			if mode == "parent_exit" {
				script = `sleep 10 & printf '%s\n' "$!"; exit 0`
			}
			var limit int64
			if mode == "output_limit" {
				script = `sleep 10 & printf '%s\n' "$!"; printf '%0512d' 0; wait`
				limit = 128
			}
			spec := RunSpec{Bin: "sh", Args: []string{"-c", script}, MaxResponseBytes: limit}
			if detached {
				// An escaped descendant holds both stdout and stderr. Closing our
				// stdout and bounding exec's stderr copy are separate requirements.
				spec.Args = []string{"-c", `"$1" -test.run=^TestExecRunnerDetachedPipeHelper$ & wait`, "sh", os.Args[0]}
				spec.Env = append(os.Environ(), "ARBITRON_RUNNER_PIPE_HELPER="+mode)
			}
			lines, wait, err := newExecRunner().Run(ctx, spec)
			require.NoError(t, err)
			var first string
			select {
			case first = <-lines:
			case <-time.After(2 * time.Second):
				t.Fatal("child did not start")
			}
			pid, err := strconv.Atoi(first)
			require.NoError(t, err)
			t.Cleanup(func() {
				p, _ := os.FindProcess(pid)
				_ = p.Kill()
			})
			if mode == "cancel" {
				cancel()
			}
			done := make(chan error, 1)
			go func() {
				for range lines {
				}
				done <- wait()
			}()
			select {
			case err := <-done:
				if mode == "output_limit" {
					assert.ErrorIs(t, err, ErrResponseTooLarge)
				} else {
					assert.Error(t, err, "cancellation cannot become a successful run")
				}
			case <-time.After(1500 * time.Millisecond):
				t.Error("pipe-holding descendant exceeded teardown bound")
				p, _ := os.FindProcess(pid)
				_ = p.Kill()
				cancel()
				<-done
			}
			if detached {
				return // escaped groups require OS containment; cleanup owns this helper
			}
			assert.Eventually(t, func() bool {
				// A killed orphan may remain a zombie until init reaps it.
				state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
				return err != nil || strings.HasPrefix(strings.TrimSpace(string(state)), "Z")
			}, time.Second, 10*time.Millisecond, "descendant is still running")
		})
	}
}

func TestExecRunnerDetachedPipeHelper(t *testing.T) {
	mode := os.Getenv("ARBITRON_RUNNER_PIPE_HELPER")
	if mode == "" {
		return
	}
	if _, err := syscall.Setsid(); err != nil {
		t.Fatal(err)
	}
	if mode == "detached_stdout" {
		_ = os.Stderr.Close()
	}
	fmt.Println(os.Getpid())
	time.Sleep(10 * time.Second)
	os.Exit(0)
}
