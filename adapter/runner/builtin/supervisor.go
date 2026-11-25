package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/runner"
)

// ErrNoAssignment: a supervised launch whose context has no complete runner
// assignment. It fails closed (contracts §25 "supervised assignment provenance").
var ErrNoAssignment = errors.New("builtin supervisor: supervised launch has no runner assignment in its context")

// ErrStopUnconfirmed: the process group could not be proven gone.
var ErrStopUnconfirmed = errors.New("builtin supervisor: process group exit not confirmed")

// errProcessGone: no live (non-zombie) process has this pid.
var errProcessGone = errors.New("builtin supervisor: process is gone")

const (
	guardianGo        = "go\n"
	guardianAbortExit = 97
)

// SupervisorOptions tunes stopping. Zero values take the defaults.
type SupervisorOptions struct {
	Grace time.Duration                           // SIGTERM -> SIGKILL; default 5s
	Poll  time.Duration                           // exit polling; default 25ms
	Kill  func(pid int, sig syscall.Signal) error // default syscall.Kill; test seam for EPERM/timeouts
}

// ProcessSupervisor is the built-in runner's supervised launch wrapper
// (procenv.Launcher) and its BuiltInStopper. Every launch is tied to the exact
// job fence stamped by builtin.Run. The durable permit is written before the
// guardian exists. The guardian records its pid/start marker before it may run
// the real command, so a crash between spawn and record cannot leave an
// untracked writer.
type ProcessSupervisor struct {
	store runner.BuiltInProcessStore
	opt   SupervisorOptions
	mu    sync.Mutex
	live  map[runner.Fence]*supervised
}

// supervised is this process's handle on a guardian it started; exited closes
// after the caller's cmd.Wait reaped the leader.
type supervised struct {
	pid    int
	exited chan struct{}
}

var _ procenv.Launcher = (*ProcessSupervisor)(nil)

// NewProcessSupervisor builds the supervisor. A nil store panics.
func NewProcessSupervisor(store runner.BuiltInProcessStore, opt SupervisorOptions) *ProcessSupervisor {
	if store == nil {
		panic("builtin: NewProcessSupervisor requires a BuiltInProcessStore")
	}
	if opt.Grace <= 0 {
		opt.Grace = 5 * time.Second
	}
	if opt.Poll <= 0 {
		opt.Poll = 25 * time.Millisecond
	}
	if opt.Kill == nil {
		opt.Kill = syscall.Kill
	}
	return &ProcessSupervisor{store: store, opt: opt, live: map[runner.Fence]*supervised{}}
}

// Start implements procenv.Launcher: permit -> spawn guardian -> record
// pid+marker -> release.
func (s *ProcessSupervisor) Start(ctx context.Context, cmd *exec.Cmd) (func(), error) {
	a, ok := runner.AssignmentFromContext(ctx)
	if !ok {
		return nil, ErrNoAssignment
	}
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	f := a.Fence
	if err := s.store.Permit(ctx, f); err != nil {
		return nil, fmt.Errorf("builtin supervisor: launch permit: %w", err)
	}
	goR, goW, err := armGuardian(cmd)
	if err != nil {
		return nil, errors.Join(err, s.abandon(ctx, f))
	}
	startErr := cmd.Start()
	_ = goR.Close()
	if startErr != nil {
		_ = goW.Close()
		return nil, errors.Join(fmt.Errorf("builtin supervisor: start: %w", startErr), s.abandon(ctx, f))
	}
	pid := cmd.Process.Pid
	marker, err := processStartMarker(pid)
	if err == nil {
		err = s.store.Record(ctx, f, pid, marker)
	}
	if err != nil {
		_ = goW.Close() // the unreleased guardian exits 97 before any real work
		_ = cmd.Wait()  // reap it here: the caller never receives this command
		return nil, errors.Join(fmt.Errorf("builtin supervisor: record guardian: %w", err), s.abandon(ctx, f))
	}
	h := &supervised{pid: pid, exited: make(chan struct{})}
	s.mu.Lock()
	s.live[f] = h
	s.mu.Unlock()
	_, _ = io.WriteString(goW, guardianGo) // a failed write means the guardian already exited; the caller's Wait reports it
	_ = goW.Close()
	return func() { s.finish(f, h) }, nil
}

