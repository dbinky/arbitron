package claudecli

import (
	"context"
	"sync"
)

// fakeRunner is a CommandRunner that replays canned stdout lines.
type fakeRunner struct {
	lines     []string
	waitErr   error
	startErr  error
	gotSpec   RunSpec
	runCalled bool
}

func (f *fakeRunner) Run(_ context.Context, spec RunSpec) (<-chan string, func() error, error) {
	f.runCalled = true
	f.gotSpec = spec
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range f.lines {
			ch <- l
		}
	}()
	return ch, func() error { return f.waitErr }, nil
}

// ctxAwareFakeRunner is a CommandRunner that honours its ctx. It models the
// production behaviour where ctx cancellation kills the subprocess and the
// stdout-reader goroutine returns. Used by the goleak regression test for
// MF9 to ensure the Provider's StreamCall goroutine returns instead of
// blocking forever on `out <- chunk` after the consumer cancelled.
type ctxAwareFakeRunner struct {
	lines []string

	// readerStarted is closed once the runner goroutine is running, so
	// tests can synchronise on "the producer is alive" before tearing down.
	readerStarted chan struct{}

	mu       sync.Mutex
	finished bool
}

func newCtxAwareFakeRunner(lines []string) *ctxAwareFakeRunner {
	return &ctxAwareFakeRunner{
		lines:         lines,
		readerStarted: make(chan struct{}),
	}
}

func (f *ctxAwareFakeRunner) Run(ctx context.Context, _ RunSpec) (<-chan string, func() error, error) {
	ch := make(chan string)
	done := make(chan struct{})
	go func() {
		defer close(ch)
		defer close(done)
		close(f.readerStarted)
		for _, l := range f.lines {
			select {
			case ch <- l:
			case <-ctx.Done():
				return
			}
		}
	}()
	wait := func() error {
		<-done
		f.mu.Lock()
		f.finished = true
		f.mu.Unlock()
		return nil
	}
	return ch, wait, nil
}

// Finished reports whether wait() observed the reader goroutine return.
func (f *ctxAwareFakeRunner) Finished() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finished
}
