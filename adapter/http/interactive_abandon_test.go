package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"nhooyr.io/websocket"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Tests: POST /instances/{id}/interactive/abandon
// ---------------------------------------------------------------------------

func abandonRequest(t *testing.T, convo *mockConversationHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	h := adapthttp.NewInteractiveHandler(convo, newMockInstanceRepo(), newMockNodeExecRepo())
	req := httptest.NewRequest(http.MethodPost, "/instances/"+id+"/interactive/abandon", nil)
	req = withChiURLParam(req, "id", id)
	w := httptest.NewRecorder()
	h.AbandonSession(w, req)
	return w
}

func TestInteractiveHandler_AbandonSession_Success(t *testing.T) {
	convo := &mockConversationHandler{}
	id := uuid.New()

	w := abandonRequest(t, convo, id.String())

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "abandoned", resp["status"])
	assert.Equal(t, id.String(), resp["instanceId"])
	require.Len(t, convo.abandonCalls, 1)
	assert.Equal(t, id, convo.abandonCalls[0])
}

func TestInteractiveHandler_AbandonSession_ErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"not found", fmt.Errorf("conversation: %w", instance.ErrNotFound), http.StatusNotFound, "NOT_FOUND"},
		{"forbidden", fmt.Errorf("conversation: %w", instance.ErrForbidden), http.StatusForbidden, "FORBIDDEN"},
		{"not a resolution session", fmt.Errorf("conversation: %w", engine.ErrNotResolutionSession), http.StatusConflict, "only available for gather conflict-resolution sessions"},
		{"no active session", fmt.Errorf("conversation: %w", instance.ErrNotInteractiveWait), http.StatusConflict, "no active interactive session"},
		{"turn in progress", fmt.Errorf("conversation: %w", instance.ErrInteractiveTurnInProgress), http.StatusConflict, "turn is in progress"},
		{"internal", fmt.Errorf("db: connection reset by peer"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err
			convo := &mockConversationHandler{abandonSessionFn: func(context.Context, uuid.UUID) error { return err }}
			w := abandonRequest(t, convo, uuid.New().String())
			assert.Equal(t, tc.wantCode, w.Code)
			assert.Contains(t, w.Body.String(), tc.wantBody)
		})
	}
}

func TestInteractiveHandler_AbandonSession_BadUUID(t *testing.T) {
	convo := &mockConversationHandler{}
	w := abandonRequest(t, convo, "bad-uuid")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, convo.abandonCalls)
}

func TestRouter_InteractiveAbandonRoute(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	convo := &mockConversationHandler{}
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, convo, nil)
	id := uuid.New()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+id.String()+"/interactive/abandon", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, convo.abandonCalls, 1)
	assert.Equal(t, id, convo.abandonCalls[0])
}

// ---------------------------------------------------------------------------
// Tests: interactive.abandon routes to ConversationHandler.AbandonSession
// ---------------------------------------------------------------------------

func TestStreamHandler_InteractiveAbandon_RoutesToAbandonSession(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{}
	inst := testInstance(instance.StateWaiting)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/instances/"+inst.ID.String()+"/ws", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.abandon"}`)))
	require.Eventually(t, func() bool {
		convo.mu.Lock()
		defer convo.mu.Unlock()
		return len(convo.abandonCalls) == 1
	}, 2*time.Second, 20*time.Millisecond)
	convo.mu.Lock()
	defer convo.mu.Unlock()
	assert.Equal(t, inst.ID, convo.abandonCalls[0])
	assert.Empty(t, convo.endCalls, "abandon is never mistaken for end")
}

func TestStreamHandler_InteractiveAbandon_NotAResolutionSessionReturnsInteractiveError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()
	convo := &mockConversationHandler{abandonSessionFn: func(context.Context, uuid.UUID) error {
		return fmt.Errorf("conversation: %w", engine.ErrNotResolutionSession)
	}}
	inst := testInstance(instance.StateWaiting)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServerWithConvo(instRepo, bus, convo)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/instances/"+inst.ID.String()+"/ws", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"interactive.abandon"}`)))
	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(raw, &evt))
	assert.Equal(t, "interactive.error", evt["type"])
	assert.Equal(t, "Only a gather conflict-resolution session can be abandoned.", evt["data"].(map[string]any)["message"])
}

// ---------------------------------------------------------------------------
// Tests: history hydration for a parallel_group conflict-resolution session
// ---------------------------------------------------------------------------

func groupWaitingInstance(t *testing.T) *instance.Instance {
	t.Helper()
	nodeID := "g1"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{map[string]any{"id": "g1", "type": "parallel_group", "config": map[string]any{
			"group": map[string]any{"form": "branches", "tasks": []any{map[string]any{"key": "a", "entry": "a1"}},
				"completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining",
				"gatherMode": "results_and_files", "resolverProfile": "default-agent"}}}},
	}
	return inst
}

func TestStreamHandler_HistoryHydration_GroupResolutionSessionIsTagged(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()
	inst := groupWaitingInstance(t)
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: "g1", Attempt: 2, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{
			"kind": "group_resolution", "groupRunId": uuid.NewString(), "mergeIndex": float64(0), "attempt": float64(1),
			"messages": []any{map[string]any{"role": "assistant", "content": "I merged shared.txt"}},
		},
		StartedAt: time.Now(),
	}}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, nil)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/instances/"+inst.ID.String()+"/ws", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(raw, &evt))
	assert.Equal(t, "interactive.history", evt["type"])
	data := evt["data"].(map[string]any)
	assert.Equal(t, "g1", data["nodeId"])
	assert.Equal(t, "group_resolution", data["sessionKind"])
	assert.Len(t, data["messages"], 1)
}

func TestStreamHandler_HistoryHydration_EndedGroupSessionIsNotHydrated(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()
	inst := groupWaitingInstance(t)
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: "g1", Attempt: 2, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{
			"kind": "group_resolution", "groupRunId": uuid.NewString(), "mergeIndex": float64(0), "attempt": float64(1),
			"endedAt": time.Now().UTC().Format(time.RFC3339Nano), "messages": []any{},
		},
		StartedAt: time.Now(),
	}}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, nil)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/instances/"+inst.ID.String()+"/ws", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{Type: "node_started", Timestamp: time.Now(), Data: map[string]any{"nodeId": "X"}})
	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(raw, &evt))
	assert.Equal(t, "node_started", evt["type"], "an ended resolution session sends no history")
}

func TestStreamHandler_HistoryHydration_GroupWaitTraceIsNotASession(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	bus := eventbus.New()
	inst := groupWaitingInstance(t)
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: "g1", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"groupRunId": uuid.NewString(), "state": "waiting"}, StartedAt: time.Now(),
	}}

	srv := newStreamTestServerFull(instRepo, nodeExecRepo, bus, nil)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/instances/"+inst.ID.String()+"/ws", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{Type: "node_started", Timestamp: time.Now(), Data: map[string]any{"nodeId": "X"}})
	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(raw, &evt))
	assert.Equal(t, "node_started", evt["type"])
}
