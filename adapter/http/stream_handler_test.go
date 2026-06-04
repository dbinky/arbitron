package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mockConversationHandler records calls to HandleMessage and EndSession.
type mockConversationHandler struct {
	mu             sync.Mutex
	handleCalls    []handleMessageCall
	endCalls       []uuid.UUID
	handleMessageFn func(ctx context.Context, instanceID uuid.UUID, content string) error
	endSessionFn    func(ctx context.Context, instanceID uuid.UUID) (map[string]any, error)
}

type handleMessageCall struct {
	InstanceID uuid.UUID
	Content    string
}

func (m *mockConversationHandler) HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error {
	m.mu.Lock()
	m.handleCalls = append(m.handleCalls, handleMessageCall{InstanceID: instanceID, Content: content})
	fn := m.handleMessageFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, instanceID, content)
	}
	return nil
}

func (m *mockConversationHandler) EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error) {
	m.mu.Lock()
	m.endCalls = append(m.endCalls, instanceID)
	fn := m.endSessionFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, instanceID)
	}
	return nil, nil
}

func newStreamTestServer(instRepo *mockInstanceRepo, bus engine.EventBus) *httptest.Server {
	return newStreamTestServerWithConvo(instRepo, bus, nil)
}

func newStreamTestServerWithConvo(instRepo *mockInstanceRepo, bus engine.EventBus, convo engine.ConversationHandler) *httptest.Server {
	return newStreamTestServerFull(instRepo, nil, bus, convo)
}

// streamGetterFunc adapts a function to the connect-time GetInstance check the
// stream handler needs. The default test wiring delegates to the repo with an
// unconstrained (admin-equivalent) view; auth-specific tests inject their own.
type streamGetterFunc func(ctx context.Context, id uuid.UUID) (*instance.Instance, error)

func (f streamGetterFunc) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	return f(ctx, id)
}

func newStreamTestServerFull(instRepo *mockInstanceRepo, nodeExecRepo instance.NodeExecutionRepository, bus engine.EventBus, convo engine.ConversationHandler) *httptest.Server {
	// Default: the connect-time check resolves the instance unconstrained, so the
	// existing stream tests (no identity injected) keep passing. Per-user connect
	// scoping is covered explicitly in TestStreamHandler_ConnectAuthorization.
	getter := streamGetterFunc(func(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
		return instRepo.Get(ctx, id)
	})
	sh := adapthttp.NewStreamHandler(instRepo, nodeExecRepo, bus, convo, getter)

	r := chi.NewRouter()
	r.Get("/instances/{id}/ws", sh.HandleWebSocket)

	return httptest.NewServer(r)
}

// scopedStreamServer wires a per-instance stream handler behind an
// identity-injecting middleware, with a getter that applies the same
// owner-or-admin rule the engine's GetInstance enforces (returning ErrNotFound
// for a non-viewer). This exercises the connect-time authorization without
// importing the engine's unexported canView.
func scopedStreamServer(instRepo *mockInstanceRepo, bus engine.EventBus, id identity.Identity) *httptest.Server {
	getter := streamGetterFunc(func(ctx context.Context, instID uuid.UUID) (*instance.Instance, error) {
		inst, err := instRepo.Get(ctx, instID)
		if err != nil {
			return nil, err
		}
		if !id.IsAdmin() && inst.OwnerID != id.UserID {
			return nil, instance.ErrNotFound
		}
		return inst, nil
	})
	sh := adapthttp.NewStreamHandler(instRepo, nil, bus, nil, getter)
	r := chi.NewRouter()
	r.Get("/instances/{id}/ws", withIdentityMiddleware(id, sh.HandleWebSocket))
	return httptest.NewServer(r)
}

// --- Per-instance connect authorization (Phase 4 Task 3) ---