// abandon closes a permit whose guardian never ran real work. A concurrent
// StopAndWait may already have closed it.
func (s *ProcessSupervisor) abandon(ctx context.Context, f runner.Fence) error {
	err := s.store.MarkStopped(context.WithoutCancel(ctx), f, runner.ProcessPermitted)
	if errors.Is(err, runner.ErrProcessStateChanged) {
		return nil
	}
	return err
}

// finish runs after the caller reaped the leader. It confirms `stopped` only
// when no member of the group survives. Otherwise the row stays running and
// StopAndWait will stop the survivors.
func (s *ProcessSupervisor) finish(f runner.Fence, h *supervised) {
	close(h.exited)
	s.mu.Lock()
	if s.live[f] == h {
		delete(s.live, f)
	}
	s.mu.Unlock()
	alive, err := s.groupAlive(h.pid)
	// Reaping the leader does not imply that the kernel has reaped the
	// simultaneously terminated descendants yet. Confirm for a bounded grace
	// period; a surviving group still leaves the receipt running.
	deadline := time.Now().Add(s.opt.Grace)
	for err == nil && alive && time.Now().Before(deadline) {
		time.Sleep(s.opt.Poll)
		alive, err = s.groupAlive(h.pid)
	}
	if err == nil && !alive {
		err = s.store.MarkStopped(context.Background(), f, runner.ProcessRunning)
		if errors.Is(err, runner.ErrProcessStateChanged) {
			err = nil
		}
	}
	if err != nil {
		slog.Warn("builtin supervisor: supervised process exit not confirmed",
			slog.String("job", f.JobID.String()), slog.Int64("generation", f.Generation), slog.Any("error", err))
	}
}

// groupAlive reports whether any member of process group pgid exists. EPERM
// means it exists.
func (s *ProcessSupervisor) groupAlive(pgid int) (bool, error) {
	err := s.opt.Kill(-pgid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, fmt.Errorf("builtin supervisor: probe process group %d: %w", pgid, err)
	}
}

// armGuardian rewrites cmd so that /bin/sh, leading its own process group,
// blocks until the server writes "go" on an inherited pipe and then execs the
// real command in place (same pid, same start marker). If the server dies first,
// the pipe closes and the guardian exits guardianAbortExit without running
// anything. The write end is close-on-exec, so no other child inherits it.
func armGuardian(cmd *exec.Cmd) (goR, goW *os.File, err error) {
	fd := 3 + len(cmd.ExtraFiles)
	if fd > 9 {
		return nil, nil, fmt.Errorf("builtin supervisor: too many inherited files (%d)", len(cmd.ExtraFiles))
	}
	goR, goW, err = os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("builtin supervisor: guardian pipe: %w", err)
	}
	script := fmt.Sprintf(`IFS= read -r arbitron_go <&%d || exit %d; [ "$arbitron_go" = go ] || exit %d; exec %d<&-; exec "$@"`,
		fd, guardianAbortExit, guardianAbortExit, fd)
	orig := cmd.Args
	if len(orig) == 0 {
		orig = []string{cmd.Path}
	}
	cmd.Args = append([]string{"sh", "-c", script, "arbitron-guardian", cmd.Path}, orig[1:]...)
	cmd.Path = "/bin/sh"
	cmd.ExtraFiles = append(cmd.ExtraFiles, goR)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return goR, goW, nil
}

var _ runner.BuiltInStopper = (*ProcessSupervisor)(nil)

