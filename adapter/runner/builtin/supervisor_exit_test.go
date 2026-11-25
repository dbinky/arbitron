package builtin_test

import (
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runner/builtin"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/require"
)

func TestProcessSupervisor_ExitConfirmsGroupAfterDelayedReaping(t *testing.T) {
	f := newSupFixture(t)
	ctx, a := f.assign(t)
	var probes atomic.Int32
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{
		Grace: time.Second, Poll: time.Millisecond,
		Kill: func(pid int, sig syscall.Signal) error {
			require.Less(t, pid, -1)
			require.Equal(t, syscall.Signal(0), sig)
			if probes.Add(1) == 1 {
				return nil
			}
			return syscall.ESRCH
		},
	})
	_, err := process.NewSupervisedLocalProcessRunner(sup).RunProcess(ctx, engine.ProcessSpec{Command: "/bin/sh", Args: []string{"-c", "true"}})
	require.NoError(t, err)
	_, _, state := f.row(t, a.Fence)
	require.Equal(t, "stopped", state, "a transient surviving group must be checked again before leaving a stale running receipt")
	require.GreaterOrEqual(t, probes.Load(), int32(2))
}
