# Queue Live Row Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Active queue rows reflect the current node live (Step + the underlying state) as the engine advances, via a surgical in-place row patch instead of a full-list refetch.

**Architecture:** The engine already publishes `node.started` / `node.failed` onto the system event bus. We (1) enrich those two events with the node's friendly label + effective backend, (2) include the instance id in the system-WebSocket envelope so the client can map an event to a row, and (3) patch the matching row's fields in `QueueView` directly from the event. The existing lifecycle-event debounced refetch is left intact as the reconciliation backstop.

**Tech Stack:** Go (domain/engine, adapter/http), `nhooyr.io/websocket`; React + TypeScript dashboard, Vitest + @testing-library/react.

## Global Constraints

- Strict TDD: write the failing test first, watch it fail, implement minimally, watch it pass, commit. (5 scenario categories where applicable.)
- Hexagonal architecture: domain logic stays in `domain/`, HTTP/transport concerns in `adapter/http`.
- The per-instance stream handler (`StreamHandler`) and the Execution/trace view MUST NOT change — they already update live.
- `node.completed` is intentionally NOT enriched or patched.
- New fields are additive only (optional `instanceId`, extra `Data` keys) — no breaking changes to existing event/envelope consumers.
- Friendly label fallback rule (verbatim from `enricher.go`): label = `node.Label` if non-empty else `node.ID`; backend = `node.Config.Backend` if `!= workflow.BackendUnset` else the deployment default (else empty).

---

## File Structure

**Backend**
- `domain/engine/enricher.go` — extract a pure `nodeLabelAndBackend` helper; `resolveNodeFields` delegates to it.
- `domain/engine/engine.go` — stamp `nodeLabel`+`backend` into `node.started` (~line 804) and `node.failed` (~line 887) event `Data`; add nil-safe `defaultAgentBackendForEvents()`.
- `adapter/http/system_stream_handler.go` — write a `{instanceId,type,timestamp,data}` envelope instead of the bare event; add shared `writeWSValue` helper.

**Frontend**
- `dashboard/src/api/types.ts` — add optional `instanceId?: string` to `WebSocketEvent`.
- `dashboard/src/routes/QueueView.tsx` — patch the matching `active` row on `node.started` / `node.failed`; keep lifecycle refetch.
- `dashboard/src/routes/QueueView.test.tsx` — new test file.

---

## Task 1: Enrich node.started / node.failed events with label + backend

**Files:**
- Modify: `domain/engine/enricher.go` (extract helper; `resolveNodeFields` ~line 119)
- Modify: `domain/engine/engine.go` (node.started emit ~804; node.failed emit ~887; add `defaultAgentBackendForEvents`)
- Test: `domain/engine/enricher_test.go` (helper unit test), `domain/engine/engine_test.go` (event-data assertion)

**Interfaces:**
- Produces: `func nodeLabelAndBackend(node workflow.Node, defaultBackend string) (label, backend string)` — package-private (`engine`).
- Produces: `node.started` and `node.failed` event `Data` maps now additionally carry string keys `"nodeLabel"` and `"backend"` (alongside existing `"nodeId"`, `"nodeType"`, and—for failed—`"error"`).

- [ ] **Step 1: Write the failing helper test**

In `domain/engine/enricher_test.go`, add:

```go
func TestNodeLabelAndBackend(t *testing.T) {
	t.Run("label uses Label when set", func(t *testing.T) {
		n := workflow.Node{ID: "n1", Label: "Architect R1"}
		label, _ := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "Architect R1", label)
	})
	t.Run("label falls back to node id when Label empty", func(t *testing.T) {
		n := workflow.Node{ID: "n1"}
		label, _ := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "n1", label)
	})
	t.Run("backend uses node config when set", func(t *testing.T) {
		n := workflow.Node{ID: "n1", Config: workflow.NodeConfig{Backend: "codex"}}
		_, backend := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "codex", backend)
	})
	t.Run("backend falls back to default when unset", func(t *testing.T) {
		n := workflow.Node{ID: "n1"}
		_, backend := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "claude-code", backend)
	})
	t.Run("backend empty when unset and no default", func(t *testing.T) {
		n := workflow.Node{ID: "n1"}
		_, backend := nodeLabelAndBackend(n, "")
		assert.Equal(t, "", backend)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/engine/ -run TestNodeLabelAndBackend -v`
