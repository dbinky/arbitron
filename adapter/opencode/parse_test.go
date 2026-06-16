package opencode

import (
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
	summary, steps, tok := parseOutput(lines)
	assert.Contains(t, summary, "Let me check the files.")
	assert.Contains(t, summary, "DONE")
	assert.Equal(t, 25, tok) // 20 + 5
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
	summary, steps, _ := parseOutput(lines)
	assert.Contains(t, summary, "boom happened")
	require.NotEmpty(t, steps)
	assert.Equal(t, "error", steps[0].Kind)
}

func TestParseOutput_SkipsNonJSONAndEmpty(t *testing.T) {
	summary, steps, tok := parseOutput([]string{"", "   ", "not json at all", "[log] line"})
	assert.Empty(t, summary)
	assert.Empty(t, steps)
	assert.Zero(t, tok)
}
