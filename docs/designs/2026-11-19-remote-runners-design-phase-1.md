# Remote Runners — Design Phase 1: Runner seam, built-in runner and capacity health

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md`
**Idea doc (context only):** `docs/ideas/remote-runners.md`
**Evidence:** `docs/evidence/remote-runners/` (transport, exhaustion, session migration)
**Phase set:** 1 Runner seam, built-in runner & capacity health · 2 Remote runners: connectivity & keys · 3 Workspace mobility & recovery · 4 Interactive session continuity · 5 Dashboard.

## 0. Decisions that shape every phase (aligned to the spec)

| # | Decision |
|---|---|
| D1 | **Step-level dispatch inside today's per-run goroutine.** The engine still walks the graph on the server and the workflow-level queue (`QueueConfig{GlobalCap, PerUserCap}`) is unchanged. Each provider/process execution operation is placed, slotted, recorded and dispatched by a new domain service; pure graph/control nodes remain server-side. Not a node-scheduler rewrite; not run-level pinning. |
| D2 | **The server stores immutable committed workspace checkpoints, and every runner uses the same absolute execution workspace root.** `{{workspace}}` keeps its current value. The built-in runner works at that path in a mutable working copy separate from the committed checkpoint; all runners commit changed files after successful steps (Phase 3). |
| D3 | **The seam is placement-aware decorators on the existing selector ports, plus one new `ProcessRunner` port** for `script`/`tool_call`. Step identity travels on the context, stamped by the engine; a missing identity fails closed. |
| D4 | **Capacity sources are per runner and per credential kind:** Anthropic subscription, OpenAI subscription, LunaRoute (a remote runner uses its own `lr_` key; the built-in runner uses the profile's existing server connection), and server-connection sources (all other connection-backed bindings) that run **only on the built-in runner**. The built-in runner has a step limit like every runner (default 8, configurable). |
| D5 | **Classified source outcomes are handled inside the step:** exhausted or logged-out means mark the source and re-place without spending `retries`; transient or LunaRoute busy means backoff on the same source (Retry-After honoured), then an ordinary step error. |
| D6 | **Capacity waits preserve the existing lifecycle state.** Executing graphs stay `running`; interactive sessions may remain `waiting` while recovery is projected. Slot/capacity/move/recovery states are an *execution-status* projection from the ledger, not new `InstanceState` values. |
| D7 | Server restart is treated as losing the built-in runner (Phase 3). **Phase 1 keeps today's restart behaviour.** |
| D8 | Runner keys bind atomically to the first machine proving possession of its generated keypair (Phase 2). |
| D9 | `script` bundles: the script's resolved directory travels as a content-addressed bundle; passthrough env comes from the runner (Phase 3). |
| D10 | Stay-versus-move uses explicit tiers with a configurable load threshold and a workspace size cap (Phase 3). |
| D11 | An interrupted interactive turn rolls the conversation back; the workspace stays at its last checkpoint (Phase 4). |
| D12 | Reads are open to any authenticated user (run-scoped data filtered by existing run visibility); administration and keys are admin only and audited in the `domain/runner` service. |

### Spec ↔ idea-doc reconciliation (spec wins)
- **Version skew:** the idea doc left it open. The spec decides: incompatible runner or harness versions stay visible with an explanation and get no new work. There is no self-update (out of scope).
- **"Byte-for-byte unchanged" local mode** (idea §15) is narrowed by the spec's per-runner execution limit. The built-in runner enforces its step limit (default 8), gains visible waits/source health and checkpointed recovery, and needs no runner key/configuration. Phase 3 also gives tool_call the workspace cwd required by the spec. Preserve existing local model/tool/env controls and ordinary workflow outputs; do not claim byte-for-byte execution behavior is unchanged.
- **LunaRoute key placement:** the idea doc puts an `lr_` key in each `runner.env`. Today LunaRoute is a server-side `Connection`. Per D4, both exist: the server connection backs the built-in runner's LunaRoute source, and runner-local keys back remote ones. No key ever crosses machines.
- **Pool events:** the idea doc lists "recent pool events" only as a dashboard feature. Their producer is placed here (§3.9) so that Phase 5 is fed.

## 1. Purpose and scope

Introduce the runner seam and run today's execution through it as a **one-runner pool** (the built-in runner), with capacity sources, health, placement, wait states and the operator surface. After this phase every provider/process execution operation goes through placement and the job ledger in production, including in local mode, before a second machine exists.

In scope: the pool model and registry, the job ledger with generation fencing, the `StepDispatcher`, the selector decorators, the `ProcessRunner` port, the built-in `StepRunner`, structured outcome classification in the claudecli and codex adapters, the supported-harness-version table, the source health monitor with logged-out alerts, the execution-status projection and events, the pool event log, placement fields in the trace, and the MCP/REST runner tools (list, get, update, cordon, uncordon, source probe).

Out of scope: remote connectivity and keys (Phase 2), workspace transfer, moves, failover, drain and restart re-attach (Phase 3), session portability and the durable turn lease (Phase 4), and dashboard views (Phase 5).

### Spec coverage across the phase set

These are required contracts; unresolved implementation/source tasks remain in §5.

| Spec requirement/outcome | Owning design sections |
|---|---|
| Automatic macOS startup, local harness/accounts, credentials staying local; macOS-only remotes and preserved Linux server | Phase 2 §§3.4, 3.7–3.8, §5 RR-05–RR-07; Phase 1 §§1, 3.8 |
| Default eight-step limit, configurable capacity, no slots for idle waits | Phase 1 §§3.1–3.5, 3.14 |
| Spread by workload/headroom/health; exact model and family controls | Phase 1 §§3.1, 3.4–3.6; Phase 2 §3.6 |
| Hermetic chat; workspace/process affinity and capacity/load moves | Phase 2 §3.6; Phase 3 §§3.1–3.4 |
| Independent parallel branches and child placement | Phase 1 operation identities; Phase 3 §§2, 3.3–3.5; Phase 5 §3.3 |
| Interactive pins move only for exhaustion/logout/drain/loss | Phase 4 §§3.3–3.5, 3.8 |
| Move instead of waiting when an eligible alternative exists; visible missing capacity/reset | Phase 1 §§3.4–3.5, 3.12; Phase 3 §§3.2, 3.5; Phase 4 §7 |
| Confirmed automatic subscription recovery, logout alert/login restoration, transient LunaRoute/provider retries | Phase 1 §§3.10–3.11, 7 |
| Incompatible runner/harness visible with reason and no assignments | Phase 1 §3.10; Phase 2 §§3.4, 7; Phase 5 §3.1 |
| Recoverable successful workspace steps and interactive context | Phase 3 §§3.3, 3.5; Phase 4 §§3.1–3.3, 3.6 |
| Suspect near 30 s, loss near configurable 2 min, brief interruptions do not move work | Phase 2 §3.4; Phase 3 §§3.1–3.2; Phase 4 §3.4 |
| Preserve completed steps; automatically repeat interrupted work or wait visibly | Phase 3 §§3.3, 3.5; Phase 4 §§3.3, 4 |
| Interrupted turn restores last successful state and resends exact message; full available context/fallback/loss notice | Phase 4 §§3.1–3.6, 4, 7 |
| Single workspace/session owner, stale-result rejection, concurrent-turn rejection | Phase 1 §3.3; Phase 3 §§3.1, 3.6; Phase 4 §3.3 |
| Restart reconnect/adoption without needless retries; disclose possible repeated external effects | Phase 2 §§3.1, 3.4; Phase 3 §§3.3, 3.5, 7; Phase 4 §4; Phase 5 §3.3 |
| Unique once-shown machine-bound runner-only keys, metadata, admin audit and informed revoke | Phase 2 §§3.2–3.3; Phase 5 §3.4 |
| Cordon/restore, limits/labels, health probes; safe drain with progress/capacity blockers | Phase 1 §3.14; Phase 3 §3.7; Phase 4 §3.8 |
| Dashboard read-only except keys; REST/MCP parity and existing visibility restrictions | Phase 1 §3.14; Phase 2 §3.3; Phase 3 §3.7; Phase 5 §§1, 6 |
| Live pool, detail metrics/hardware/versions/contact/checks, queue/move/recovery, graph/history/branch/session placement | Phase 1 §§3.9, 3.12–3.14; Phase 2 §§3.4, 3.7; Phase 3 §3.1; Phase 4 §§3.5, 7; Phase 5 §§3–5 |
| Useful local-only pool with no setup; non-goals unchanged | Phase 1 §1; Phase 2 §§1, 3.7; Phase 4 §1; Phase 5 §§1, 3.1 |

## 2. Existing architecture this phase extends

| Seam | Location | How this phase uses it |
|---|---|---|
| Per-step retry loop | `domain/engine/engine.go:Engine.executeWithRetry` (:1507) | Persists/stamps invocation and operation identity plus retry attempt before `Execute`. Extend completion to check persistence errors and commit/replay result/checkpoint/continuation (Phase 3); add placement facts to `NodeExecution`. |
| Interactive turn entry | `domain/engine/conversation.go:conversationHandler.HandleMessage` (:221; selector call at :305) | Stamps a distinct durable session/turn operation for later turns outside `executeWithRetry`; Phase 4 persists pending turn intents before dispatch. |
| Group resolution session | `domain/engine/group_resolution.go:GroupResolutionBridge.StartResolution` (:237), run from the gather reconciler; already attaches the run with `ContextWithInstanceID` (:266) | Stamps step identity (parent run, group step, resolution) next to the existing run-ID attachment. The context-value precedent is `ContextWithInstanceID`. |
| Chat / agent / stream selectors | `domain/engine/ports.go:ChatProviderSelector` (:63), `AgentProviderSelector` (:71), `InteractiveStreamSelector` (:78); concrete in `adapter/provider/factory.go:ChatProviderFor` (:192) / `AgentProviderFor` (:251) and `adapter/provider/interactive_stream_selector.go` | Wrapped by runner-routing decorators at the composition root. The inner selectors are unchanged. |
| Selector callers | `executor_llm.go:78`, `executor_agent.go:89`, `executor_interactive.go:128`, `conversation.go:305`, `child_input_preparer.go:110`, `group_resolution.go:276` | All are routed through placement with no code change in the callers, because they use the decorated selectors. |
| Process execution | `adapter/process/runner.go:run` (:33), used by `executor_script.go:ScriptExecutor` and `executor_toolcall.go:ToolCallExecutor` | Both executors call a new domain `ProcessRunner` port instead of `run()` directly. |
| Resolved binding | `domain/provider/provider.go:ResolvedBinding` (Provider, Backend, Model, `ConnectionRef`) | Source of the step's model group (credential kind and model family). |
| Model family classifier | `domain/modelidentity` family classifier (LunaRoute phase 1) | Family half of the model group; never crossed. |
| Claude stream parser | `adapter/claudecli/streamjson.go:parseEvent` (:130), which drops `rate_limit_event`, `quotaLimits` and `error` | Extended to surface structured outcome fields. |
| Codex failure path | `adapter/codex/parse.go:foldEvent` (:110), `failureCause` (:189); rollout locator `adapter/codex/rollout.go:findRollout` (:73) | Outcome read from the rollout's `codex_error_info` / `token_count.rate_limits`. |
| Error sanitizing | `adapter/claudecli/process_error.go:SanitizeProcessError` (:9) | The outcome *class* is attached before sanitizing; text is still dropped. |
| LunaRoute classification | `domain/provider/lunaroute_classify.go:ClassifyLunaRoute` (:213), `ErrLunaRouteBusy` (`lunaroute_errors.go:36`) | Mapped to the *transient* outcome (backpressure), never *exhausted*. |
| Codex readiness and version floor | `adapter/codex/readiness.go:Readiness` (:79), `adapter/codex/version.go:minCodexVersion` (:20) | Reused by the source probe; the floor seeds the version table. |
| Fence precedent | `domain/layout/store.go:Fence` (:83), lease and heartbeat in `domain/layout/worker.go` | Pattern for ledger generation checks. |
| Non-run notification | `domain/notification/notification.go:TypeProfileRefusesTriggers`, emitted at `cmd/arbitron/pin_preflight.go:56`; Teams via `adapter/notification` | Pattern for the new logged-out-source notification type. |
| Queue read model | `domain/engine/enricher.go:InstanceEnricher` (:40) | Adds execution status and runner to enriched run summaries. |
| Event bus | `domain/engine/ports.go:EventBus` (:319); system stream `adapter/http/system_stream_handler.go` with `canSee` (:119) | New `runner.*`, `source.*` and `execution.status` events. |
| Audit | `domain/audit/service.go:Service.Record` (:41) | `runner.update`, `runner.cordon`, `runner.uncordon` and `runner.source_probe` actions. |
| Admin check | `domain/identity/identity.go:IsAdmin` (:49); per-package `requireAdmin` precedent (`domain/provider/authz.go`) | `domain/runner` service authz. |
| MCP / REST pattern | `adapter/mcp/register.go:RegisterAllTools`; `adapter/http/server.go` `/api/v1` route group | New runner tools and handlers, thin over the service. |
| Composition root | `cmd/arbitron/main.go:initDomainWithModelDependencies` (:572); factory built at :1033; interactive selector at :1376; `eng.FreezeExecutors` at :1514 | Wiring point for decorators, registry, ledger, dispatcher and monitor. |

## 3. Components

### 3.1 Pool model (`domain/runner`)
- **Job:** the vocabulary.
  - **Runner:** name, built-in flag, step limit, labels, cordon state, compatibility state with reason, liveness.
  - **Capacity source:** credential kind, served model families, health state, in-flight count.
  - **Model group:** credential kind plus model family, for capacity aggregation only; never the complete execution eligibility key.
  - **Health:** auth status and capacity state are separate observations; capacity is available, exhausted until T, exhausted with unknown reset, or probation. Logged out blocks eligibility without erasing a known exhaustion/reset. Congestion is transient pressure, not exhaustion.
- **Source identity:** (runner, credential kind, credential reference). There is one local source per subscription kind, one per server LunaRoute connection, one per other server connection, and one remote LunaRoute source for its local key. Connection sources remain separate even when their families match. Exhaustion applies to subscription sources only; LunaRoute and server-connection sources are never exhausted.
- **Execution requirements:** preserve the complete resolved request: exact requested/pinned model and model-evidence rules, provider route, effective backend, context window, effort, allowed tools, and workspace/session affinity. Filter by advertised support for that request before ranking by headroom, workload and machine health. Unknown family/capability is never assumed eligible; existing native opaque-route behavior remains built-in unless its identity is proved. Pooling neither changes model pins nor bypasses existing family-separation checks.
- **Depends on:** nothing outside the domain.

### 3.2 Runner registry (port in `domain/runner`, SQLite adapter)
- **Job:** persist runners, their configuration (step limit, labels, cordon) and their sources with health. It is read by placement and the operator surface.
- **Built-in runner:** registered at boot. Its sources are derived from what the server can execute:
  - Anthropic subscription when the claude CLI is present.
  - OpenAI subscription when a separate binary/version probe finds Codex; use `codex.Readiness` for auth health, not binary inventory. It collapses incompatible and missing binaries into `NotInstalled`, so cannot explain version skew alone.
  - One LunaRoute source per existing LunaRoute connection.
  - One server-connection source per other connection.
- **Interface shape:** read the pool snapshot, read or update a runner's configuration, record a source health transition.

### 3.3 Job ledger (port in `domain/runner`, SQLite adapter)
- **Job:** the durable record of each placed step execution.
  - **Fields:** run, node, durable invocation ID, operation ID (including child-input preparation or session/turn identity), workflow retry attempt, runner, source, generation, state (queued, offered, running, completed, failed, fenced), lease deadline, placement reason, and durable result/checkpoint references. Graph back-edges allocate a fresh invocation; reconnect/restart reuses it.
  - **Lifecycle:** a queued intent is written **before** waiting. Placement and slot reservation are one transaction: validate runner/source eligibility, transition to offered and reserve capacity without exceeding the limit. Queued intents reserve no slot; offered/running attempts do. A rejected/fenced attempt releases its slot before re-placement, and duplicate acceptance/completion cannot double-count.
- **Fencing:** every runner-originated mutation passes through **one** fence check (durable operation identity, assigned runner and job generation, plus workspace/session generation when applicable). This makes the check structural rather than per-handler (idea §17 risk).
- **Slot accounting:** derived only from offered/running assignments per runner. Capacity/slot waits retain queued intents but no assignment; timer, child and idle human waits hold no slot. Re-placement closes the prior assignment first.
- **Workspace homes:** the ledger also reserves room for each run's workspace home and its generation. That record is populated from Phase 3; in Phase 1 the home is always the built-in runner.

### 3.4 Placement (pure domain function in `domain/runner`)
- **Job:** given a step's model group, workspace affinity and a pool snapshot, return **either** a (runner, source, reason) placement **or** a wait reason.
- **Wait reasons:**
  - queued · slot: an eligible source exists but no slot is free.
  - waiting · capacity: no eligible source. The reason names the missing group and the next known reset.
- **Rules:**
  - Gates: a free slot, a healthy source of the model group and verified support for the full execution requirements (§3.1).
  - Rank eligible sources using headroom, slot workload and machine health; missing telemetry/headroom is explicitly unknown, not zero usage or proof of health. Use deterministic fair tie-breaking so equal/unknown headroom still spreads independent jobs. Local mode requires no telemetry setup.
  - Never crosses model families.
  - Excludes cordoned, suspect, lost and incompatible runners.
  - Server-connection groups are eligible only on the built-in runner.
  - Probation sources take one step at a time.
- **Pure:** no I/O; it is unit-testable with table tests. The stay-versus-move tiers (D10) are added in Phase 3 behind the same function.

### 3.5 StepDispatcher (domain service in `domain/runner`)
- **Job:** turn "run this step on a source of group G" into a placed, recorded, executed and classified result.
- **Flow:**
  1. Read the step identity from the context; if it is missing, fail with a typed error.
  2. Persist/reuse its queued intent, then place the step. If it must wait, publish execution status and block until pool state changes, honouring context cancellation (so pause and cancel work).
  3. Atomically reserve a slot and write the offered assignment for the queued intent; retry placement if a competing reservation wins.
  4. Invoke the chosen runner's `StepRunner`.
  5. On a classified outcome:
     - **exhausted / logged out:** transition the source (and alert), then re-place with generation + 1. This does **not** consume `retries`.
     - **transient / busy:** bounded backoff on the same source; then an ordinary error.
     - **other:** an ordinary error.
  6. Return the provider/process result and placement facts to the executor for output validation; record a durable operation result. Successful execution is acknowledged only through the fenced engine completion hook after validation. Phase 3 couples this to checkpoint/result/continuation commit; Phase 4 extends it to turn commits. An adapter returning bytes is not yet a completed workflow step.
- **Idempotency key:** (run, invocation ID, operation ID, workflow retry attempt), persisted before dispatch. Re-placement changes the job generation, never the logical operation. This lets Phase 3 adopt live jobs and replay already committed results after restart without conflating loop visits or interactive turns.
- **Depends on:** registry, ledger, placement, the `StepRunner` port, the event bus and a clock.

### 3.6 Runner-routing selector decorators (`domain/runner`, wired in `cmd/arbitron`)
- **Job:** wrap the three existing selector ports.
  - A decorated `ChatProviderFor` / `AgentProviderFor` / `StreamProviderFor` returns a provider whose `Chat` / `StreamChat` / `RunAgent` derives the **model group** from the `ResolvedBinding`:
    - First recognize LunaRoute by canonical provider; never mistake its codex/pi backend for a subscription.
    - Any other nonempty `ConnectionRef`: server-connection `<ref>`, built-in only, regardless of backend.
    - No connection, tool-suppressed `claude-cli` chat or claude-code agent: Anthropic subscription (chat commonly has an empty backend).
    - No connection, codex agent: OpenAI subscription.
    - Other locally supported paths, including pi/opencode, retain their existing factory behavior on the built-in runner; their credential/capability/outcome mapping for remote placement is **UNRESOLVED DEPENDENCY RR-02**, not inferred from the backend name.
  - Model family comes from `domain/modelidentity/family.go:ClassifyFamily(binding.Provider, binding.Model)`. Model group is only a grouping key; execution eligibility also enforces §3.1's full request.
  - It then delegates to the `StepDispatcher`.
- **Phase 1 affinity:** chat and agent calls are pinned to the built-in runner because no other runner exists yet. In Phase 2, chat calls become hermetic and placeable anywhere.
- **Consumer contracts:** selector interfaces stay stable, but callers gain operation identity and completion/commit integration as needed; the spawn input preparer needs a distinct operation identity. The two non-executor callers (`conversationHandler` turns and `GroupResolutionBridge.StartResolution`) gain the durable operation-identity stamp (§5). The spawn input preparer uses its own sub-operation identity under the parent invocation.
- **Group resolution:** this is an `interactive_agent` stream editing a gather staging directory, never hermetic chat. It stays built-in in Phases 1–3; Phase 4 must give the staging resource the same checkpoint, ownership and session-move guarantees as other interactive work (§5 RR-11). A permanent built-in exception would prevent draining or recovering these sessions.
- **Exempt paths:** probe and validation paths (`adapter/probe`, `adapter/provider/path_inventory.go`) keep the undecorated selectors. Read-only discovery/auth/version probes are not workflow steps and consume no slots. Any token-spending quota-confirmation probe is real execution: give it a synthetic durable operation ID and reserve a runner slot through the dispatcher; do not bypass the execution limit.

### 3.7 ProcessRunner port (`domain/engine`) and its routing
- **Job:** "run this rendered command with this env, stdin, working directory and timeout; return stdout, stderr and exit code."
- **Callers:** `ScriptExecutor` and `ToolCallExecutor` call it instead of `adapter/process/runner.go:run`. The adapter implementation is today's `run()`.
- **Routing:** the routing wrapper sends it through the `StepDispatcher` with **no model group**. A process step needs only a slot on the workspace's home, which in Phase 1 is the built-in runner.

### 3.8 Built-in runner (`adapter/runner/builtin`)
- **Job:** implement the domain `StepRunner` port in-process.
  - Chat, agent and stream jobs call the **inner** (undecorated) providers from the existing factory.
  - Process jobs call the `ProcessRunner` adapter.
  - Classified outcomes from the adapters are returned to the dispatcher.
- **Working directories:** the existing absolute execution path is preserved. Phase 3 separates its mutable files from the immutable checkpoint. Preserve controlled local env construction; remote env construction occurs on the runner (Phase 3).

### 3.9 Pool event log (`domain/runner`, SQLite adapter)
- **Job:** a bounded, persisted record of pool events with their cause, for the Phase 5 pool view and for MCP reads. Each event names the cause: a source state change and its classified outcome, an operator action and its actor, or a placement re-route.
- **Extension:** Phases 2–4 add connects and disconnects, moves, drains and session moves.
- **Fed by:** the dispatcher, the health monitor and the runner service, which publish to the event bus and append here through one recorder.

### 3.10 Harness outcome classification (`adapter/claudecli`, `adapter/codex`)
- **Job:** produce a domain `SourceOutcome`: exhausted (with window and reset), logged out, transient, or other. It is built **from structured fields only**, never prose, and is attached to the error before `SanitizeProcessError`.
  - **Claude (required decoding; not yet implemented, RR-03):** fixtures are synthesized from the cited evidence (decided 2026-11-22: real per-version capture certification is a Non-Goal; unknown shapes fail closed to *other*); parse `rate_limit_event`, the assistant `error` kind, `api_error_status` and `quotaLimits` / `rate_limit_info`. `authentication_failed` means logged out. `overloaded`/529, and the 429 "temporarily limiting requests" without `quotaLimits` rejected, are transient. `rate_limit_event` with `allowed_warning` is recorded as a headroom hint for placement.
  - **Codex:** after a failed turn, read the rollout file (located through `findRollout`) for `codex_error_info`:
    - `usage_limit_exceeded`: exhausted.
    - `unauthorized`: logged out.
    - `server_overloaded`, `rate_limit_exceeded` and `response_stream_disconnected`: transient.
    - The reset time comes from `resets_at`.
  - **Windows:** keyed by limit identity, model scope and duration, not primary/secondary labels; normalize reset timestamps/units. Same-duration per-model buckets must not overwrite one another.
- **Supported-harness-version table:** owned by these adapters, next to real fixtures. Existing `minCodexVersion` is only the execution floor; `minLunaRouteCodexVersion` is a separate credential-safety floor. Neither proves rollout/RPC/session portability support. Pin runner capability floors from fixtures; Claude and new Codex capability floors remain **UNRESOLVED DEPENDENCY RR-03**. **Resolved 2026-11-22 (contracts §22 RR-03a/c):** floors stay evidence-inferred (Claude 2.1.209/2.1.278/2.1.284; Codex 0.170.0) and are enforced by `CheckVersion`; real per-version capture certification is a Non-Goal (human decision).

### 3.11 Source health monitor (`domain/runner` service, probe adapters)
- **Job:** keep source health current and return sources to service automatically.
  - **Periodic probe:** about every 60 seconds per source, plus on demand through `runner_source_probe`:
    - Claude: an auth-status check.
    - Codex: `codex.Readiness` plus `codex app-server` `account/rateLimits/read`, which spends no tokens.
    - LunaRoute: the existing connection probe.
  - **Exhausted until T:** reset time merely permits a single probation verification after jitter, never proves availability. Codex `ordinaryUsageAllowed == true` can confirm recovery; false retains exhaustion and null remains unknown regardless of percentages. Otherwise a successful bounded same-model probe/step confirms recovery. Never run ordinary parallel traffic until confirmed.
  - **Exhausted with unknown reset:** re-checked periodically with the bounded slot-counted confirmation probe (RR-03), not an auth-only check.
  - **Logged out:**
    - On entry: raise a notification of a new type (sibling to `TypeProfileRefusesTriggers`, Teams included).
    - **On a successful auth probe:** clear logged-out status; confirm capacity separately before returning to available, and record the event. Auth success alone cannot clear exhaustion.
- **Depends on:** the registry, a `SourceProber` port per credential kind, the notification service, and the event log.

### 3.12 Execution-status projection
- **Job:** per run, expose queued intents as well as active assignments' status (queued · slot, waiting · capacity with reason and reset, running on runner X) through `InstanceEnricher`, MCP and REST instance reads, and `execution.status` events. Phase 3 adds moving workspace and recovering.
- **Unchanged:** the run's `InstanceState` (D6).

### 3.13 Trace placement fields
- **Job:** `NodeExecution` gains runner, source and placement reason, set from the dispatcher's placement facts through `nodeResultToExecution`. They are exposed by `trace_get` / `trace_get_node` and the HTTP trace endpoints.
- **Event:** the existing `node.started` event precedes placement and cannot promise a runner. Publish `execution.status` when assignment is reserved, and on each re-placement; include durable operation ID and runner/source. Completed trace fields include the attempt/move history, including failures and first interactive turns that persist their own record.

### 3.14 Runner service and operator surface (`domain/runner` service, `adapter/mcp`, `adapter/http`)
- **Job:** the authorized boundary for pool reads and administration.
  - **Reads:** `runner_list`, `runner_get` (sources, health, slots, execution counts), and `runner_events` (the pool event log, newest first, bounded).
  - **Admin-only, audited:** `runner_update` (step limit and labels), `runner_cordon`, `runner_uncordon`, `runner_source_probe`.
  - **Pattern:** MCP tools and `/api/v1/runners` REST routes stay thin over the service, following the `instance_pause` pattern.
- **Visibility (D12):** run-scoped data inside runner reads (which runs are on a runner) is filtered by the existing run-visibility predicate.
- Filtering applies in the service to assignments, queue items, workspace/session links, placement/move history and event payloads before REST/MCP/WebSocket publication. Pool totals may be aggregate; never expose hidden run IDs, names or prompts through events. Existing `engine.canView` and HTTP `canSee` are package-private precedents, not an exported authorization service; the callable boundary is **UNRESOLVED DEPENDENCY RR-01**.

## 4. Data flow

1. **Engine:** `executeWithRetry` stamps the step identity on the context, then calls `NodeExecutor.Execute`. Interactive turns (`HandleMessage`) and resolution sessions (`StartResolution`) stamp it at their own entry points.
2. **Executor:** resolves the binding and asks a **decorated** selector for a provider, then calls `Chat` / `StreamChat` / `RunAgent`. A script or tool call calls the routed `ProcessRunner`.
3. **Decorator:** derives the model group from the binding and calls the `StepDispatcher`.
4. **Dispatcher, placement:** gets a placement or a wait.
   - On a wait: publish `execution.status`, block until registry or ledger changes wake it, or until the context is cancelled.
   - On a placement: write the ledger row (generation), then call the built-in `StepRunner`.
5. **Built-in runner:** calls the inner provider or process adapter, then returns the result plus a classified outcome.
6. **Dispatcher, outcome:** completes, re-places (exhausted or logged out: source transition, event log, alert), or backs off (transient). It closes the job row and returns the result with its placement facts.
7. **Engine:** writes `NodeExecution` with runner, source and reason.

The health monitor runs alongside: probes, then registry transitions, then events and the log, then dispatcher waits are re-evaluated.

## 5. Data Provenance

Repository verification below records the pre-implementation baseline: **UNRESOLVED DEPENDENCY** labels identify inputs that had no implemented producer or verified source when this design was written. The reviewed implementation-task ownership, runtime wiring and sink proofs are in `docs/plans/remote-runners/2026-11-20-contracts.md` §§19–23. A named, fully specified producer task closes a dependency for the planning gate; it does not claim that code already exists. Real evidence/platform decisions still marked `ESCALATION:` in those tasks remain unresolved and must be reported. Upstream gaps propagate to their consumers; a fallback closes a gap only when it delivers the spec outcome.

| Leaf input | Consumer | Verified producer or explicit dependency |
|---|---|---|
| Run/node and retry counter | Dispatcher identity | `domain/engine/engine.go:Engine.executeWithRetry`; run context in `domain/engine/executor_timer.go:ContextWithInstanceID` |
| Durable invocation, child-preparation operation, turn ID and generation | Ledger, deduplication, fencing | **UNRESOLVED DEPENDENCY RR-01:** the three stamping entry points exist (`domain/engine/engine.go:Engine.executeWithRetry`, `domain/engine/conversation.go:conversationHandler.HandleMessage`, `domain/engine/group_resolution.go:GroupResolutionBridge.StartResolution`), but no durable invocation/operation producer exists. Add it and persist/reuse it on restart; `attempt` resets on every loop visit. |
| Resolved model, provider, connection reference, model pin/evidence rules, context window, effort and tools | Eligibility and dispatch | `domain/provider/provider.go:ResolvedBinding`; `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`; `domain/provider/pinning.go:PinningResolver.Resolve` |
| Effective agent backend and selector defaults | Routing | `domain/engine/executor_agent.go:AgentCallExecutor.Execute`; `adapter/provider/interactive_stream_selector.go:InteractiveStreamSelector.agentStreamFor`; chat routing in `adapter/provider/factory.go:Factory.ChatProviderFor` |
| Actual model family | Eligibility | `domain/modelidentity/family.go:ClassifyFamily(routeProvider, model)`; unresolved family is not a known family or wildcard. |
| Connection metadata, separate credential references | Built-in source inventory | `domain/provider/connection.go:ConnectionStore.List` (metadata only); **UNRESOLVED DEPENDENCY RR-02:** no runner source/capability inventory or safe binding-to-local-source mapper exists; connection presence proves neither supported models nor account availability. Preserve factory validation and built-in execution for existing supported paths. |
| Codex installation/login verdict | Subscription health | `adapter/codex/readiness.go:Readiness`; raw version producer `adapter/codex/version.go:compatibleVersion` is package-private. `Readiness` merges missing and incompatible binaries; RR-03 must expose separate version evidence. |
| Claude binary presence, login and supported local harness capability | Inventory, monitor | `cmd/arbitron/main.go:initDomainWithModelDependencies` configures adapters, not proof of an installed/logged-in CLI. **UNRESOLVED DEPENDENCY RR-02/RR-03:** implement explicit local binary/version/auth/capability probes; do not infer sources from configuration alone. **Resolved 2026-11-22 (RR-03b):** `claude auth status` shape observed on 2.1.329 (`docs/evidence/remote-runners/claude-auth-status.md`): `loggedIn:true`+exit 0 → ok, `loggedIn:false`+non-zero → logged_out, else unknown (P1 T13 `ClaudeAuthProber`). |
| Claude structured quota/auth/congestion fields and reset/headroom hints | Classifier, health | **UNRESOLVED DEPENDENCY RR-03:** `adapter/claudecli/streamjson.go:parseEvent` does not produce classified outcomes. Add fixture-backed decoding; the research's richer `quotaLimits` examples are session JSONL, not proof all fields reach stdout. Validate stream shapes and add a verified session-log reader if required. Evidence: `docs/evidence/remote-runners/research-exhaustion.md`, Claude §§A–C. **Resolved 2026-11-22 (RR-03a, decision C):** synthetic fixtures from the cited evidence plus fail-closed-to-other are the authoritative coverage; real capture certification is a Non-Goal (P1 T11). |
| Codex structured outcomes, reset windows, proactive headroom and recovery confirmation | Classifier, monitor | Existing locator `adapter/codex/rollout.go:findRollout` and stdout event folder `adapter/codex/parse.go:foldEvent` do not produce quota classification. **UNRESOLVED DEPENDENCY RR-03:** add rollout outcome reader and `account/rateLimits/read` client with fixtures, timestamp/window-unit normalization and `ordinaryUsageAllowed` tri-state semantics. Evidence: `docs/evidence/remote-runners/research-exhaustion.md`, Codex §§A–C. **Resolved 2026-11-22 (RR-03c/d):** observed 0.171.0 logged-out rollout (`task_complete.error.codex_error_info` `http_connection_failed.http_status_code:401` → logged_out) and `codex app-server` exchange (stdin must stay open until id 1; any id-1 error → capacity unknown) in `docs/evidence/remote-runners/codex-logged-out.md` and `codex-app-server.md` (P1 T12, P1 T13). |
| LunaRoute busy and retry-after | Backoff | `domain/provider/lunaroute_classify.go:ClassifyLunaRoute`; connectivity/auth probe selected by `adapter/probe/factory.go:ProbeFactory.ProbeFor` and implemented by `adapter/probe/lunaroute.go:lunaRouteProbe.Probe`. Probe success alone does not prove exact-model capacity. |
| Existing Codex execution/safety version floors | Compatibility seed | `adapter/codex/version.go:minCodexVersion` and `minLunaRouteCodexVersion`; neither certifies the newly required quota/RPC/export features. |
| Claude supported floor; Codex quota/RPC/session floors and captured fixtures | Compatibility | **UNRESOLVED DEPENDENCY RR-03:** no feature-specific compatibility table exists. Claude 2.1.328 and Codex 0.170.0 research are evidence, not a verified minimum-version matrix. Plans must pin and fixture-test these capabilities. **Resolved 2026-11-22 (RR-03a–d):** floors evidence-inferred and not certified; captured-fixture gate removed by human decision; observed evidence as above. |
| Claude exhaustion recheck when reset is unknown | Automatic return to service | **UNRESOLVED DEPENDENCY RR-03:** no free usage producer is implemented; define a bounded, token-spending same-model verification probe through the unmodified harness. Preserve the existing unresolved research constraint; auth status alone cannot prove quota recovery. |
| Limits (default 8), probe intervals/bounds, backoff, retention, ranking/fairness policy | Registry, dispatcher, monitor | **UNRESOLVED DEPENDENCY RR-04:** no runner config exists in `domain/config/config.go`; implement defaults, validation, runtime updates and clocks. Limits/headroom must not be invented from profiles. |
| Queued intents, atomic slot reservations, source health/headroom, wait/reset status, assignment/placement history and pool events | Placement, APIs, trace, later phases | **UNRESOLVED DEPENDENCY RR-01:** `domain/runner`, registry/ledger/event stores, dispatcher and execution-status producers do not exist. Implement their transactional and visibility contracts in this phase, including durable wait intents with no reserved slots. |
| Machine health/headroom ranking inputs | Placement | **UNRESOLVED DEPENDENCY RR-06:** OS telemetry adapter is a Phase 2 prerequisite for measured health/load; Phase 1 uses slot counts and verified source signals, with unknown telemetry explicit. Missing measurements cannot exclude the otherwise usable local pool. |
| Workspace affinity; commands/env/stdin/timeouts | Dispatch descriptors | `domain/engine/conventions.go:WorkDirFromVars`; `adapter/process/executor_script.go:ScriptExecutor.Execute`; `adapter/process/executor_toolcall.go:ToolCallExecutor.Execute`. **UNRESOLVED DEPENDENCY RR-09:** add the ProcessRunner port and descriptors; preserve script cwd, fix tool cwd in Phase 3, and construct remote base/passthrough env on the runner. |
| Actor/admin identity and existing run visibility semantics | Runner service authz, audit | `domain/identity/identity.go:FromContext`, `Identity.IsAdmin`; `domain/engine/authz.go:canView` and `adapter/http/system_stream_handler.go:SystemStreamHandler.canSee` are existing private precedents. **UNRESOLVED DEPENDENCY RR-01:** expose a service-owned callable visibility gate for runner-linked data/events rather than directly calling private helpers or duplicating handler-only authorization. |
| Logged-out notification type and delivery | Monitor | Delivery precedent `domain/notification/notification.go:TypeProfileRefusesTriggers`, audit sink `domain/audit/service.go:Service.Record`. **UNRESOLVED DEPENDENCY RR-03:** implement the new source-auth notification producer and Teams mapping; no logged-out-source event exists today. |
| Placement facts in trace/start/live projections | Engine, Phase 5 | Existing sink `domain/engine/engine.go:nodeResultToExecution` and `domain/engine/enricher.go:InstanceEnricher`; **UNRESOLVED DEPENDENCY RR-16:** extend persisted records, instance/trace DTOs and event schemas; pre-placement `node.started` has no assigned runner. |

### Open dependency register (for produce-plans)

This is the original producer checklist. Use the reviewed task-level ownership and remaining escalations in the shared contracts §§19–23 to determine planning closure; merely citing an earlier phase does not close an item.

| ID | Owning phase(s) | Required producer or verification |
|---|---|---|
| RR-01 | 1 | Durable invocation/operation identity; transactional ledger, queued intents/slots, registry/event stores and service visibility gate. |
| RR-02 | 1–2 | Source/capability inventory and safe exact-request-to-local-credential routing, including existing pi/opencode paths. |
| RR-03 | 1–2, 4 | Structured classifier/probe producers, real harness fixtures and feature-specific version floors; bounded Claude unknown-reset verification and auth alerts. |
| RR-04 | 1–4 | Validated runner limits, clocks, liveness/retry/lease/move/retention configuration and defaults. |
| RR-05 | 2 | Runner binary/config/install/autostart, key identity/atomic claim/auth routes, compatibility protocol/hub and retained-result/output replay. |
| RR-06 | 2 | Actual macOS and Linux built-in telemetry interfaces, units, timestamps and unavailable semantics, especially thermal/memory pressure. |
| RR-07 | 2 | Verified common absolute execution-root layout/permissions for Macs connecting to Linux-hosted servers. |
| RR-08 | 3 | Immutable workspace checkpoints, manifest/Git fidelity, resource ownership/history, transfers/tokens, drain progress and deletion inventory. |
| RR-09 | 1, 3 | ProcessRunner descriptors, script bundles/entrypoints/external references, command availability and runner-local env construction. |
| RR-10 | 3–4 | Crash-safe result/checkpoint/engine-continuation commit and replay, including persistence errors and completed-result adoption. |
| RR-11 | 3–4 | Group/gather mutation integration and portable, fenced group-resolution staging resources. |
| RR-12 | 4 | Durable turn intents, renewable/reclaimable leases, session generations and coherent turn commit/startup recovery. |
| RR-13 | 4 | Session locator/export/import/bundle adapters, Codex stripping/compaction handling and safe Claude absolute sidecar paths. |
| RR-14 | 4 | Real two-account resume/fidelity verification and optional account-fingerprint producer; conservative stripping/notices remain required. |
| RR-15 | 3 | Archive compression selection: vet/add zstd or explicitly choose gzip and verify round trips. |
| RR-16 | 1–5 | Filtered runner/queue/trace/key/session/drain REST/MCP DTOs, event schemas, durable notices and client reconciliation. |

### Deferred to plans
- **Composition-root wiring** in `cmd/arbitron/main.go`:
  - Construct the registry, ledger, event log, dispatcher, health monitor and built-in runner.
  - Wrap the three selectors (factory at :1033, interactive selector at :1376) before they reach executors, the spawn preparer and the group resolver.
  - Hand the inner selectors to the built-in runner and to probe/validation paths.
  - Pass the routed `ProcessRunner` to `NewScriptExecutor` / `NewToolCallExecutor` (:1082–1083).
  - Start the health monitor in `domainStack.startRuntime`.
- **SQLite schema and migrations** (determine the current migration head in the plan) for runners, sources, the job ledger, workspace-home records and the pool event log, plus `NodeExecution` placement columns. Use the existing hand-written SQL repository pattern.
- **Built-in runner registration and source population** at boot from the binary checks and the connection store.
- **Config:** the step-limit default, probe interval, transient backoff bounds, and the event-log retention bound.
- **Execution-status fields** on enriched instance summaries, MCP/REST instance reads and the dashboard API types. Phase 5 renders them.
- **New notification type** and its Teams payload mapping (`adapter/notification/teams_payload.go`).
- **Fixtures** of real Claude stream-json and Codex rollout output per pinned version, using the real Codex 0.170 text (U+2019, `chatgpt.com/settings/usage`), replacing the inaccurate ASCII fixtures noted in the idea doc.

## 6. Integration seams
- **Decorator boundary:** consumers of the selector ports keep their contract. The composition root selects routing implementations; callers participate in durable identity and completion without choosing runners.
- **`StepRunner` port:** the single seam that Phase 2's remote runner implements.
- **Ledger fence:** the single seam every runner-originated write in Phases 2–4 passes through.
- **Placement function:** the single place Phase 3 extends with stay-versus-move.
- **Execution-status projection and pool event log:** the read seams Phase 5 renders.

## 7. Error handling
- **Missing step identity** on a decorated call is a typed error; there is never a silent unplaced run. Probe paths use undecorated selectors.
- **Classification fails closed to *other*:** an ordinary step error, never "exhausted". Unknown or new JSON shapes therefore cannot park a healthy source.
- **Exhausted or logged out:** re-placement does not consume `retries`. It repeats while eligible sources exist; when none remain, the step waits for capacity visibly with the reason and next reset.
- **Transient or busy:** bounded backoff that honours Retry-After, then the workflow's existing `errorBehavior`.
- **Capacity waits:** never time out by themselves; pause and cancel end them through context cancellation.
- **A ledger write failure** fails the step. Execution never runs unrecorded.
- **Secrets:** the outcome class and reset time are safe to surface in health diagnostics. Raw provider error text, credentials and tool output never enter health/audit metadata, which preserves the existing redaction rules on pinned and LunaRoute runs.

## 8. Testing — STRICT TDD (all five categories required)
For every component and category below: write a failing test, run it to prove the intended failure, implement the minimum change, rerun to green, then refactor while green. Use fake clocks and fault injection at ownership/commit boundaries; do not substitute post-hoc tests for STRICT TDD. Each category below is required.
- **Happy path:** in local mode, an existing workflow suite produces the same results and outputs as today, with every dispatched provider/process operation recorded on the built-in runner. Pure graph/control nodes remain server-side and consume no runner slots.
- **Success:**
  - Placement picks the source with the most headroom.
  - An exhausted source causes a re-place onto another eligible source without spending `retries`.
  - A probation source returns to available after one successful step.
  - A logged-out source returns automatically after a successful probe.
- **Failure:**
  - A logged-out classification raises one notification and records an event.
  - With no eligible source, the step waits · capacity naming the group and reset.
  - A family mismatch is never placed.
  - A server-connection group is never placed off the built-in runner.
- **Error:**
  - A decorated call without step identity is rejected, and each of the three stamping sites (node step, interactive turn, resolution session) is covered by a test proving it reaches the dispatcher stamped.
  - A ledger write failure fails the step.
  - A malformed stream-json or rollout record classifies as *other*.
  - Cancelling a run while it waits ends the wait.
- **Edge-case:**
  - LunaRoute busy is never treated as exhaustion.
  - The ninth concurrent step queues · slot and starts when a slot frees.
  - A reset time already in the past goes straight to probation.
  - A Claude 429 "temporarily limiting" without `quotaLimits` rejected is transient.
  - A Codex message using ASCII `'` versus U+2019 makes no difference, because classification never reads prose.
  - Read-only probe/validation paths consume no slots; token-spending quota-confirmation probes do reserve slots and cannot oversubscribe the runner.
- **Fixtures:** classifier tests run against captured real CLI output for each pinned harness version.

- **Cross-phase contract regressions:** concurrent reservations never exceed eight; queued waits use no slots; loop revisits and child-preparation calls have distinct durable operation IDs; exact model pins/tool scope survive re-placement; LunaRoute-on-codex is never classified as subscription; two connections never share health; hidden-run data is absent from REST/MCP/events; pure graph nodes consume no slots.