func TestStreamHandler_ConnectAuthorization(t *testing.T) {
	owner := identity.Identity{UserID: uuid.New(), Username: "owner", Role: identity.RoleUser}
	other := identity.Identity{UserID: uuid.New(), Username: "other", Role: identity.RoleUser}
	admin := identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}

	newInst := func(repo *mockInstanceRepo) *instance.Instance {
		inst := testInstance(instance.StateRunning)
		inst.OwnerID = owner.UserID
		repo.instances[inst.ID] = inst
		return inst
	}

	t.Run("owner connects: upgrade succeeds", func(t *testing.T) {
		repo := newMockInstanceRepo()
		inst := newInst(repo)
		bus := eventbus.New()
		srv := scopedStreamServer(repo, bus, owner)
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		require.NoError(t, err)
		defer conn.Close(websocket.StatusNormalClosure, "")

		time.Sleep(50 * time.Millisecond)
		bus.Publish(inst.ID, engine.Event{Type: "node_started", Timestamp: time.Now().UTC(), Data: map[string]any{}})
		_, _, err = conn.Read(ctx)
		require.NoError(t, err)
	})

	t.Run("admin (non-owner) connects: upgrade succeeds", func(t *testing.T) {
		repo := newMockInstanceRepo()
		inst := newInst(repo)
		bus := eventbus.New()
		srv := scopedStreamServer(repo, bus, admin)
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		require.NoError(t, err)
		conn.Close(websocket.StatusNormalClosure, "")
	})

	t.Run("non-owner non-admin: 404 before upgrade, no socket", func(t *testing.T) {
		repo := newMockInstanceRepo()
		inst := newInst(repo)
		bus := eventbus.New()
		srv := scopedStreamServer(repo, bus, other)
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
		conn, resp, err := websocket.Dial(ctx, wsURL, nil)
		assert.Error(t, err, "non-owner must be rejected before the WS upgrade")
		if conn != nil {
			conn.Close(websocket.StatusNormalClosure, "")
		}
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "rejected with 404, identical to a missing instance")
	})

	t.Run("no identity in context: 404 before upgrade, same as non-owner (fail-closed, no existence leak)", func(t *testing.T) {
		// This pins the fail-closed behaviour for an unauthenticated caller that
		// somehow reaches the handler without the auth middleware (e.g. local test
		// wiring, middleware misconfiguration). The handler's service replicates the
		// engine's rule: no identity → ErrNotFound, so the caller gets a 404 before
		// the WebSocket is accepted, and existence is not leaked.
		repo := newMockInstanceRepo()
		inst := newInst(repo)
		bus := eventbus.New()

		getter := streamGetterFunc(func(ctx context.Context, instID uuid.UUID) (*instance.Instance, error) {
			if _, ok := identity.FromContext(ctx); !ok {
				return nil, instance.ErrNotFound
			}
			return repo.Get(ctx, instID)
		})
		sh := adapthttp.NewStreamHandler(repo, nil, bus, nil, getter)
		r := chi.NewRouter()
		r.Get("/instances/{id}/ws", sh.HandleWebSocket) // no identity middleware

		srv := httptest.NewServer(r)
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
		conn, resp, err := websocket.Dial(ctx, wsURL, nil)
		assert.Error(t, err, "no identity must be rejected before the WS upgrade")
		if conn != nil {
			conn.Close(websocket.StatusNormalClosure, "")
		}
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "rejected with 404, identical to non-owner (no existence leak)")
	})
}

// ---------------------------------------------------------------------------
// Tests: Connect to running instance and receive events
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToRunningInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Publish an event after connection is established.
	evt := engine.Event{
		Type:      "node_started",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		Data:      map[string]any{"nodeId": "A"},
	}

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)
	bus.Publish(inst.ID, evt)

	// Read the event from WebSocket.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "node_started", received.Type)
}

// ---------------------------------------------------------------------------
// Tests: Connect to completed instance gets state_changed and closes
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToCompletedInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateCompleted)
	now := time.Now()
	inst.CompletedAt = &now
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Should receive a state_changed event for the terminal state.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "state_changed", received.Type)

	// Connection should close after the terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal event")
}

