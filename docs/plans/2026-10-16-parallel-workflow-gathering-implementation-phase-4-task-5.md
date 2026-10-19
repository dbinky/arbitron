# Parallel Workflow Gathering — Phase 4 Task 5: Subtree WebSocket mode, subtree-aware terminal handling, wiring

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the run and queue live views one WebSocket per selected run that carries the events of the run AND its authorized descendants (each frame naming its source), stays open while any visible part of the subtree is active — even after the selected run itself finished — and routes interactive controls to the explicitly named, authorized session instead of whichever child last emitted.
**Architecture:** `StreamHandler.HandleWebSocket` gains a `?subtree=1` branch (after the unchanged connect-time authorization) implemented in a new file `adapter/http/stream_subtree.go`: it subscribes through `SubtreeSubscriber` (production: `*engine.SubtreeEventBridge`, P4-T4) BEFORE upgrading, writes frames `{type,timestamp,data,sourceInstanceId}`, and decides termination only from `GraphStateService.GraphState(...).Active` (P4-T1/T3) on every terminal event and on a bounded periodic re-check, then sends a final `subtree.inactive` frame and closes normally so the client refetches. The per-instance mode is untouched except for two behaviour-preserving extractions (`routeInteractive`/`writeInteractiveError`, `interactiveHistoryEvent`). `server.go` and `cmd/arbitron/main.go` wire the bridge.
**Tech Stack:** Go 1.25.5, `nhooyr.io/websocket`, chi v5, testify, `net/http/httptest`, `adapter/eventbus`.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("The queue's live run view reflects every concurrently active node across all fanned-out paths"; "Tasks continuing independently after a first-success decision remain visible"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §3 ("On-wire events identify the source instance"; "Route interactive controls by the selected session's owner/identity, never by whichever child last emitted an event"; "Extend `StreamHandler.HandleWebSocket` terminal handling: a terminal descendant must not close its parent's stream; a terminal parent with active descendants still needs a stream. Close only once the subscribed subtree is inactive, then refetch final state"; "Add bounded periodic refetch while the subtree is active") and Data Provenance row "Ancestor routing, authorized subtree subscription, subtree-aware terminal handling" (PLANNED DEPENDENCY P4-B — the stream half is resolved here).

## Additional live-stream sink proofs

Extend the real-router composition test with (1) a public child becoming private then
public during ONE WebSocket subscription, asserting no private token reaches the socket
and later public tokens resume; and (2) a terminal root with more than 1,000 children,
only the last one active, asserting no `subtree.inactive` frame before that task finishes.
Use bounded event/gate synchronization. These tests exercise `routerDeps()` and the actual
bridge/read service together; separately mocked handler and bridge tests are insufficient.


**Interfaces**
- Consumes (existing code):
  - `adapter/http/stream_handler.go:21-25` `inboundMessage`; `:29-35` `interactiveErrorEvent`; `:42-69` `streamInstanceGetter`, `StreamHandler`, `NewStreamHandler`; `:71-104` `isTerminalState`, `isTerminalEvent`; `:106-208` `HandleWebSocket` (authorize via `service.GetInstance` before `websocket.Accept`, terminal close at `:144-154` and `:196-200`); `:210-281` `handleInteractiveInbound`; `:283-332` `sendInteractiveHistory`.
  - `adapter/http/system_stream_handler.go:17-19` `wsWriteTimeout`; `:132-148` `writeWSValue`, `writeWSEvent`.
  - `adapter/http/server.go:54-133` `RouterDeps`; `:219` `streamHandler := NewStreamHandler(...)`; `:344` `r.Get("/{id}/ws", streamHandler.HandleWebSocket)`.
  - `domain/engine/conversation.go:29-35` `ConversationHandler{HandleMessage, EndSession}` (+ P3-T9 `AbandonSession(ctx, instanceID) error` and WS type `interactive.abandon`).
  - Test idioms: `adapter/http/stream_handler_test.go:31-64` `mockConversationHandler`, `:77-81` `streamGetterFunc`, `:83-101` `newStreamTestServerFull`; `adapter/http/instance_handler_test.go:89-130` `mockInstanceRepo`, `:246-300` `mockNodeExecRepo`, `:497-510` `testInstance`; `adapter/http/server_test.go:218-268` `newRouterTestDeps`.
- Consumes (earlier tasks): P4-T3 `adapter/http.GraphStateService`, `RouterDeps.GraphState`, `(*domainStack).routerDeps()`, `domainStack.graphState`, `cmd/arbitron/graph_state_e2e_test.go` helpers `bootGraphStateStack`, `gsServeAPI`, fixture workflow `gs-fanout`; P4-T4 `engine.NewSubtreeEventBridge`, `(*SubtreeEventBridge).SubscribeSubtree`, `(*SubtreeEventBridge).InSubtree`; P4-T1 `engine.GraphState{Active, Contexts}`.
- Produces (exact):
  - `adapter/http`: `const SubtreeInactiveEventType = "subtree.inactive"`; `type SubtreeSubscriber interface { SubscribeSubtree(ctx context.Context, rootID uuid.UUID) (<-chan engine.ScopedEvent, func(), error); InSubtree(ctx context.Context, rootID, id uuid.UUID) bool }`; `func (h *StreamHandler) SetSubtreeStreaming(sub SubtreeSubscriber, activity GraphStateService, recheck time.Duration)` (`recheck <= 0` ⇒ 15s); `RouterDeps.SubtreeEvents SubtreeSubscriber`.
  - Wire protocol (subtree mode, `GET /api/v1/instances/{id}/ws?subtree=1`): every server frame is `{"type","timestamp","data","sourceInstanceId"}`; the final frame is `{"type":"subtree.inactive","sourceInstanceId":<root>,"data":{"rootInstanceId":<root>,"state":<root state>}}` followed by a normal close (1000, "subtree inactive"); inbound controls `{"type":"interactive.send"|"interactive.end"|"interactive.abandon","content":...,"instanceId":"<target>"}` — `instanceId` is REQUIRED; the target must be the root or in its subtree and viewable; errors are `interactive.error` frames carrying `sourceInstanceId` = the named target. Not wired ⇒ HTTP 501 `NOT_IMPLEMENTED` before upgrade; unauthorized root ⇒ HTTP 404 (unchanged authorization).
  - Per-instance mode (no `subtree` param): byte-for-byte unchanged frames (no `sourceInstanceId`), unchanged terminal close, unchanged inbound routing (an `instanceId` field is ignored).
  - `cmd/arbitron`: `domainStack.subtreeEvents *engine.SubtreeEventBridge`; `routerDeps()` sets `SubtreeEvents`.

**Wiring & data provenance**
- Composition root: Task 5c of THIS task — `cmd/arbitron/main.go:initDomain` builds `engine.NewSubtreeEventBridge(bus, eng, instanceRepo)`; `routerDeps()` (P4-T3) passes it as `RouterDeps.SubtreeEvents`; `NewRouterFromDeps` calls `streamHandler.SetSubtreeStreaming(d.SubtreeEvents, d.GraphState, 0)` when both are set.
- Producers:
  - `SubtreeSubscriber` ← `domainStack.subtreeEvents` ← `engine.NewSubtreeEventBridge` over `bus` (`main.go:618` `bus := eventbus.New()`), `eng` (`main.go:800`), `instanceRepo` (`main.go:373`).
  - Activity ← `RouterDeps.GraphState` ← `domainStack.graphState` (P4-T3) — the same authorized `Active` the HTTP read returns, so the stream and the refetch agree.
  - Root authorization ← `streamInstanceGetter` = `RouterDeps.Service` = `eng` (unchanged).
  - Interactive dispatch target ← the inbound frame's `instanceId`, gated by `SubtreeSubscriber.InSubtree` + `streamInstanceGetter.GetInstance`, then `ConversationHandler` (which re-authorizes, `conversation.go:149-157`).
- Provenance probes: `TestSubtreeStreamE2E_ParentStreamSeesBranchesAndClosesOnlyWhenSubtreeInactive` (fails at Dial with 501 if the bridge is not constructed, not in `routerDeps`, or not passed to the stream handler); `TestRouter_SubtreeStreamingWiredFromDeps`; `TestStreamSubtree_NotWiredIs501`.

### Task 5a: Behaviour-preserving extractions in the per-instance stream
**Files:**
- Modify: `adapter/http/stream_handler.go:21-35` (message structs), `:210-332` (`handleInteractiveInbound`, `sendInteractiveHistory`)
- Test:   existing `adapter/http/stream_handler_test.go` (regression, unchanged) + new `adapter/http/stream_instance_id_regression_test.go` (Step 1)

- [x] Step 1: Write the regression test FIRST (it must pass before and after the refactor). Create `adapter/http/stream_instance_id_regression_test.go`:

```go
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
```

- [x] Step 2: Run the regression set, confirm it is green BEFORE refactoring.

```bash
go test ./adapter/http/ -run 'TestStreamHandler_' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/http	X.XXXs
```

- [x] Step 3: Refactor. In `adapter/http/stream_handler.go`:

(a) Replace lines 21-35 (`inboundMessage` and `interactiveErrorEvent`) with:

```go
// inboundMessage represents a JSON message received from the WebSocket client.
// InstanceID names the target session on a SUBTREE stream (required there);
// the per-instance stream ignores it and always targets its URL instance.
type inboundMessage struct {
	Type       string `json:"type"`
	Content    string `json:"content,omitempty"`
	InstanceID string `json:"instanceId,omitempty"`
}

// interactiveErrorEvent is sent back to the client when an interactive
// operation fails. SourceInstanceID is set only on subtree streams (nil keeps
// the per-instance wire shape unchanged).
type interactiveErrorEvent struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      struct {
		Message string `json:"message"`
	} `json:"data"`
	SourceInstanceID *uuid.UUID `json:"sourceInstanceId,omitempty"`
}
```

(b) Replace `handleInteractiveInbound`, `sendInteractiveHistory` and P3-T9's `isGroupNode` (lines 210-332 of the pre-Phase-3 file, i.e. from the comment `// handleInteractiveInbound parses an inbound JSON message` to the end of the file as P3-T9 Step 16 left it) with the code below. The code already carries every P3-T9 behaviour: the `case "interactive.abandon"` dispatch, the `engine.ErrNotResolutionSession` client message, the group-resolution history hydration tagged `"sessionKind": "group_resolution"`, and `isGroupNode` (P3-T9 also added the `domain/workflow` import it needs). Keep every case that exists at implementation time.

```go
// handleInteractiveInbound parses an inbound JSON message and routes it to the
// stream's own instance. Unknown message types are silently ignored for
// forward compatibility.
func (h *StreamHandler) handleInteractiveInbound(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, instanceID uuid.UUID, data []byte) {
	var msg inboundMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Warn("stream: ignoring malformed inbound message", "error", err)
		return
	}
	h.routeInteractive(ctx, conn, writeMu, instanceID, msg, nil)
}

// routeInteractive dispatches an interactive control to the conversation
// handler for target. source, when non-nil, stamps sourceInstanceId on an error
// frame (subtree streams); the per-instance stream passes nil.
func (h *StreamHandler) routeInteractive(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, target uuid.UUID, msg inboundMessage, source *uuid.UUID) {
	if h.conversationHandler == nil {
		// No conversation handler wired — ignore interactive messages.
		return
	}

	var handlerErr error
	switch msg.Type {
	case "interactive.send":
		handlerErr = h.conversationHandler.HandleMessage(ctx, target, msg.Content)
	case "interactive.end":
		_, handlerErr = h.conversationHandler.EndSession(ctx, target)
	case "interactive.abandon":
		handlerErr = h.conversationHandler.AbandonSession(ctx, target)
	default:
		// Unknown type — silently ignore for forward compatibility.
		return
	}
	if handlerErr == nil {
		return
	}

	// Map domain errors to generic client-safe messages. Internal details
	// (DB errors, state machine internals) are logged server-side only.
	var clientMsg string
	switch {
	case errors.Is(handlerErr, instance.ErrNotFound):
		clientMsg = "Session not found"
	case errors.Is(handlerErr, instance.ErrNotInteractiveWait):
		clientMsg = "No active interactive session"
	case errors.Is(handlerErr, engine.ErrNotResolutionSession):
		clientMsg = "Only a gather conflict-resolution session can be abandoned."
	case errors.Is(handlerErr, instance.ErrInteractiveTurnInProgress):
		clientMsg = "A turn is already in progress; wait for the current response before sending again."
		// Not a server fault — a benign double-send/retry. Log at Warn (never
		// slog.Error, so it doesn't page on-call) with instanceID + caller for
		// abuse/retry-storm visibility.
		caller := "unknown"
		if id, ok := identity.FromContext(ctx); ok {
			caller = id.UserID.String()
		}
		slog.Warn("interactive turn-in-progress rejected", "instanceID", target, "type", msg.Type, "caller", caller)
	default:
		clientMsg = "An error occurred processing your request"
		slog.Error("interactive handler error", "instanceID", target, "type", msg.Type, "error", handlerErr)
	}
	h.writeInteractiveError(ctx, conn, writeMu, clientMsg, source)
}

// writeInteractiveError sends an interactive.error frame under writeMu.
func (h *StreamHandler) writeInteractiveError(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, clientMsg string, source *uuid.UUID) {
	errEvt := interactiveErrorEvent{
		Type:             engine.EventTypeInteractiveError,
		Timestamp:        time.Now().UTC(),
		SourceInstanceID: source,
	}
	errEvt.Data.Message = clientMsg

	errData, err := json.Marshal(errEvt)
	if err != nil {
		slog.Error("stream: failed to marshal interactive.error", "error", err)
		return
	}

	writeMu.Lock()
	writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	writeErr := conn.Write(writeCtx, websocket.MessageText, errData)
	cancel()
	writeMu.Unlock()

	if writeErr != nil {
		slog.Error("stream: failed to write interactive.error", "error", writeErr)
	}
}

// sendInteractiveHistory checks if the instance is in interactive wait state
// and, if so, sends an interactive.history event with the full conversation.
func (h *StreamHandler) sendInteractiveHistory(ctx context.Context, conn *websocket.Conn, inst *instance.Instance) {
	historyEvt, ok := h.interactiveHistoryEvent(ctx, inst)
	if !ok {
		return
	}
	if err := writeWSEvent(ctx, conn, historyEvt); err != nil {
		slog.Warn("stream: failed to send interactive.history", "instanceId", inst.ID, "error", err)
	}
}

// interactiveHistoryEvent builds the interactive.history event for an instance
// in interactive wait, or reports false when there is none to send. A
// parallel_group waiting on an OPEN conflict-resolution session (P3-T8/P3-T9)
// hydrates the same way, tagged "sessionKind": "group_resolution"; the group's
// wait trace and ended sessions do not.
func (h *StreamHandler) interactiveHistoryEvent(ctx context.Context, inst *instance.Instance) (engine.Event, bool) {
	if inst.State != instance.StateWaiting {
		return engine.Event{}, false
	}
	if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
		return engine.Event{}, false
	}
	if h.nodeExecs == nil {
		return engine.Event{}, false
	}

	nodeID := *inst.CurrentNodeID

	// Check if the current node is interactive (or a group that may host a
	// resolution session) by parsing the workflow snapshot.
	interactive := isInteractiveNode(inst.WorkflowSnapshot, nodeID)
	if !interactive && !isGroupNode(inst.WorkflowSnapshot, nodeID) {
		return engine.Event{}, false
	}

	// Load the node execution record (the newest one: a resolution session is
	// numbered past the group's wait trace).
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, inst.ID, nodeID)
	if err != nil {
		if interactive {
			slog.Warn("stream: failed to load node execution for history hydration", "instanceId", inst.ID, "nodeId", nodeID, "error", err)
		}
		return engine.Event{}, false
	}

	sessionKind := ""
	if !interactive {
		if !engine.IsOpenGroupResolutionSession(nodeExec.LLMConversation) {
			return engine.Event{}, false
		}
		sessionKind = engine.ConversationKindGroupResolution
	}

	// Extract messages from llm_conversation.
	var messages any
	if nodeExec.LLMConversation != nil {
		if msgs, ok := nodeExec.LLMConversation["messages"]; ok {
			messages = msgs
		}
	}
	if messages == nil {
		messages = []any{}
	}

	data := map[string]any{
		"nodeId":   nodeID,
		"messages": messages,
	}
	if sessionKind != "" {
		data["sessionKind"] = sessionKind
	}
	return engine.Event{
		Type:      engine.EventTypeInteractiveHistory,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}, true
}

// isGroupNode reports whether nodeID is a parallel_group node in the snapshot
// (P3-T9, carried over unchanged).
func isGroupNode(snapshot map[string]any, nodeID string) bool {
	node, err := engine.FindNodeByID(snapshot, nodeID)
	if err != nil {
		return false
	}
	return node.Type == workflow.NodeTypeParallelGroup
}
```

- [x] Step 4: Run the regression set again, confirm it is still green. It includes P3-T9's `TestStreamHandler_InteractiveAbandon_*` and `TestStreamHandler_HistoryHydration_*` tests, which fail if this extraction drops the abandon dispatch, the `ErrNotResolutionSession` message or the `sessionKind` tag.

```bash
go test ./adapter/http/ -run 'TestStreamHandler_|TestInteractive' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/http	X.XXXs
```

- [x] Step 5: Commit.

Reviewed in order: (1) extraction only, preserving abandon, error messages and history tags; (2) the socket regression asserts the conversation sink receives the URL instance despite a conflicting inbound target; (3) diff and gofmt match neighboring stream conventions. Regression set passed before and after extraction.

```bash
git add adapter/http/stream_handler.go adapter/http/stream_instance_id_regression_test.go
git commit -m "refactor(stream): extract interactive routing and history building (no behaviour change)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5b: Subtree mode
**Files:**
- Create: `adapter/http/stream_subtree.go`
- Modify: `adapter/http/stream_handler.go:49-56` (struct fields), `:121-129` (branch after authorization)
- Modify: `adapter/http/server.go:54-133` (`RouterDeps.SubtreeEvents`), `:219` (wire)
- Test:   `adapter/http/stream_subtree_test.go` (new)

- [x] Step 1: Write the failing subtree tests. Create `adapter/http/stream_subtree_test.go`:

```go
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

func TestRouter_SubtreeStreamingWiredFromDeps(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, _, cfg, qm, qr := newRouterTestDeps()
	bus := eventbus.New()
	root := testInstance(instance.StateRunning)
	instRepo.instances[root.ID] = root
	always := activityFunc(func(context.Context, uuid.UUID) (*engine.GraphState, error) {
		return &engine.GraphState{Active: true}, nil
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
```

- [x] Step 2: Run them, confirm they fail.

Confirmed compile failure: missing SetSubtreeStreaming, SubtreeInactiveEventType and RouterDeps.SubtreeEvents.

```bash
go test ./adapter/http/ -run 'TestStreamSubtree|TestStreamHandler_PerInstanceModeUnchangedBySubtreeWiring|TestRouter_SubtreeStreamingWiredFromDeps' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/adapter/http_test [github.com/arbitron/arbitron/adapter/http.test]
adapter/http/stream_subtree_test.go:131:6: sh.SetSubtreeStreaming undefined (type *http.StreamHandler has no field or method SetSubtreeStreaming)
adapter/http/stream_subtree_test.go:290:35: undefined: adapthttp.SubtreeInactiveEventType
adapter/http/stream_subtree_test.go:441:42: unknown field SubtreeEvents in struct literal of type http.RouterDeps
...
FAIL	github.com/arbitron/arbitron/adapter/http [build failed]
```

- [x] Step 3: Add the handler fields and the branch. In `adapter/http/stream_handler.go`:

(a) In the `StreamHandler` struct (lines 50-56) add after `service streamInstanceGetter`:

```go
	// Subtree mode (?subtree=1). Nil until SetSubtreeStreaming; requests for
	// subtree mode are refused with 501 while unwired.
	subtree        SubtreeSubscriber
	activity       GraphStateService
	subtreeRecheck time.Duration
```

(b) In `HandleWebSocket`, immediately after the authorization block (after line 129, the closing `}` of `if err != nil { ... }` following `h.service.GetInstance`) insert:

```go

	// Subtree mode: the selected run AND its authorized descendants, closed by
	// subtree activity rather than by this instance's own terminal state.
	if r.URL.Query().Get("subtree") == "1" {
		h.handleSubtree(w, r, inst)
		return
	}
```

- [x] Step 4: Create `adapter/http/stream_subtree.go`:

```go
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// SubtreeInactiveEventType is the final frame of a subtree stream: the whole
// visible subtree is inactive, the server closes normally, and the client
// refetches the final aggregate state.
const SubtreeInactiveEventType = "subtree.inactive"

// defaultSubtreeRecheck bounds how long a dropped final event (the bus drops on
// a full buffer) or a transition with no emitter can keep a stream open.
const defaultSubtreeRecheck = 15 * time.Second

// SubtreeSubscriber is the authorized, ancestry-routed event source behind
// ?subtree=1 (engine.SubtreeEventBridge).
type SubtreeSubscriber interface {
	SubscribeSubtree(ctx context.Context, rootID uuid.UUID) (<-chan engine.ScopedEvent, func(), error)
	InSubtree(ctx context.Context, rootID, id uuid.UUID) bool
}

// subtreeWSFrame is the subtree-mode wire frame: the engine.Event fields plus
// the instance that emitted it.
type subtreeWSFrame struct {
	Type             string    `json:"type"`
	Timestamp        time.Time `json:"timestamp"`
	Data             any       `json:"data"`
	SourceInstanceID uuid.UUID `json:"sourceInstanceId"`
}

func subtreeFrame(source uuid.UUID, evt engine.Event) subtreeWSFrame {
	return subtreeWSFrame{Type: evt.Type, Timestamp: evt.Timestamp, Data: evt.Data, SourceInstanceID: source}
}

// SetSubtreeStreaming wires subtree mode. activity supplies GraphState.Active
// for the subscribed root (the same authorized activity the HTTP read
// returns). recheck <= 0 uses defaultSubtreeRecheck.
func (h *StreamHandler) SetSubtreeStreaming(sub SubtreeSubscriber, activity GraphStateService, recheck time.Duration) {
	if recheck <= 0 {
		recheck = defaultSubtreeRecheck
	}
	h.subtree = sub
	h.activity = activity
	h.subtreeRecheck = recheck
}

// handleSubtree serves ?subtree=1 for an already-authorized root.
func (h *StreamHandler) handleSubtree(w http.ResponseWriter, r *http.Request, root *instance.Instance) {
	if h.subtree == nil || h.activity == nil {
		WriteError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "subtree streaming is not available")
		return
	}
	ctx := r.Context()

	// Subscribe BEFORE the first activity check and before upgrading, so no
	// transition between the check and the subscription can be missed.
	ch, unsub, err := h.subtree.SubscribeSubtree(ctx, root.ID)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to subscribe")
		return
	}
	defer unsub()

	// Decide connect-time activity BEFORE upgrading: once the client sees the
	// handshake it may act (and the subtree may change), and every change after
	// this point reaches the already-open subscription.
	activeAtConnect, rootState := h.subtreeActivity(ctx, root.ID)

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept writes its own HTTP error response on failure.
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")
	conn.SetReadLimit(wsMaxReadMessageSize)

	// writeMu coordinates writes between the outbound loop and the read
	// goroutine (which may send interactive.error frames).
	var writeMu sync.Mutex
	write := func(f subtreeWSFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeWSValue(ctx, conn, f)
	}

	if !activeAtConnect {
		h.closeSubtreeInactive(conn, write, root.ID, rootState)
		return
	}

	if evt, ok := h.interactiveHistoryEvent(ctx, root); ok {
		if err := write(subtreeFrame(root.ID, evt)); err != nil {
			return
		}
	}

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			h.handleSubtreeInbound(ctx, conn, &writeMu, root.ID, data)
		}
	}()

	ticker := time.NewTicker(h.subtreeRecheck)
	defer ticker.Stop()
	for {
		select {
		case sev, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "event stream ended")
				return
			}
			if err := write(subtreeFrame(sev.InstanceID, sev.Event)); err != nil {
				return
			}
			if !isTerminalEvent(sev.Event) {
				continue
			}
			// A terminal descendant — or a terminal root with active
			// descendants — never ends the stream on its own.
			if active, state := h.subtreeActivity(ctx, root.ID); !active {
				h.closeSubtreeInactive(conn, write, root.ID, state)
				return
			}
		case <-ticker.C:
			if active, state := h.subtreeActivity(ctx, root.ID); !active {
				h.closeSubtreeInactive(conn, write, root.ID, state)
				return
			}
		case <-clientDone:
			return
		case <-ctx.Done():
			conn.Close(websocket.StatusGoingAway, "server shutting down")
			return
		}
	}
}

