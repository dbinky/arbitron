package engine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fullTurnIdentity() engine.InteractiveTurnIdentity {
	return engine.InteractiveTurnIdentity{RunID: uuid.New(), NodeID: "talk", InvocationID: uuid.New(),
		RequestID: uuid.New(), TurnIndex: 3, Generation: 7}
}

// Happy: the identity travels through the context and a committed conversation.
func TestTurnIdentity_ContextAndCommittedRoundTrip(t *testing.T) {
	id := fullTurnIdentity()
	got, ok := engine.TurnIdentityFromContext(engine.ContextWithTurnIdentity(context.Background(), id))
	require.True(t, ok)
	assert.Equal(t, id, got)

	// In memory (struct value) and after a JSON round trip through the DB (map value).
	conv := map[string]any{engine.ConversationKeyTurnIdentity: id}
	got, ok = engine.CommittedTurnIdentity(conv)
	require.True(t, ok)
	assert.Equal(t, id, got)
	raw, err := json.Marshal(conv)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	got, ok = engine.CommittedTurnIdentity(decoded)
	require.True(t, ok)
	assert.Equal(t, id, got)
	assert.Equal(t, id.RunID.String(), decoded[engine.ConversationKeyTurnIdentity].(map[string]any)["instanceId"], "wire name is instanceId")
}

// Success: acceptance stamps the identity from the persisted exact intent.
func TestTurnIdentity_FromIntentCopiesExactFields(t *testing.T) {
	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), RunID: uuid.New(), InvocationID: uuid.New(),
		NodeID: "talk", TurnIndex: 2, SessionGeneration: 5, Kind: engine.TurnIntentMessage}
	got := engine.TurnIdentityFromIntent(in)
	assert.Equal(t, engine.InteractiveTurnIdentity{RunID: in.RunID, NodeID: "talk", InvocationID: in.InvocationID,
		RequestID: in.RequestID, TurnIndex: 2, Generation: 5}, got)
	require.NoError(t, got.Validate())
}

// Failure: incomplete identities are rejected; nothing is "committed".
func TestTurnIdentity_IncompleteIsRejected(t *testing.T) {
	for name, mut := range map[string]func(*engine.InteractiveTurnIdentity){
		"run":        func(i *engine.InteractiveTurnIdentity) { i.RunID = uuid.Nil },
		"node":       func(i *engine.InteractiveTurnIdentity) { i.NodeID = "" },
		"invocation": func(i *engine.InteractiveTurnIdentity) { i.InvocationID = uuid.Nil },
		"request":    func(i *engine.InteractiveTurnIdentity) { i.RequestID = uuid.Nil },
		"generation": func(i *engine.InteractiveTurnIdentity) { i.Generation = 0 },
		"turn index": func(i *engine.InteractiveTurnIdentity) { i.TurnIndex = -1 },
	} {
		id := fullTurnIdentity()
		mut(&id)
		assert.ErrorIs(t, id.Validate(), engine.ErrInvalidTurnIdentity, name)
		_, ok := engine.CommittedTurnIdentity(map[string]any{engine.ConversationKeyTurnIdentity: id})
		assert.False(t, ok, name)
	}
}

// Error: garbage under the key is not an identity.
func TestTurnIdentity_MalformedCommittedValue(t *testing.T) {
	for _, v := range []any{"text", 42, map[string]any{"instanceId": "not-a-uuid"}} {
		_, ok := engine.CommittedTurnIdentity(map[string]any{engine.ConversationKeyTurnIdentity: v})
		assert.False(t, ok)
	}
}

// Edge: absent context/conversation values.
func TestTurnIdentity_AbsentValues(t *testing.T) {
	_, ok := engine.TurnIdentityFromContext(context.Background())
	assert.False(t, ok)
	_, ok = engine.CommittedTurnIdentity(nil)
	assert.False(t, ok)
	_, ok = engine.CommittedTurnIdentity(map[string]any{})
	assert.False(t, ok)
}
