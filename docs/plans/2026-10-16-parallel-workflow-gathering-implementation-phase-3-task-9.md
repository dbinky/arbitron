# Parallel Workflow Gathering — Phase 3 Task 9: Abandon intent across engine, MCP, HTTP, WebSocket and dashboard

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give operators an explicit, durable "abandon this gather" action for a parallel-group conflict-resolution session — distinct from "end" (= finish and verify) — reachable through MCP, REST, WebSocket and the dashboard, which rolls the gather back and routes the group to its failure path.
**Architecture:** `ConversationHandler` gains `AbandonSession(ctx, instanceID) error`: same authorization/state gate and turn guard as `EndSession`, valid only for an open group-resolution session (`ErrNotResolutionSession` otherwise), it records the session as abandoned and calls `GroupResolutionCompleter.OnResolutionSessionEnded(..., abandoned=true)` (P3-T8); the gatherer (P3-T6) then rolls back and routes `failure` on the parent's re-entry. Transports are thin: MCP tool `interactive_abandon_session`, `POST /api/v1/instances/{id}/interactive/abandon`, WS client message `interactive.abandon`; the WS history frame tags group sessions with `sessionKind` so the dashboard's `InteractivePanel` shows an "Abandon gather" button only for them.
**Tech Stack:** Go 1.25.5 (`domain/engine`, `adapter/mcp` with mark3labs/mcp-go, `adapter/http` with chi + nhooyr websocket), React 19 + Vitest (dashboard), testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Unresolved conflicts pause gathering and open an interactive resolution session"; "Downstream work proceeds only after the conflicts are resolved and gathering succeeds"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §4 ("Ordinary `EndSession` means 'finish and verify', not 'abandon'. Add an explicit durable abandon action usable through MCP/HTTP/UI; it aborts and rolls back the gather, records conflict details and routes `failure`. Parent cancellation aborts/rolls back and leaves the parent cancelled. Existing transports do not currently produce an abandon intent.").

**Interfaces**
- Consumes:
  - P3-T8 (`domain/engine/conversation.go` as replaced by P3-T8): `loadAndValidate(ctx, instanceID) (*instance.Instance, string, workflow.NodeConfig, *groupSessionTarget, error)`, `completeSession(ctx, inst, instanceID, nodeID, output, reason string, group *groupSessionTarget, abandoned bool) error`, `turnInProgress`; `GroupResolutionBridge`, `NewGroupResolutionBridge`, `IsOpenGroupResolutionSession`, `ConversationKindGroupResolution`; test fakes `groupResolutionSetter`, `newGroupConvoFixture`, `recordingCompleter`, `completerCall`, `fakeNodeWaker`, `recordingNotifier`, `groupResolverResolver`, `groupResolverProfile`, `groupWorkflowSnapshot`.
  - P3-T6: `NewGroupGatherer`, `ResolutionOutcomeAbandoned`; test fakes `newGatherFixture`, `gatherParentDir`, `gatherSection`, `fakeGatherArea`, and the `memGroupRuns` helpers `snapshot` / `update`; `memGroupRuns` / `newMemGroupRuns` themselves are P2-T8's shared fake (`domain/engine/group_fakes_test.go`, extended by P3-T4).
  - Existing: `adapter/mcp/tools_interactive.go:11-37` (registration), `adapter/mcp/register_test.go:78-81` (`expectedTools`), `adapter/http/interactive_handler.go:1-60`, `adapter/http/server.go:345-347` (routes), `adapter/http/stream_handler.go:210-332` (`handleInteractiveInbound`, `sendInteractiveHistory`), `adapter/http/instance_handler.go:158-164` (`isInteractiveNode`), `dashboard/src/api/types.ts:319-355`, `dashboard/src/hooks/useInteractiveSession.ts:1-157`, `dashboard/src/components/InteractivePanel.tsx:1-84`, `dashboard/src/components/NotificationPopover.tsx:6-17`.
- Produces (exact):
  - `ConversationHandler` interface gains `AbandonSession(ctx context.Context, instanceID uuid.UUID) error` (contract).
  - `var ErrNotResolutionSession = errors.New("conversation: not a group conflict-resolution session")` (package `engine`).
  - MCP tool `interactive_abandon_session` (arg `instanceId`), result `{"status":"abandoned","instanceId":"<id>"}`.
  - HTTP `POST /api/v1/instances/{id}/interactive/abandon` → `200 {"status":"abandoned","instanceId"}`; 404 `NOT_FOUND`, 403 `FORBIDDEN`, 409 `CONFLICT` (no active session / not a resolution session / turn in progress), 400 bad id, 500 otherwise. (`func (h *InteractiveHandler) AbandonSession(w http.ResponseWriter, r *http.Request)`.)
  - WS inbound `{"type":"interactive.abandon"}`; WS `interactive.history` data gains `"sessionKind":"group_resolution"` for a group session.
  - Dashboard: `InteractiveSessionState` gains `sessionKind: InteractiveSessionKind | null`, `isAbandoning: boolean`, `abandonSession: () => void`; `export type InteractiveSessionKind = 'interactive' | 'group_resolution'`; `InteractiveHistoryEvent.data.sessionKind?: 'group_resolution'`; `InteractiveEndedEvent.data.reason` admits `'abandoned' | 'cancelled'`.

**Wiring & data provenance**
- Composition root: the transports are already wired to `ds.conversationHandler` (MCP `Deps.ConversationHandler`, main.go:298; HTTP `RouterDeps.ConversationHandler`, main.go:1288; WS via `NewStreamHandler` in server.go:221). The abandon path becomes live end to end when **P3-T11** calls `SetGroupResolution(groupRunRepo, groupResolution)` on the concrete handler and wires `groupExecutor.SetGatherer(gatherer)`; P3-T11's e2e `TestGroupFilesE2E_ConflictAbandon_RoutesFailureAndRestoresParentFiles` proves it through `initDomain`.
- Producers:
  - `instanceID` — the operator: MCP argument, URL `{id}`, or the WS connection's instance id (`stream_handler.go:107-160`, `HandleWebSocket`).
  - The session addressed — P3-T8 `GroupResolutionBridge.StartResolution` (record on the parent at the group node) + P3-T6 `GatherReport.ActiveSession`.
  - `sessionKind` on the history frame — the session record's `LLMConversation["kind"]` written by P3-T8.
  - The abandon's effect — P3-T8 `GroupResolutionBridge.OnResolutionSessionEnded(abandoned=true)` records `Outcome: "abandoned"` and wakes the parent; P3-T6 `FileGatherer.reenterResolution` rolls back and settles `failure`.
