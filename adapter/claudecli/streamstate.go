package claudecli

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
)

// streamState accumulates the per-line bookkeeping for a streaming claude
// invocation. It is intentionally small and stateless across instances —
// callers create one per StreamCall.
//
// SF3 / PR #8 thread 3291907738: extracting this from the StreamCall closure
// makes the partial-failure paths individually testable (parseEvent error
// mid-stream, result-with-is_error, exit-with-no-result) without spinning up
// a full Provider.
type streamState struct {
	sessionID string
	tokensIn  int
	tokensOut int
	failed    bool
}

// Handle processes one parsed event line and emits zero or one chunk via
// sendChunk. sendChunk returns false if the consumer is gone (ctx cancelled);
// Handle then sets failed and the caller should stop forwarding.
//
// rawLine is the on-the-wire line; parseEvent's result is consumed here.
// A parse error becomes a chunk-with-error; the caller MUST keep draining
// the runner's lines channel so the subprocess can be reaped.
func (s *streamState) Handle(rawLine string, sendChunk func(engine.LLMStreamChunk) bool) {
	if s.failed {
		return
	}
	ev, ok, parseErr := parseEvent([]byte(rawLine))
	if parseErr != nil {
		if !sendChunk(engine.LLMStreamChunk{Error: parseErr}) {
			// consumer gone; still mark failed so Finalize knows
		}
		s.failed = true
		return
	}
	if !ok {
		return
	}
	if ev.SessionID != "" {
		s.sessionID = ev.SessionID
	}
	switch ev.Kind {
	case eventAssistant:
		if ev.Text != "" {
			if !sendChunk(engine.LLMStreamChunk{Token: ev.Text}) {
				s.failed = true
			}
		}
	case eventResult:
		s.tokensIn, s.tokensOut = ev.TokensIn, ev.TokensOut
		if ev.IsError {
			if !sendChunk(engine.LLMStreamChunk{
				Error: fmt.Errorf("claudecli: claude reported an error: %s", ev.Text),
			}) {
				// consumer gone
			}
			s.failed = true
		}
	}
}

// Finalize emits the terminal chunk after the runner's lines channel has
// been fully drained and wait() has returned. waitErr is whatever wait()
// returned (nil on clean exit). No chunk is emitted when Handle already
// reported a failure — the consumer has the error.
func (s *streamState) Finalize(waitErr error, sendChunk func(engine.LLMStreamChunk) bool) {
	if s.failed {
		return
	}
	if waitErr != nil {
		sendChunk(engine.LLMStreamChunk{
			Error: fmt.Errorf("claudecli: claude exited with error: %w", waitErr),
		})
		return
	}
	sendChunk(engine.LLMStreamChunk{
		Done:      true,
		SessionID: s.sessionID,
		TokensIn:  s.tokensIn,
		TokensOut: s.tokensOut,
	})
}

// makeSendChunk returns the sendChunk closure used by both Handle and
// Finalize: non-blocking via select on runCtx.
func makeSendChunk(runCtx context.Context, out chan<- engine.LLMStreamChunk) func(engine.LLMStreamChunk) bool {
	return func(c engine.LLMStreamChunk) bool {
		select {
		case out <- c:
			return true
		case <-runCtx.Done():
			return false
		}
	}
}