Expected: FAIL — `undefined: nodeLabelAndBackend`.

- [ ] **Step 3: Extract the helper and delegate from resolveNodeFields**

In `domain/engine/enricher.go`, add the helper (place it just above `resolveNodeFields`):

```go
// nodeLabelAndBackend derives the friendly step label and effective backend for
// a node, applying the same fallbacks the enriched list uses: label falls back
// to the node id; backend falls back to defaultBackend when the node inherits
// (BackendUnset). Shared by the enricher and the engine's node-event emitters so
// a live-patched queue row matches what a refetch would produce.
func nodeLabelAndBackend(node workflow.Node, defaultBackend string) (label, backend string) {
	if node.Label != "" {
		label = node.Label
	} else {
		label = node.ID
	}
	if node.Config.Backend != workflow.BackendUnset {
		backend = node.Config.Backend
	} else if defaultBackend != "" {
		backend = defaultBackend
	}
	return label, backend
}
```

Replace the body of `resolveNodeFields` (the label/backend block) so it ends with:

```go
	node, err := FindNodeByID(s.WorkflowSnapshot, nodeID)
	if err != nil {
		return "", ""
	}
	return nodeLabelAndBackend(node, e.defaultAgentBackend)
}
```

(Delete the now-duplicated `if node.Label != ""` / `if node.Config.Backend != ...` block that previously lived in `resolveNodeFields`.)

- [ ] **Step 4: Run helper test to verify it passes**

Run: `go test ./domain/engine/ -run TestNodeLabelAndBackend -v`
Expected: PASS (all 5 subtests).

- [ ] **Step 5: Write the failing engine event-data test**

In `domain/engine/engine_test.go`, add below `TestEngine_PublishesNodeStartedEvents`:

```go
func TestEngine_NodeStartedEventCarriesLabelAndBackend(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	started := f.eventBus.eventsOfType(id, "node.started")
	require.NotEmpty(t, started)
	for _, e := range started {
		d, ok := e.Data.(map[string]any)
		require.True(t, ok, "node.started Data should be a map")
		nodeID, _ := d["nodeId"].(string)
		label, hasLabel := d["nodeLabel"].(string)
		_, hasBackend := d["backend"]
		assert.True(t, hasLabel, "node.started Data should carry nodeLabel")
		assert.Equal(t, nodeID, label, "linear nodes have no Label, so label == nodeId")
		assert.True(t, hasBackend, "node.started Data should carry a backend key")
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./domain/engine/ -run TestEngine_NodeStartedEventCarriesLabelAndBackend -v`
Expected: FAIL — `node.started Data should carry nodeLabel` (key absent).

- [ ] **Step 7: Stamp label+backend into the two events**

In `domain/engine/engine.go`, add the nil-safe accessor (near the other small `*Engine` helpers):

```go
// defaultAgentBackendForEvents returns the deployment-wide default agent backend
// used to stamp node events, sourced from the wired enricher (nil-safe).
func (e *Engine) defaultAgentBackendForEvents() string {
	if e.enricher != nil {
		return e.enricher.defaultAgentBackend
	}
	return ""
}
```

Replace the `node.started` publish (~line 804) with:

```go
		nlabel, nbackend := nodeLabelAndBackend(node, e.defaultAgentBackendForEvents())
		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeNodeStarted,
			Timestamp: time.Now(),
			Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type), "nodeLabel": nlabel, "backend": nbackend},
		})
```

Replace the `node.failed` publish in `handleNodeError` (~line 887) with:

```go
	flabel, fbackend := nodeLabelAndBackend(node, e.defaultAgentBackendForEvents())
	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeNodeFailed,
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type), "nodeLabel": flabel, "backend": fbackend, "error": execErr.Error()},
	})
```

- [ ] **Step 8: Run both engine tests + the package**