// ---------------------------------------------------------------------------
// Tests: Connect to failed instance gets state_changed and closes
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToFailedInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateFailed)
	now := time.Now()
	inst.CompletedAt = &now
	failedNode := "B"
	inst.FailedNodeID = &failedNode
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Should receive a state_changed event for the terminal state.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "state_changed", received.Type)

	// Connection should close after the terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal event")
}

// ---------------------------------------------------------------------------
// Tests: Connect to cancelled instance gets state_changed and closes
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToCancelledInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateCancelled)
	now := time.Now()
	inst.CompletedAt = &now
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "state_changed", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal event")
}

// ---------------------------------------------------------------------------
// Tests: Connect to nonexistent instance returns 404
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToNonexistentInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := uuid.New()
	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + id.String() + "/ws"

	// WebSocket dial should fail because server returns 404 before upgrade.
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Tests: Invalid instance ID returns 400
// ---------------------------------------------------------------------------

func TestStreamHandler_InvalidInstanceID(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/not-a-uuid/ws"

	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Tests: Client disconnect triggers clean unsubscribe
// ---------------------------------------------------------------------------

func TestStreamHandler_ClientDisconnect(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Client disconnects.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Wait a moment, then publish. Should not panic.
	time.Sleep(50 * time.Millisecond)
	assert.NotPanics(t, func() {
		bus.Publish(inst.ID, engine.Event{
			Type:      "test",
			Timestamp: time.Now(),
			Data:      "after-disconnect",
		})
	})
}

// ---------------------------------------------------------------------------
// Tests: Terminal state_changed event during streaming closes connection
// ---------------------------------------------------------------------------

func TestStreamHandler_TerminalEventDuringStreaming(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Send a non-terminal event first.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt1 engine.Event
	require.NoError(t, json.Unmarshal(msg, &evt1))
	assert.Equal(t, "node_started", evt1.Type)

	// Now send a terminal state_changed event.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      map[string]any{"state": "completed"},
	})

	_, msg, err = conn.Read(ctx)
	require.NoError(t, err)
	var evt2 engine.Event
	require.NoError(t, json.Unmarshal(msg, &evt2))
	assert.Equal(t, "state_changed", evt2.Type)

	// Connection should close after terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal state_changed")
}

// ---------------------------------------------------------------------------
// Tests: Engine domain event types close the connection
// ---------------------------------------------------------------------------

func TestStreamHandler_EngineCompletedEventClosesConnection(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Publish the engine's real completed event type.
	bus.Publish(inst.ID, engine.Event{
		Type:      "instance.completed",
		Timestamp: time.Now(),
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "instance.completed", received.Type)

	// Connection should close after the terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after instance.completed")
}

func TestStreamHandler_EngineFailedEventClosesConnection(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{
		Type:      "instance.failed",
		Timestamp: time.Now(),
		Data:      map[string]any{"failedNodeId": "B", "error": "node B exploded"},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "instance.failed", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after instance.failed")
}

func TestStreamHandler_EngineCancelledEventClosesConnection(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{
		Type:      "instance.cancelled",
		Timestamp: time.Now(),
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "instance.cancelled", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after instance.cancelled")
}

// ---------------------------------------------------------------------------
// Tests: Multiple events received in order
// ---------------------------------------------------------------------------

func TestStreamHandler_MultipleEventsInOrder(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Publish 3 events.
	for i := 0; i < 3; i++ {
		bus.Publish(inst.ID, engine.Event{
			Type:      "node_completed",
			Timestamp: time.Now(),
			Data:      map[string]any{"index": float64(i)},
		})
	}

	// Read 3 events.
	for i := 0; i < 3; i++ {
		_, msg, err := conn.Read(ctx)
		require.NoError(t, err, "reading event %d", i)
		var evt engine.Event
		require.NoError(t, json.Unmarshal(msg, &evt))
		assert.Equal(t, "node_completed", evt.Type)
	}
}

// ---------------------------------------------------------------------------
// Tests: isTerminalEvent edge cases
// ---------------------------------------------------------------------------

func TestStreamHandler_StateChangedStringTerminalState(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// state_changed with a string "completed" should be treated as terminal.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      "completed",
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "state_changed", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should close after state_changed(completed)")
}

func TestStreamHandler_StateChangedStringNonTerminal(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// state_changed with "running" is NOT terminal — connection should stay open.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      "running",
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "state_changed", received.Type)

	// Send a follow-up event to confirm connection is still open.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, msg, err = conn.Read(ctx)
	require.NoError(t, err)
	var follow engine.Event
	require.NoError(t, json.Unmarshal(msg, &follow))
	assert.Equal(t, "node_started", follow.Type)
}

func TestStreamHandler_StateChangedMapNoStateKey(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// state_changed map without "state" key should NOT be terminal.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      map[string]any{"progress": float64(50)},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "state_changed", received.Type)

	// Connection should still be open — send another event to confirm.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "B"},
	})

	_, msg, err = conn.Read(ctx)
	require.NoError(t, err)
	var follow engine.Event
	require.NoError(t, json.Unmarshal(msg, &follow))
	assert.Equal(t, "node_started", follow.Type)
}