- Provenance probes: `TestGroupAbandon_Chain_RollsBackAndRoutesFailure` asserts the SINK (the parent's working-area content restored, failure port, abandoned attempt) through the REAL handler + bridge + gatherer; `TestRegisterAllTools_APISurface` fails if the MCP tool is not registered; `TestRouter_InteractiveAbandonRoute` fails if the route is not mounted.

### Task 9: Abandon intent
**Files:**
- Modify: `domain/engine/conversation.go` (as replaced by P3-T8: import block, `ConversationHandler` interface, new `AbandonSession` after `EndSession`)
- Modify: `adapter/mcp/tools_interactive.go:11-37` (+ new handler)
- Modify: `adapter/mcp/tools_interactive_test.go:17-30` (mock gains `AbandonSession`) and append tests
- Modify: `adapter/mcp/register_test.go:78-81`
- Modify: `adapter/http/interactive_handler.go` (append `AbandonSession`)
- Modify: `adapter/http/server.go:345-347`
- Modify: `adapter/http/stream_handler.go:1-19` (imports), `:210-282` (`handleInteractiveInbound`), `:283-332` (`sendInteractiveHistory`)
- Modify: `adapter/http/stream_handler_test.go:30-64` (mock gains `AbandonSession`) and append tests
- Modify: `adapter/http/interactive_handler_test.go` (append tests)
- Modify: `plugin/skills/workflow-running.md:50-54` (tool table row)
- Modify: `dashboard/src/api/types.ts:319-342`, `dashboard/src/hooks/useInteractiveSession.ts`, `dashboard/src/components/InteractivePanel.tsx`, `dashboard/src/components/InteractivePanel.module.css`, `dashboard/src/components/NotificationPopover.tsx:6-17`
- Test: `domain/engine/conversation_abandon_test.go` (package `engine_test`), `dashboard/src/hooks/useInteractiveSession.test.ts` (append), `dashboard/src/components/InteractivePanel.test.tsx` (helper + append), `dashboard/src/components/NotificationPopover.test.tsx` (new)

#### 9.1 Engine: ConversationHandler.AbandonSession

- [x] Step 1: Write the failing engine tests. Create `domain/engine/conversation_abandon_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Unit: the handler ------------------------------------------------------

func TestConversationHandler_AbandonSession_RecordsAbandonAndClosesSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	require.NoError(t, f.handler.AbandonSession(convoCtx(), f.parentID))

	assert.Equal(t, []completerCall{{instanceID: f.parentID, nodeID: "g1", abandoned: true}}, f.completer.all())
	row := f.exec(f.sessionRow)
	assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation))
	assert.Equal(t, "abandoned", row.LLMConversation["endReason"])
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	data := ended[0].Data.(map[string]any)
	assert.Equal(t, "abandoned", data["reason"])
	assert.Equal(t, "group_resolution", data["sessionKind"])
	p := f.parent()
	assert.Equal(t, instance.StateWaiting, p.State, "the completer, not the handler, decides whether to wake")
	_, wrote := p.Variables["g1"]
	assert.False(t, wrote)
}

func TestConversationHandler_AbandonSession_OrdinaryInteractiveSessionIsRefused(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	completer := &recordingCompleter{}
	h.(groupResolutionSetter).SetGroupResolution(newMemGroupRuns(), completer)

	err := h.AbandonSession(convoCtx(), instanceID)
	assert.ErrorIs(t, err, engine.ErrNotResolutionSession)

	inst, gerr := instRepo.Get(context.Background(), instanceID)
	require.NoError(t, gerr)
	assert.Equal(t, instance.StateWaiting, inst.State, "an ordinary session is untouched")
	assert.NotContains(t, inst.Variables, "interact")
	assert.Empty(t, eventBus.eventsOfType(instanceID, engine.EventTypeInteractiveEnded))
	assert.Empty(t, completer.all())
}

func TestConversationHandler_AbandonSession_AuthorizationMatchesEndSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	err := f.handler.AbandonSession(context.Background(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotFound, "no identity: existence is not leaked")

	stranger := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "x", Role: identity.RoleUser})
	assert.ErrorIs(t, f.handler.AbandonSession(stranger, f.parentID), instance.ErrNotFound)

	f.instances.mu.Lock()
	f.instances.instances[f.parentID].Visibility = instance.VisibilityDepartment
	f.instances.instances[f.parentID].Department = "eng"
	f.instances.mu.Unlock()
	peer := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "peer", Role: identity.RoleUser, Departments: []string{"eng"}})
	assert.ErrorIs(t, f.handler.AbandonSession(peer, f.parentID), instance.ErrForbidden, "a department peer may view but not abandon")

	assert.Empty(t, f.completer.all())
	assert.True(t, engine.IsOpenGroupResolutionSession(f.exec(f.sessionRow).LLMConversation))
}

func TestConversationHandler_AbandonSession_RepeatIsRejected(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	require.NoError(t, f.handler.AbandonSession(convoCtx(), f.parentID))

	assert.ErrorIs(t, f.handler.AbandonSession(convoCtx(), f.parentID), instance.ErrNotInteractiveWait)
	assert.Len(t, f.completer.all(), 1)
}

func TestConversationHandler_AbandonSession_TurnInProgressIsRejected(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	started := make(chan struct{})
	release := make(chan struct{})
	f.agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		close(started)
		ch := make(chan engine.ChatChunk, 2)
		go func() {
			<-release
			ch <- engine.ChatChunk{Token: "ok"}
			ch <- engine.ChatChunk{Done: true}
			close(ch)
		}()
		return ch, nil
	}
	done := make(chan error, 1)
	go func() { done <- f.handler.HandleMessage(convoCtx(), f.parentID, "keep going") }()
	<-started

	assert.ErrorIs(t, f.handler.AbandonSession(convoCtx(), f.parentID), instance.ErrInteractiveTurnInProgress)
	close(release)
	require.NoError(t, <-done)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_AbandonSession_CancelledParentIsRefused(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.instances.mu.Lock()
	f.instances.instances[f.parentID].State = instance.StateCancelled
	f.instances.mu.Unlock()

	assert.ErrorIs(t, f.handler.AbandonSession(convoCtx(), f.parentID), instance.ErrNotInteractiveWait)
	assert.Empty(t, f.completer.all())
	assert.True(t, engine.IsOpenGroupResolutionSession(f.exec(f.sessionRow).LLMConversation),
		"the cancelled parent's session is closed by the reconciler (P3-T10), not by a transport")
}

// --- Chain: real handler + bridge + gatherer --------------------------------

type abandonChain struct {
	f         *gatherFixture
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	queue     *mockQueueManager
	bridge    *engine.GroupResolutionBridge
	gatherer  *engine.FileGatherer
	handler   engine.ConversationHandler
}

// newAbandonChain wires the REAL GroupResolutionBridge and FileGatherer and a
// REAL conversation handler over shared fakes: run a/b with b conflicting.
func newAbandonChain(t *testing.T) *abandonChain {
	t.Helper()
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = groupResolverProfile })
	c := &abandonChain{f: f, instances: newMockInstanceRepo(), nodeExecs: newMockNodeExecutionRepo(), queue: newMockQueueManager()}
	agent := newMockStreamingChatProvider()
	agent.streamCallFn = cannedStreamWithSession([]string{"I could not combine shared.txt safely."}, "sess-1")
	selector := &fakeStreamSelector{chatProv: agent, agentProv: agent}
	node := "g1"
	parent := *f.parent
	parent.OwnerID = convoTestOwner
	parent.CurrentNodeID = &node
	parent.WorkflowSnapshot = groupWorkflowSnapshot(t)
	c.instances.instances[parent.ID] = &parent
	c.bridge = engine.NewGroupResolutionBridge(f.groups, c.instances, c.nodeExecs, groupResolverResolver(), selector,
		newOwnerScopedFileReader(), &recordingNotifier{}, f.bus, &fakeNodeWaker{instances: c.instances}, c.queue)
	c.gatherer = engine.NewGroupGatherer(f.groups, f.area, c.bridge, f.bus)
	c.handler = engine.NewConversationHandler(c.instances, c.nodeExecs, selector, groupResolverResolver(), f.bus, c.queue)
	c.handler.(groupResolutionSetter).SetGroupResolution(f.groups, c.bridge)
	return c
}

func (c *abandonChain) setParentState(s instance.State) {
	c.instances.mu.Lock()
	defer c.instances.mu.Unlock()
	c.instances.instances[c.f.parent.ID].State = s
}

func (c *abandonChain) parent(t *testing.T) *instance.Instance {
	t.Helper()
	p, err := c.instances.Get(context.Background(), c.f.parent.ID)
	require.NoError(t, err)
	return p
}

func (c *abandonChain) gather(t *testing.T) (*engine.GatherStep, error) {
	t.Helper()
	return c.gatherer.Gather(context.Background(), c.f.groups.snapshot(c.f.runID), c.parent(t))
}

func TestGroupAbandon_Chain_RollsBackAndRoutesFailure(t *testing.T) {
	c := newAbandonChain(t)
	step, err := c.gather(t)
	require.NoError(t, err)
	require.False(t, step.Done, "the conflict opened a session")
	c.setParentState(instance.StateWaiting) // what the executor does on a Done=false step (P3-T10)

	require.NoError(t, c.handler.AbandonSession(convoCtx(), c.f.parent.ID))
	assert.Equal(t, instance.StateQueued, c.parent(t).State, "abandon wakes the waiting parent")
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, c.f.groups.snapshot(c.f.runID).Gather.Attempts[0].Outcome)

	c.setParentState(instance.StateRunning) // the queue restarted the parent at the group node
	step, err = c.gather(t)
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, "rolled_back", gatherSection(t, step)["state"])
	assert.Equal(t, 1, c.f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, c.f.area.parentFiles(gatherParentDir),
		"SINK: no task change (not even a's clean merge) reached the parent's working area")

	execs, err := c.nodeExecs.ListByInstance(context.Background(), c.f.parent.ID)
	require.NoError(t, err)
	var closed int
	for _, e := range execs {
		if e.LLMConversation["kind"] == engine.ConversationKindGroupResolution {
			assert.Equal(t, "abandoned", e.LLMConversation["endReason"])
			closed++
		}
	}
	assert.Equal(t, 1, closed)
}

// Parent cancellation mid-session: every transport refuses (the session is
// aborted for operators), a stray completion callback never resurrects the
// parent, and nothing is rolled back HERE — the reconciler (P3-T10,
// TestGroupGatherReconciler_CancelledParentMidSession_RollsBackAndSettles)
// rolls back and settles while the parent stays cancelled.
func TestGroupAbandon_Chain_CancelledParentMidSessionStaysCancelled(t *testing.T) {
	c := newAbandonChain(t)
	_, err := c.gather(t)
	require.NoError(t, err)
	c.setParentState(instance.StateCancelled)

	assert.ErrorIs(t, c.handler.AbandonSession(convoCtx(), c.f.parent.ID), instance.ErrNotInteractiveWait)
	_, err = c.handler.EndSession(convoCtx(), c.f.parent.ID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.ErrorIs(t, c.handler.HandleMessage(convoCtx(), c.f.parent.ID, "hello?"), instance.ErrNotInteractiveWait)

	require.NoError(t, c.bridge.OnResolutionSessionEnded(context.Background(), c.f.parent.ID, "g1", true))
	assert.Equal(t, instance.StateCancelled, c.parent(t).State)
	assert.Zero(t, c.queue.notifyCount())
	assert.Equal(t, instance.GroupRunResolvingConflicts, c.f.groups.snapshot(c.f.runID).State)
	assert.Zero(t, c.f.area.applyCount())
}
```

- [x] Step 2: Run; confirm failure.

```bash
go test ./domain/engine/ -run 'TestConversationHandler_AbandonSession|TestGroupAbandon_Chain' -count=1
```
Expected:
```
domain/engine/conversation_abandon_test.go:...: f.handler.AbandonSession undefined (type engine.ConversationHandler has no field or method AbandonSession)
domain/engine/conversation_abandon_test.go:...: undefined: engine.ErrNotResolutionSession
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement in `domain/engine/conversation.go` (the P3-T8 version).

Replace the import block:
```go
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
```
with:
```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
```

Replace the interface:
```go
type ConversationHandler interface {
	HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error
	// EndSession ends the interactive session and returns the captured output.
	// The output is captured before any state transition to avoid a race with
	// the queue manager advancing the instance.
	EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error)
}
```
with:
```go
type ConversationHandler interface {
	HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error
	// EndSession ends the interactive session and returns the captured output.
	// The output is captured before any state transition to avoid a race with
	// the queue manager advancing the instance. For a parallel_group
	// conflict-resolution session it means "finish and verify".
	EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error)
	// AbandonSession abandons a parallel_group conflict-resolution session:
	// the gather is rolled back and the group routes its failure path. It is
	// refused (ErrNotResolutionSession) for ordinary interactive sessions.
	AbandonSession(ctx context.Context, instanceID uuid.UUID) error
}

