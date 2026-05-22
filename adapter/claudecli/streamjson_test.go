package claudecli

import (
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
}
