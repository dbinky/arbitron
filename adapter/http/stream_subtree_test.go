package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// ---------------------------------------------------------------------------
// Subtree harness: a REAL engine.SubtreeEventBridge over the real bus and the
// mock repo, a per-id authorization getter, and a switchable activity source.
//
//   root ── a ── a1        other ── other1
//        └─ b
// ---------------------------------------------------------------------------

type activityFunc func(ctx context.Context, id uuid.UUID) (*engine.GraphState, error)

func (f activityFunc) GraphState(ctx context.Context, id uuid.UUID) (*engine.GraphState, error) {
	return f(ctx, id)
}

type subtreeHarness struct {
	repo   *mockInstanceRepo
	bus    *eventbus.InProcessEventBus
	convo  *mockConversationHandler
	active atomic.Bool
	denied map[uuid.UUID]bool // written before start only
	srv    *httptest.Server

	root, a, a1, b, other, other1 uuid.UUID
}

func newSubtreeHarness(rootState instance.State) *subtreeHarness {
	h := &subtreeHarness{repo: newMockInstanceRepo(), bus: eventbus.New(), convo: &mockConversationHandler{}, denied: map[uuid.UUID]bool{}}
	h.active.Store(true)
	h.root = h.add(nil, rootState)
	h.a = h.add(&h.root, instance.StateRunning)
	h.a1 = h.add(&h.a, instance.StateRunning)
	h.b = h.add(&h.root, instance.StateRunning)
	h.other = h.add(nil, instance.StateRunning)
	h.other1 = h.add(&h.other, instance.StateRunning)
	return h
}

func (h *subtreeHarness) add(parent *uuid.UUID, st instance.State) uuid.UUID {
	inst := testInstance(st)
	if parent != nil {
		p := *parent
		inst.ParentInstanceID = &p
	}
	h.repo.instances[inst.ID] = inst
	return inst.ID
}

func (h *subtreeHarness) getter() streamGetterFunc {
	return func(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
		if h.denied[id] {
			return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
		}
		return h.repo.Get(ctx, id)
	}
}

func (h *subtreeHarness) start(t *testing.T, wire bool, recheck time.Duration, nodeExecs instance.NodeExecutionRepository) {
	t.Helper()
	sh := adapthttp.NewStreamHandler(h.repo, nodeExecs, h.bus, h.convo, h.getter())
	if wire {
		bridge := engine.NewSubtreeEventBridge(h.bus, h.getter(), h.repo)
		sh.SetSubtreeStreaming(bridge, activityFunc(func(ctx context.Context, id uuid.UUID) (*engine.GraphState, error) {
			inst, err := h.getter()(ctx, id)
			if err != nil {
				return nil, err
			}
			return &engine.GraphState{RootInstanceID: id, Active: h.active.Load(),
				Contexts: []engine.ExecutionContext{{InstanceID: id, State: string(inst.State)}}}, nil
		}), recheck)
	}
	r := chi.NewRouter()
	r.Get("/instances/{id}/ws", sh.HandleWebSocket)
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
}

func (h *subtreeHarness) url(id uuid.UUID, query string) string {
	return "ws" + h.srv.URL[len("http"):] + "/instances/" + id.String() + "/ws" + query
}

// dial returns once the handler has subscribed AND made its connect-time
// activity decision: subtree mode does both BEFORE it upgrades, so no sleep is
// needed before publishing or flipping h.active.
func (h *subtreeHarness) dial(t *testing.T, ctx context.Context, id uuid.UUID) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, h.url(id, "?subtree=1"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func (h *subtreeHarness) publish(id uuid.UUID, typ string) {
	h.bus.Publish(id, engine.Event{Type: typ, Timestamp: time.Now().UTC(), Data: map[string]any{"marker": typ + "@" + id.String()}})
}

func readFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(msg, &m))
	return m
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ---------------------------------------------------------------------------
// Happy path / routing
// ---------------------------------------------------------------------------

