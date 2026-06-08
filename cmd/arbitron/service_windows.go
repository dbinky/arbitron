//go:build windows

package main

import (
	"context"

	"golang.org/x/sys/windows/svc"
)

// serviceName is the Windows Service name the binary dispatches under. It must
// match the name P4 Task 3 registers via `New-Service -Name arbitron` /
// `sc.exe create arbitron`.
const serviceName = "arbitron"

// isWindowsService is a seam over svc.IsWindowsService so tests can force the
// interactive (non-SCM) branch deterministically without a live SCM.
var isWindowsService = svc.IsWindowsService

// runUnderServiceManager reports whether the process was started by the Windows
// SCM and, if so, runs serve under svc.Run, translating SCM Stop/Shutdown
// control requests into ctx cancellation.
//
// When the binary is run interactively from a console (not started by the SCM),
// svc.IsWindowsService is false and this returns (false, nil) so the caller
// drives the normal interactive serve path — identical to the non-Windows twin.
func runUnderServiceManager(ctx context.Context, serve func(context.Context) error) (bool, error) {
	inService, err := isWindowsService()
	if err != nil {
		return false, err
	}
	if !inService {
		// Interactive Windows run: behave exactly like a console process.
		return false, nil
	}

	h := &serviceHandler{ctx: ctx, serve: serve}
	if err := svc.Run(serviceName, h); err != nil {
		return true, err
	}
	return true, h.serveErr
}

// serviceHandler adapts the Arbitron serve loop to the svc.Handler contract. It
// runs serve(ctx) in a goroutine and maps SCM Stop/Shutdown into cancellation of
// a derived context, reporting StartPending -> Running -> StopPending -> Stopped
// along the way.
type serviceHandler struct {
	ctx      context.Context
	serve    func(context.Context) error
	serveErr error
}

// Execute satisfies svc.Handler. It is exercised by the live SCM on Windows; its
// control-request handling is unit-tested via handleControlLoop with fake
// channels (no live SCM needed).
func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	s <- svc.Status{State: svc.StartPending}

	serveCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		h.serveErr = h.serve(serveCtx)
		close(done)
	}()

	s <- svc.Status{State: svc.Running, Accepts: accepted}

	handleControlLoop(r, s, done, cancel)

	s <- svc.Status{State: svc.Stopped}
	return false, 0
}

// handleControlLoop processes SCM control requests until a Stop/Shutdown is
// received or the serve goroutine exits on its own. On Stop/Shutdown it reports
// StopPending, cancels the serve context, and waits for serve to return. It is
// factored out of Execute so it can be unit-tested with fake channels.
func handleControlLoop(r <-chan svc.ChangeRequest, s chan<- svc.Status, done <-chan struct{}, cancel context.CancelFunc) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	for {
		select {
		case <-done:
			// serve returned on its own (error or clean exit); stop reporting.
			return
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return
			default:
				// Unhandled control: re-report current accepted state.
				s <- svc.Status{State: svc.Running, Accepts: accepted}
			}
		}
	}
}
