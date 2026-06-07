//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows/svc"
)

// When the binary is run interactively on Windows (not started by the SCM),
// runUnderServiceManager must report handled == false so the caller drives the
// normal interactive serve path. The IsWindowsService probe is forced false via
// the package seam.
func TestRunUnderServiceManager_InteractiveWindowsRun(t *testing.T) {
	orig := isWindowsService
	t.Cleanup(func() { isWindowsService = orig })
	isWindowsService = func() (bool, error) { return false, nil }

	called := 0
	handled, err := runUnderServiceManager(context.Background(), func(context.Context) error {
		called++
		return nil
	})

	require.NoError(t, err)
	assert.False(t, handled, "interactive Windows run must not be handled by the SCM dispatch")
	assert.Equal(t, 0, called, "the caller drives serve on an interactive run, not runUnderServiceManager")
}

// An error from the IsWindowsService probe propagates and reports not-handled so
// the caller can decide; serve must not be invoked.
func TestRunUnderServiceManager_ProbeError(t *testing.T) {
	orig := isWindowsService
	t.Cleanup(func() { isWindowsService = orig })
	wantErr := assert.AnError
	isWindowsService = func() (bool, error) { return false, wantErr }

	handled, err := runUnderServiceManager(context.Background(), func(context.Context) error {
		t.Fatal("serve must not be called when the probe errors")
		return nil
	})

	assert.False(t, handled)
	assert.ErrorIs(t, err, wantErr)
}

// The control handler maps an SCM Stop into ctx cancellation, reports
// StopPending, and returns once serve has drained — without a live SCM.
func TestHandleControlLoop_StopCancelsContext(t *testing.T) {
	r := make(chan svc.ChangeRequest, 1)
	s := make(chan svc.Status, 8)
	done := make(chan struct{})

	cancelled := false
	cancel := func() { cancelled = true; close(done) }

	go func() { r <- svc.ChangeRequest{Cmd: svc.Stop} }()

	loopDone := make(chan struct{})
	go func() { handleControlLoop(r, s, done, cancel); close(loopDone) }()

	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleControlLoop did not return after Stop")
	}

	assert.True(t, cancelled, "Stop must cancel the serve context")

	var sawStopPending bool
	for {
		select {
		case st := <-s:
			if st.State == svc.StopPending {
				sawStopPending = true
			}
		default:
			require.True(t, sawStopPending, "loop must report StopPending on Stop")
			return
		}
	}
}

// Shutdown behaves like Stop: cancel + StopPending + return.
func TestHandleControlLoop_ShutdownCancelsContext(t *testing.T) {
	r := make(chan svc.ChangeRequest, 1)
	s := make(chan svc.Status, 8)
	done := make(chan struct{})

	cancelled := false
	cancel := func() { cancelled = true; close(done) }

	go func() { r <- svc.ChangeRequest{Cmd: svc.Shutdown} }()

	loopDone := make(chan struct{})
	go func() { handleControlLoop(r, s, done, cancel); close(loopDone) }()

	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleControlLoop did not return after Shutdown")
	}
	assert.True(t, cancelled, "Shutdown must cancel the serve context")
}

// If serve exits on its own (done closes) before any control request, the loop
// returns without cancelling.
func TestHandleControlLoop_ServeExitsOnItsOwn(t *testing.T) {
	r := make(chan svc.ChangeRequest)
	s := make(chan svc.Status, 8)
	done := make(chan struct{})

	cancelled := false
	cancel := func() { cancelled = true }

	close(done) // serve already returned

	loopDone := make(chan struct{})
	go func() { handleControlLoop(r, s, done, cancel); close(loopDone) }()

	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleControlLoop did not return when serve exited on its own")
	}
	assert.False(t, cancelled, "no control request means no cancellation")
}