Run: `go test ./domain/engine/ -run 'TestNodeLabelAndBackend|TestEngine_NodeStartedEventCarriesLabelAndBackend|TestEngine_PublishesNodeStartedEvents' -v`
Expected: PASS.
Run: `go test ./domain/engine/`
Expected: PASS (no regressions in the package).

- [ ] **Step 9: Commit**

```bash
git add domain/engine/enricher.go domain/engine/engine.go domain/engine/enricher_test.go domain/engine/engine_test.go
git commit -m "feat(engine): stamp nodeLabel+backend into node.started/node.failed events"
```

---

## Task 2: Include instanceId in the system-WebSocket envelope

**Files:**
- Modify: `adapter/http/system_stream_handler.go` (envelope + `writeWSValue` helper; refactor `writeWSEvent` to delegate)
- Test: `adapter/http/system_stream_handler_test.go`

**Interfaces:**
- Consumes: `engine.ScopedEvent{ InstanceID uuid.UUID; Event engine.Event }` from `SubscribeAll()`.
- Produces: each system-WS message is now JSON `{ "instanceId": "<uuid>", "type": ..., "timestamp": ..., "data": ... }`. The per-instance `StreamHandler` payload (`{type,timestamp,data}`) is unchanged.

- [ ] **Step 1: Write the failing test**

In `adapter/http/system_stream_handler_test.go`, add a test that the message written for a scoped event includes the instance id. Match the existing test style in that file (it already exercises `node_started`/`node_completed` envelopes). Add:

```go
func TestSystemStream_EnvelopeIncludesInstanceID(t *testing.T) {
	instID := uuid.New()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// systemWSEnvelope is the shape written to system-WS clients.
	err := enc.Encode(systemWSEnvelope{
		InstanceID: instID,
		Type:       "node.started",
		Timestamp:  time.Unix(0, 0).UTC(),
		Data:       map[string]any{"nodeId": "A"},
	})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, instID.String(), got["instanceId"])
	assert.Equal(t, "node.started", got["type"])
	nested, ok := got["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "A", nested["nodeId"])
}
```

