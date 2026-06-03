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
