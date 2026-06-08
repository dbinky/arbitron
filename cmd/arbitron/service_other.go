//go:build !windows

package main

import "context"

// runUnderServiceManager reports whether the process was started by the Windows
// Service Control Manager (SCM) and, if so, runs the provided serve function
// under svc.Run, translating SCM Start/Stop/Shutdown control requests into ctx
// cancellation.
//
// This is the non-Windows twin of the windows-tagged implementation: there is no
// SCM on Linux/macOS, so it is always a no-op that returns (false, nil) without
// invoking serve. The caller (runServe) then drives the normal interactive serve
// path unchanged. The signature is identical to service_windows.go so runServe
// can call it unconditionally on every platform.
func runUnderServiceManager(_ context.Context, _ func(context.Context) error) (handled bool, err error) {
	return false, nil
}
