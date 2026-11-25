package builtin_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/process"
	"github.com/arbitron/arbitron/adapter/runner/builtin"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ runner.BuiltInStopper = (*builtin.ProcessSupervisor)(nil)

// startSurvivor launches, through sup, a job whose process group ignores
// SIGTERM and has a TERM-ignoring background descendant, as an interrupted
// agent would. The leader is reaped in the background (after a real restart,
// init does this). It returns the descendant's pid and the file the leader
// keeps appending to.
func startSurvivor(t *testing.T, f *supFixture, sup *builtin.ProcessSupervisor, ctx context.Context) (child int, ticks string) {
	t.Helper()
	ticks, childFile := f.file("ticks"), f.file("child")
	script := `trap "" TERM; (trap "" TERM; while :; do sleep 0.05; done) >/dev/null 2>&1 & echo $! > "$0"; echo up; while :; do echo tick >> "$1"; sleep 0.05; done`
	lines, wait, err := claudecli.NewSupervisedExecRunner(sup).Run(ctx, claudecli.RunSpec{Bin: "/bin/sh", Args: []string{"-c", script, childFile, ticks}})
	require.NoError(t, err)
	require.Equal(t, "up", <-lines)
	go func() {
		for range lines {
		}
		_ = wait()
	}()
	child = readPID(t, childFile)
	a, _ := runner.AssignmentFromContext(ctx)
	leader, _, _ := f.row(t, a.Fence)
	// Kill leftovers only when the test failed before a confirmed stop: once the
	// row is stopped, the ids may already belong to unrelated processes.
	t.Cleanup(func() {
		if _, _, s := f.row(t, a.Fence); s != "stopped" && leader > 1 {
			_ = syscall.Kill(-leader, syscall.SIGKILL)
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	})
	return child, ticks
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	require.NoError(t, err)
	return st.Size()
}

func stopCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestProcessSupervisor_StopAndWaitAfterRestartKillsTheWholeGroup(t *testing.T) {
	f := newSupFixture(t)
	ctx, a := f.assign(t)
	child, ticks := startSurvivor(t, f, builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{}), ctx)
	leader, _, state := f.row(t, a.Fence)
	require.Equal(t, "running", state)

	// Restart: a new supervisor on a new DB handle has no in-memory handle.
	restarted := builtin.NewProcessSupervisor(f.reopen(t), builtin.SupervisorOptions{Grace: 200 * time.Millisecond})
	f.fence(t, a)
	require.NoError(t, restarted.StopAndWait(stopCtx(t, 10*time.Second), a.Fence))

	assert.ErrorIs(t, syscall.Kill(-leader, 0), syscall.ESRCH, "the whole process group is gone")
	assert.ErrorIs(t, syscall.Kill(child, 0), syscall.ESRCH, "the TERM-ignoring descendant was killed too")
	_, _, state = f.row(t, a.Fence)
	assert.Equal(t, "stopped", state)
	before := fileSize(t, ticks)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, fileSize(t, ticks), "no write after a confirmed stop")
}

func TestProcessSupervisor_StoppedIsPersistedOnlyAfterConfirmedExit(t *testing.T) {
	f := newSupFixture(t)
	ctx, a := f.assign(t)
	var leader atomic.Int64
	var mu sync.Mutex
	var aliveAtStop []bool
	store := &hookedStore{BuiltInProcessStore: f.store, beforeStop: func(from runner.BuiltInProcessState) error {
		if from == runner.ProcessRunning {
			mu.Lock()
			aliveAtStop = append(aliveAtStop, syscall.Kill(-int(leader.Load()), 0) == nil)
			mu.Unlock()
		}
		return nil
	}}
	sup := builtin.NewProcessSupervisor(store, builtin.SupervisorOptions{Grace: 100 * time.Millisecond})
	startSurvivor(t, f, sup, ctx)
	pid, _, _ := f.row(t, a.Fence)
	leader.Store(int64(pid))
	f.fence(t, a)

	require.NoError(t, sup.StopAndWait(stopCtx(t, 10*time.Second), a.Fence))
	mu.Lock()
	assert.NotEmpty(t, aliveAtStop)
	assert.NotContains(t, aliveAtStop, true, "stopped is written only once the group is gone")
	mu.Unlock()
	_, _, state := f.row(t, a.Fence)
	assert.Equal(t, "stopped", state)
	require.NoError(t, sup.StopAndWait(stopCtx(t, time.Second), a.Fence), "idempotent once stopped")
}

func TestProcessSupervisor_PIDReuseIsNeverSignalled(t *testing.T) {
	f := newSupFixture(t)
	_, a := f.assign(t)
	stranger := exec.Command("/bin/sleep", "30")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, stranger.Start())
	t.Cleanup(func() { _ = stranger.Process.Kill(); _ = stranger.Wait() })
	// The recorded guardian died long ago. Its pid now belongs to an unrelated group leader.
	require.NoError(t, f.store.Permit(context.Background(), a.Fence))
	require.NoError(t, f.store.Record(context.Background(), a.Fence, stranger.Process.Pid, runtime.GOOS+":stale-start"))
	f.fence(t, a)
	var mu sync.Mutex
	var signalled []syscall.Signal
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{Kill: func(pid int, sig syscall.Signal) error {
		if sig != 0 {
			mu.Lock()
			signalled = append(signalled, sig)
			mu.Unlock()
		}
		return syscall.Kill(pid, sig)
	}})

	require.NoError(t, sup.StopAndWait(stopCtx(t, 5*time.Second), a.Fence))
	mu.Lock()
	assert.Empty(t, signalled, "a reused pid is identified by its start marker and never signalled")
	mu.Unlock()
	assert.NoError(t, syscall.Kill(stranger.Process.Pid, 0), "the unrelated process is alive")
	_, _, state := f.row(t, a.Fence)
	assert.Equal(t, "stopped", state, "the recorded group is provably gone")
}