// ---------------------------------------------------------------------------
// Tests: Instance repo internal error returns 500 before upgrade
// ---------------------------------------------------------------------------

func TestStreamHandler_InstanceRepoError(t *testing.T) {
	bus := eventbus.New()

	errRepo := &errInstanceRepo{err: fmt.Errorf("db down")}
	getter := streamGetterFunc(func(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
		return errRepo.Get(ctx, id)
	})
	sh := adapthttp.NewStreamHandler(errRepo, nil, bus, nil, getter)
	r := chi.NewRouter()
	r.Get("/instances/{id}/ws", sh.HandleWebSocket)
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := uuid.New()
	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + id.String() + "/ws"

	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	}
}

// ===========================================================================
// Tests: Inbound bidirectional messaging
// ===========================================================================

// ---------------------------------------------------------------------------
// Tests: interactive.send routes to ConversationHandler.HandleMessage
// ---------------------------------------------------------------------------

func TestStreamHandler_InteractiveSend_RoutesToHandleMessage(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{}

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Send an interactive.send message.
	msg := `{"type":"interactive.send","content":"hello world"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err)

	// Wait for the handler to process the message.
	time.Sleep(100 * time.Millisecond)

	convo.mu.Lock()
	defer convo.mu.Unlock()
	require.Len(t, convo.handleCalls, 1)
	assert.Equal(t, inst.ID, convo.handleCalls[0].InstanceID)
	assert.Equal(t, "hello world", convo.handleCalls[0].Content)
}

// ---------------------------------------------------------------------------
// Tests: interactive.end routes to ConversationHandler.EndSession
// ---------------------------------------------------------------------------

func TestStreamHandler_InteractiveEnd_RoutesToEndSession(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{}

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	msg := `{"type":"interactive.end"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	convo.mu.Lock()
	defer convo.mu.Unlock()
	require.Len(t, convo.endCalls, 1)
	assert.Equal(t, inst.ID, convo.endCalls[0])
}

// ---------------------------------------------------------------------------
// Tests: HandleMessage error sends interactive.error event back
// ---------------------------------------------------------------------------

func TestStreamHandler_InteractiveSend_ErrorReturnsInteractiveError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{
		handleMessageFn: func(_ context.Context, _ uuid.UUID, _ string) error {
			return fmt.Errorf("conversation: %w", instance.ErrNotInteractiveWait)
		},
	}

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	msg := `{"type":"interactive.send","content":"hello"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err)

	// Read the interactive.error event back.
	_, rawMsg, err := conn.Read(ctx)
	require.NoError(t, err)

	var evt map[string]any
	require.NoError(t, json.Unmarshal(rawMsg, &evt))
	assert.Equal(t, "interactive.error", evt["type"])
	data, ok := evt["data"].(map[string]any)
	require.True(t, ok)
	// Generic client-safe message, not the raw error string.
	assert.Equal(t, "No active interactive session", data["message"])

	// Verify the error event carries a non-zero timestamp. If Timestamp were
	// accidentally removed from interactiveErrorEvent the frontend would display
	// "0001-01-01T00:00:00Z" on every error, but this assertion would still pass
	// if the field is absent (null) — so we explicitly require it.
	tsStr, tsOk := evt["timestamp"].(string)
	require.True(t, tsOk, "interactive.error must include a string timestamp field")
	ts, parseErr := time.Parse(time.RFC3339Nano, tsStr)
	require.NoError(t, parseErr, "interactive.error timestamp must parse as RFC3339")
	assert.False(t, ts.IsZero(), "interactive.error timestamp must not be the zero time")
}

// ---------------------------------------------------------------------------
// Tests: EndSession error sends interactive.error event back
// ---------------------------------------------------------------------------

func TestStreamHandler_InteractiveEnd_ErrorReturnsInteractiveError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{
		endSessionFn: func(_ context.Context, _ uuid.UUID) (map[string]any, error) {
			return nil, fmt.Errorf("conversation: %w", instance.ErrNotInteractiveWait)
		},
	}

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	msg := `{"type":"interactive.end"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err)

	_, rawMsg, err := conn.Read(ctx)
	require.NoError(t, err)

	var evt map[string]any
	require.NoError(t, json.Unmarshal(rawMsg, &evt))
	assert.Equal(t, "interactive.error", evt["type"])
	data, ok := evt["data"].(map[string]any)
	require.True(t, ok)
	// Generic client-safe message, not the raw error string.
	assert.Equal(t, "No active interactive session", data["message"])

	tsStr, tsOk := evt["timestamp"].(string)
	require.True(t, tsOk, "interactive.error must include a string timestamp field")
	ts, parseErr := time.Parse(time.RFC3339Nano, tsStr)
	require.NoError(t, parseErr, "interactive.error timestamp must parse as RFC3339")
	assert.False(t, ts.IsZero(), "interactive.error timestamp must not be the zero time")
}