// subtreeActivity reports whether any visible context of rootID's subtree is
// still active, plus the root's state for the final frame. A root that became
// unviewable or vanished is inactive; any other read failure keeps the stream
// open (it carries only authorized events) until the next check.
func (h *StreamHandler) subtreeActivity(ctx context.Context, rootID uuid.UUID) (bool, string) {
	gs, err := h.activity.GraphState(ctx, rootID)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			return false, ""
		}
		slog.Warn("stream: subtree activity check failed; keeping the stream open", "instanceId", rootID, "error", err)
		return true, ""
	}
	if gs == nil {
		return false, ""
	}
	state := ""
	if len(gs.Contexts) > 0 {
		state = gs.Contexts[0].State
	}
	return gs.Active, state
}

func (h *StreamHandler) closeSubtreeInactive(conn *websocket.Conn, write func(subtreeWSFrame) error, rootID uuid.UUID, state string) {
	frame := subtreeWSFrame{
		Type:             SubtreeInactiveEventType,
		Timestamp:        time.Now().UTC(),
		SourceInstanceID: rootID,
		Data:             map[string]any{"rootInstanceId": rootID.String(), "state": state},
	}
	if err := write(frame); err != nil {
		return
	}
	conn.Close(websocket.StatusNormalClosure, "subtree inactive")
}

