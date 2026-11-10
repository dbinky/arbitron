package claudecli

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
)

// streamState accumulates the per-line bookkeeping for a streaming claude
// invocation. It is intentionally small and stateless across instances —
// callers create one per StreamChat.
//
// SF3 / PR #8 thread 3291907738: extracting this from the StreamChat closure
// makes the partial-failure paths individually testable (parseEvent error
// mid-stream, result-with-is_error, exit-with-no-result) without spinning up
// a full Provider.
type streamState struct {
	sessionID string
	tokensIn  int
	tokensOut int
	failed    bool
	// wantModel, when set, is the pinned model every result's modelUsage must
	// contain; a result without it fails the stream instead of completing.
	wantModel string
	sawResult bool
}

// Handle processes one parsed event line and emits zero or one chunk via
// sendChunk. sendChunk returns false if the consumer is gone (ctx cancelled);
// Handle then sets failed and the caller should stop forwarding.
//
// rawLine is the on-the-wire line; parseEvent's result is consumed here.
// A parse error becomes a chunk-with-error; the caller MUST keep draining
// the runner's lines channel so the subprocess can be reaped.
func (s *streamState) Handle(rawLine string, sendChunk func(engine.ChatChunk) bool) {
	if s.failed {
		return
	}
	ev, ok, parseErr := parseEvent([]byte(rawLine))
	if parseErr != nil {
		if !sendChunk(engine.ChatChunk{Error: parseErr}) {
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
			if !sendChunk(engine.ChatChunk{Token: ev.Text}) {
				s.failed = true
			}
		}
	case eventResult:
		// One turn can carry several results: Claude Code re-wakes a print
		// session each time a background subagent finishes. A pinned turn needs
		// EVERY result to prove the model, so one unproven or failed wake fails
		// the whole turn (latched before Done; the caller still drains and reaps).
		s.sawResult = true
		s.tokensIn += ev.TokensIn
		s.tokensOut += ev.TokensOut
		if !ev.IsError && s.wantModel != "" && !executedModelMatches(s.wantModel, ev.ModelUsage) {
			sendChunk(engine.ChatChunk{
				Error: fmt.Errorf("claudecli: pinned model %q absent from result modelUsage", s.wantModel),
			})
			s.failed = true
			return
		}
		if ev.IsError {
			err := errModelExecutionEvidence
			if s.wantModel == "" {
				err = fmt.Errorf("claudecli: claude reported an error: %s", ev.Text)
			}
			sendChunk(engine.ChatChunk{Error: err})
			s.failed = true
		}
	}
}

// Finalize emits the terminal chunk after the runner's lines channel has
// been fully drained and wait() has returned. waitErr is whatever wait()
// returned (nil on clean exit). No chunk is emitted when Handle already
// reported a failure — the consumer has the error.
func (s *streamState) Finalize(waitErr error, sendChunk func(engine.ChatChunk) bool) {
	if s.failed {
		return
	}
	if waitErr != nil {
		if s.wantModel != "" {
			waitErr = SanitizeProcessError(waitErr)
		}
		sendChunk(engine.ChatChunk{
			Error: fmt.Errorf("claudecli: claude exited with error: %w", waitErr),
		})
		return
	}
	if s.wantModel != "" && !s.sawResult {
		sendChunk(engine.ChatChunk{
			Error: fmt.Errorf("claudecli: no result event to confirm pinned model %q", s.wantModel),
		})
		return
	}
	sendChunk(engine.ChatChunk{
		Done:      true,
		SessionID: s.sessionID,
		TokensIn:  s.tokensIn,
		TokensOut: s.tokensOut,
	})
}

// makeSendChunk returns the sendChunk closure used by both Handle and
// Finalize: non-blocking via select on runCtx.
func makeSendChunk(runCtx context.Context, out chan<- engine.ChatChunk) func(engine.ChatChunk) bool {
	return func(c engine.ChatChunk) bool {
		select {
		case out <- c:
			return true
		case <-runCtx.Done():
			return false
		}
	}
}
