package claudecli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvent(t *testing.T) {
	t.Run("system init event yields session id", func(t *testing.T) {
		line := []byte(`{"type":"system","subtype":"init","session_id":"sess-1","model":"claude"}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventSystem, ev.Kind)
		assert.Equal(t, "sess-1", ev.SessionID)
	})

	t.Run("assistant event concatenates text blocks", func(t *testing.T) {
		line := []byte(`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventAssistant, ev.Kind)
		assert.Equal(t, "Hello world", ev.Text)
	})

	t.Run("assistant event ignores non-text blocks", func(t *testing.T) {
		line := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","text":""},{"type":"text","text":"hi"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "hi", ev.Text)
	})

	t.Run("result event yields text, tokens, session id", func(t *testing.T) {
		line := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s2","usage":{"input_tokens":10,"output_tokens":5}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventResult, ev.Kind)
		assert.Equal(t, "done", ev.Text)
		assert.Equal(t, "s2", ev.SessionID)
		assert.False(t, ev.IsError)
		assert.Equal(t, 10, ev.TokensIn)
		assert.Equal(t, 5, ev.TokensOut)
	})

	t.Run("result event with is_error true", func(t *testing.T) {
		line := []byte(`{"type":"result","is_error":true,"result":"boom","session_id":"s"}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, ev.IsError)
	})

	t.Run("user event recognised but carries no text", func(t *testing.T) {
		line := []byte(`{"type":"user","session_id":"s","message":{"content":[{"type":"tool_result"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, eventUser, ev.Kind)
		assert.Equal(t, "", ev.Text)
	})

	t.Run("blank line is skipped", func(t *testing.T) {
		_, ok, err := parseEvent([]byte("   "))
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("unknown event type is skipped", func(t *testing.T) {
		_, ok, err := parseEvent([]byte(`{"type":"stream_event"}`))
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("malformed JSON returns an error", func(t *testing.T) {
		_, ok, err := parseEvent([]byte(`{not json`))
		require.Error(t, err)
		assert.False(t, ok)
	})

	t.Run("realistic large text block passes through untruncated", func(t *testing.T) {
		// A single assistant turn (design doc, plan, code file) is routinely
		// tens of KB. The defensive cap must not silently chop legitimate
		// output — that manifested as interactive_agent replies cut off
		// mid-word. The cap is a guard against pathological blocks only.
		doc := strings.Repeat("z", 50000)
		line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + doc + `"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, 50000, len(ev.Text), "a 50KB block must flow through whole")
	})

	t.Run("text content block over the defensive cap is truncated", func(t *testing.T) {
		// SF13 / PR #8 thread 3291945042: defensive cap on a single text
		// block so a hostile prompt cannot balloon downstream string
		// handling before the runner's per-request bytes cap trips.
		big := strings.Repeat("z", maxContentBlockChars+1000)
		line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + big + `"}]}}`)
		ev, ok, err := parseEvent(line)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, maxContentBlockChars, len(ev.Text))
	})

	t.Run("inner/outer guard invariant: MaxResponseBytes is >= 256x maxContentBlockChars", func(t *testing.T) {
		// PR #18 (security LOW, comment 3350479602 / 3350533606): the two
		// memory guards are now load-bearing in relation to each other —
		// maxContentBlockChars bounds a single block, the per-request
		// MaxResponseBytes bounds the whole stream. The "256x headroom" claim in
		// the maxContentBlockChars doc comment must not silently rot if someone
		// bumps either bound. defaultMaxResponseBytes mirrors the production
		// default wired by adapter/config (llm.maxresponsebytes = 256 MiB).
		const defaultMaxResponseBytes int64 = 256 * 1024 * 1024
		assert.GreaterOrEqual(t, defaultMaxResponseBytes/int64(maxContentBlockChars), int64(256),
			"the per-request backstop must stay comfortably (>=256x) above the per-block cap")
	})
}

// --- cache-aware input accounting ------------------------------------------
//
// Anthropic splits input tokens across THREE buckets and `input_tokens` holds
// only the uncached remainder. A real result event from Claude Code 2.1.278
// reads input_tokens:2, cache_creation_input_tokens:27208,
// cache_read_input_tokens:26579 — so reading input_tokens alone reports 2 of
// 53,789 actual input tokens. That under-reporting is what made the claude-code
// backend unmeasurable against codex, whose `input_tokens` is already a total.

// HAPPY — all three input buckets sum into TokensIn.
func TestParseEvent_ResultSumsAllInputTokenBuckets(t *testing.T) {
	line := []byte(`{"type":"result","session_id":"s1","result":"done","usage":{"input_tokens":2,"cache_creation_input_tokens":27208,"cache_read_input_tokens":26579,"output_tokens":707}}`)
	ev, ok, err := parseEvent(line)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 2+27208+26579, ev.TokensIn, "every input bucket must be counted")
	assert.Equal(t, 707, ev.TokensOut)
}

// SUCCESS — a fully cached turn (input_tokens 0) still reports its real input
// volume rather than zero.
func TestParseEvent_FullyCachedTurnIsNotZeroInput(t *testing.T) {
	line := []byte(`{"type":"result","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":98765,"output_tokens":12}}`)
	ev, ok, err := parseEvent(line)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 98765, ev.TokensIn, "a cache hit is still an input token")
}

// EDGE (back-compat) — a usage block with no cache fields at all (an older CLI,
// or a provider that does not cache) still reports input_tokens unchanged.
func TestParseEvent_UsageWithoutCacheFieldsIsUnchanged(t *testing.T) {
	line := []byte(`{"type":"result","usage":{"input_tokens":41,"output_tokens":9}}`)
	ev, ok, err := parseEvent(line)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 41, ev.TokensIn)
	assert.Equal(t, 9, ev.TokensOut)
}

// EDGE — a result event with no usage block at all yields zero counts and no
// panic.
func TestParseEvent_ResultWithoutUsageIsZero(t *testing.T) {
	ev, ok, err := parseEvent([]byte(`{"type":"result","result":"done"}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, ev.TokensIn)
	assert.Zero(t, ev.TokensOut)
}

// ERROR — a malformed usage block fails the whole line closed rather than
// silently reporting zero tokens for a real turn.
func TestParseEvent_MalformedUsageFailsClosed(t *testing.T) {
	_, _, err := parseEvent([]byte(`{"type":"result","usage":{"input_tokens":"not-a-number"}}`))
	require.Error(t, err)
}

func TestParseEvent_ResultModelUsageKeys(t *testing.T) {
	ev, ok, err := parseEvent([]byte(`{"type":"result","result":"r","modelUsage":{"b":{"inputTokens":1},"a":{}}}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, ev.ModelUsage)
}

// Claude Code 2.1.300 emits non-assistant events whose `message` is a plain
// string. Decoding `message` as an object for EVERY line crashed run d35eab7c's
// produce-plans with "cannot unmarshal string into Go struct field
// rawEnvelope.message", failing the node on an event it never needed to read.
// Only an assistant event's message carries text, so only it is decoded.
func TestParseEvent_StringMessageOnNonAssistantEvents(t *testing.T) {
	// HAPPY — a system event with a string message still yields its session id.
	ev, ok, err := parseEvent([]byte(`{"type":"system","subtype":"task_notification","session_id":"s1","message":"Background task finished"}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, eventSystem, ev.Kind)
	assert.Equal(t, "s1", ev.SessionID)

	// SUCCESS — a user event with a string message is still recognised.
	ev, ok, err = parseEvent([]byte(`{"type":"user","session_id":"s2","message":"queued note"}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, eventUser, ev.Kind)

	// EDGE — a result event's text and tokens are unaffected by a string message.
	ev, ok, err = parseEvent([]byte(`{"type":"result","result":"done","message":"x","usage":{"input_tokens":3,"output_tokens":4}}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "done", ev.Text)
	assert.Equal(t, 3, ev.TokensIn)
}

// FAILURE — an unrecognised event type with a string message is skipped, not fatal.
func TestParseEvent_UnknownEventWithStringMessageIsSkipped(t *testing.T) {
	_, ok, err := parseEvent([]byte(`{"type":"stream_notice","message":"heads up"}`))
	require.NoError(t, err)
	assert.False(t, ok)
}

// ERROR — an ASSISTANT event whose message is not an object still fails closed:
// its text is the agent's output, and silently dropping it would lose a turn.
func TestParseEvent_AssistantStringMessageFailsClosed(t *testing.T) {
	_, _, err := parseEvent([]byte(`{"type":"assistant","message":"not an object"}`))
	require.Error(t, err)
}