// handleSubtreeInbound routes an interactive control by its EXPLICIT target
// instance — never by whichever descendant emitted last. The target must be
// the root or inside its subtree AND viewable by the caller; the conversation
// handler then applies its own intervention checks.
func (h *StreamHandler) handleSubtreeInbound(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, rootID uuid.UUID, data []byte) {
	var msg inboundMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Warn("stream: ignoring malformed inbound message", "error", err)
		return
	}
	if h.conversationHandler == nil {
		return
	}
	switch msg.Type {
	case "interactive.send", "interactive.end", "interactive.abandon":
	default:
		// Unknown type — silently ignore for forward compatibility.
		return
	}
	target, err := uuid.Parse(msg.InstanceID)
	if err != nil {
		h.writeInteractiveError(ctx, conn, writeMu, "instanceId is required on a subtree stream", &rootID)
		return
	}
	if !h.subtreeTargetAllowed(ctx, rootID, target) {
		// Outside the subtree, unviewable and unknown all read the same.
		h.writeInteractiveError(ctx, conn, writeMu, "Session not found", &target)
		return
	}
	h.routeInteractive(ctx, conn, writeMu, target, msg, &target)
}

func (h *StreamHandler) subtreeTargetAllowed(ctx context.Context, rootID, target uuid.UUID) bool {
	if target != rootID && !h.subtree.InSubtree(ctx, rootID, target) {
		return false
	}
	_, err := h.service.GetInstance(ctx, target)
	return err == nil
}
```

- [x] Step 5: Wire it in the router. In `adapter/http/server.go`:

(a) In `RouterDeps`, after the `GraphState GraphStateService` field (added by P4-T3) add:

```go
	// SubtreeEvents is the authorized, ancestry-routed event source behind
	// GET /instances/{id}/ws?subtree=1 (engine.SubtreeEventBridge). Subtree mode
	// is enabled only when BOTH SubtreeEvents and GraphState are set; otherwise
	// it answers 501 and the per-instance mode is unaffected.
	SubtreeEvents SubtreeSubscriber