// ---------------------------------------------------------------------------
// Tests: Unknown message types are silently ignored
// ---------------------------------------------------------------------------

func TestStreamHandler_UnknownMessageType_Ignored(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{}

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Send an unknown type.
	msg := `{"type":"unknown.thing","content":"surprise"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err)

	// Wait and verify no calls were made.
	time.Sleep(100 * time.Millisecond)

	convo.mu.Lock()
	defer convo.mu.Unlock()
	assert.Empty(t, convo.handleCalls, "unknown type should not invoke HandleMessage")
	assert.Empty(t, convo.endCalls, "unknown type should not invoke EndSession")

	// Connection should still be open. Send an event over the bus to confirm.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, rawMsg, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt engine.Event
	require.NoError(t, json.Unmarshal(rawMsg, &evt))
	assert.Equal(t, "node_started", evt.Type)
}

// ---------------------------------------------------------------------------
// Tests: Events from ConversationHandler flow back over WebSocket
// ---------------------------------------------------------------------------

func TestStreamHandler_InteractiveSend_EventsFlowBack(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	// The mock handler publishes events to the bus during HandleMessage,
	// simulating what the real ConversationHandler does.
	convo := &mockConversationHandler{
		handleMessageFn: func(_ context.Context, instanceID uuid.UUID, _ string) error {
			bus.Publish(instanceID, engine.Event{
				Type:      "interactive.token",
				Timestamp: time.Now(),
				Data:      map[string]any{"token": "Hello"},
			})
			bus.Publish(instanceID, engine.Event{
				Type:      "interactive.message",
				Timestamp: time.Now(),
				Data:      map[string]any{"role": "assistant", "content": "Hello"},
			})
			return nil
		},
	}

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	msg := `{"type":"interactive.send","content":"hi"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err)

	// Read the two events published by the mock handler.
	_, rawMsg1, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt1 map[string]any
	require.NoError(t, json.Unmarshal(rawMsg1, &evt1))
	assert.Equal(t, "interactive.token", evt1["type"])

	_, rawMsg2, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt2 map[string]any
	require.NoError(t, json.Unmarshal(rawMsg2, &evt2))
	assert.Equal(t, "interactive.message", evt2["type"])
}