func TestStreamSubtree_ForwardsDescendantEventsWithSource(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	h.publish(h.other1, engine.EventTypeNodeStarted) // another tree: never forwarded
	h.publish(h.a1, engine.EventTypeNodeStarted)
	h.publish(h.root, engine.EventTypeNodeCompleted)

	f := readFrame(t, ctx, conn)
	assert.Equal(t, engine.EventTypeNodeStarted, f["type"])
	assert.Equal(t, h.a1.String(), f["sourceInstanceId"])
	assert.Equal(t, map[string]any{"marker": engine.EventTypeNodeStarted + "@" + h.a1.String()}, f["data"])
	assert.Contains(t, f, "timestamp")
	f = readFrame(t, ctx, conn)
	assert.Equal(t, h.root.String(), f["sourceInstanceId"])
}

func TestStreamSubtree_IntermediateParentExcludesSiblings(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.a)

	h.publish(h.b, engine.EventTypeNodeStarted)
	h.publish(h.root, engine.EventTypeNodeStarted)
	h.publish(h.a1, engine.EventTypeNodeStarted)

	assert.Equal(t, h.a1.String(), readFrame(t, ctx, conn)["sourceInstanceId"])
}

func TestStreamSubtree_UnauthorizedDescendantDropped(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.denied[h.a1] = true
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	h.publish(h.a1, engine.EventTypeInteractiveToken)
	h.publish(h.a, engine.EventTypeNodeStarted)

	f := readFrame(t, ctx, conn)
	assert.Equal(t, h.a.String(), f["sourceInstanceId"], "an unviewable descendant's token never reaches the subscriber")
}

func TestStreamSubtree_HistoryHydrationCarriesSource(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	nodeID := "review"
	rootInst := h.repo.instances[h.root]
	rootInst.CurrentNodeID = &nodeID
	rootInst.WorkflowSnapshot = map[string]any{"name": "test-wf", "nodes": []any{
		map[string]any{"id": "review", "type": "interactive_llm", "config": map[string]any{}},
	}}
	execs := newMockNodeExecRepo()
	execs.executions = []*instance.NodeExecution{{
		ID: uuid.New(), InstanceID: h.root, NodeID: "review", Attempt: 1, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}},
		StartedAt:       time.Now(),
	}}
	h.start(t, true, time.Minute, execs)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	f := readFrame(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveHistory, f["type"])
	assert.Equal(t, h.root.String(), f["sourceInstanceId"])
}

// A socket on a grandparent (or one reconnecting mid-session) must hydrate an
// open descendant session, not only the root's: otherwise the subscriber sees
// later tokens of a conversation whose history it never received. A
// descendant the caller cannot view is never hydrated.
func TestStreamSubtree_HistoryHydrationIncludesVisibleDescendantSessions(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	snapshot := map[string]any{"name": "test-wf", "nodes": []any{
		map[string]any{"id": "review", "type": "interactive_llm", "config": map[string]any{}},
	}}
	nodeID := "review"
	execs := newMockNodeExecRepo()
	for _, id := range []uuid.UUID{h.a1, h.b} {
		inst := h.repo.instances[id]
		inst.State = instance.StateWaiting
		inst.CurrentNodeID = &nodeID
		inst.WorkflowSnapshot = snapshot
		execs.executions = append(execs.executions, &instance.NodeExecution{
			ID: uuid.New(), InstanceID: id, NodeID: "review", Attempt: 1, Status: instance.NodeStatusSucceeded,
			LLMConversation: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "from " + id.String()}}},
			StartedAt:       time.Now(),
		})
	}
	h.denied[h.b] = true
	sh := adapthttp.NewStreamHandler(h.repo, execs, h.bus, h.convo, h.getter())
	sh.SetSubtreeStreaming(engine.NewSubtreeEventBridge(h.bus, h.getter(), h.repo), activityFunc(func(_ context.Context, id uuid.UUID) (*engine.GraphState, error) {
		return &engine.GraphState{RootInstanceID: id, Active: true, Contexts: []engine.ExecutionContext{
			{InstanceID: h.root, State: string(instance.StateWaiting)},
			{InstanceID: h.a, State: string(instance.StateRunning)},
			{InstanceID: h.a1, State: string(instance.StateWaiting)},
			{InstanceID: h.b, State: string(instance.StateWaiting)}, // stale/unredacted: still re-authorized
		}}, nil
	}), time.Minute)
	r := chi.NewRouter()
	r.Get("/instances/{id}/ws", sh.HandleWebSocket)
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	f := readFrame(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveHistory, f["type"])
	assert.Equal(t, h.a1.String(), f["sourceInstanceId"])
	msgs := f["data"].(map[string]any)["messages"].([]any)
	assert.Equal(t, "from "+h.a1.String(), msgs[0].(map[string]any)["content"])

	h.publish(h.a, engine.EventTypeNodeStarted)
	f = readFrame(t, ctx, conn)
	assert.Equal(t, engine.EventTypeNodeStarted, f["type"], "the hidden descendant's history must not be hydrated")
	assert.Equal(t, h.a.String(), f["sourceInstanceId"])
}

