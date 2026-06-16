package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOutput_JSON_TextToolTokens(t *testing.T) {
	lines := []string{
		`[12:00:00] INFO some non-json log line`, // skipped
		`{"type":"step_start","timestamp":1,"sessionID":"ses_x","part":{"type":"step-start"}}`,
		`{"type":"text","timestamp":2,"sessionID":"ses_x","part":{"type":"text","text":"Let me check the files."}}`,
		`{"type":"tool_use","timestamp":3,"sessionID":"ses_x","part":{"type":"tool","tool":"bash","state":{"status":"completed"}}}`,
		`{"type":"step_finish","timestamp":4,"sessionID":"ses_x","part":{"type":"step-finish","tokens":{"total":100,"input":80,"output":20}}}`,
		`{"type":"text","timestamp":5,"sessionID":"ses_x","part":{"type":"text","text":"DONE"}}`,
		`{"type":"step_finish","timestamp":6,"sessionID":"ses_x","part":{"type":"step-finish","tokens":{"output":5}}}`,
	}
	summary, steps, tokIn, tok := parseOutput(lines)
	assert.Contains(t, summary, "Let me check the files.")
	assert.Contains(t, summary, "DONE")
	assert.Equal(t, 80, tokIn) // 80 + 0 (second step_finish omits input)
	assert.Equal(t, 25, tok)   // 20 + 5
	// at least one assistant step and one tool step
	var kinds []string
	for _, s := range steps {
		kinds = append(kinds, s.Kind)
	}
	assert.Contains(t, kinds, "assistant")
	assert.Contains(t, kinds, "tool")
}

func TestParseOutput_JSON_ErrorSurfaced(t *testing.T) {
	lines := []string{
		`{"type":"error","sessionID":"ses_x","error":{"name":"UnknownError","data":{"message":"boom happened"}}}`,
	}
	summary, steps, _, _ := parseOutput(lines)
	assert.Contains(t, summary, "boom happened")
	require.NotEmpty(t, steps)
	assert.Equal(t, "error", steps[0].Kind)
}

func TestParseOutput_SkipsNonJSONAndEmpty(t *testing.T) {
	summary, steps, tokIn, tok := parseOutput([]string{"", "   ", "not json at all", "[log] line"})
	assert.Empty(t, summary)
	assert.Empty(t, steps)
	assert.Zero(t, tokIn)
	assert.Zero(t, tok)
}

// TestParseOutput_TextEventsAreWholeMessage documents the WHOLE-MESSAGE assumption
// for `text` events (design doc: "Token-by-token streaming — still whole-message").
// A multi-paragraph reply arriving as ONE text event must yield ONE assistant step
// and ONE summary block — NOT N delta steps, and NOT self-concatenated. If a future
// real transcript shows opencode emits per-token deltas, this test will still pass
// for the whole-message case but the parser must then coalesce on the step boundary.
func TestParseOutput_TextEventsAreWholeMessage(t *testing.T) {
	const para = "First paragraph.\n\nSecond paragraph spanning\nmultiple lines.\n\nThird."
	b, err := json.Marshal(map[string]any{
		"type": "text",
		"part": map[string]any{"type": "text", "text": para},
	})
	require.NoError(t, err)
	summary, steps, _, _ := parseOutput([]string{string(b)})

	require.Len(t, steps, 1, "one whole-message text event must produce exactly one assistant step")
	assert.Equal(t, "assistant", steps[0].Kind)
	assert.Equal(t, para, steps[0].Summary, "the step carries the whole message verbatim")
	assert.Equal(t, para, summary, "summary is the whole message, not self-concatenated fragments")
}

// TestParseOutput_StepFinishTokensArePerStep documents the PER-STEP token assumption:
// each step_finish reports the tokens for THAT step, so a 3-step mission's total is
// the SUM (not the last cumulative value, and not an N-squared over-count). If a real
// transcript ever shows cumulative totals, the parser must switch to taking the last
// step_finish instead of summing.
func TestParseOutput_StepFinishTokensArePerStep(t *testing.T) {
	lines := []string{
		`{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":100,"output":10}}}`,
		`{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":50,"output":20}}}`,
		`{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":30,"output":5}}}`,
	}
	_, _, tokIn, tokOut := parseOutput(lines)
	assert.Equal(t, 180, tokIn, "per-step input tokens are summed (100+50+30)")
	assert.Equal(t, 35, tokOut, "per-step output tokens are summed (10+20+5)")
}

// TestParseOutput_ProviderErrorSummaryCapped verifies a very long provider error
// message is length-capped (with an ellipsis) when folded into the summary, bounding
// any provider info leak; the step keeps the same capped text.
func TestParseOutput_ProviderErrorSummaryCapped(t *testing.T) {
	long := strings.Repeat("x", maxErrorSummaryLen*2)
	b, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"name": "UnknownError", "data": map[string]any{"message": long}},
	})
	require.NoError(t, err)
	summary, steps, _, _ := parseOutput([]string{string(b)})

	require.NotEmpty(t, steps)
	assert.LessOrEqual(t, len([]rune(summary)), maxErrorSummaryLen+1, "summary capped (+1 for ellipsis)")
	assert.True(t, strings.HasSuffix(summary, "…"), "truncated summary ends with an ellipsis")
}
