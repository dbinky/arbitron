package pi

import (
	"context"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// multiRunner replays a sequence of canned invocations (version probe, then
// run...), recording each RunSpec. It satisfies claudecli.CommandRunner — the
// SAME subprocess seam the claudecli/codex/opencode adapters use, never
// redefined — and honours ctx cancellation when a script sets blockOnCtx.
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
		if s.blockOnCtx {
			return ctx.Err()
		}
		return s.waitErr
	}
	return ch, wait, nil
}

// compatibleProbe is the canned `pi --version` reply that passes the probe.
func compatibleProbe() runScript { return runScript{lines: []string{minPiVersion}} }