// ---------------------------------------------------------------------------
// Terminal handling (edge cases)
// ---------------------------------------------------------------------------

func TestStreamSubtree_TerminalDescendantDoesNotClose(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	h.publish(h.a1, engine.EventTypeInstanceCompleted)
	h.publish(h.a, engine.EventTypeNodeStarted)

	assert.Equal(t, engine.EventTypeInstanceCompleted, readFrame(t, ctx, conn)["type"])
	f := readFrame(t, ctx, conn)
	assert.Equal(t, h.a.String(), f["sourceInstanceId"], "the stream survived a terminal descendant")
}

func TestStreamSubtree_TerminalRootWithActiveDescendantsKeepsStreaming(t *testing.T) {
	h := newSubtreeHarness(instance.StateCompleted)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	h.publish(h.b, engine.EventTypeNodeStarted)
	assert.Equal(t, h.b.String(), readFrame(t, ctx, conn)["sourceInstanceId"], "a completed root does not close while descendants are active")

	h.publish(h.root, engine.EventTypeInstanceCompleted)
	h.publish(h.b, engine.EventTypeNodeCompleted)
	assert.Equal(t, h.root.String(), readFrame(t, ctx, conn)["sourceInstanceId"])
	assert.Equal(t, h.b.String(), readFrame(t, ctx, conn)["sourceInstanceId"])
}

func TestStreamSubtree_ClosesOnlyWhenSubtreeInactive(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	h.active.Store(false)
	h.publish(h.a1, engine.EventTypeInstanceCompleted)

	assert.Equal(t, engine.EventTypeInstanceCompleted, readFrame(t, ctx, conn)["type"], "the terminal event itself is delivered first")
	f := readFrame(t, ctx, conn)
	assert.Equal(t, adapthttp.SubtreeInactiveEventType, f["type"])
	assert.Equal(t, h.root.String(), f["sourceInstanceId"])
	data := f["data"].(map[string]any)
	assert.Equal(t, h.root.String(), data["rootInstanceId"])
	assert.Equal(t, string(instance.StateWaiting), data["state"])
	_, _, err := conn.Read(ctx)
	require.Error(t, err)
	assert.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(err))
}

func TestStreamSubtree_InactiveAtConnectSendsFinalFrameAndCloses(t *testing.T) {
	h := newSubtreeHarness(instance.StateCompleted)
	h.active.Store(false)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	f := readFrame(t, ctx, conn)
	assert.Equal(t, adapthttp.SubtreeInactiveEventType, f["type"])
	assert.Equal(t, string(instance.StateCompleted), f["data"].(map[string]any)["state"])
	_, _, err := conn.Read(ctx)
	assert.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(err))
}

// A dropped final event (or a transition with no emitter) cannot leave the
// stream open forever: the bounded periodic re-check closes it.
func TestStreamSubtree_PeriodicRecheckClosesAfterSilentTransition(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, 50*time.Millisecond, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	h.active.Store(false) // no event published

	assert.Equal(t, adapthttp.SubtreeInactiveEventType, readFrame(t, ctx, conn)["type"])
}

// ---------------------------------------------------------------------------
// Authorization / wiring (error cases)
// ---------------------------------------------------------------------------

