package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Canned codex-cli 0.137.0 `codex exec --json` JSONL event stream: a thread/turn
// start, a command-execution item, a terminal agent_message, and a turn.completed
// carrying usage. Verified against the real binary at implementation time.
var codexEventLines = []string{
	`{"type":"thread.started","thread_id":"thr-123"}`,
	`{"type":"turn.started"}`,
	`{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc 'echo hi'","status":"in_progress"}}`,
	`{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc 'echo hi'","exit_code":0,"status":"completed"}}`,
	`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Edited the workspace toward the focus area."}}`,
	`{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":9,"output_tokens":5,"reasoning_output_tokens":0}}`,
}

// HAPPY — a full event stream folds into Summary (terminal agent_message), Steps
// (one per completed item), token counts, and the thread id.
func TestParseOutput_FullStream(t *testing.T) {
	res, err := parseOutput(codexEventLines)
	require.NoError(t, err)
	assert.Equal(t, "Edited the workspace toward the focus area.", res.summary,
		"Summary is the terminal agent_message text")
	assert.Equal(t, "thr-123", res.sessionID)
	assert.Equal(t, 11, res.tokensIn)
	assert.Equal(t, 5, res.tokensOut)

	require.Len(t, res.steps, 2, "one step per item.completed (command + assistant)")
	assert.Equal(t, "tool_call", res.steps[0].Kind, "command_execution maps to tool_call")
	assert.Contains(t, res.steps[0].Summary, "echo hi")
	assert.Equal(t, "assistant", res.steps[1].Kind, "agent_message maps to assistant")
	assert.Equal(t, "Edited the workspace toward the focus area.", res.steps[1].Summary)
	assert.False(t, res.failed, "successful turn must not set parsed.failed")
}

// HAPPY — when several agent_message items appear, the LAST one is the Summary
// but every one is still folded into the ordered Steps.
func TestParseOutput_MultipleAssistantMessages(t *testing.T) {
	lines := []string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"first"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"second"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"final answer"}}`,
	}
	res, err := parseOutput(lines)
	require.NoError(t, err)
	assert.Equal(t, "final answer", res.summary, "Summary is the LAST agent_message")
	require.Len(t, res.steps, 3)
	assert.Equal(t, "first", res.steps[0].Summary)
	assert.Equal(t, "final answer", res.steps[2].Summary)
}

// FORWARD-COMPAT — unknown event kinds and unknown item types are tolerated:
// they neither error nor pollute the steps, so a newer codex that adds events
// does not break the adapter.
func TestParseOutput_UnknownEventsTolerated(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"some.future.event","payload":{"whatever":true}}`,
		`{"type":"item.completed","item":{"type":"some_future_item","blob":"x"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	res, err := parseOutput(lines)
	require.NoError(t, err)
	assert.Equal(t, "done", res.summary)
	require.Len(t, res.steps, 1, "the unknown item type is dropped, only agent_message folds")
	assert.Equal(t, "assistant", res.steps[0].Kind)
}

// FORWARD-COMPAT / TOLERANCE — blank and whitespace-only lines are skipped (codex
// interleaves non-JSON log noise on stdout in some builds); they are not steps
// and not an error.
func TestParseOutput_BlankLinesSkipped(t *testing.T) {
	lines := []string{
		"",
		"   ",
		`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`,
		"\t",
	}
	res, err := parseOutput(lines)
	require.NoError(t, err)
	assert.Equal(t, "hi", res.summary)
	require.Len(t, res.steps, 1)
}

// EDGE — an empty stream parses tolerantly into a zero-value result (no error,
// no panic).
func TestParseOutput_Empty(t *testing.T) {
	res, err := parseOutput(nil)
	require.NoError(t, err)
	assert.Empty(t, res.summary)
	assert.Empty(t, res.steps)
	assert.Zero(t, res.tokensIn)
	assert.Zero(t, res.tokensOut)
}

// ERROR — a malformed JSON line yields a typed parse error (ErrParse) naming the
// failure; we fail closed rather than silently dropping a line we cannot read.
func TestParseOutput_MalformedJSON(t *testing.T) {
	lines := []string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
		`{"type":"item.completed", BROKEN`,
	}
	res, err := parseOutput(lines)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrParse)
	assert.Nil(t, res)
}

// ERROR-EVENT — a terminal turn.failed / error event surfaces its message as the
// Summary so the caller is never handed a silent empty result for a failed turn.
func TestParseOutput_ErrorEventSurfacesMessage(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"error","message":"model not supported for this account"}`,
		`{"type":"turn.failed","error":{"message":"model not supported for this account"}}`,
	}
	res, err := parseOutput(lines)
	require.NoError(t, err)
	assert.Contains(t, res.summary, "model not supported")
	require.Len(t, res.steps, 1, "the error folds into a single result step (dedup)")
	assert.Equal(t, "result", res.steps[0].Kind)
	assert.True(t, res.failed, "failed flag must be set even when both events carry the same message")
}

// STREAM-HONESTY — parsed.failed is the signal the stream adapter's Finalize
// reads to emit a typed error chunk rather than a silent Done. Pin its value
// across all failure shapes: error-only, turn.failed-only, and the normal
// codex dual-event pair (error + turn.failed carrying the same message).
func TestParseOutput_FailedFlagSetOnTerminalError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{
			"error event only",
			[]string{`{"type":"error","message":"model unavailable"}`},
		},
		{
			"turn.failed only",
			[]string{`{"type":"turn.failed","error":{"message":"model unavailable"}}`},
		},
		{
			"error then turn.failed (codex normal dedup pair)",
			[]string{
				`{"type":"error","message":"model unavailable"}`,
				`{"type":"turn.failed","error":{"message":"model unavailable"}}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := parseOutput(tc.lines)
			require.NoError(t, err)
			assert.True(t, res.failed, "parsed.failed must be true so stream Finalize fails closed")
		})
	}
}
