package codex

import (
	"context"
)

// fakeTurnSession is a hand-driven turnSession that replays a scripted sequence of
// turnEvents. It mirrors fake_runner_test.go's philosophy — no live codex, no
// subprocess, no network — and is the substrate for every later
// interactive-provider test.
//
// Like the runner fake it is fail-closed by construction: a startErr makes Start
// return no stream, and an empty script makes Next report the terminal outcome
// immediately rather than blocking.
type fakeTurnSession struct {
	// events is the scripted turnEvent sequence Next replays in order. When it is
	// exhausted (or empty), Next reports terminal completion via io.EOF-style
	// handling driven by the event kinds.
	events []turnEvent
	// startErr, when set, makes Start fail closed: it returns (nil, startErr) and
	// no stream is produced.
	startErr error
	// blockOnCtx, when set, makes the returned stream honour ctx cancellation:
	// Next blocks until ctx is done, then returns the context error.
	blockOnCtx bool

	// gotCfg records the turnConfig Start received for argument assertions
	// (prompt, resumeID, model, workdir, effort).
	gotCfg turnConfig
	// startCalled records whether Start was invoked.
	startCalled bool
}

// Start records cfg, fails closed on startErr, then returns a stream that replays
// the scripted events.
func (f *fakeTurnSession) Start(ctx context.Context, cfg turnConfig) (turnStream, error) {
	f.startCalled = true
	f.gotCfg = cfg
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &fakeTurnStream{events: f.events, blockOnCtx: f.blockOnCtx}, nil
}

// fakeTurnStream replays a scripted []turnEvent in order via Next, then reports
// terminal completion (io.EOF) once the script is exhausted. With blockOnCtx set
// it instead honours ctx cancellation, returning the context error.
type fakeTurnStream struct {
	events     []turnEvent
	idx        int
	blockOnCtx bool
}

// Next returns the next scripted event, or — when blockOnCtx is set — blocks
// until ctx is done and returns the context error. An exhausted (or empty)
// script reports terminal completion via io.EOF so the caller stops folding.
func (s *fakeTurnStream) Next(ctx context.Context) (turnEvent, error) {
	if s.blockOnCtx {
		<-ctx.Done()
		return turnEvent{}, ctx.Err()
	}
	if s.idx >= len(s.events) {
		return turnEvent{}, errStreamDone
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}
