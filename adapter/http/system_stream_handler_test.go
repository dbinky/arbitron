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

// withIdentityMiddleware injects the given identity into the request context so
// the system stream handler (which requires an identity) can run in tests.
func withIdentityMiddleware(id identity.Identity, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r.WithContext(identity.WithIdentity(r.Context(), id)))
	}
}

// newSystemStreamTestServer wires the handler behind an admin identity so the
// pre-existing fan-out tests (which expect every event) keep passing; per-user
// scoping is covered in the dedicated scoping tests below.
func newSystemStreamTestServer(bus engine.EventBus) *httptest.Server {
	sh := adapthttp.NewSystemStreamHandler(bus, newMockInstanceRepo())
	admin := identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}

	r := chi.NewRouter()
	r.Get("/ws", withIdentityMiddleware(admin, sh.HandleWebSocket))

	return httptest.NewServer(r)
}

// scopedSysStreamServer wires the system stream behind a given identity and a
// repo that resolves instance ownership.
func scopedSysStreamServer(bus engine.EventBus, repo *mockInstanceRepo, id identity.Identity) *httptest.Server {
	sh := adapthttp.NewSystemStreamHandler(bus, repo)
	r := chi.NewRouter()
	r.Get("/ws", withIdentityMiddleware(id, sh.HandleWebSocket))
	return httptest.NewServer(r)
}

// countingInstanceRepo wraps a mockInstanceRepo and counts Get calls per id so
// the ownership cache can be asserted.
type countingInstanceRepo struct {
	*mockInstanceRepo
	mu       sync.Mutex
	getCalls map[uuid.UUID]int
	getErr   error
}

func (r *countingInstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	if r.getCalls == nil {
		r.getCalls = map[uuid.UUID]int{}
	}
	r.getCalls[id]++
	r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.mockInstanceRepo.Get(ctx, id)
}

func (r *countingInstanceRepo) callsFor(id uuid.UUID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getCalls[id]
}

func ownedInstanceForOwner(repo *mockInstanceRepo, owner uuid.UUID) *instance.Instance {
	inst := &instance.Instance{
		ID:        uuid.New(),
		OwnerID:   owner,
		State:     instance.StateRunning,
		Variables: map[string]any{},
	}
	repo.instances[inst.ID] = inst
	return inst
}

func readOneEvent(t *testing.T, conn *websocket.Conn, ctx context.Context) engine.Event {
	t.Helper()
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt engine.Event
	require.NoError(t, json.Unmarshal(msg, &evt))
	return evt
}

// --- System stream scoping (Phase 4 Task 3) ---

func TestSystemStreamHandler_Scoping_NonAdminSeesOnlyOwn(t *testing.T) {
	bus := eventbus.New()
	repo := newMockInstanceRepo()
	ownerA := identity.Identity{UserID: uuid.New(), Username: "a", Role: identity.RoleUser}
	ownerB := uuid.New()

	instA := ownedInstanceForOwner(repo, ownerA.UserID)
	instB := ownedInstanceForOwner(repo, ownerB)

	srv := scopedSysStreamServer(bus, repo, ownerA)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)
	// Publish B (must be dropped), then A (must be delivered).
	bus.Publish(instB.ID, engine.Event{Type: "b_event", Timestamp: time.Now().UTC(), Data: map[string]any{}})
	bus.Publish(instA.ID, engine.Event{Type: "a_event", Timestamp: time.Now().UTC(), Data: map[string]any{}})

	evt := readOneEvent(t, conn, ctx)
	assert.Equal(t, "a_event", evt.Type, "non-admin receives only their own instance's events; B was dropped")
}

func TestSystemStreamHandler_Scoping_AdminSeesAll(t *testing.T) {
	bus := eventbus.New()
	repo := newMockInstanceRepo()
	admin := identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}
	instA := ownedInstanceForOwner(repo, uuid.New())
	instB := ownedInstanceForOwner(repo, uuid.New())

	srv := scopedSysStreamServer(bus, repo, admin)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)
	bus.Publish(instA.ID, engine.Event{Type: "a_event", Timestamp: time.Now().UTC(), Data: map[string]any{}})
	bus.Publish(instB.ID, engine.Event{Type: "b_event", Timestamp: time.Now().UTC(), Data: map[string]any{}})

	e1 := readOneEvent(t, conn, ctx)
	e2 := readOneEvent(t, conn, ctx)
	assert.Equal(t, "a_event", e1.Type)
	assert.Equal(t, "b_event", e2.Type, "admin receives events for both owners' instances")
}

func TestSystemStreamHandler_Scoping_LookupErrorFailsClosed(t *testing.T) {
	bus := eventbus.New()
	owner := identity.Identity{UserID: uuid.New(), Username: "a", Role: identity.RoleUser}
	base := newMockInstanceRepo()
	instOwned := ownedInstanceForOwner(base, owner.UserID)
	repo := &countingInstanceRepo{mockInstanceRepo: base, getErr: fmt.Errorf("ownership lookup failed")}

	sh := adapthttp.NewSystemStreamHandler(bus, repo)
	r := chi.NewRouter()
	r.Get("/ws", withIdentityMiddleware(owner, sh.HandleWebSocket))
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)
	// Ownership lookup errors -> event dropped (fail-closed). The read should time out.
	bus.Publish(instOwned.ID, engine.Event{Type: "should_drop", Timestamp: time.Now().UTC(), Data: map[string]any{}})

	readCtx, readCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer readCancel()
	_, _, err = conn.Read(readCtx)
	assert.Error(t, err, "an event whose ownership cannot be resolved is dropped")
}