// Contract lock: the service name the binary dispatches under via svc.Run must
// match the -Name argument used by Register-ArbitronService in install.ps1
// (currently `New-Service -Name 'arbitron'`). If either side changes, the SCM
// silently cannot find the service and Stop/Start/Interrogate all fail.
func TestServiceName_MatchesPowerShellRegistration(t *testing.T) {
	assert.Equal(t, "arbitron", serviceName,
		"serviceName must match New-Service -Name arbitron in install.ps1; changing either side breaks SCM dispatch")
}

// An unrecognized SCM command (e.g. Pause, Continue) must not exit the loop —
// it re-reports {Running, AcceptStop|AcceptShutdown} and keeps waiting.
// This exercises the default branch in handleControlLoop.
func TestHandleControlLoop_UnknownCommandReportsRunning(t *testing.T) {
	r := make(chan svc.ChangeRequest, 2)
	s := make(chan svc.Status, 8)
	done := make(chan struct{})
	cancel := func() { close(done) }

	// svc.Pause is not in the accepted set → default branch → re-report Running.
	// Follow immediately with Stop so the loop can exit.
	r <- svc.ChangeRequest{Cmd: svc.Pause}
	r <- svc.ChangeRequest{Cmd: svc.Stop}

	loopDone := make(chan struct{})
	go func() { handleControlLoop(r, s, done, cancel); close(loopDone) }()

	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleControlLoop did not return after Stop")
	}

	var sawRunning bool
	for {
		select {
		case st := <-s:
			if st.State == svc.Running {
				sawRunning = true
			}
		default:
			assert.True(t, sawRunning,
				"an unhandled SCM command must re-report Running, not silently drop the status update")
			return
		}
	}
}

// Interrogate echoes the current status back without stopping the loop.
func TestHandleControlLoop_InterrogateEchoesStatus(t *testing.T) {
	r := make(chan svc.ChangeRequest, 2)
	s := make(chan svc.Status, 8)
	done := make(chan struct{})
	cancel := func() { close(done) }

	cur := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	r <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: cur}
	r <- svc.ChangeRequest{Cmd: svc.Stop}

	loopDone := make(chan struct{})
	go func() { handleControlLoop(r, s, done, cancel); close(loopDone) }()

	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleControlLoop did not return")
	}

	echoed := <-s
	assert.Equal(t, cur.State, echoed.State, "Interrogate must echo the current status state")
}

// Execute emits StartPending before Running and Stopped after the loop, even
// when no control requests arrive (serve exits on its own). The SCM requires
// this exact sequence; a missing StartPending causes a "service did not start"
// error; a missing Stopped leaves the SCM waiting indefinitely.
func TestServiceHandler_Execute_StateSequence(t *testing.T) {
	r := make(chan svc.ChangeRequest) // no control requests
	s := make(chan svc.Status, 8)

	serveDone := make(chan struct{})
	h := &serviceHandler{
		ctx: context.Background(),
		serve: func(ctx context.Context) error {
			close(serveDone) // signal so the test can unblock serve via ctx wait
			<-ctx.Done()
			return nil
		},
	}

	execDone := make(chan struct{})
	go func() {
		h.Execute(nil, r, s)
		close(execDone)
	}()

	// Wait for serve to start (it signals serveDone), then send Stop so Execute finishes.
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("serve goroutine did not start")
	}

	r <- svc.ChangeRequest{Cmd: svc.Stop}

	select {
	case <-execDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after Stop")
	}

	// Drain and collect state sequence.
	var states []svc.State
	for {
		select {
		case st := <-s:
			states = append(states, st.State)
		default:
			goto done
		}
	}
done:
	require.GreaterOrEqual(t, len(states), 3, "Execute must emit at least StartPending, Running, Stopped")
	assert.Equal(t, svc.StartPending, states[0], "first status must be StartPending")
	assert.Equal(t, svc.Running, states[1], "second status must be Running")
	assert.Equal(t, svc.Stopped, states[len(states)-1], "last status must be Stopped")
}
