package http_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/instance"
)

// A per-instance stream keeps routing controls to its URL instance even if a
// client sends an instanceId field (that field is subtree-mode only).
func TestStreamHandler_PerInstanceModeIgnoresInstanceIDField(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{}
	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst
	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/instances/"+inst.ID.String()+"/ws", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	other := uuid.New()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.send","content":"hi","instanceId":"`+other.String()+`"}`)))

	require.Eventually(t, func() bool {
		convo.mu.Lock()
		defer convo.mu.Unlock()
		return len(convo.handleCalls) == 1
	}, 2*time.Second, 20*time.Millisecond)
	convo.mu.Lock()
	defer convo.mu.Unlock()
	assert.Equal(t, inst.ID, convo.handleCalls[0].InstanceID)
}