```

(b) Immediately after `streamHandler := NewStreamHandler(d.Instances, d.NodeExecs, d.EventBus, d.ConversationHandler, d.Service)` (line 219) add:

```go
	if d.SubtreeEvents != nil && d.GraphState != nil {
		streamHandler.SetSubtreeStreaming(d.SubtreeEvents, d.GraphState, 0)
	}
```

- [x] Step 6: Run, confirm PASS; then the whole package (all existing stream tests must stay green), race detector on the stream tests, gofmt, vet.

```bash
go test ./adapter/http/ -run 'TestStreamSubtree|TestStreamHandler_PerInstanceModeUnchangedBySubtreeWiring|TestRouter_SubtreeStreamingWiredFromDeps' -count=1
go test -race ./adapter/http/ -run 'TestStream' -count=1
go test ./adapter/http/ -count=1
gofmt -w adapter/http/stream_handler.go adapter/http/stream_subtree.go adapter/http/server.go adapter/http/stream_subtree_test.go
gofmt -l adapter/http/stream_handler.go adapter/http/stream_subtree.go adapter/http/server.go adapter/http/stream_subtree_test.go
go vet ./adapter/http/
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/http	X.XXXs
ok  	github.com/arbitron/arbitron/adapter/http	X.XXXs
ok  	github.com/arbitron/arbitron/adapter/http	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 7: Commit.