func TestProcessSupervisor_RefusesToSignalInitOrBroadcast(t *testing.T) {
	f := newSupFixture(t)
	_, a := f.assign(t)
	_, err := f.db.Exec(`INSERT INTO builtin_processes(job_id, generation, guardian_pid, process_start_marker, state) VALUES (?, 1, 1, 'x', 'running')`, a.Job.ID.String())
	require.NoError(t, err)
	var calls atomic.Int32
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{Kill: func(int, syscall.Signal) error { calls.Add(1); return nil }})

	require.ErrorIs(t, sup.StopAndWait(stopCtx(t, time.Second), a.Fence), builtin.ErrStopUnconfirmed)
	assert.Zero(t, calls.Load(), "kill(-1) would signal every process")
	_, _, state := f.row(t, a.Fence)
	assert.Equal(t, "running", state)
}

func TestProcessSupervisor_StopFailurePropagatesAndKeepsTheRowRunning(t *testing.T) {
	boom := errors.New("database is locked")
	realProbe := func(pid int, sig syscall.Signal) (error, bool) {
		if sig == 0 {
			return syscall.Kill(pid, 0), true
		}
		return nil, false
	}
	cases := map[string]struct {
		opt     builtin.SupervisorOptions
		hook    func(from runner.BuiltInProcessState) error
		timeout time.Duration
		want    []error
	}{
		"permission": {opt: builtin.SupervisorOptions{Kill: func(pid int, sig syscall.Signal) error {
			if err, ok := realProbe(pid, sig); ok {
				return err
			}
			return syscall.EPERM
		}}, timeout: 5 * time.Second, want: []error{syscall.EPERM}},
		"timeout": {opt: builtin.SupervisorOptions{Grace: 50 * time.Millisecond, Kill: func(pid int, sig syscall.Signal) error {
			if err, ok := realProbe(pid, sig); ok {
				return err
			}
			return nil // the signal is swallowed, so the group never exits
		}}, timeout: 300 * time.Millisecond, want: []error{builtin.ErrStopUnconfirmed, context.DeadlineExceeded}},
		"storage": {hook: func(runner.BuiltInProcessState) error { return boom }, timeout: 5 * time.Second, want: []error{boom}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSupFixture(t)
			ctx, a := f.assign(t)
			sup := builtin.NewProcessSupervisor(&hookedStore{BuiltInProcessStore: f.store, beforeStop: tc.hook}, tc.opt)
			runCtx, cancel := context.WithCancel(ctx)
			lines, wait, err := claudecli.NewSupervisedExecRunner(sup).Run(runCtx, claudecli.RunSpec{Bin: "/bin/sh", Args: []string{"-c", "echo up; exec sleep 30"}})
			require.NoError(t, err)
			require.Equal(t, "up", <-lines)
			done := make(chan struct{})
			go func() {
				for range lines {
				}
				_ = wait()
				close(done)
			}()
			t.Cleanup(func() { cancel(); <-done })
			f.fence(t, a)

			err = sup.StopAndWait(stopCtx(t, tc.timeout), a.Fence)
			for _, w := range tc.want {
				require.ErrorIs(t, err, w)
			}
			_, _, state := f.row(t, a.Fence)
			assert.Equal(t, "running", state, "an unconfirmed stop never writes stopped, so Supersede, restore and retry stay blocked")
		})
	}
}

func TestProcessSupervisor_StopAndWaitWithoutAReleasedGuardianIsDurablyStopped(t *testing.T) {
	f := newSupFixture(t)
	sup := builtin.NewProcessSupervisor(f.store, builtin.SupervisorOptions{})

	ctx, never := f.assign(t)
	f.fence(t, never)
	require.NoError(t, sup.StopAndWait(stopCtx(t, time.Second), never.Fence))
	pid, _, state := f.row(t, never.Fence)
	assert.Equal(t, []any{0, "stopped"}, []any{pid, state}, "nothing was ever permitted, so nothing can be writing")
	_, err := process.NewSupervisedLocalProcessRunner(sup).RunProcess(ctx, engine.ProcessSpec{Command: "/bin/sh", Args: []string{"-c", "true"}})
	require.ErrorIs(t, err, runner.ErrFenced, "a stopped, fenced job can never launch again")

	// A permit left by a crash between Permit and Record.
	_, crashed := f.assign(t)
	require.NoError(t, f.store.Permit(context.Background(), crashed.Fence))
	f.fence(t, crashed)
	require.NoError(t, sup.StopAndWait(stopCtx(t, time.Second), crashed.Fence))
	_, _, state = f.row(t, crashed.Fence)
	assert.Equal(t, "stopped", state, "an unreleased guardian cannot have done work")
}
