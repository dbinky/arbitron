# Remote Runners — Design Phase 5: Dashboard

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md` (Administration and visibility)
**Idea doc (context only):** `docs/ideas/remote-runners.md` §13
**Depends on:** Phases 1–4 (every data source rendered here must be implemented there; see unresolved provenance in §5).
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
  - A logged-out source, plus incompatible software, disk/transfer errors and blocked recovery/drain items that require operator attention.
  - Runs waiting · capacity, each with its reason.
  - Runners that are offline, suspect or incompatible, each with its explanation.
- **Model-group cards:** group by credential kind AND actual model family, with individual connection sources below them. LunaRoute is a route/credential kind, not a model family; preserve exact requested models in capacity-wait details.
  - Each shows the healthy source count, in flight, queued, a next-reset countdown, and a spread bar per runner.
  - LunaRoute busy (429s) renders as pressure, never as exhaustion.
- **Runner cards:**
  - State: online, suspect or lost; cordoned or draining; incompatible with its reason.
  - Version, heartbeat age, slots used out of the limit, live CPU and memory.
  - A chip per source with its state and countdown.
  - Count of homed workspaces.
  - Drain progress, when draining.
- **Recent pool events:** each with its cause (actor or classified outcome).
- **Runner keys (admin only):** the list (name, runner or "unclaimed", created, last used), plus the Add-runner and Revoke modals (§3.4). The secret response is never retained in shared data-hook caches, browser storage, URLs, logs or events.
- **Local mode:** the same page with a single built-in card, which still shows auth and capacity status (spec: "pool remains useful with only the local machine").

### 3.2 Runner detail (`routes/RunnerDetail*`)
- **Telemetry:** sparklines from the recent telemetry window, and current stats: CPU, memory and pressure, load, free workspace disk, thermal state and uptime.
- **Machine:** hardware capacity, runner and harness versions, last contact and health checks per source.
- **Assignments:** homed workspaces (linked to runs), pinned interactive sessions, and drain progress with blocking capacity.

### 3.3 Queue and run-graph additions
- **Queue:**
  - A **Runner** column showing the active operation's assigned runner first; show workspace home and interactive pin separately. A hermetic chat may execute away from its workspace home, and simultaneous branch assignments need distinct rows/details.
  - An **execution-status** badge: queued · slot; waiting · capacity with reason and countdown; moving workspace with progress; recovering.
  - Interactive rows show their pin.
  - Parallel groups show each branch's runner.
- **Run graph:**
  - Nodes are coloured by the runner they ran on; server-side pure nodes are grey.
  - Move badges sit on the edges where the workspace moved.
  - The node panel shows the placement reason, the source, the workspace's history of homes, any lossy session-move notice, and the durable recovery disclosure that interrupted work may repeat external effects.

### 3.4 Runner-key modals (`components/RunnerKey*`)
- **Add runner:**
  1. Enter a unique name, then Create.
  2. The secret is shown **once**, obscured, with a Copy button, a "won't be shown again" warning, and the `runner.env` snippet and setup guidance (including the workspace-root requirement).
  - Cancelling before Create makes no API call.
  - Closing after Create discards the secret from memory.
- **Revoke:** a confirmation stating that the runner disconnects immediately and that affected work recovers automatically, possibly repeating interrupted steps. It recommends draining first.

### 3.5 Pool data hooks (`hooks/usePool*`, `hooks/useRunner*`)
- **Job:** load REST snapshots, then invalidate and reload authoritative snapshots on relevant system WebSocket events. When the socket is disconnected, show a stale-data banner and poll.

## 4. Data flow
1. A route mounts and the hook fetches REST snapshots: `runner_list` / `runner_get` (Phase 1), keys (Phase 2), the pool event log (Phase 1), and the enriched instance list (Phase 1 execution status).
2. The hook subscribes to `/api/v1/ws` and invalidates snapshots on events: `runner.*` and `source.*` (Phases 1–2), `execution.status` (Phases 1 and 3), `workspace.moved` (Phase 3), and session moves (Phase 4).
3. Countdowns are computed client-side from reset timestamps.
4. The run graph reads each visible owner's trace placement fields (Phase 1), the home history (Phase 3) and session moves (Phase 4) through the extended trace and instance APIs. Phase 1/3/4 extensions and P5 T7 per-owner trace loads are required producers, not values inferred by the UI.

## 5. Data Provenance

Repository verification below records the pre-implementation baseline: **UNRESOLVED DEPENDENCY** labels identify inputs that had no implemented producer or verified source when this design was written. The reviewed implementation-task ownership, runtime wiring and sink proofs are in `docs/plans/remote-runners/2026-11-20-contracts.md` §§19–23. A named, fully specified producer task closes a dependency for the planning gate; it does not claim that code already exists. Real evidence/platform decisions still marked `ESCALATION:` in those tasks remain unresolved and must be reported. Upstream gaps propagate to their consumers; a fallback closes a gap only when it delivers the spec outcome.

| Rendered leaf input | Required production producer | Client/root consumer and sink proof |
|---|---|---|
| Runner identity/labels/limits/liveness/compatibility/reason | P1 registry/service/DTO, P2 hub/admission, roots P1 T16/P2 T10 | P5 T1 decoder → T2 authoritative snapshots → T3/T5 views → T8 App/real-root browser |
| Sources/model families/exact-model wait/auth/exhaustion/reset/headroom/probe time | P1 inventory/classifiers/HealthMonitor and service DTO; remote P2 sources/heartbeats | T1/T3/T5; reset countdown never mutates health; unavailableReason is hard unavailable cause, distinct from logout |
| Operation assignments/slots/queued/distribution | P1 durable ledger/ExecutionStatusProjector and enricher; P3 resource moves; P4 pending turn/move | T2/T6 authorized list snapshots; active chat runner, workspace home, session pin and parallel jobs stay distinct |
| CPU/memory/pressure/load/disk/thermal/uptime/cores/RAM/current timestamp and recent window | P2 OS telemetry adapters, Hub.Latest/Window and built-in loop; P2 T12 DTO/window | T1 decode → T3/T5 shared timestamp freshness helper; stale/zero/future/unsupported samples are unavailable even with connected socket |
| Alerts and persisted disk/transfer/blocked recovery/drain causes | P1 pool event log/monitor, P3 mobility/recovery/drain projections, P4 session recovery | T3 poolModel and detail view display actual server reasons; no client inference from empty measurements |
| Events/actor and visible run/resource/session links | P1 EventRecorder/Service.Events, `identity.FromContext`, HTTP filtered system stream | T1/T2 event-log display; server visibility filtering authoritative; browser never receives hidden identity |
| Key metadata/create plaintext/revoke result/setup | P2 KeyService and REST/admin audit; installer/system config setup fields | T4 modals keep secret in lifetime-bound component state only; T8 real server create/list/stream canary proof |
| Current viewer admin flag | Existing MeHandler.Me → meView.IsAdmin | T4 controls; server rechecks every mutation |
| Run state/current node baseline | Existing Instance/InstanceEnricher/InstanceState | T6 preserves existing state vocabulary; execution states remain separate projection |
| Trace placement and failed attempts | P1 dispatcher placement sink → execution record/trace DTO; P3 completion receipts | T7 root AND visible branch/child traces loaded through existing getTrace/get; keyed by owner+definition node |
| Home/move history and exact move-to-operation linkage | P3 HomeStore.History/WorkspaceHistorySource; targetJobId/invocationId/operationId and generations in P3 T3/T6/T12 | T7 joins exact placement job; drain-only moves remain history without invented execution edge |
| Runner detail homes/drain progress/pinned sessions | P3 T12/T14 service projections; P4 T14 exact active session index | T5 getRunner response, no nil-index release workaround |
| Interactive pin/context loss/restart/current generation | P4 T1/T6/T8 durable fields; P4 T9/T12 full-identity events/history | T7 actual useInteractiveSession clears partial output/discards superseded frames; InteractivePanel persists notices after reload; T8 browser sink |
| REST/event reconciliation/polling | Existing system socket; P1–P4 authorized list/detail snapshots | T2 events invalidate serialized/coalesced loads. No cross-endpoint atomic cursor is inferred from timestamp or event-log ID; epoch guards discard stale HTTP responses |

All rows have named planned producers, root wiring and final consumers. They remain mandatory implementation scope. Missing/nil/empty production input must fail the relevant root sink or render unavailable, never masquerade as healthy/default data. Optional RR-13/RR-14 native-fidelity research does not block supported visible recovery.

### Deferred to plans
- **Read endpoints consumed:** `runner_events` (Phase 1 §3.14) for the pool event log and the home history on instance and trace reads (Phase 3 §3.1). This phase adds no server endpoints.
- **TypeScript API types and WebSocket event decoding** for all new event kinds.
- **Running `make ui`** and committing the regenerated `adapter/dashboard/dist`.
- **Visual details** (palette for runner colours, layout of the card grid), following the existing CSS-module conventions.

## 6. Integration seams
- The dashboard consumes only REST and events defined in Phases 1–4. It introduces no server endpoints of its own.
- The key modals are the only writing UI, and they call the Phase 2 REST endpoints.

## 7. Error handling
- **A WebSocket disconnect or failed REST freshness read** shows a stale-data banner; disconnected sockets fall back to polling.
- **A failed REST load** shows an inline error with a retry.
- **A key create failure** shows the error and does not open the secret panel. If copying fails, the modal says so and keeps the secret visible until it is dismissed.
- **A revoke failure** shows the error and leaves the key listed.
- **Unknown event kinds** are ignored, never crashed on.

## 8. Testing — STRICT TDD (all five categories required)
Write and run each Vitest test to fail first, implement the minimum change, rerun to green, then refactor while green. Run `npx tsc --noEmit`, focused Vitest coverage and `make ui`; verify the regenerated embedded dashboard. All five categories below are required.
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

- **Read-model regressions:** hidden-run links/events never render; active chat runner differs visibly from workspace home; simultaneous branch assignments remain distinct; logged-out plus exhausted capacity is not falsely healthy after auth returns; built-in rollback, session loss notices, recovery errors and blocked drain/capacity render from durable server fields; the secret never enters persistent browser storage.