Review in order: (1) checked protocol, explicit authorized controls, periodic/terminal activity and per-instance compatibility; added subtree abandon/error coverage. Alignment requires unknown activity to keep resync alive: a nil GraphState regression failed (premature inactive), then passed after treating it as unknown. (2) bridge tests drive a real bus through sockets and assert frames and conversation targets; router test detects absent dependencies. (3) followed existing write timeouts/mutex and authorization patterns; gofmt/vet/diff clean. Focused, race stream and whole HTTP package gates pass after review.

```bash
git add adapter/http/stream_handler.go adapter/http/stream_subtree.go adapter/http/server.go adapter/http/stream_subtree_test.go
git commit -m "feat(stream): subtree WebSocket mode with source ids, subtree-aware close and explicit interactive targets" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5c: Composition-root wiring
**Files:**
- Modify: `cmd/arbitron/main.go` — `domainStack` field (after the `graphState` field added by P4-T3), construction next to `graphState := engine.NewGraphStateReader(...)` (P4-T3, just before `return &domainStack{` at line 1192), the literal, and `(*domainStack).routerDeps()` (P4-T3)
- Test:   `cmd/arbitron/subtree_stream_e2e_test.go` (package `main`)

- [x] Step 1: Write the failing composition-root test. Create `cmd/arbitron/subtree_stream_e2e_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// PROVENANCE PROBE for the subtree wiring. If initDomain does not build the
// bridge, routerDeps does not pass it, or NewRouterFromDeps does not hand it to
// the stream handler, the upgrade is refused with 501 and Dial fails. The sink
// is the WebSocket frame sequence a live view consumes.
func TestSubtreeStreamE2E_ParentStreamSeesBranchesAndClosesOnlyWhenSubtreeInactive(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "gs-fanout", map[string]any{}, "normal")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/api/v1/instances/"+parentID.String()+"/ws?subtree=1", nil)
	code := 0
	if resp != nil {
		code = resp.StatusCode
	}
	require.NoError(t, err, "subtree streaming must be wired through the composition root (HTTP %d)", code)
	defer conn.Close(websocket.StatusNormalClosure, "")

	type frame struct {
		Type   string    `json:"type"`
		Source uuid.UUID `json:"sourceInstanceId"`
	}
	var frames []frame
	for {
		_, msg, rerr := conn.Read(ctx)
		if rerr != nil {
			require.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(rerr), "the stream must end with a normal close, got: %v", rerr)
			break
		}
		var f frame
		require.NoError(t, json.Unmarshal(msg, &f))
		frames = append(frames, f)
	}

	require.NotEmpty(t, frames)
	assert.Equal(t, adapthttp.SubtreeInactiveEventType, frames[len(frames)-1].Type, "the stream closes only after the subtree-inactive frame")

	childSources := map[uuid.UUID]bool{}
	firstChildTerminal, rootTerminal := -1, -1
	for i, f := range frames {
		if f.Source == parentID {
			if f.Type == engine.EventTypeInstanceCompleted {
				rootTerminal = i
			}
			continue
		}
		child, gerr := ds.instanceRepo.Get(context.Background(), f.Source)
		require.NoError(t, gerr)
		require.NotNil(t, child.ParentInstanceID)
		assert.Equal(t, parentID, *child.ParentInstanceID, "only the parent's own descendants are streamed")
		childSources[f.Source] = true
		if f.Type == engine.EventTypeInstanceCompleted && firstChildTerminal < 0 {
			firstChildTerminal = i
		}
	}
	assert.Len(t, childSources, 2, "both concurrently running branches stream through the parent's subscription")
	require.GreaterOrEqual(t, firstChildTerminal, 0, "a branch's terminal event is forwarded")
	require.Greater(t, rootTerminal, firstChildTerminal, "a terminal descendant did not close the parent's stream")

	parent, err := ds.instanceRepo.Get(context.Background(), parentID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, parent.State)
}
```

- [x] Step 2: Run it, confirm it fails.

All three root tests (branch lifetime plus the added live visibility and 1,001-child sink proofs) fail at Dial with HTTP 501 before composition wiring.

```bash
go test ./cmd/arbitron/ -run 'TestSubtreeStreamE2E' -count=1
```
Expected:
```
--- FAIL: TestSubtreeStreamE2E_ParentStreamSeesBranchesAndClosesOnlyWhenSubtreeInactive
    subtree_stream_e2e_test.go:35:
        	Error:      	Received unexpected error:
        	            	failed to WebSocket dial: expected handshake response status code 101 but got 501
        	Messages:   	subtree streaming must be wired through the composition root (HTTP 501)
