package codex

import (
	"context"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// fakeRunner is a claudecli.CommandRunner that replays canned stdout lines and
// records the RunSpec it received. It mirrors the claudecli/opencode package
// fakes — the SAME subprocess seam is reused, never redefined. It serves the
// single-invocation version-probe and pure-arg tests.
type fakeRunner struct {
	lines     []string
	waitErr   error
	startErr  error
	gotSpec   claudecli.RunSpec
	runCalled bool
	// blockOnCtx, when set, makes the runner honour ctx cancellation: it blocks
	// sending lines until ctx is done, then closes the channel.
	blockOnCtx bool
}

func (f *fakeRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	f.runCalled = true
	f.gotSpec = spec
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		if f.blockOnCtx {
			<-ctx.Done()
			return
		}
		for _, l := range f.lines {
			ch <- l
		}
	}()
	wait := func() error {
		if f.blockOnCtx {
			return ctx.Err()
		}
		return f.waitErr
	}
	return ch, wait, nil
}

// multiRunner replays a sequence of canned invocations (probe, then run...),
// recording each RunSpec. It satisfies claudecli.CommandRunner and honours ctx
// cancellation when a script sets blockOnCtx. Mirrors opencode's multiRunner.
type multiRunner struct {
	scripts []runScript
	calls   []claudecli.RunSpec
	idx     int
}

type runScript struct {
	lines      []string
	waitErr    error
	startErr   error
	blockOnCtx bool
	// ctxWaitErr, when set with blockOnCtx, is returned by wait() instead of the
	// bare ctx.Err(). It mirrors production, where a timeout kill makes the real
	// runner return a decorated *claudecli.StderrError (exit "signal: killed" +
	// captured stderr) while runCtx is independently done.
	ctxWaitErr error
	// onWait, when set, runs as the process exits (before wait returns), the
	// point at which a real codex has finished writing its rollout.
	onWait func()
}

func (m *multiRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	m.calls = append(m.calls, spec)
	var s runScript
	if m.idx < len(m.scripts) {
		s = m.scripts[m.idx]
	}
	m.idx++
	if s.startErr != nil {
		return nil, nil, s.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		if s.blockOnCtx {
			<-ctx.Done()
			return
		}
		for _, l := range s.lines {
			ch <- l
		}
	}()
	wait := func() error {
		if s.onWait != nil {
			s.onWait()
		}
		if s.blockOnCtx {
			if s.ctxWaitErr != nil {
				return s.ctxWaitErr
			}
			return ctx.Err()
		}
		return s.waitErr
	}
	return ch, wait, nil
}

// compatibleProbe is the canned `codex --version` reply that passes the probe.
func compatibleProbe() runScript { return runScript{lines: []string{"codex-cli 0.163.0"}} }

// lunaRouteCompatibleProbe reports the oldest release a LunaRoute launch accepts.
func lunaRouteCompatibleProbe() runScript {
	return runScript{lines: []string{"codex-cli " + minLunaRouteCodexVersion}}
}

func emptyMCPProbe() runScript { return runScript{lines: []string{`[]`}} }