// ErrNotResolutionSession is returned by AbandonSession when the instance's
// waiting session is an ordinary interactive session: those are ended, never
// abandoned.
var ErrNotResolutionSession = errors.New("conversation: not a group conflict-resolution session")
```

Insert immediately AFTER the closing `}` of `EndSession` (before `// sessionExecution returns the record ...`):
```go
// AbandonSession abandons the open parallel_group conflict-resolution session
// of instanceID. It shares EndSession's authorization/state gate (so the WS,
// REST and MCP surfaces inherit the same owner/lead/admin rule and the same
// ErrNotFound/ErrForbidden/ErrNotInteractiveWait answers) and its turn guard.
// The session record is closed with reason "abandoned" and the completer
// records the attempt as abandoned and wakes the parent, whose gatherer rolls
// the gather back and routes the group's failure port. A cancelled parent is
// refused by the state gate: cancellation is handled by the reconciler.
func (h *conversationHandler) AbandonSession(ctx context.Context, instanceID uuid.UUID) error {
	inst, nodeID, _, group, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}
	if group == nil {
		return fmt.Errorf("conversation: instance %s node %s: %w", instanceID, nodeID, ErrNotResolutionSession)
	}
	if _, inFlight := h.turnInProgress.LoadOrStore(instanceID, struct{}{}); inFlight {
		return fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrInteractiveTurnInProgress)
	}
	defer h.turnInProgress.Delete(instanceID)

	output := group.exec.Output
	if output == nil {
		output = map[string]any{}
	}
	return h.completeSession(ctx, inst, instanceID, nodeID, output, "abandoned", group, true)
}
```

- [x] Step 4: Run the engine tests; they pass (the adapter mocks are fixed in 9.2/9.3, so build only the engine package here).

```bash
go test ./domain/engine/ -run 'TestConversationHandler_AbandonSession|TestGroupAbandon_Chain' -count=1
go test ./domain/engine/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	...
```

- [x] Step 5: Confirm the adapters now fail to build (their ConversationHandler mocks lack the method) — this is the red for 9.2/9.3.