// StopAndWait implements runner.BuiltInStopper. The caller has already fenced
// the job in the ledger, so Permit refuses any relaunch. It persists `stopped`
// only after confirmed whole-group exit (or for a permit that never released
// its guardian). On any error the row is left as it was.
func (s *ProcessSupervisor) StopAndWait(ctx context.Context, f runner.Fence) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrStopUnconfirmed, err)
		}
		p, ok, err := s.store.Get(ctx, f)
		if err != nil {
			return fmt.Errorf("builtin supervisor: read process: %w", err)
		}
		switch {
		case !ok:
			err = s.store.MarkStopped(ctx, f, runner.ProcessAbsent)
		case p.State == runner.ProcessStopped:
			return nil
		case p.State == runner.ProcessPermitted:
			// Not released: Record (CAS from permitted) now fails and the
			// in-flight Start kills its guardian before any real work.
			err = s.store.MarkStopped(ctx, f, runner.ProcessPermitted)
		case p.State == runner.ProcessRunning:
			if err := s.stopGroup(ctx, f, p); err != nil {
				return err
			}
			err = s.store.MarkStopped(ctx, f, runner.ProcessRunning)
		default:
			return fmt.Errorf("builtin supervisor: unknown process state %q", p.State)
		}
		if errors.Is(err, runner.ErrProcessStateChanged) {
			continue // a concurrent launch/finish moved the row; re-read it
		}
		if err != nil {
			return fmt.Errorf("builtin supervisor: persist stopped: %w", err)
		}
		return nil
	}
}

// stopGroup proves the recorded identity, then sends SIGTERM to the group,
// waits Grace, sends SIGKILL, and waits until the group is confirmed gone.
func (s *ProcessSupervisor) stopGroup(ctx context.Context, f runner.Fence, p runner.BuiltInProcess) error {
	pgid := p.GuardianPID
	if pgid <= 1 {
		return fmt.Errorf("%w: refusing to signal recorded guardian pid %d", ErrStopUnconfirmed, pgid)
	}
	s.mu.Lock()
	h := s.live[f]
	s.mu.Unlock()
	if h == nil { // after a restart: prove the pid still names our guardian
		marker, err := processStartMarker(pgid)
		switch {
		case errors.Is(err, errProcessGone):
			// The leader is gone. Surviving descendants keep the group id,
			// and a pid is never reused while its group exists.
		case err != nil:
			return fmt.Errorf("builtin supervisor: start marker of %d: %w", pgid, err)
		case marker != p.StartMarker:
			return nil // the pid was reused, so the recorded group is already gone: never signal it
		}
	}
	if err := s.signal(pgid, syscall.SIGTERM); err != nil {
		return err
	}
	gone, err := s.waitGone(ctx, h, pgid, s.opt.Grace)
	if err != nil || gone {
		return err
	}
	if err := s.signal(pgid, syscall.SIGKILL); err != nil {
		return err
	}
	_, err = s.waitGone(ctx, h, pgid, 0)
	return err
}

func (s *ProcessSupervisor) signal(pgid int, sig syscall.Signal) error {
	if err := s.opt.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("builtin supervisor: %s process group %d: %w", sig, pgid, err)
	}
	return nil
}

// waitGone polls until the group is confirmed gone. For an in-process guardian
// it also waits until the caller has reaped the leader, because a zombie
// leader still counts as a group member. limit > 0 bounds the wait (the grace
// before SIGKILL). limit == 0 waits until ctx ends.
func (s *ProcessSupervisor) waitGone(ctx context.Context, h *supervised, pgid int, limit time.Duration) (bool, error) {
	var deadline <-chan time.Time
	if limit > 0 {
		t := time.NewTimer(limit)
		defer t.Stop()
		deadline = t.C
	}
	tick := time.NewTicker(s.opt.Poll)
	defer tick.Stop()
	for {
		reaped := h == nil
		if h != nil {
			select {
			case <-h.exited:
				reaped = true
			default:
			}
		}
		if reaped {
			alive, err := s.groupAlive(pgid)
			if err != nil {
				return false, err
			}
			if !alive {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("%w: process group %d: %w", ErrStopUnconfirmed, pgid, ctx.Err())
		case <-deadline:
			return false, nil
		case <-tick.C:
		}
	}
}
