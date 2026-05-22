package claudecli

import "context"

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
