package claudecli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
)

// SF3 / PR #8 thread 3291907738: Handle / Finalize are tested directly so
// the partway-failure paths don't need a full Provider spin-up.

// recordingSink collects emitted chunks and never blocks.
type recordingSink struct {
	chunks []engine.ChatChunk
}

func (s *recordingSink) send(c engine.ChatChunk) bool {
	s.chunks = append(s.chunks, c)
	return true
}

func TestStreamState_HandleAssistantThenFinalize(t *testing.T) {
	var st streamState
	sink := &recordingSink{}

	st.Handle(`{"type":"system","session_id":"s1"}`, sink.send)
	st.Handle(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`, sink.send)
	st.Handle(`{"type":"result","is_error":false,"result":"hi","session_id":"s1","usage":{"input_tokens":2,"output_tokens":1}}`, sink.send)
	st.Finalize(nil, sink.send)

	require.Len(t, sink.chunks, 2)
	assert.Equal(t, "hi", sink.chunks[0].Token)
	last := sink.chunks[1]
	assert.True(t, last.Done)
	assert.Equal(t, "s1", last.SessionID)
	assert.Equal(t, 2, last.TokensIn)
	assert.Equal(t, 1, last.TokensOut)
}

func TestStreamState_ParseErrorMidStream_StopsForwarding(t *testing.T) {
	var st streamState
	sink := &recordingSink{}

	st.Handle(`{"type":"assistant","message":{"content":[{"type":"text","text":"a"}]}}`, sink.send)
	st.Handle(`{bad`, sink.send) // malformed -> error chunk + failed=true
	st.Handle(`{"type":"assistant","message":{"content":[{"type":"text","text":"b"}]}}`, sink.send)
	st.Finalize(nil, sink.send) // must NOT emit a Done chunk after a failure

	require.Len(t, sink.chunks, 2)
	assert.Equal(t, "a", sink.chunks[0].Token)
	require.Error(t, sink.chunks[1].Error)
	assert.True(t, st.failed)
}

func TestStreamState_ResultIsError_EmitsErrorChunkAndStopsForwarding(t *testing.T) {
	var st streamState
	sink := &recordingSink{}

	st.Handle(`{"type":"result","is_error":true,"result":"upstream boom","session_id":"s"}`, sink.send)
	st.Handle(`{"type":"assistant","message":{"content":[{"type":"text","text":"ignored"}]}}`, sink.send)
	st.Finalize(nil, sink.send)

	require.Len(t, sink.chunks, 1)
	require.Error(t, sink.chunks[0].Error)
	assert.Contains(t, sink.chunks[0].Error.Error(), "upstream boom")
}

func TestStreamState_WaitError_EmittedByFinalize(t *testing.T) {
	var st streamState
	sink := &recordingSink{}

	st.Finalize(errors.New("exit status 1"), sink.send)

	require.Len(t, sink.chunks, 1)
	require.Error(t, sink.chunks[0].Error)
	assert.Contains(t, sink.chunks[0].Error.Error(), "exit status 1")
}

func TestStreamState_ConsumerGoneFlagsFailed(t *testing.T) {
	var st streamState
	// sendChunk returns false to simulate consumer cancellation.
	send := func(engine.ChatChunk) bool { return false }

	st.Handle(`{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}}`, send)
	assert.True(t, st.failed, "consumer-gone must flag failed so Finalize stops")
}

func TestMakeSendChunk_NonBlocking(t *testing.T) {
	// sendChunk returns false (consumer gone) when runCtx is already
	// cancelled at the time of the send.
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan engine.ChatChunk)
	send := makeSendChunk(ctx, out)
	cancel()

	ok := send(engine.ChatChunk{Token: "x"})
	assert.False(t, ok)
}