func TestStreamSubtree_UnauthorizedRootIs404BeforeUpgrade(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.denied[h.root] = true
	h.start(t, true, time.Minute, nil)

	_, resp, err := websocket.Dial(testCtx(t), h.url(h.root, "?subtree=1"), nil)

	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestStreamSubtree_NotWiredIs501(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, false, time.Minute, nil)

	_, resp, err := websocket.Dial(testCtx(t), h.url(h.root, "?subtree=1"), nil)

	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Interactive controls are routed by explicit, authorized target
// ---------------------------------------------------------------------------

func TestStreamSubtree_InteractiveControlRoutedByExplicitTargetNotLastEmitter(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	h.publish(h.a1, engine.EventTypeInteractiveToken) // a1 is the last emitter
	require.Equal(t, h.a1.String(), readFrame(t, ctx, conn)["sourceInstanceId"])

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.send","content":"hi","instanceId":"`+h.a.String()+`"}`)))
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.end","instanceId":"`+h.root.String()+`"}`)))

	require.Eventually(t, func() bool {
		h.convo.mu.Lock()
		defer h.convo.mu.Unlock()
		return len(h.convo.handleCalls) == 1 && len(h.convo.endCalls) == 1
	}, 2*time.Second, 20*time.Millisecond)
	h.convo.mu.Lock()
	defer h.convo.mu.Unlock()
	assert.Equal(t, h.a, h.convo.handleCalls[0].InstanceID)
	assert.Equal(t, "hi", h.convo.handleCalls[0].Content)
	assert.Equal(t, []uuid.UUID{h.root}, h.convo.endCalls)
}

func TestStreamSubtree_InteractiveControlWithoutTargetIsRejected(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.send","content":"hi"}`)))

	f := readFrame(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveError, f["type"])
	assert.Equal(t, "instanceId is required on a subtree stream", f["data"].(map[string]any)["message"])
	h.convo.mu.Lock()
	defer h.convo.mu.Unlock()
	assert.Empty(t, h.convo.handleCalls)
}

func TestStreamSubtree_InteractiveControlOutsideSubtreeOrUnviewableIsRejected(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.denied[h.a1] = true
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	for _, target := range []uuid.UUID{h.other1, h.a1, uuid.New()} {
		require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.send","content":"x","instanceId":"`+target.String()+`"}`)))
		f := readFrame(t, ctx, conn)
		assert.Equal(t, engine.EventTypeInteractiveError, f["type"])
		assert.Equal(t, "Session not found", f["data"].(map[string]any)["message"], "outside, unviewable and unknown targets are indistinguishable")
		assert.Equal(t, target.String(), f["sourceInstanceId"])
	}
	h.convo.mu.Lock()
	defer h.convo.mu.Unlock()
	assert.Empty(t, h.convo.handleCalls)
}

func TestStreamSubtree_UnknownInboundTypeIgnored(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"client.ping"}`)))
	h.publish(h.a, engine.EventTypeNodeStarted)

	f := readFrame(t, ctx, conn)
	assert.Equal(t, engine.EventTypeNodeStarted, f["type"], "no interactive.error for an unknown type")
}

// ---------------------------------------------------------------------------
// Regression: the per-instance mode is untouched by subtree wiring
// ---------------------------------------------------------------------------

func TestStreamHandler_PerInstanceModeUnchangedBySubtreeWiring(t *testing.T) {
	h := newSubtreeHarness(instance.StateRunning)
	h.start(t, true, 50*time.Millisecond, nil)
	ctx := testCtx(t)
	conn, _, err := websocket.Dial(ctx, h.url(h.root, ""), nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	h.publish(h.a1, engine.EventTypeNodeStarted) // descendants are NOT part of a per-instance stream
	h.publish(h.root, engine.EventTypeNodeStarted)

	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	var f map[string]any
	require.NoError(t, json.Unmarshal(raw, &f))
	assert.Equal(t, map[string]any{"marker": engine.EventTypeNodeStarted + "@" + h.root.String()}, f["data"])
	assert.NotContains(t, f, "sourceInstanceId", "per-instance frames keep their existing shape")

	h.publish(h.root, engine.EventTypeInstanceCompleted)
	_, _, err = conn.Read(ctx)
	require.NoError(t, err, "the terminal event itself is delivered")
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "a per-instance stream still closes on its own terminal event, regardless of subtree activity")
}

func TestStreamSubtree_AbandonErrorNamesExplicitTarget(t *testing.T) {
	h := newSubtreeHarness(instance.StateWaiting)
	h.convo.abandonSessionFn = func(_ context.Context, id uuid.UUID) error {
		if id != h.a {
			return fmt.Errorf("unexpected target %s", id)
		}
		return engine.ErrNotResolutionSession
	}
	h.start(t, true, time.Minute, nil)
	ctx := testCtx(t)
	conn := h.dial(t, ctx, h.root)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.abandon","instanceId":"`+h.a.String()+`"}`)))
	f := readFrame(t, ctx, conn)
	assert.Equal(t, engine.EventTypeInteractiveError, f["type"])
	assert.Equal(t, h.a.String(), f["sourceInstanceId"])
	assert.Equal(t, "Only a gather conflict-resolution session can be abandoned.", f["data"].(map[string]any)["message"])
	h.convo.mu.Lock()
	defer h.convo.mu.Unlock()
	assert.Equal(t, []uuid.UUID{h.a}, h.convo.abandonCalls)
}