```bash
go vet ./adapter/http/ ./adapter/mcp/
```
Expected:
```
adapter/mcp/tools_interactive_test.go:...: cannot use conv (variable of type *mockConversationHandler) as engine.ConversationHandler value ...: missing method AbandonSession
adapter/http/...: ... *mockConversationHandler does not implement engine.ConversationHandler (missing method AbandonSession)
```

- [x] Step 6: Commit the engine part.

```bash
git add domain/engine/conversation.go domain/engine/conversation_abandon_test.go
git commit -m "feat(engine): explicit abandon intent for group conflict-resolution sessions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### 9.2 MCP tool `interactive_abandon_session`

- [x] Step 7: Update the MCP test mock and add failing tests. In `adapter/mcp/tools_interactive_test.go` replace lines 17-30 (the mock) with:

```go
type mockConversationHandler struct {
	handleErr    error
	endOutput    map[string]any
	endErr       error
	abandonErr   error
	abandonCalls []uuid.UUID
}

func (m *mockConversationHandler) HandleMessage(_ context.Context, _ uuid.UUID, _ string) error {
	return m.handleErr
}

func (m *mockConversationHandler) EndSession(_ context.Context, _ uuid.UUID) (map[string]any, error) {
	return m.endOutput, m.endErr
}

func (m *mockConversationHandler) AbandonSession(_ context.Context, id uuid.UUID) error {
	m.abandonCalls = append(m.abandonCalls, id)
	return m.abandonErr
}
```
Add `"fmt"` and `"github.com/arbitron/arbitron/domain/engine"` to that file's imports, and append:

```go
func TestInteractiveAbandonSession(t *testing.T) {
	t.Run("happy path abandons the session of the given instance", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		id := uuid.New()
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": id.String()})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, `"status": "abandoned"`, "MarshalResult indents with two spaces")
		assert.Contains(t, text, id.String())
		assert.Equal(t, []uuid.UUID{id}, conv.abandonCalls)
	})

	t.Run("non-owner gets the not-found tool error", func(t *testing.T) {
		conv := &mockConversationHandler{abandonErr: fmt.Errorf("conversation: %w", instance.ErrNotFound)}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": uuid.New().String()})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not found")
	})

	t.Run("ordinary interactive session is refused with an actionable error", func(t *testing.T) {
		conv := &mockConversationHandler{abandonErr: fmt.Errorf("conversation: %w", engine.ErrNotResolutionSession)}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": uuid.New().String()})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not a group conflict-resolution session")
		assert.Contains(t, text, "interactive_end_session")
	})

	t.Run("missing instanceId returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "instanceId is required")
		assert.Empty(t, conv.abandonCalls)
	})

	t.Run("invalid UUID returns error", func(t *testing.T) {
		conv := &mockConversationHandler{}
		srv := newInteractiveServer(conv, &stubInstanceRepo{}, &stubNodeExecRepo{})
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": "not-a-uuid"})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "invalid instanceId")
	})

	t.Run("nil ConversationHandler returns not available", func(t *testing.T) {
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{Instances: &stubInstanceRepo{}, NodeExecs: &stubNodeExecRepo{}})
		adaptmcp.RegisterInteractiveTools(srv)
		result := callTool(t, srv, "interactive_abandon_session", map[string]any{"instanceId": uuid.New().String()})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "not available")
	})
}
```

In `adapter/mcp/register_test.go` replace lines 78-81:
```go
	// Interactive session (3)
	"interactive_send_message",
	"interactive_end_session",
	"interactive_get_history",
```
with:
```go
	// Interactive session (4)
	"interactive_send_message",
	"interactive_end_session",
	"interactive_get_history",
	"interactive_abandon_session",
```

- [x] Step 8: Run; confirm the new tests fail.

```bash
go test ./adapter/mcp/ -run 'TestInteractiveAbandonSession|TestRegisterAllTools' -count=1
```
Expected:
```
--- FAIL: TestInteractiveAbandonSession (0.00s)
    --- FAIL: TestInteractiveAbandonSession/happy_path_abandons_the_session_of_the_given_instance
        ...: tool interactive_abandon_session not found ...
--- FAIL: TestRegisterAllTools_Count ...
--- FAIL: TestRegisterAllTools_APISurface ... expected tool "interactive_abandon_session" not found in RegisterAllTools
FAIL
```

- [x] Step 9: Implement. In `adapter/mcp/tools_interactive.go`, replace lines 30-37 (the `interactive_get_history` registration and the closing brace of `RegisterInteractiveTools`) with:

```go
	srv.AddTool(
		gomcp.NewTool("interactive_get_history",
			gomcp.WithDescription("Retrieve conversation history for reconnection"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		interactiveGetHistoryHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("interactive_abandon_session",
			gomcp.WithDescription("Abandon a parallel-group conflict-resolution session: the gather is rolled back "+
				"(no task changes reach the parent's working area) and the group continues on its failure path. "+
				"Only valid while a parallel_group is resolving conflicts; end an ordinary interactive session with "+
				"interactive_end_session instead. Ending a resolution session (interactive_end_session) means "+
				"'finish and verify', not 'abandon'."),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID of the parent run waiting at the parallel_group node")),
		),
		interactiveAbandonSessionHandler(srv),
	)
}

func interactiveAbandonSessionHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConversationHandler == nil {
			return gomcp.NewToolResultError("interactive sessions not available"), nil
		}

		id, err := ParseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if err := srv.deps.ConversationHandler.AbandonSession(ctx, id); err != nil {
			if errors.Is(err, engine.ErrNotResolutionSession) {
				return gomcp.NewToolResultError(fmt.Sprintf("failed to abandon session: %s (use interactive_end_session for an ordinary interactive session)", err)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to abandon session: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "abandoned", "instanceId": id.String()})
	}
}
```
and replace the file's import block (lines 3-9) with:
```go
import (
	"context"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/engine"
)
```

- [x] Step 10: Run the MCP package; it passes.

```bash
go test ./adapter/mcp/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/mcp	...
```

- [x] Step 11: Document the tool for agents. In `plugin/skills/workflow-running.md` replace lines 50-54 (the Interactive tool table) with:

```markdown
| Tool                          | Purpose                                                        |
| ----------------------------- | -------------------------------------------------------------- |
| `interactive_send_message`    | Send user message during interactive step                      |
| `interactive_end_session`     | End an interactive session early (group resolution: verify)    |
| `interactive_get_history`     | Load conversation history for a session                        |
| `interactive_abandon_session` | Abandon a parallel-group conflict resolution (rolls back)      |
```

- [x] Step 12: Commit.

```bash
git add adapter/mcp/tools_interactive.go adapter/mcp/tools_interactive_test.go adapter/mcp/register_test.go plugin/skills/workflow-running.md
git commit -m "feat(mcp): interactive_abandon_session tool" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### 9.3 HTTP route and WebSocket message

- [x] Step 13: Update the HTTP package mock and write failing tests. In `adapter/http/stream_handler_test.go` replace lines 30-37 (the mock struct) with:

```go
// mockConversationHandler records calls to HandleMessage, EndSession and AbandonSession.
type mockConversationHandler struct {
	mu               sync.Mutex
	handleCalls      []handleMessageCall
	endCalls         []uuid.UUID
	abandonCalls     []uuid.UUID
	handleMessageFn  func(ctx context.Context, instanceID uuid.UUID, content string) error
	endSessionFn     func(ctx context.Context, instanceID uuid.UUID) (map[string]any, error)
	abandonSessionFn func(ctx context.Context, instanceID uuid.UUID) error
}
```
and insert after the `EndSession` method (after line 64):