// ---------------------------------------------------------------------------
// Tests: No ConversationHandler (nil) — inbound messages ignored gracefully
// ---------------------------------------------------------------------------

func TestStreamHandler_NilConversationHandler_InboundIgnored(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	// Pass nil for ConversationHandler.
	srv := newStreamTestServerWithConvo(instRepo, bus, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Send a message — should not panic or crash.
	msg := `{"type":"interactive.send","content":"hello"}`
	err = conn.Write(ctx, websocket.MessageText, []byte(msg))
	require.NoError(t, err)

	// Connection should still be open.
	time.Sleep(100 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, rawMsg, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt engine.Event
	require.NoError(t, json.Unmarshal(rawMsg, &evt))
	assert.Equal(t, "node_started", evt.Type)
}

// ---------------------------------------------------------------------------
// Tests: Malformed JSON inbound does not crash the connection
// ---------------------------------------------------------------------------

func TestStreamHandler_MalformedJSON_Ignored(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{}

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Send malformed JSON.
	err = conn.Write(ctx, websocket.MessageText, []byte("{not valid json"))
	require.NoError(t, err)

	// Connection should still be open.
	time.Sleep(100 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, rawMsg, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt engine.Event
	require.NoError(t, json.Unmarshal(rawMsg, &evt))
	assert.Equal(t, "node_started", evt.Type)
}

// ---------------------------------------------------------------------------
// Tests: Concurrent write safety — error events from read goroutine
// don't race with outbound events from write goroutine
// ---------------------------------------------------------------------------

func TestStreamHandler_ConcurrentWriteSafety(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	// Handler that returns an error and takes a moment to allow concurrent events.
	convo := &mockConversationHandler{
		handleMessageFn: func(_ context.Context, _ uuid.UUID, _ string) error {
			return fmt.Errorf("test error for concurrent write")
		},
	}

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Send multiple messages that will cause errors, while also publishing
	// bus events concurrently. This tests for data races under -race.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			msg := `{"type":"interactive.send","content":"msg"}`
			if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			bus.Publish(inst.ID, engine.Event{
				Type:      "node_started",
				Timestamp: time.Now(),
				Data:      map[string]any{"nodeId": "A"},
			})
			time.Sleep(10 * time.Millisecond)
		}
	}()

	wg.Wait()

	// Read all available messages — we're mainly checking no panics or data races.
	// Under -race, a concurrent write violation would be caught.
	readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer readCancel()
	for {
		_, _, err := conn.Read(readCtx)
		if err != nil {
			break
		}
	}
}

// ===========================================================================
// Tests: History hydration on WebSocket connection
// ===========================================================================

// ---------------------------------------------------------------------------
// Tests: interactive.history sent for instance in interactive wait
// ---------------------------------------------------------------------------

func TestStreamHandler_HistoryHydration_InteractiveWait(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	nodeID := "review-design"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{
				"id":   "review-design",
				"type": "interactive_llm",
				"config": map[string]any{
					"systemPrompt": "You are a reviewer.",
				},
			},
		},
	}
	instRepo.instances[inst.ID] = inst

	// Create a node execution with conversation history.
	nodeExecRepo.executions = []*instance.NodeExecution{
		{
			ID:         uuid.New(),
			InstanceID: inst.ID,
			NodeID:     "review-design",
			Attempt:    1,
			Status:     instance.NodeStatusSucceeded,
			LLMConversation: map[string]any{
				"messages": []any{
					map[string]any{"role": "user", "content": "hello"},
					map[string]any{"role": "assistant", "content": "hi there"},
				},
			},
			StartedAt: time.Now(),
		},
	}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// First event should be interactive.history.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var evt map[string]any
	require.NoError(t, json.Unmarshal(msg, &evt))
	assert.Equal(t, "interactive.history", evt["type"])

	data, ok := evt["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "review-design", data["nodeId"])

	messages, ok := data["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 2)

	msg0, ok0 := messages[0].(map[string]any)
	require.True(t, ok0, "first message must be a map")
	assert.Equal(t, "user", msg0["role"])
	assert.Equal(t, "hello", msg0["content"])

	msg1, ok1 := messages[1].(map[string]any)
	require.True(t, ok1, "second message must be a map")
	assert.Equal(t, "assistant", msg1["role"])
	assert.Equal(t, "hi there", msg1["content"])
}

// ---------------------------------------------------------------------------
// Tests: No history sent for running (non-interactive) instance
// ---------------------------------------------------------------------------

func TestStreamHandler_HistoryHydration_NotSentForRunning(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler time to send any history event.
	time.Sleep(100 * time.Millisecond)

	// Publish a real event to verify the connection is open and no history was sent.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var evt map[string]any
	require.NoError(t, json.Unmarshal(msg, &evt))
	// The first event should be the node_started, not interactive.history.
	assert.Equal(t, "node_started", evt["type"])
}

// ---------------------------------------------------------------------------
// Tests: No history sent for waiting instance on non-interactive node
// ---------------------------------------------------------------------------

func TestStreamHandler_HistoryHydration_NotSentForNonInteractiveWait(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()

	nodeID := "timer-node"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{
				"id":   "timer-node",
				"type": "timer_delay",
				"config": map[string]any{
					"durationSeconds": "30",
				},
			},
		},
	}
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(100 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "X"},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var evt map[string]any
	require.NoError(t, json.Unmarshal(msg, &evt))
	assert.Equal(t, "node_started", evt["type"])
}