FAIL	github.com/arbitron/arbitron/cmd/arbitron
```

- [x] Step 3: Wire the bridge. In `cmd/arbitron/main.go`:

(a) In `domainStack`, after the `graphState *engine.GraphStateReader` field (P4-T3) add:

```go
	// subtreeEvents serves GET /instances/{id}/ws?subtree=1: ancestry-routed,
	// per-source authorized descendant events (parallel-group live views).
	subtreeEvents *engine.SubtreeEventBridge
```

(b) Immediately after `graphState := engine.NewGraphStateReader(eng, nodeExecRepo, groupRunRepo, instanceRepo)` (P4-T3) add:

```go
	// One bridge for the process: it shares a bounded ancestry cache across
	// subscriptions and authorizes every forwarded source through the engine.
	subtreeEvents := engine.NewSubtreeEventBridge(bus, eng, instanceRepo)
```

(c) In the `return &domainStack{...}` literal, after `graphState:          graphState,` add:

```go
		subtreeEvents:       subtreeEvents,
```

(d) In `(*domainStack).routerDeps()`, after the `if ds.graphState != nil { deps.GraphState = ds.graphState }` block add:

```go
	if ds.subtreeEvents != nil {
		deps.SubtreeEvents = ds.subtreeEvents
	}
```

- [x] Step 4: Run, confirm PASS; then the command package and the full suite.

Focused subtree/graph-state tests, the whole command package, both HTTP verification sets, whole HTTP package, formatting and vet pass. All three socket sink tests also pass with `-race -count=5`. The final `go test ./... -count=1` passes every package except the existing `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` UNKNOWN_PROFILE failure, reproduced from unchanged base `8fa014e` in a temporary archive checkout.

The first broad run also reported `TestGroupFilesE2E_FailedApplyRoutesFailureAndRestoresBytesAndModes` TempDir permission-denied cleanup. It did not reproduce in ten focused runs on unchanged base, and the final full run passes the command package. No unrelated code was changed; the initial cleanup error remains an intermittent wider-suite concern, not a subtree test failure.

Completion self-review, in the required order:
1. Spec compliance: checked this task's protocol, extracted interactive behaviors, lifetime/activity rules and composition contracts against the Phase 4 design §3 and alignment. No UI or later-task features added. Unknown/nil activity keeps resync alive; authorized hidden activity remains redacted.
2. Anti-green-theater: real initDomain/routerDeps, Engine authorization, SQLite ancestry/read service and process bus feed WebSocket sinks. The live visibility test uses one non-owner socket and an ordered root-event barrier; no private token arrives and restored public tokens resume. The terminal root test has 1,000 completed children followed by the only active child; frames continue until it finishes, then the final inactive frame and normal close arrive. Temporarily removed bridge construction and its domainStack literal entry: all three tests fail with HTTP 501; restored them. Strengthened the router activity fake to check the exact root and read it through the scoped service, rather than ignoring the load-bearing ID.
3. Code quality: read neighboring per-instance/system stream implementations, verified bounded writes/read limits, write mutexes, subscription cleanup and existing dependency patterns. gofmt, vet and diff checks are clean. After all review changes, reran focused HTTP tests, HTTP race tests, the whole HTTP package, root/graph-state race tests and the full suite; only the reproduced config failure remains.


```bash
go test ./cmd/arbitron/ -run 'TestSubtreeStreamE2E|TestGraphStateE2E' -count=1
go test ./cmd/arbitron/ -count=1
gofmt -w cmd/arbitron/main.go cmd/arbitron/subtree_stream_e2e_test.go
gofmt -l cmd/arbitron/main.go cmd/arbitron/subtree_stream_e2e_test.go
go vet ./cmd/arbitron/
go test ./... -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/cmd/arbitron	1X.XXXs
ok  	github.com/arbitron/arbitron/cmd/arbitron	XX.XXXs
```
and every package `ok` for `go test ./...` (`gofmt -l` and `go vet` print nothing). Wiring check: temporarily delete `subtreeEvents := engine.NewSubtreeEventBridge(...)` and its literal entry — the test must fail with `HTTP 501`; restore.

- [x] Step 5: Commit.

```bash
git add cmd/arbitron/main.go cmd/arbitron/subtree_stream_e2e_test.go
git commit -m "feat(serve): wire SubtreeEventBridge into the composition root for subtree live streams" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
