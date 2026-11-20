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
- **Job:** load REST snapshots, then apply live events from the system WebSocket. When the socket is disconnected, show a stale-data banner and poll.

## 4. Data flow
1. A route mounts and the hook fetches REST snapshots: `runner_list` / `runner_get` (Phase 1), keys (Phase 2), the pool event log (Phase 1), and the enriched instance list (Phase 1 execution status).
2. The hook subscribes to `/api/v1/ws` and merges events: `runner.*` and `source.*` (Phases 1–2), `execution.status` (Phases 1 and 3), `workspace.moved` (Phase 3), and session moves (Phase 4).
3. Countdowns are computed client-side from reset timestamps.
4. The run graph reads the trace placement fields (Phase 1), the home history (Phase 3) and session moves (Phase 4) through the extended trace and instance APIs. Their runner fields do not exist today; Phase 1/3/4 read-model extensions are prerequisites, not data the UI can infer.

## 5. Data Provenance

Repository verification distinguishes existing producers from required implementation. **UNRESOLVED DEPENDENCY** means the named value/capability has no implemented producer or verified source yet; a proposed component, earlier phase or research observation does not resolve it. The RR identifiers group cross-phase plan tasks; every occurrence remains open until implementation/source verification. Upstream unresolved inputs propagate to their consumers.

| Rendered leaf input | Verified producer or explicit dependency |
|---|---|
| Runner identity/labels/limits, availability/compatibility and reason | **UNRESOLVED DEPENDENCY RR-01/RR-05/RR-16:** Phase 1 registry/service and Phase 2 admission/hub REST snapshots/events must be implemented. No existing `runner_list`/`runner_get` endpoint supplies these fields. |
| Per-model-family sources, exact-model capacity reasons, auth/exhaustion/headroom/reset windows and last health check | **UNRESOLVED DEPENDENCY RR-02/RR-03/RR-16:** upstream inventory/classifier/prober and read DTOs. Browser countdown uses the server's normalized absolute reset timestamp; elapsed time alone does not turn a source green. |
| Active/queued operations, slot usage, per-machine distribution | **UNRESOLVED DEPENDENCY RR-01/RR-16:** derive from queued intents and offered/running assignments (not workflow counts); preserve separate active chat runner, workspace home, session pin and parallel branches. |
| CPU/memory/pressure/load/disk/thermal/uptime/cores/RAM, version and timestamp samples | **UNRESOLVED DEPENDENCY RR-05/RR-06/RR-16:** Phase 2 OS probes, hub recent-sample window and read/events. All OS metrics currently lack producers, including the Linux built-in equivalents. Unknown/stale/unsupported values render explicitly. |
| Logged-out alerts and actionable compatibility/disk/drain/recovery failures | **UNRESOLVED DEPENDENCY RR-03/RR-08/RR-16:** upstream monitor/mobility event producers and persisted reasons; no client inference from missing measurements or a dropped socket. |
| Recent events/causes, actor names and run/workspace/session links | **UNRESOLVED DEPENDENCY RR-01/RR-16:** bounded pool event log, service authorization and filtered REST/WebSocket DTOs. Hidden run IDs/names/prompts must not enter browser payloads; actor identity originates in `domain/identity/identity.go:FromContext`. |
| Key metadata (name, claimed machine/runner, created and last use), one-time secret/create and revoke results | **UNRESOLVED DEPENDENCY RR-05/RR-16:** Phase 2 key service/admin-auth REST DTOs are absent. Read secret only from the create response, discard on close; no event or key-list recovery of it. |
| Revocation confirmation and setup snippet | Spec-backed UI copy plus **UNRESOLVED DEPENDENCY RR-05/RR-07:** validated installer config fields and workspace-root instructions. Confirmation must explain immediate disconnect, repeated effects and drain recommendation and send the required service acknowledgment. |
| Run state and current-node baseline | `domain/instance/instance.go:Instance`; `domain/engine/enricher.go:InstanceEnricher`; `dashboard/src/api/types.ts:InstanceState`. Capacity/slot/move/recovery are separate execution projections, not additional InstanceState values. |
| Per-operation wait/move/recovery state/reason/reset/progress | **UNRESOLVED DEPENDENCY RR-01/RR-08/RR-12/RR-16:** Phase 1 queued intents, Phase 3 progress/recovery, Phase 4 pending turns and extended instance read models. Idle interactive capacity recovery must also render while InstanceState is waiting. |
| Per-node runner/source/reason and attempt history | Existing record `domain/instance/instance.go:NodeExecution`; **UNRESOLVED DEPENDENCY RR-16:** Phase 1 trace placement fields/HTTP DTOs and live assignment events are absent today, including failed attempts and self-persisted interactive first turns. |
| Home history, branch assignments and drain progress/blockers | **UNRESOLVED DEPENDENCY RR-08/RR-11/RR-16:** Phase 3 resource/history/drain projections. Independent branches originate in `domain/engine/group_launcher.go:GroupLauncher`, not a single parent runner field. |
| Session pin, restart/loss notice and restoration error | **UNRESOLVED DEPENDENCY RR-12/RR-13/RR-16:** Phase 4 conversation/move fields, durable notices and events; reasoning stripping and uncertain Claude context emit notices even if import succeeds. |
| Current viewer's admin flag | `adapter/http/me_handler.go:MeHandler.Me` emits `meView.IsAdmin` from `domain/identity/identity.go:Identity.IsAdmin`; `dashboard/src/api/types.ts:Me` supplies the client shape. The server enforces key authorization regardless of UI flags. |
| Event delivery and polling fallback | Existing transport `dashboard/src/hooks/useSystemWebSocket.ts:useSystemWebSocket` and `adapter/http/system_stream_handler.go:SystemStreamHandler.HandleWebSocket`; polling precedent `dashboard/src/hooks/useGraphState.ts:GRAPH_STATE_POLL_MS`. **UNRESOLVED DEPENDENCY RR-16:** runner event schemas/snapshot reconciliation need server and client producers, including version/cursor handling to avoid replaying stale updates after REST reload. |

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
