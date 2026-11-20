# Remote Runners — Design Phase 5: Dashboard

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md` (Administration and visibility)
**Idea doc (context only):** `docs/ideas/remote-runners.md` §13
**Depends on:** Phases 1–4 (every data source rendered here is produced there).
**Cross-phase decisions:** see Phase 1 §0, especially D6 (execution status) and D12 (read and admin boundary).

## 1. Purpose and scope

Give operators live, read-only views of the pool, plus the one interactive exception: runner-key management.

In scope:
- **Pool view:** alerts, model-group cards, runner cards, recent events, and the keys section with the Add-runner and Revoke modals.
- **Runner-detail view.**
- **Queue additions:** a Runner column, execution-status badges, interactive pins and branch placement.
- **Run-graph additions:** runner colouring, workspace-move badges, and placement reason and home history in the node panel.
- **Local-mode rendering**, then `make ui`.

Out of scope:
- Dashboard controls for cordon, drain, limits or probes. Those stay in MCP and REST, because the dashboard is read-only except for keys.
- Historical usage charts (spec non-goal). Sparklines show only the recent in-memory window.

## 2. Existing architecture this phase extends

| Seam | Location | How this phase uses it |
|---|---|---|
| Routes | `dashboard/src/App.tsx` (:18–24) | Adds `/pool` and `/pool/runners/:id`. |
| Nav | `dashboard/src/components/NavBar.tsx` (:13–45) | Adds a Pool item. |
| System live stream | `dashboard/src/hooks/useSystemWebSocket.ts` (`/api/v1/ws`); server `adapter/http/system_stream_handler.go` | Adds handlers for `runner.*`, `source.*`, `execution.status`, `workspace.moved` and session-move events. |
| Polling fallback precedent | `dashboard/src/hooks/useGraphState.ts` (`GRAPH_STATE_POLL_MS`) | Same fallback pattern while the WebSocket is down. |
| Queue | `dashboard/src/routes/QueueView.tsx`, `components/QueueSection.tsx`, `QueueTableRow.tsx`, `components/queueColumns.ts` (status column :66–71), `StateBadge.tsx` | Adds a Runner column and an execution-status badge next to the run state. |
| Run graph | `dashboard/src/routes/ExecutionView.tsx`, `graph/arranged/ArrangedGraph.tsx`, `graph/nodes/ArbitronNode.tsx` | Adds runner colour per node, edge move badges and node-panel placement. |
| API types | `dashboard/src/api/types.ts` (`InstanceState` :4, `SystemConfig` :194) | New types for runners, sources, keys, execution status and placement. |
| Admin awareness | Existing identity and admin flags exposed to the dashboard | Hides key controls for non-admins. The server enforces it regardless. |
| Build | `make ui` → `adapter/dashboard/dist` | Run after source changes. |

## 3. Components

### 3.1 Pool view (`routes/Pool*`)
- **Alerts:**
  - A logged-out source, which is the only state that needs a human.
  - Runs waiting · capacity, each with its reason.
  - Runners that are offline, suspect or incompatible, each with its explanation.
- **Model-group cards:** Anthropic, OpenAI, LunaRoute and server connections.
  - Each shows the healthy source count, in flight, queued, a next-reset countdown, and a spread bar per runner.
  - LunaRoute busy (429s) renders as pressure, never as exhaustion.
- **Runner cards:**
  - State: online, suspect or lost; cordoned or draining; incompatible with its reason.
  - Version, heartbeat age, slots used out of the limit, live CPU and memory.
  - A chip per source with its state and countdown.
  - Count of homed workspaces.
  - Drain progress, when draining.
- **Recent pool events:** each with its cause (actor or classified outcome).
- **Runner keys (admin only):** the list (name, runner or "unclaimed", created, last used), plus the Add-runner and Revoke modals (§3.4).
- **Local mode:** the same page with a single built-in card, which still shows auth and capacity status (spec: "pool remains useful with only the local machine").

### 3.2 Runner detail (`routes/RunnerDetail*`)
- **Telemetry:** sparklines from the recent telemetry window, and current stats: CPU, memory and pressure, load, free workspace disk, thermal state and uptime.
- **Machine:** hardware capacity, runner and harness versions, last contact and health checks per source.
- **Assignments:** homed workspaces (linked to runs), pinned interactive sessions, and drain progress with blocking capacity.

### 3.3 Queue and run-graph additions
- **Queue:**
  - A **Runner** column (the workspace's home or the active job's runner).
  - An **execution-status** badge: queued · slot; waiting · capacity with reason and countdown; moving workspace with progress; recovering.
  - Interactive rows show their pin.
  - Parallel groups show each branch's runner.
- **Run graph:**
  - Nodes are coloured by the runner they ran on; server-side pure nodes are grey.
  - Move badges sit on the edges where the workspace moved.
  - The node panel shows the placement reason, the source, the workspace's history of homes, and any lossy session-move notice.

### 3.4 Runner-key modals (`components/RunnerKey*`)
- **Add runner:**
  1. Enter a unique name, then Create.
  2. The secret is shown **once**, obscured, with a Copy button, a "won't be shown again" warning, and the `runner.env` snippet and setup guidance (including the workspace-root requirement).
  - Cancelling before Create makes no API call.
  - Closing after Create discards the secret from memory.
- **Revoke:** a confirmation stating that the runner disconnects immediately and that affected work recovers automatically, possibly repeating interrupted steps. It recommends draining first.

### 3.5 Pool data hooks (`hooks/usePool*`, `hooks/useRunner*`)
- **Job:** load REST snapshots, then apply live events from the system WebSocket. When the socket is disconnected, show a stale-data banner and poll.

## 4. Data flow
1. A route mounts and the hook fetches REST snapshots: `runner_list` / `runner_get` (Phase 1), keys (Phase 2), the pool event log (Phase 1), and the enriched instance list (Phase 1 execution status).
2. The hook subscribes to `/api/v1/ws` and merges events: `runner.*` and `source.*` (Phases 1–2), `execution.status` (Phases 1 and 3), `workspace.moved` (Phase 3), and session moves (Phase 4).
3. Countdowns are computed client-side from reset timestamps.
4. The run graph reads the trace placement fields (Phase 1), the home history (Phase 3) and session moves (Phase 4) through the existing trace and instance APIs.

## 5. Data Provenance

| Rendered input | Producer |
|---|---|
| Runners, sources, health, slots, step limit, labels, cordon or drain state, compatibility reason | Phase 1 registry and runner service (`runner_list`/`runner_get` REST); Phase 2 hub liveness and compatibility; Phase 3 drain |
| Live telemetry and sparklines | Phase 2 hub telemetry window (latest plus recent samples), surfaced through `runner_get` and heartbeat events |
| Thermal state, memory pressure | Phase 2 darwin adapter. Its OS source is a Phase 2 **UNRESOLVED DEPENDENCY**; the UI shows "unavailable" when absent. |
| Reset countdowns | Phase 1 source health (reset time from the classifiers and Codex rate-limit reads) |
| Logged-out alert | Phase 1 health monitor; the notification is also delivered through `domain/notification` |
| Recent events with causes | Phase 1 pool event log, extended by Phases 2–4 |
| Keys list (claimed, runner, created, last used) | Phase 2 key service REST |
| Key secret (once) | Phase 2 `runner_key_create` REST response |
| Execution status per run | Phase 1 projection via `domain/engine/enricher.go:InstanceEnricher`; Phase 3 adds moving and recovering |
| Runner per node and placement reason | Phase 1 `NodeExecution` placement fields via the trace endpoints |
| Workspace moves and home history | Phase 3 home-history records |
| Interactive pin and lossy-move notice | Phase 4 `LLMConversation` additions and events |
| Admin flag (to hide key controls) | Existing identity exposure to the dashboard; server-side enforcement in the Phase 2 key service |

### Deferred to plans
- **Read endpoints consumed:** `runner_events` (Phase 1 §3.14) for the pool event log and the home history on instance and trace reads (Phase 3 §3.1). This phase adds no server endpoints.
- **TypeScript API types and WebSocket event decoding** for all new event kinds.
- **Running `make ui`** and committing the regenerated `adapter/dashboard/dist`.
- **Visual details** (palette for runner colours, layout of the card grid), following the existing CSS-module conventions.

## 6. Integration seams
- The dashboard consumes only REST and events defined in Phases 1–4. It introduces no server endpoints of its own.
- The key modals are the only writing UI, and they call the Phase 2 REST endpoints.

## 7. Error handling
- **A WebSocket disconnect** shows a stale-data banner and falls back to polling.
- **A failed REST load** shows an inline error with a retry.
- **A key create failure** shows the error and does not open the secret panel. If copying fails, the modal says so and keeps the secret visible until it is dismissed.
- **A revoke failure** shows the error and leaves the key listed.
- **Unknown event kinds** are ignored, never crashed on.

## 8. Testing — STRICT TDD (all five categories required)
Tests are written first in Vitest, with the `npx tsc --noEmit` gate.
- **Happy path:** the pool renders a one-runner local pool with healthy sources.
- **Success:**
  - Create a key: it is shown once, Copy works, and closing discards it.
  - Live events update runner cards.
  - Countdowns tick.
  - The queue shows a waiting · capacity badge with its reason.
- **Failure:**
  - A logged-out source raises an alert.
  - An incompatible runner shows its explanation.
  - A draining runner shows blocking capacity.
- **Error:**
  - A WebSocket drop shows the stale banner and polling resumes.
  - A failed revoke shows the error.
  - A failed create shows no secret.
- **Edge-case:**
  - Cancelling the Add-runner modal makes no API call.
  - An unclaimed key renders as unclaimed.
  - A non-admin user sees no key controls.
  - Telemetry "unavailable" renders gracefully.
  - LunaRoute busy renders as pressure, not exhaustion.
  - Server-side pure nodes render grey.