func TestSystemStreamHandler_Scoping_CacheHitOncePerInstance(t *testing.T) {
	bus := eventbus.New()
	owner := identity.Identity{UserID: uuid.New(), Username: "a", Role: identity.RoleUser}
	base := newMockInstanceRepo()
	inst := ownedInstanceForOwner(base, owner.UserID)
	repo := &countingInstanceRepo{mockInstanceRepo: base}

	sh := adapthttp.NewSystemStreamHandler(bus, repo)
	r := chi.NewRouter()
	r.Get("/ws", withIdentityMiddleware(owner, sh.HandleWebSocket))
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 3; i++ {
		bus.Publish(inst.ID, engine.Event{Type: "tick", Timestamp: time.Now().UTC(), Data: map[string]any{}})
	}
	for i := 0; i < 3; i++ {
		readOneEvent(t, conn, ctx)
	}
	assert.Equal(t, 1, repo.callsFor(inst.ID), "ownership is resolved once per instance, not per event")
}

func TestSystemStreamHandler_NoIdentity_Unauthorized(t *testing.T) {
	bus := eventbus.New()
	sh := adapthttp.NewSystemStreamHandler(bus, newMockInstanceRepo())
	r := chi.NewRouter()
	r.Get("/ws", sh.HandleWebSocket) // no identity middleware

	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	assert.Error(t, err, "no identity -> rejected before upgrade")
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "")
	}
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSystemStreamHandler_ReceivesEventsFromAnyInstance(t *testing.T) {
	bus := eventbus.New()

	srv := newSystemStreamTestServer(bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	id1 := uuid.New()
	id2 := uuid.New()

	bus.Publish(id1, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		Data:      map[string]any{"nodeId": "A"},
	})

	bus.Publish(id2, engine.Event{
		Type:      "node_completed",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		Data:      map[string]any{"nodeId": "B"},
	})

	// Read first event.
	_, msg1, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt1 engine.Event
	require.NoError(t, json.Unmarshal(msg1, &evt1))
	assert.Equal(t, "node_started", evt1.Type)

	// Read second event.
	_, msg2, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt2 engine.Event
	require.NoError(t, json.Unmarshal(msg2, &evt2))
	assert.Equal(t, "node_completed", evt2.Type)
}

func TestSystemStreamHandler_ServerContextCancel_ClosesConnection(t *testing.T) {
	bus := eventbus.New()
	sh := adapthttp.NewSystemStreamHandler(bus, newMockInstanceRepo())
	admin := identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}

	// cancelCh receives the cancel func injected into the handler's request context.
	cancelCh := make(chan context.CancelFunc, 1)

	r := chi.NewRouter()
	r.Get("/ws", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithCancel(identity.WithIdentity(req.Context(), admin))
		cancelCh <- cancel
		sh.HandleWebSocket(w, req.WithContext(ctx))
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	require.NoError(t, err)

	// Retrieve the injected cancel func and wait for the handler to reach the select loop.
	var cancel context.CancelFunc
	select {
	case cancel = <-cancelCh:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start within timeout")
	}
	time.Sleep(20 * time.Millisecond)

	// Cancel the server-side context — simulates server shutdown.
	cancel()

	// The handler should send a close frame (StatusGoingAway). The client's next
	// read should return an error indicating the connection was closed.
	var wg sync.WaitGroup
	wg.Add(1)
	var readErr error
	go func() {
		defer wg.Done()
		_, _, readErr = conn.Read(dialCtx)
	}()

	wg.Wait()
	assert.Error(t, readErr, "connection must be closed after server context is cancelled")
}

// TestSystemStreamHandler_EmitsInstanceIDOnWire drives the real handler end-to-end,
// publishes an event for a known instance id, reads the raw WebSocket frame, and
// decodes into map[string]any so that instanceId cannot be silently dropped by a
// type that lacks the field (e.g. engine.Event).  The test closes the gap left by
// TestSystemStream_EnvelopeIncludesInstanceID, which only marshals the struct in
// isolation and would not catch the handler omitting the field from the envelope.
func TestSystemStreamHandler_EmitsInstanceIDOnWire(t *testing.T) {
	bus := eventbus.New()

	srv := newSystemStreamTestServer(bus) // admin identity → sees all events
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler a moment to subscribe before publishing.
	time.Sleep(50 * time.Millisecond)

	knownID := uuid.New()
	bus.Publish(knownID, engine.Event{
		Type:      "node.started",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		Data:      map[string]any{"nodeId": "X"},
	})

	// Read the raw wire bytes — NOT engine.Event, which silently drops unknown fields.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(msg, &got))

	// Core assertion: instanceId must appear in the wire envelope.
	// If the handler ever omits InstanceID from systemWSEnvelope this becomes nil and fails.
	assert.Equal(t, knownID.String(), got["instanceId"], "instanceId must be emitted in the system-WS envelope")

	// Envelope fields beyond instanceId must also survive the round-trip.
	assert.Equal(t, "node.started", got["type"], "type must be present in the envelope")
	_, hasData := got["data"]
	assert.True(t, hasData, "data must be present in the envelope")
}

func TestSystemStreamHandler_ClientDisconnect(t *testing.T) {
	bus := eventbus.New()

	srv := newSystemStreamTestServer(bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Client disconnects.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Wait a moment, then publish. Should not panic.
	time.Sleep(50 * time.Millisecond)
	assert.NotPanics(t, func() {
		bus.Publish(uuid.New(), engine.Event{
			Type:      "test",
			Timestamp: time.Now(),
			Data:      "after-disconnect",
		})
	})
}