// ---------------------------------------------------------------------------
// Tests: Nil nodeExecRepo with waiting interactive instance — nil guard
// ---------------------------------------------------------------------------

func TestStreamHandler_HistoryHydration_NilNodeExecRepo_SkipsHistory(t *testing.T) {
	// Edge: NewStreamHandler documents that nodeExecs may be nil. When a
	// waiting interactive instance connects, sendInteractiveHistory must
	// hit the nil guard and silently skip — no panic, connection stays open.
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	nodeID := "review-design"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{
				"id":   "review-design",
				"type": "interactive_llm",
				"config": map[string]any{
					"systemPrompt": "You are a reviewer.",
				},
			},
		},
	}
	instRepo.instances[inst.ID] = inst

	// Pass nil for nodeExecRepo — exercises the nil guard in sendInteractiveHistory.
	srv := newStreamTestServerFull(instRepo, nil, bus, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler time to process any history event (none should be sent).
	time.Sleep(100 * time.Millisecond)

	// Connection must still be open — publish a bus event to confirm.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": nodeID},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received map[string]any
	require.NoError(t, json.Unmarshal(msg, &received))
	// First received event must be node_started, not interactive.history.
	assert.Equal(t, "node_started", received["type"])
}

// ---------------------------------------------------------------------------
// Tests: Node execution not found during history hydration — warning path
// ---------------------------------------------------------------------------

func TestStreamHandler_HistoryHydration_NodeExecNotFound_SkipsHistory(t *testing.T) {
	// Error: the node execution record can be missing in crash-recovery scenarios
	// (instance reached waiting state but the executor never persisted the nodeExec).
	// sendInteractiveHistory must log a warning and skip — no panic, no history event,
	// connection stays open for normal bus events.
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo() // empty — no executions registered
	bus := eventbus.New()

	nodeID := "review-design"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{
				"id":   "review-design",
				"type": "interactive_llm",
				"config": map[string]any{
					"systemPrompt": "You are a reviewer.",
				},
			},
		},
	}
	instRepo.instances[inst.ID] = inst
	// nodeExecRepo has no matching execution — GetByInstanceAndNode will error.

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler time to attempt history hydration (it should skip it).
	time.Sleep(100 * time.Millisecond)

	// Connection must still be open — publish a bus event to confirm.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": nodeID},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var evtReceived map[string]any
	require.NoError(t, json.Unmarshal(msg, &evtReceived))
	// First event must be node_started, not interactive.history.
	assert.Equal(t, "node_started", evtReceived["type"])
}