(Ensure imports include `bytes`, `encoding/json`, `time`, and `github.com/google/uuid`.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./adapter/http/ -run TestSystemStream_EnvelopeIncludesInstanceID -v`
Expected: FAIL — `undefined: systemWSEnvelope`.

- [ ] **Step 3: Add the envelope type, shared writer, and use it in the handler**

In `adapter/http/system_stream_handler.go`:

Add the envelope type (near the top, after the imports):

```go
// systemWSEnvelope is the JSON shape sent to system-WS clients. It carries the
// instance id (which the bare engine.Event omits) so the client can map an
// event to a specific queue row. Additive: existing fields are unchanged.
type systemWSEnvelope struct {
	InstanceID uuid.UUID `json:"instanceId"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	Data       any       `json:"data"`
}
```

Add a shared marshalling helper and make `writeWSEvent` delegate to it (replace the existing `writeWSEvent` body):

```go
// writeWSValue serializes v as JSON and sends it as a WebSocket text message
// with a write deadline to prevent goroutine leaks from slow/frozen clients.
func writeWSValue(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

// writeWSEvent writes a bare engine.Event (used by the per-instance stream,
// whose client already knows the instance).
func writeWSEvent(ctx context.Context, conn *websocket.Conn, evt engine.Event) error {
	return writeWSValue(ctx, conn, evt)
}
```

In `HandleWebSocket`, replace the write line (currently `if err := writeWSEvent(ctx, conn, sev.Event); err != nil {`) with the envelope write:

```go
			env := systemWSEnvelope{
				InstanceID: sev.InstanceID,
				Type:       sev.Event.Type,
				Timestamp:  sev.Event.Timestamp,
				Data:       sev.Event.Data,
			}
			if err := writeWSValue(ctx, conn, env); err != nil {
				return
			}
```

- [ ] **Step 4: Run the test + package**

Run: `go test ./adapter/http/ -run TestSystemStream_EnvelopeIncludesInstanceID -v`
Expected: PASS.
Run: `go test ./adapter/http/`
Expected: PASS. (If a pre-existing system_stream test asserted the exact bare-event shape, update it to read `data`/`type` from the envelope; the per-instance `stream_handler_test.go` must remain green untouched.)

- [ ] **Step 5: Commit**

```bash
git add adapter/http/system_stream_handler.go adapter/http/system_stream_handler_test.go
git commit -m "feat(http): include instanceId in system-WS event envelope"
```

---

## Task 3: Patch queue rows live on node.started / node.failed

**Files:**
- Modify: `dashboard/src/api/types.ts` (`WebSocketEvent`)
- Modify: `dashboard/src/routes/QueueView.tsx` (event handler)
- Test: `dashboard/src/routes/QueueView.test.tsx` (new)

**Interfaces:**
- Consumes: `WebSocketEvent` now with optional `instanceId?: string`; node event `data` shaped `{ nodeId?: string; nodeLabel?: string; backend?: string }`.
- Produces: `QueueView` patches the matching `active` row's `currentNodeId`, `friendlyStep`, and `backend` from the event, with no list refetch.

- [ ] **Step 1: Add `instanceId` to WebSocketEvent**

In `dashboard/src/api/types.ts`, update the interface:

```ts
export interface WebSocketEvent {
  type: string
  timestamp: string
  data: unknown
  /** Present on system-WS envelopes; identifies the originating instance. */
  instanceId?: string
}
```

- [ ] **Step 2: Write the failing QueueView test**

Create `dashboard/src/routes/QueueView.test.tsx`:

```tsx
import { render, screen, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { WebSocketEvent, EnrichedInstanceSummary } from '../api/types'

let capturedHandler: ((e: WebSocketEvent) => void) | null = null

vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (cb: (e: WebSocketEvent) => void) => {
    capturedHandler = cb
    return { connected: true }
  },
}))
vi.mock('../hooks/useRefetchOnReconnect', () => ({ useRefetchOnReconnect: () => {} }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

const runningRow: EnrichedInstanceSummary = {
  id: 'inst-1',
  ownerId: 'u1',
  workflowName: 'pr-review',
  state: 'running',
  priority: 'normal',
  createdAt: '2024-01-01T00:00:00Z',
  updatedAt: '2024-01-01T00:00:00Z',
  runName: 'run — pr-review',
  retryCount: 0,
  friendlyStep: 'Architect R1',
  currentNodeId: 'architect-r1',
}

const listMock = vi.fn(async ({ state }: { state: string }) =>
  state === 'running' ? [runningRow] : [],
)

vi.mock('../api/client', () => ({
  api: {
    instances: { list: (p: { state: string }) => listMock(p), getNodeTrace: vi.fn() },
    me: vi.fn(async () => ({ userId: 'u1', authMode: 'local' })),
  },
}))

import QueueView from './QueueView'

function renderView() {
  return render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )
}

describe('QueueView live row patch', () => {
  beforeEach(() => {
    capturedHandler = null
    listMock.mockClear()
  })

  it('patches the row Step in place on node.started without refetching', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    act(() => {
      capturedHandler?.({
        type: 'node.started',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'inst-1',
        data: { nodeId: '10x-r1', nodeLabel: '10x R1', backend: 'claude-code' },
      })
    })

    await waitFor(() => expect(screen.getByText('10x R1')).toBeInTheDocument())
    expect(listMock.mock.calls.length).toBe(callsAfterLoad) // no refetch
  })

  it('ignores a node event for an instance not in the list', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    act(() => {
      capturedHandler?.({
        type: 'node.started',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'unknown-inst',
        data: { nodeId: 'x', nodeLabel: 'X', backend: '' },
      })
    })

    expect(screen.getByText('Architect R1')).toBeInTheDocument()
    expect(listMock.mock.calls.length).toBe(callsAfterLoad)
  })

  it('still refetches on a lifecycle event', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    act(() => {
      capturedHandler?.({
        type: 'instance.completed',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'inst-1',
        data: {},
      })
    })

    await waitFor(() => expect(listMock.mock.calls.length).toBeGreaterThan(callsAfterLoad))
  })
})
```

- [ ] **Step 3: Run it to verify it fails**

Run (from `dashboard/`): `npx vitest run src/routes/QueueView.test.tsx`
Expected: FAIL — the first test times out waiting for `10x R1` (the row is never patched).

- [ ] **Step 4: Implement the patch in QueueView**

In `dashboard/src/routes/QueueView.tsx`, add a node-event set near `STATE_CHANGE_EVENTS`:

```ts
/** Node events that should patch a single row in place (no refetch). */
const NODE_PATCH_EVENTS = new Set(['node.started', 'node.failed'])
```

Replace the `useSystemWebSocket(useCallback(...))` handler body so node events patch first and return before the refetch path:

```ts
  const { connected } = useSystemWebSocket(
    useCallback(
      (event: WebSocketEvent) => {
        if (NODE_PATCH_EVENTS.has(event.type)) {
          const instId = event.instanceId
          if (!instId) return
          const d = (event.data ?? {}) as { nodeId?: string; nodeLabel?: string; backend?: string }
          setActive((prev) => {
            const idx = prev.findIndex((r) => r.id === instId)
            if (idx === -1) return prev // not listed yet; instance.started refetch will surface it
            const next = [...prev]
            next[idx] = {
              ...next[idx],
              currentNodeId: d.nodeId ?? next[idx].currentNodeId,
              friendlyStep: d.nodeLabel ?? next[idx].friendlyStep,
              backend: d.backend ?? next[idx].backend,
            }
            return next
          })
          return
        }
        if (!STATE_CHANGE_EVENTS.has(event.type)) return
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => {
          void fetchAll()
        }, DEBOUNCE_MS)
      },
      [fetchAll],
    ),
  )
```

- [ ] **Step 5: Run the test to verify it passes**

Run (from `dashboard/`): `npx vitest run src/routes/QueueView.test.tsx`
Expected: PASS (all three tests).

- [ ] **Step 6: Run the dashboard test suite + typecheck**

Run (from `dashboard/`): `npx vitest run`
Expected: PASS (no regressions).
Run (from `dashboard/`): `npx tsc --noEmit`
Expected: no type errors.

- [ ] **Step 7: Commit**

```bash
git add dashboard/src/api/types.ts dashboard/src/routes/QueueView.tsx dashboard/src/routes/QueueView.test.tsx
git commit -m "feat(dashboard): live-patch queue rows on node.started/node.failed"
```

---

## Task 4: Manual verification

**Files:** none (verification only).

- [ ] **Step 1: Build and run the server**

Per project notes, rebuild first (a stale binary uses Postgres):
Run: `go build -o arbitron ./cmd/arbitron && ./arbitron serve`

- [ ] **Step 2: Observe a running instance in the queue**

Trigger (or watch) a multi-node workflow run. In the Queue view's **Active** section, confirm the **Step** cell advances node-to-node live (e.g. `Architect R1` → `10x R1` → …) without a manual refresh, and that Model/Retries refresh on terminal lifecycle events. Confirm the network panel shows no `/api/v1/instances` refetch on each node transition (only the system-WS frames).

- [ ] **Step 3: Note the accepted gaps**

Confirm the known, accepted behaviors hold: Elapsed does not tick; Model/Retries update on lifecycle refetch, not per node; a brand-new run appears one debounce cycle after `instance.started`.

---

## Self-Review

- **Spec coverage:** Backend event enrichment (Task 1) ✓; system-WS envelope instanceId (Task 2) ✓; frontend in-place patch on both `node.started` and `node.failed` (Task 3) ✓; ignore-unknown-instance + lifecycle refetch retained (Task 3 tests) ✓; `WebSocketEvent.instanceId` (Task 3 Step 1) ✓; non-goals (Elapsed ticker, live Model/Retries) explicitly left out and verified as accepted gaps (Task 4) ✓.
- **Placeholder scan:** none — every code/test step carries complete code and exact commands.
- **Type consistency:** `nodeLabelAndBackend(node, defaultBackend)` defined in Task 1 and reused at both engine emit sites; `systemWSEnvelope` fields (`instanceId/type/timestamp/data`) match the frontend `WebSocketEvent` (`type/timestamp/data/instanceId`) and the patch reads `data.{nodeId,nodeLabel,backend}` exactly as Task 1 emits them.