```go
func (m *mockConversationHandler) AbandonSession(ctx context.Context, instanceID uuid.UUID) error {
	m.mu.Lock()
	m.abandonCalls = append(m.abandonCalls, instanceID)
	fn := m.abandonSessionFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, instanceID)
	}
	return nil
}
```

Append to `adapter/http/interactive_handler_test.go` (add `"github.com/arbitron/arbitron/domain/engine"` to its imports):

```go
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
```

Append to `adapter/http/stream_handler_test.go`:

```go
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
```

- [x] Step 14: Run; confirm failure.

```bash
go test ./adapter/http/ -run 'TestInteractiveHandler_AbandonSession|TestRouter_InteractiveAbandonRoute|TestStreamHandler_InteractiveAbandon|TestStreamHandler_HistoryHydration_Group|TestStreamHandler_HistoryHydration_EndedGroup' -count=1
```
Expected:
```
adapter/http/interactive_handler_test.go:...: h.AbandonSession undefined (type *http.InteractiveHandler has no field or method AbandonSession)
FAIL	github.com/arbitron/arbitron/adapter/http [build failed]
```

- [x] Step 15: Implement the REST handler. Append to `adapter/http/interactive_handler.go`:

```go
// AbandonSession handles POST /instances/{id}/interactive/abandon. It abandons
// a parallel_group conflict-resolution session: the gather is rolled back and
// the group routes its failure path. Ordinary interactive sessions are ended
// (POST /interact/end), never abandoned.
func (h *InteractiveHandler) AbandonSession(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.conversationHandler.AbandonSession(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found")
		case errors.Is(err, instance.ErrForbidden):
			WriteError(w, http.StatusForbidden, "FORBIDDEN", "not allowed to abandon this session")
		case errors.Is(err, engine.ErrNotResolutionSession):
			WriteError(w, http.StatusConflict, "CONFLICT", "abandon is only available for gather conflict-resolution sessions; end an interactive session instead")
		case errors.Is(err, instance.ErrNotInteractiveWait):
			WriteError(w, http.StatusConflict, "CONFLICT", "no active interactive session")
		case errors.Is(err, instance.ErrInteractiveTurnInProgress):
			WriteError(w, http.StatusConflict, "CONFLICT", "a turn is in progress; wait for the current response")
		default:
			slog.Error("interactive: abandon session failed", "instanceID", id, "error", err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to abandon session")
		}
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"status": "abandoned", "instanceId": id.String()})
}
```

Mount the route. In `adapter/http/server.go` replace lines 345-347:
```go
			if interactiveHandler != nil {
				r.Post("/{id}/interact/end", interactiveHandler.EndSession)
			}
```
with:
```go
			if interactiveHandler != nil {
				r.Post("/{id}/interact/end", interactiveHandler.EndSession)
				r.Post("/{id}/interactive/abandon", interactiveHandler.AbandonSession)
			}
```

- [x] Step 16: Implement the WS message and the tagged history. In `adapter/http/stream_handler.go`, add `"github.com/arbitron/arbitron/domain/workflow"` to the import block (lines 16-18 become):
```go
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
```
Replace the `switch msg.Type { ... }` block in `handleInteractiveInbound` (lines 224-233):
```go
	var handlerErr error
	switch msg.Type {
	case "interactive.send":
		handlerErr = h.conversationHandler.HandleMessage(ctx, instanceID, msg.Content)
	case "interactive.end":
		_, handlerErr = h.conversationHandler.EndSession(ctx, instanceID)
	default:
		// Unknown type — silently ignore for forward compatibility.
		return
	}
```
with:
```go
	var handlerErr error
	switch msg.Type {
	case "interactive.send":
		handlerErr = h.conversationHandler.HandleMessage(ctx, instanceID, msg.Content)
	case "interactive.end":
		_, handlerErr = h.conversationHandler.EndSession(ctx, instanceID)
	case "interactive.abandon":
		handlerErr = h.conversationHandler.AbandonSession(ctx, instanceID)
	default:
		// Unknown type — silently ignore for forward compatibility.
		return
	}
```
and in the error-mapping `switch` (lines 239-255) insert, immediately after the `case errors.Is(handlerErr, instance.ErrNotInteractiveWait):` arm:
```go
		case errors.Is(handlerErr, engine.ErrNotResolutionSession):
			clientMsg = "Only a gather conflict-resolution session can be abandoned."
```
Replace `sendInteractiveHistory` (lines 283-332) with:

```go
// sendInteractiveHistory checks if the instance is in interactive wait state
// and, if so, sends an interactive.history event with the full conversation.
// A parallel_group waiting on an OPEN conflict-resolution session hydrates the
// same way, tagged "sessionKind": "group_resolution" so the dashboard can offer
// the abandon action; the group's wait trace and ended sessions do not.
func (h *StreamHandler) sendInteractiveHistory(ctx context.Context, conn *websocket.Conn, inst *instance.Instance) {
	if inst.State != instance.StateWaiting {
		return
	}
	if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
		return
	}
	if h.nodeExecs == nil {
		return
	}

	nodeID := *inst.CurrentNodeID

	// Check if the current node is interactive (or a group that may host a
	// resolution session) by parsing the workflow snapshot.
	interactive := isInteractiveNode(inst.WorkflowSnapshot, nodeID)
	if !interactive && !isGroupNode(inst.WorkflowSnapshot, nodeID) {
		return
	}

	// Load the node execution record (the newest one: a resolution session is
	// numbered past the group's wait trace).
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, inst.ID, nodeID)
	if err != nil {
		if interactive {
			slog.Warn("stream: failed to load node execution for history hydration", "instanceId", inst.ID, "nodeId", nodeID, "error", err)
		}
		return
	}

	sessionKind := ""
	if !interactive {
		if !engine.IsOpenGroupResolutionSession(nodeExec.LLMConversation) {
			return
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
	historyEvt := engine.Event{
		Type:      engine.EventTypeInteractiveHistory,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
	if err := writeWSEvent(ctx, conn, historyEvt); err != nil {
		slog.Warn("stream: failed to send interactive.history", "instanceId", inst.ID, "error", err)
	}
}

// isGroupNode reports whether nodeID is a parallel_group node in the snapshot.
func isGroupNode(snapshot map[string]any, nodeID string) bool {
	node, err := engine.FindNodeByID(snapshot, nodeID)
	if err != nil {
		return false
	}
	return node.Type == workflow.NodeTypeParallelGroup
}
```

- [x] Step 17: Run the HTTP package (all of it: existing history/interactive tests guard the unchanged paths).

```bash
go test ./adapter/http/ -count=1
gofmt -l adapter/http/ adapter/mcp/
go vet ./adapter/http/ ./adapter/mcp/
```
Expected: `ok  	github.com/arbitron/arbitron/adapter/http	...`; no `gofmt`/`vet` output.

- [x] Step 18: Commit.

