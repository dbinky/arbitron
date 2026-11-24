package runner

import (
	"errors"
	"fmt"
)

var (
	ErrRemoteProbeUnavailable = errors.New("runner: remote probe unavailable")
	ErrRemoteProbeTimeout     = errors.New("runner: remote probe timed out")
	// ErrRemoteProberNotWired indicates missing transport wiring at the root.
	ErrRemoteProberNotWired = fmt.Errorf("%w: no remote prober installed", ErrRemoteProbeUnavailable)
)