func TestStreamSubtree_UnavailableActivityDoesNotProveInactivity(t *testing.T) {
	for _, unavailable := range []string{"storage error", "nil state", "empty state", "missing root context"} {
		t.Run(unavailable, func(t *testing.T) {
			h := newSubtreeHarness(instance.StateCompleted)
			sh := adapthttp.NewStreamHandler(h.repo, nil, h.bus, h.convo, h.getter())
			sh.SetSubtreeStreaming(engine.NewSubtreeEventBridge(h.bus, h.getter(), h.repo), activityFunc(func(_ context.Context, id uuid.UUID) (*engine.GraphState, error) {
				if id != h.root {
					return nil, fmt.Errorf("unexpected activity root %s", id)
				}
				if unavailable == "storage error" {
					return nil, fmt.Errorf("storage unavailable")
				}
				if unavailable == "empty state" {
					return &engine.GraphState{}, nil
				}
				if unavailable == "missing root context" {
					return &engine.GraphState{RootInstanceID: id, Contexts: []engine.ExecutionContext{}}, nil
				}
				return nil, nil
			}), time.Minute)
			r := chi.NewRouter()
			r.Get("/instances/{id}/ws", sh.HandleWebSocket)
			h.srv = httptest.NewServer(r)
			t.Cleanup(h.srv.Close)
			ctx := testCtx(t)
			conn := h.dial(t, ctx, h.root)
			h.publish(h.a, engine.EventTypeInstanceCompleted)
			h.publish(h.b, engine.EventTypeNodeStarted)
			assert.Equal(t, h.a.String(), readFrame(t, ctx, conn)["sourceInstanceId"])
			assert.Equal(t, h.b.String(), readFrame(t, ctx, conn)["sourceInstanceId"], "unknown activity must keep resync alive")
		})
	}
}

func TestRouter_SubtreeStreamingWiredFromDeps(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, _, cfg, qm, qr := newRouterTestDeps()
	bus := eventbus.New()
	root := testInstance(instance.StateRunning)
	instRepo.instances[root.ID] = root
	always := activityFunc(func(ctx context.Context, id uuid.UUID) (*engine.GraphState, error) {
		assert.Equal(t, root.ID, id, "activity must be checked for the subscribed root")
		inst, err := svc.GetInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		return &engine.GraphState{RootInstanceID: id, Active: true,
			Contexts: []engine.ExecutionContext{{InstanceID: id, State: string(inst.State)}}}, nil
	})
	deps := adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo,
		Snapshots: snapRepo, FileReader: fileReader, EventBus: bus, Config: cfg,
		QueueManager: qm, QueueRepo: qr,
		GraphState: always, SubtreeEvents: engine.NewSubtreeEventBridge(bus, svc, instRepo),
	}
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(deps))
	defer srv.Close()
	ctx := testCtx(t)

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/api/v1/instances/"+root.ID.String()+"/ws?subtree=1", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	bus.Publish(root.ID, engine.Event{Type: engine.EventTypeNodeStarted, Timestamp: time.Now().UTC()})
	assert.Equal(t, root.ID.String(), readFrame(t, ctx, conn)["sourceInstanceId"])

	deps.SubtreeEvents = nil
	unwired := httptest.NewServer(adapthttp.NewRouterFromDeps(deps))
	defer unwired.Close()
	_, resp, err := websocket.Dial(ctx, "ws"+unwired.URL[len("http"):]+"/api/v1/instances/"+root.ID.String()+"/ws?subtree=1", nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}