```bash
git add adapter/http/interactive_handler.go adapter/http/interactive_handler_test.go adapter/http/server.go adapter/http/stream_handler.go adapter/http/stream_handler_test.go
git commit -m "feat(http): abandon route, interactive.abandon WS message, group-session history hydration" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### 9.4 Dashboard: "Abandon gather"

- [x] Step 19: Write the failing dashboard tests.

In `dashboard/src/components/InteractivePanel.test.tsx` replace the `makeSession` helper (lines 11-26) with:
```tsx
function makeSession(overrides: Partial<InteractiveSessionState> = {}): InteractiveSessionState {
  return {
    messages: [],
    streamingContent: '',
    isStreaming: false,
    isActive: true,
    isEnding: false,
    isAbandoning: false,
    nodeId: 'review-design',
    sessionKind: 'interactive',
    error: null,
    connected: true,
    sendMessage: vi.fn(),
    endSession: vi.fn(),
    abandonSession: vi.fn(),
    seedFromInstance: vi.fn(),
    ...overrides,
  }
}
```
and append:
```tsx
// ---------------------------------------------------------------------------
// Group conflict resolution: the "Abandon gather" action
// ---------------------------------------------------------------------------

describe('InteractivePanel — group conflict resolution', () => {
  it('does not offer abandon for an ordinary interactive session', () => {
    render(<InteractivePanel session={makeSession()} nodeName="review-design" />)
    expect(screen.queryByRole('button', { name: /abandon gather/i })).not.toBeInTheDocument()
    expect(screen.getByText('Interactive Session')).toBeInTheDocument()
  })

  it('offers abandon only for a group_resolution session and labels the panel', () => {
    render(<InteractivePanel session={makeSession({ sessionKind: 'group_resolution' })} nodeName="gather-1" />)
    expect(screen.getByRole('button', { name: /abandon gather/i })).toBeInTheDocument()
    expect(screen.getByText('Gather Conflict Resolution')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /end session/i })).toBeInTheDocument()
  })

  it('calls abandonSession when Abandon gather is clicked', async () => {
    const abandonSession = vi.fn()
    const endSession = vi.fn()
    render(
      <InteractivePanel
        session={makeSession({ sessionKind: 'group_resolution', abandonSession, endSession })}
        nodeName="gather-1"
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: /abandon gather/i }))
    expect(abandonSession).toHaveBeenCalledOnce()
    expect(endSession).not.toHaveBeenCalled()
  })

  it('disables abandon while streaming, abandoning, ending or disconnected', () => {
    for (const overrides of [
      { isStreaming: true },
      { isAbandoning: true },
      { isEnding: true },
      { connected: false },
    ]) {
      const { unmount } = render(
        <InteractivePanel session={makeSession({ sessionKind: 'group_resolution', ...overrides })} nodeName="gather-1" />,
      )
      expect(screen.getByRole('button', { name: /abandon gather/i })).toBeDisabled()
      unmount()
    }
  })
})
```

Append to `dashboard/src/hooks/useInteractiveSession.test.ts`:
```ts
// ---------------------------------------------------------------------------
// Group conflict resolution: sessionKind + abandon
// ---------------------------------------------------------------------------

const groupHistoryEvent = (): WebSocketEvent => ({
  type: 'interactive.history',
  timestamp: new Date().toISOString(),
  data: {
    nodeId: 'gather-1',
    sessionKind: 'group_resolution',
    messages: [{ role: 'assistant', content: 'I merged shared.txt' }],
  },
})

