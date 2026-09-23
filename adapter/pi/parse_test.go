package pi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures below are trimmed from a REAL `pi -p --mode json` stream
// (pi 0.87.0). Field names and nesting are verbatim; only the bulky system
// prompt and tool schemas are elided.

const (
	sessionLine = `{"type":"session","version":3,"id":"01a0c55a-44ef-71da-96be-a1ab13d85460","timestamp":"2026-09-21T19:03:42.577Z","cwd":"/tmp/ws"}`
	userLine    = `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"do the thing"}],"timestamp":1790017422609}}`
	systemLine  = `{"type":"message_end","message":{"role":"system","content":"","sections":{"preamble":"You are an expert coding assistant"},"timestamp":1790017422609}}`
)

// assistantLine builds a terminal assistant message_end with the given text,
// stop reason and token usage.
func assistantLine(text, stopReason string, in, out int) string {
	return `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"` + text +
		`"}],"api":"anthropic-messages","provider":"anthropic","model":"claude-sonnet-5","usage":{"input":` +
		itoa(in) + `,"output":` + itoa(out) + `,"cacheRead":0,"cacheWrite":0,"totalTokens":` + itoa(in+out) +
		`},"stopReason":"` + stopReason + `","timestamp":1790017422621}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// HAPPY — a complete successful stream folds into summary, session id and token
// counts, and the terminal assistant text becomes the summary.
func TestParseOutput_HappyStream(t *testing.T) {
	res, err := parseOutput([]string{
		sessionLine,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		systemLine,
		userLine,
		assistantLine("Done: edited the workspace.", "stop", 1200, 45),
		`{"type":"agent_settled"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "Done: edited the workspace.", res.summary)
	assert.Equal(t, "01a0c55a-44ef-71da-96be-a1ab13d85460", res.sessionID)
	assert.Equal(t, 1200, res.tokensIn)
	assert.Equal(t, 45, res.tokensOut)
	assert.False(t, res.failed)
	assert.Empty(t, res.failureMsg)
}

// SUCCESS — tool calls in assistant content become "tool_call" steps in order,
// and assistant text becomes an "assistant" step, so the trace shows the work.
func TestParseOutput_FoldsToolCallsAndTextAsStepsInOrder(t *testing.T) {
	res, err := parseOutput([]string{
		sessionLine,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"tc1","name":"bash","arguments":{"command":"go test ./..."}}],"usage":{"input":10,"output":2,"totalTokens":12},"stopReason":"toolUse","timestamp":1}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"tc2","name":"edit","arguments":{"path":"a.go"}}],"usage":{"input":20,"output":3,"totalTokens":23},"stopReason":"toolUse","timestamp":2}}`,
		assistantLine("All green.", "stop", 30, 5),
	})
	require.NoError(t, err)
	require.Len(t, res.steps, 3)
	assert.Equal(t, "tool_call", res.steps[0].Kind)
	assert.Equal(t, "bash", res.steps[0].Summary)
	assert.Equal(t, "tool_call", res.steps[1].Kind)
	assert.Equal(t, "edit", res.steps[1].Summary)
	assert.Equal(t, "assistant", res.steps[2].Kind)
	assert.Equal(t, "All green.", res.steps[2].Summary)
	assert.Equal(t, "All green.", res.summary)
}

// FAILURE — pi reports a failed turn INSIDE the stream while the process still
// exits 0, so stopReason "error" must mark the fold failed and carry the
// provider's message. This is the whole reason the adapter cannot trust the
// exit code.
func TestParseOutput_StopReasonErrorMarksFailedWithMessage(t *testing.T) {
	res, err := parseOutput([]string{
		sessionLine,
		`{"type":"message_end","message":{"role":"assistant","content":[],"provider":"anthropic","model":"claude-haiku-4-5","usage":{"input":0,"output":0,"totalTokens":0},"stopReason":"error","errorMessage":"400 You're out of extra usage.","timestamp":1}}`,
	})
	require.NoError(t, err)
	assert.True(t, res.failed, "stopReason error must mark the fold failed")
	assert.Equal(t, "400 You're out of extra usage.", res.failureMsg)
}

// FAILURE — an aborted turn is a failure too, even without an errorMessage: it
// must never read as a clean empty success.
func TestParseOutput_StopReasonAbortedMarksFailed(t *testing.T) {
	res, err := parseOutput([]string{
		sessionLine,
		`{"type":"message_end","message":{"role":"assistant","content":[],"usage":{"input":1,"output":0,"totalTokens":1},"stopReason":"aborted","timestamp":1}}`,
	})
	require.NoError(t, err)
	assert.True(t, res.failed)
	assert.Empty(t, res.failureMsg, "aborted carries no provider message")
}

// ERROR — a non-blank line that is not valid JSON fails closed, so a truncated
// or garbled stream is never silently folded into a partial success.
func TestParseOutput_MalformedLineFailsClosed(t *testing.T) {
	_, err := parseOutput([]string{sessionLine, `{"type":"message_end", BROKEN`})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrParse)
}

// EDGE — the terminal agent_end event repeats the WHOLE transcript. Folding it
// would double every step and re-count tokens, so it must be ignored.
func TestParseOutput_AgentEndTranscriptIsNotDoubleCounted(t *testing.T) {
	assistant := assistantLine("Done.", "stop", 100, 10)
	res, err := parseOutput([]string{
		sessionLine,
		assistant,
		`{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input":100,"output":10,"totalTokens":110},"stopReason":"stop","timestamp":1}],"willRetry":false}`,
		`{"type":"agent_settled"}`,
	})
	require.NoError(t, err)
	assert.Len(t, res.steps, 1, "agent_end must not re-fold the transcript")
	assert.Equal(t, 100, res.tokensIn, "tokens must not be double counted")
	assert.Equal(t, 10, res.tokensOut)
}

// EDGE — system and user messages carry no assistant work: they must not
// become steps and must never overwrite the summary.
func TestParseOutput_SystemAndUserMessagesAreIgnored(t *testing.T) {
	res, err := parseOutput([]string{
		sessionLine,
		assistantLine("the real answer", "stop", 5, 1),
		userLine,
		systemLine,
	})
	require.NoError(t, err)
	assert.Equal(t, "the real answer", res.summary)
	assert.Len(t, res.steps, 1)
}

// EDGE — message_update deltas and unknown/future event types are skipped, so a
// newer pi that adds events does not break the fold or pollute the trace.
func TestParseOutput_DeltasAndUnknownEventsAreSkipped(t *testing.T) {
	res, err := parseOutput([]string{
		sessionLine,
		`{"type":"message_update","usage":{"input":9,"output":9,"totalTokens":18},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Do"}}`,
		`{"type":"tool_execution_end","toolCallId":"tc1","toolName":"bash","result":{},"isError":false}`,
		`{"type":"some_future_event","payload":{"x":1}}`,
		assistantLine("Done.", "stop", 100, 10),
	})
	require.NoError(t, err)
	assert.Len(t, res.steps, 1, "only assistant message_end produces steps")
	assert.Equal(t, 100, res.tokensIn, "delta usage must not win over the final message")
}

// EDGE — blank lines are tolerated and an empty stream folds to a zero result
// without error (the caller decides what an empty result means).
func TestParseOutput_BlankAndEmptyStreams(t *testing.T) {
	res, err := parseOutput([]string{"", "   "})
	require.NoError(t, err)
	assert.Empty(t, res.summary)
	assert.Empty(t, res.steps)
	assert.False(t, res.failed)

	res2, err2 := parseOutput(nil)
	require.NoError(t, err2)
	assert.Empty(t, res2.summary)
}