describe('useInteractiveSession — group conflict resolution', () => {
  it('starts with no session kind and not abandoning', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    expect(result.current.sessionKind).toBeNull()
    expect(result.current.isAbandoning).toBe(false)
  })

  it('tags a history event with sessionKind group_resolution', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(groupHistoryEvent())
    expect(result.current.sessionKind).toBe('group_resolution')
    expect(result.current.isActive).toBe(true)
    expect(result.current.nodeId).toBe('gather-1')
  })

  it('treats a history event without sessionKind as an ordinary interactive session', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(historyEvent())
    expect(result.current.sessionKind).toBe('interactive')
  })

  it('abandonSession sends interactive.abandon and sets isAbandoning', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(groupHistoryEvent())
    act(() => {
      result.current.abandonSession()
    })
    expect(mockSend).toHaveBeenCalledWith({ type: 'interactive.abandon' })
    expect(result.current.isAbandoning).toBe(true)
  })

  it('an abandoned ended event closes the session and clears the kind', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(groupHistoryEvent())
    act(() => {
      result.current.abandonSession()
    })
    fireEvent({
      type: 'interactive.ended',
      timestamp: new Date().toISOString(),
      data: { reason: 'abandoned', sessionKind: 'group_resolution' },
    })
    expect(result.current.isActive).toBe(false)
    expect(result.current.isAbandoning).toBe(false)
    expect(result.current.sessionKind).toBeNull()
  })

  it('an error clears isAbandoning so the operator can retry', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    fireEvent(groupHistoryEvent())
    act(() => {
      result.current.abandonSession()
    })
    fireEvent(errorEvent('Only a gather conflict-resolution session can be abandoned.'))
    expect(result.current.isAbandoning).toBe(false)
  })

  it('seedFromInstance marks the session interactive when the kind is unknown', () => {
    const { result } = renderHook(() => useInteractiveSession('inst-123'))
    act(() => {
      result.current.seedFromInstance('review-design')
    })
    expect(result.current.sessionKind).toBe('interactive')
  })
})
```

Create `dashboard/src/components/NotificationPopover.test.tsx`:
```tsx
import { render, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import NotificationPopover from './NotificationPopover'
import type { NotificationItem } from '../api/types'

function item(type: string): NotificationItem {
  return {
    id: 'n-1',
    instanceId: '0f0e0d0c-0000-0000-0000-000000000001',
    workflowName: 'gathering-wf',
    type,
    read: false,
    dismissed: false,
    createdAt: new Date().toISOString(),
  }
}

function renderWith(items: NotificationItem[]) {
  return render(
    <NotificationPopover
      items={items}
      dismissedItems={[]}
      showDismissed={false}
      hasMoreDismissed={false}
      onItemClick={vi.fn()}
      onDismiss={vi.fn()}
      onShowDismissed={vi.fn()}
      onLoadMore={vi.fn()}
    />,
  )
}

describe('NotificationPopover — conflict_resolution', () => {
  it('renders a distinct icon for a gather conflict-resolution notification', () => {
    renderWith([item('conflict_resolution')])
    expect(screen.getByText('⇄')).toBeInTheDocument()
    expect(screen.getByText(/gathering-wf/)).toBeInTheDocument()
  })

  it('still falls back to the bell for unknown types', () => {
    renderWith([item('something_new')])
    expect(screen.getByText('🔔')).toBeInTheDocument()
  })
})
```

- [x] Step 20: Run; confirm failure.

```bash
cd dashboard && npx vitest run src/components/InteractivePanel.test.tsx src/hooks/useInteractiveSession.test.ts src/components/NotificationPopover.test.tsx
```
Expected:
```
 FAIL  src/components/InteractivePanel.test.tsx > InteractivePanel — group conflict resolution > offers abandon only for a group_resolution session and labels the panel
TestingLibraryElementError: Unable to find an accessible element with the role "button" and name `/abandon gather/i`
 FAIL  src/hooks/useInteractiveSession.test.ts > useInteractiveSession — group conflict resolution > starts with no session kind and not abandoning
AssertionError: expected undefined to be null
 FAIL  src/components/NotificationPopover.test.tsx > NotificationPopover — conflict_resolution > renders a distinct icon ...
TestingLibraryElementError: Unable to find an element with the text: ⇄
```

- [x] Step 21: Implement the types. In `dashboard/src/api/types.ts` replace lines 319-323 (`InteractiveHistoryEvent`):
```ts
/** Sent on WebSocket connect for instances in interactive-wait state. */
export interface InteractiveHistoryEvent {
  type: 'interactive.history'
  data: { nodeId: string; messages: InteractiveMessage[] }
}
```
with:
```ts
/** Sent on WebSocket connect for instances in interactive-wait state. */
export interface InteractiveHistoryEvent {
  type: 'interactive.history'
  data: {
    nodeId: string
    messages: InteractiveMessage[]
    /** Present when the session is a parallel-group gather conflict resolution. */
    sessionKind?: 'group_resolution'
  }
}
```
and replace lines 337-341 (`InteractiveEndedEvent`):
```ts
/** The interactive session has ended. */
export interface InteractiveEndedEvent {
  type: 'interactive.ended'
  data: { reason: 'llm_completed' | 'user_ended'; output?: unknown }
}
```
with:
```ts
/** The interactive session has ended. */
export interface InteractiveEndedEvent {
  type: 'interactive.ended'
  data: {
    reason: 'llm_completed' | 'user_ended' | 'abandoned' | 'cancelled'
    output?: unknown
    sessionKind?: 'group_resolution'
  }
}
```

- [x] Step 22: Implement the hook. Replace `dashboard/src/hooks/useInteractiveSession.ts` with:

```ts
import { useState, useCallback, useRef } from 'react'
import type {
  InteractiveMessage,
  InteractiveEvent,
  WebSocketEvent,
} from '../api/types'
import { useInstanceWebSocket } from './useInstanceWebSocket'

/** A message with a stable key for React list rendering. */
export interface KeyedMessage extends InteractiveMessage {
  /** Stable identifier for use as a React list key. */
  key: number
}

/**
 * What kind of session is live: an ordinary interactive node, or a
 * parallel-group gather conflict resolution (which can also be abandoned).
 */
export type InteractiveSessionKind = 'interactive' | 'group_resolution'

/** State and actions returned by the useInteractiveSession hook. */
export interface InteractiveSessionState {
  /** Complete messages in the conversation. */
  messages: KeyedMessage[]
  /** Partial content being streamed (current assistant response). */
  streamingContent: string
  /** True while tokens are arriving. */
  isStreaming: boolean
  /** True while the interactive session is live. */
  isActive: boolean
  /** The node ID that initiated the interactive session. */
  nodeId: string | null
  /** Kind of the live session; null when no session is live. */
  sessionKind: InteractiveSessionKind | null
  /** Last error message, if any. */
  error: string | null
  /** Whether the WebSocket is connected. */
  connected: boolean
  /** True while the end-session request is in flight (button should be disabled). */
  isEnding: boolean
  /** True while the abandon request is in flight (button should be disabled). */
  isAbandoning: boolean
  /** Send a user message to the interactive session. */
  sendMessage: (content: string) => void
  /** End the interactive session (for a gather conflict resolution: finish and verify). */
  endSession: () => void
  /** Abandon a gather conflict resolution: roll back and route the group to failure. */
  abandonSession: () => void
  /**
   * Pre-warm session state from the REST instance response.
   * Activates the panel immediately on page load without waiting for
   * interactive.history — eliminates the flash of the normal side panel.
   * Safe to call multiple times; nodeId is only set if not already known.
   */
  seedFromInstance: (nodeId: string) => void
}

/**
 * Manages an interactive chat session for a workflow instance.
 *
 * Wraps useInstanceWebSocket and provides a chat-specific interface with
 * state management for messages, streaming tokens, and session lifecycle.
 */
export function useInteractiveSession(
  instanceId: string | undefined,
): InteractiveSessionState {
  const [messages, setMessages] = useState<KeyedMessage[]>([])
  const [streamingContent, setStreamingContent] = useState('')
  const [isStreaming, setIsStreaming] = useState(false)
  const [isActive, setIsActive] = useState(false)
  const [isEnding, setIsEnding] = useState(false)
  const [isAbandoning, setIsAbandoning] = useState(false)
  const [nodeId, setNodeId] = useState<string | null>(null)
  const [sessionKind, setSessionKind] = useState<InteractiveSessionKind | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Counter for stable message keys, persists across re-renders.
  const nextKeyRef = useRef(0)

  // Use a ref to hold the send function so event handler doesn't go stale
  const sendRef = useRef<(data: object) => void>(() => {})

  const assignKey = useCallback((msg: InteractiveMessage): KeyedMessage => {
    return { ...msg, key: nextKeyRef.current++ }
  }, [])

  const handleEvent = useCallback((event: WebSocketEvent) => {
    const e = event as unknown as InteractiveEvent
    switch (e.type) {
      case 'interactive.history':
        nextKeyRef.current = 0
        setMessages(e.data.messages.map((m) => ({ ...m, key: nextKeyRef.current++ })))
        setNodeId(e.data.nodeId)
        setSessionKind(e.data.sessionKind === 'group_resolution' ? 'group_resolution' : 'interactive')
        setIsActive(true)
        setError(null)
        setStreamingContent('')
        setIsStreaming(false)
        break

      case 'interactive.message':
        if (e.data.role === 'assistant') {
          // Replace streaming content with the complete message
          setStreamingContent('')
          setIsStreaming(false)
        }
        setMessages((prev) => [
          ...prev,
          assignKey({ role: e.data.role, content: e.data.content }),
        ])
        break

      case 'interactive.token':
        setStreamingContent((prev) => prev + e.data.token)
        setIsStreaming(true)
        break

      case 'interactive.ended':
        setIsActive(false)
        setIsEnding(false)
        setIsAbandoning(false)
        setNodeId(null)
        setSessionKind(null)
        setStreamingContent('')
        setIsStreaming(false)
        break

      case 'interactive.error':
        setError(e.data.message)
        setIsStreaming(false)
        setStreamingContent('')
        setIsEnding(false)
        setIsAbandoning(false)
        break

      default:
        // Ignore non-interactive events (instance state changes, etc.)
        break
    }
  }, [assignKey])

  const { connected, send } = useInstanceWebSocket(instanceId, handleEvent)
  sendRef.current = send

  const sendMessage = useCallback(
    (content: string) => {
      sendRef.current({ type: 'interactive.send', content })
    },
    [],
  )

  const endSession = useCallback(() => {
    setIsEnding(true)
    sendRef.current({ type: 'interactive.end' })
  }, [])

  const abandonSession = useCallback(() => {
    setIsAbandoning(true)
    sendRef.current({ type: 'interactive.abandon' })
  }, [])

  const seedFromInstance = useCallback((newNodeId: string) => {
    setIsActive(true)
    setNodeId((current) => current ?? newNodeId)
    setSessionKind((current) => current ?? 'interactive')
  }, [])

  return {
    messages,
    streamingContent,
    isStreaming,
    isActive,
    isEnding,
    isAbandoning,
    nodeId,
    sessionKind,
    error,
    connected,
    sendMessage,
    endSession,
    abandonSession,
    seedFromInstance,
  }
}
```

- [x] Step 23: Implement the panel. Replace lines 23-40 of `dashboard/src/components/InteractivePanel.tsx` (from `return (` through the closing `</div>` of the header) with:

```tsx
  const isGroupResolution = session.sessionKind === 'group_resolution'

  return (
    <div className={styles.panel}>
      {/* Header */}
      <div className={styles.header}>
        <div className={styles.headerTitle}>
          <span className={styles.headerLabel}>
            {isGroupResolution ? 'Gather Conflict Resolution' : 'Interactive Session'}
          </span>
          <span className={styles.headerSep}>&mdash;</span>
          <span className={styles.headerNode}>{nodeName}</span>
        </div>
        <div className={styles.headerActions}>
          {isGroupResolution && (
            <button
              className={styles.abandonButton}
              onClick={session.abandonSession}
              disabled={session.isAbandoning || session.isEnding || session.isStreaming || !session.connected}
              title="Abandon the gather: merged changes are rolled back and the group continues on its failure path"
            >
              Abandon gather
            </button>
          )}
          <button
            className={styles.endButton}
            onClick={session.endSession}
            disabled={session.isEnding || session.isAbandoning || session.isStreaming || !session.connected}
            title={
              session.isStreaming
                ? 'Wait for the assistant to finish responding'
                : isGroupResolution
                  ? 'Finish: Arbitron verifies no conflicts remain, then continues the gather'
                  : 'End the interactive session'
            }
          >
            End Session
          </button>
        </div>
      </div>
```

Append to `dashboard/src/components/InteractivePanel.module.css` (after `.endButton:hover`, line 65):
```css

.headerActions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.abandonButton {
  padding: 4px 10px;
  font-size: 11px;
  font-weight: 600;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-primary);
  cursor: pointer;
  transition: background 0.15s ease, border-color 0.15s ease;
  white-space: nowrap;
  flex-shrink: 0;
}

.abandonButton:hover {
  background: rgba(231, 76, 60, 0.1);
  border-color: rgba(231, 76, 60, 0.4);
}

.abandonButton:disabled,
.endButton:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

In `dashboard/src/components/NotificationPopover.tsx` replace lines 6-17 (`typeIcon`) with:
```tsx
function typeIcon(type: string): string {
  switch (type) {
    case 'failed':
      return '⚠'
    case 'completed':
      return '✓'
    case 'needs_interaction':
      return '💬'
    case 'conflict_resolution':
      return '⇄'
    default:
      return '🔔'
  }
}
```

- [x] Step 24: Run the dashboard gates.

```bash
cd dashboard && npx tsc --noEmit && npx vitest run
```
Expected: `tsc` prints nothing; vitest ends with `Test Files  N passed (N)` and no failures.

- [x] Step 25: Refresh the embedded SPA and commit.

```bash
make ui
git add dashboard/src/api/types.ts dashboard/src/hooks/useInteractiveSession.ts dashboard/src/hooks/useInteractiveSession.test.ts dashboard/src/components/InteractivePanel.tsx dashboard/src/components/InteractivePanel.module.css dashboard/src/components/InteractivePanel.test.tsx dashboard/src/components/NotificationPopover.tsx dashboard/src/components/NotificationPopover.test.tsx adapter/dashboard/dist
git commit -m "feat(dashboard): Abandon gather action for group conflict-resolution sessions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [x] Step 26: Full Go gate.

Executed `go test ./... -count=1`: all packages pass except the existing `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` unknown-profile failure. Reproduced that same failure from unchanged base `0d46044` in a separate archive checkout. This unrelated red is tolerated by the mission; focused task tests are green.

```bash
go test ./... -count=1
```
Expected: every package `ok` (no `FAIL`).

### Pass evidence and completion gate

Engine, MCP and HTTP each followed red → implementation → green in the planned order; dashboard red was 9 failing new assertions before implementation. Supplemental transport tests live in `tools_interactive_abandon_test.go` and `interactive_abandon_test.go` beside the existing mock definitions. The whole engine, MCP and HTTP packages pass. Focused engine and HTTP race tests, touched-package vet, formatting and diff checks pass. Dashboard TypeScript and all 52 files / 537 tests pass; `make ui` refreshed the embedded bundle (existing large-chunk warning).

Self-review (in order):
1. Spec compliance: implemented only explicit abandon, the specified transport contracts/history tag, panel/hook types/actions and notification icon. Preserved EndSession's finish-and-verify behavior and ordinary-session rejection; did not activate file mode ahead of its assigned root task.
2. Anti-green-theater: planned chain asserts rollback/failure and no parent variable overwrite. Strengthened the existing real Git + SQLite bridge/gatherer integration with an abandon mode: actual task forks/finalized refs produce a staged conflict; the real handler records abandon, persists a closed session, wakes via real node-addressed SQL, and re-entry yields failure/rolled_back with baseline parent bytes AND permissions. Temporarily changed only the production abandon flag to false: this sink test failed on the abandoned outcome/rollback and unwanted provider turn; restored it and reran green with `-race`. Registration and router tests prove transport mounting; WS tests assert actual tagged frames. These local tests do not substitute for initDomain coverage.
3. Code quality: reviewed adjacent conversation/end, MCP and HTTP handler patterns and existing panel/hook conventions; updated the turn-guard documentation for the new method, formatted/vetted, and reran focused tests after review.

- [x] Completion gate: Prove operator abandon through the real production composition root restores parent files and routes downstream failure.

> RESOLVED (P3-T10/P3-T11): `initDomain` now constructs the real Git working area, task finalizer, resolution bridge, gatherer and recovery/pruning components, wires the shared conversation handler with `SetGroupResolution`, and enables results-and-files support before freezing executors. `TestGroupFilesE2E_ConflictAbandon_RoutesFailureAndRestoresParentFiles` passes with `-race`: real task file writes produce a persisted conflict session; operator abandon routes downstream failure, whose tool reads baseline `shared.txt` and confirms even the clean task's `a.txt` was not applied. The persisted attempt/session say abandoned, the gather is rolled back, and the original successful task decision remains unchanged. No implementation changes were needed to close this previously blocked verification gate.

Completion self-review (in order):
1. Spec compliance: reviewed the abandon handler against EndSession, transport registration/error mapping/history, dashboard action/types, and the production bridge/gatherer wiring. The specified distinction between abandon and finish-and-verify, authorization/state/turn gates, and rollback/failure sinks are present; no extra behavior was added.
2. Anti-green-theater: temporarily removed only `groupSessions.SetGroupResolution(groupRunRepo, groupResolution)` from `initDomain`. The real-root test failed at the abandon call (`node group-1 is not interactive`), proving it depends on production wiring. Restored the line and reran with `-race`, green. The test's downstream file reads and persisted outcomes depend on real nonempty task writes, not fabricated group inputs. No mutation remains.
3. Code quality: reviewed neighboring end-session, HTTP/MCP handlers, WS history, and panel/hook conventions. Formatting, touched-package vet and diff checks are clean; no cleanup was necessary. Reran focused root/engine/HTTP race tests and dashboard component/hook tests after review, green.

Completion verification: the engine abandon/chain race tests, whole HTTP/MCP packages, focused HTTP race tests, TypeScript, and all 52 dashboard files / 537 tests pass. `go test ./... -count=1` passes every package except the already documented `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` unknown-profile failure; this unrelated red does not block the green task tests or production-root sink proof. Dashboard source and embedded assets are unchanged in this verification-only pass.
