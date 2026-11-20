# Remote Runners — Design Phase 1: Runner seam, built-in runner and capacity health

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md`
**Idea doc (context only):** `docs/ideas/remote-runners.md`
**Evidence:** `docs/evidence/remote-runners/` (transport, exhaustion, session migration)
**Phase set:** 1 Runner seam, built-in runner & capacity health · 2 Remote runners: connectivity & keys · 3 Workspace mobility & recovery · 4 Interactive session continuity · 5 Dashboard.

## 0. Decisions that shape every phase (approved in the brainstorm)

| # | Decision |
|---|---|
| D1 | **Step-level dispatch inside today's per-run goroutine.** The engine still walks the graph on the server and the workflow-level queue (`QueueConfig{GlobalCap, PerUserCap}`) is unchanged. Each execution step is placed, slotted, recorded and dispatched by a new domain service. Not a node-scheduler rewrite; not run-level pinning. |
| D2 | **The server's workspace directory is the checkpoint of record, and every runner uses the same absolute workspace root.** `{{workspace}}` keeps its current value. The built-in runner works in place; remote runners pull before a step and push changed files after it (Phase 3). |
| D3 | **The seam is placement-aware decorators on the existing selector ports, plus one new `ProcessRunner` port** for `script`/`tool_call`. Step identity travels on the context, stamped by the engine; a missing identity fails closed. |
| D4 | **Capacity sources are per runner and per credential kind:** Anthropic subscription, OpenAI subscription, LunaRoute (a remote runner uses its own `lr_` key; the built-in runner uses the profile's existing server connection), and server-connection sources (all other connection-backed bindings) that run **only on the built-in runner**. The built-in runner has a step limit like every runner (default 8, configurable). |
| D5 | **Classified source outcomes are handled inside the step:** exhausted or logged-out means mark the source and re-place without spending `retries`; transient or LunaRoute busy means backoff on the same source (Retry-After honoured), then an ordinary step error. |
| D6 | **The run stays `running`.** Wait states are an *execution-status* projection from the job ledger, not new `InstanceState` values. |
| D7 | Server restart is treated as losing the built-in runner (Phase 3). **Phase 1 keeps today's restart behaviour.** |
| D8 | Runner keys bind to a runner-generated machine keypair (Phase 2). |
| D9 | `script` bundles: the script's resolved directory travels as a content-addressed bundle; passthrough env comes from the runner (Phase 3). |
| D10 | Stay-versus-move uses explicit tiers with a configurable load threshold and a workspace size cap (Phase 3). |
| D11 | An interrupted interactive turn rolls the conversation back; the workspace stays at its last checkpoint (Phase 4). |
| D12 | Reads are open to any authenticated user (run-scoped data filtered by existing run visibility); administration and keys are admin only and audited in the `domain/runner` service. |

### Spec ↔ idea-doc reconciliation (spec wins)
- **Version skew:** the idea doc left it open. The spec decides: incompatible runner or harness versions stay visible with an explanation and get no new work. There is no self-update (out of scope).
- **"Byte-for-byte unchanged" local mode** (idea §15) is narrowed by the spec's per-runner execution limit. The single intended local change is that the built-in runner enforces its step limit (default 8). Local mode also gains the new visible wait states and source health; otherwise its behaviour is unchanged.
- **LunaRoute key placement:** the idea doc puts an `lr_` key in each `runner.env`. Today LunaRoute is a server-side `Connection`. Per D4, both exist: the server connection backs the built-in runner's LunaRoute source, and runner-local keys back remote ones. No key ever crosses machines.
- **Pool events:** the idea doc lists "recent pool events" only as a dashboard feature. Their producer is placed here (§3.9) so that Phase 5 is fed.

## 1. Purpose and scope

Introduce the runner seam and run today's execution through it as a **one-runner pool** (the built-in runner), with capacity sources, health, placement, wait states and the operator surface. After this phase every execution step goes through placement and the job ledger in production, including in local mode, before a second machine exists.

In scope: the pool model and registry, the job ledger with generation fencing, the `StepDispatcher`, the selector decorators, the `ProcessRunner` port, the built-in `StepRunner`, structured outcome classification in the claudecli and codex adapters, the supported-harness-version table, the source health monitor with logged-out alerts, the execution-status projection and events, the pool event log, placement fields in the trace, and the MCP/REST runner tools (list, get, update, cordon, uncordon, source probe).

Out of scope: remote connectivity and keys (Phase 2), workspace transfer, moves, failover, drain and restart re-attach (Phase 3), session portability and the durable turn lease (Phase 4), and dashboard views (Phase 5).

## 2. Existing architecture this phase extends

| Seam | Location | How this phase uses it |
|---|---|---|
| Per-step retry loop | `domain/engine/engine.go:Engine.executeWithRetry` (:1507) | Stamps step identity (run, step, attempt) on the context before `Execute`. It still writes `NodeExecution` after return, now with placement fields. |
| Interactive turn entry | `domain/engine/conversation.go:conversationHandler.HandleMessage` (:221; selector call at :305) | Stamps step identity (run, step, turn) for later interactive turns, which run outside `executeWithRetry`. |
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
  - **Model group:** credential kind plus model family.
  - **Health states:** available; exhausted until T; exhausted with unknown reset; probation; logged out.
- **Rule:** at most one source per credential kind per runner. Exhaustion applies to subscription sources only; LunaRoute and server-connection sources are never exhausted.
- **Depends on:** nothing outside the domain.

### 3.2 Runner registry (port in `domain/runner`, SQLite adapter)
- **Job:** persist runners, their configuration (step limit, labels, cordon) and their sources with health. It is read by placement and the operator surface.
- **Built-in runner:** registered at boot. Its sources are derived from what the server can execute:
  - Anthropic subscription when the claude CLI is present.
  - OpenAI subscription when `codex.Readiness` reports it is installed.
  - One LunaRoute source per existing LunaRoute connection.
  - One server-connection source per other connection.
- **Interface shape:** read the pool snapshot, read or update a runner's configuration, record a source health transition.

### 3.3 Job ledger (port in `domain/runner`, SQLite adapter)
- **Job:** the durable record of each placed step execution.
  - **Fields:** run, step, attempt, runner, source, generation, state (offered, running, completed, failed, fenced), lease deadline, placement reason.
  - **Lifecycle:** the record is written **before** dispatch and is the only basis for slot counting.
- **Fencing:** every runner-originated mutation passes through **one** fence check (run and step identity plus generation). This makes the check structural rather than per-handler (idea §17 risk).
- **Slot accounting:** derived from non-terminal job rows per runner. Waiting runs (timer, child, human) have no job, so they hold no slot.
- **Workspace homes:** the ledger also reserves room for each run's workspace home and its generation. That record is populated from Phase 3; in Phase 1 the home is always the built-in runner.

### 3.4 Placement (pure domain function in `domain/runner`)
- **Job:** given a step's model group, workspace affinity and a pool snapshot, return **either** a (runner, source, reason) placement **or** a wait reason.
- **Wait reasons:**
  - queued · slot: an eligible source exists but no slot is free.
  - waiting · capacity: no eligible source. The reason names the missing group and the next known reset.
- **Rules:**
  - Two gates: a free slot **and** a healthy source of the step's model group.
  - Spread-first: most headroom wins.
  - Never crosses model families.
  - Excludes cordoned, suspect, lost and incompatible runners.
  - Server-connection groups are eligible only on the built-in runner.
  - Probation sources take one step at a time.
- **Pure:** no I/O; it is unit-testable with table tests. The stay-versus-move tiers (D10) are added in Phase 3 behind the same function.

### 3.5 StepDispatcher (domain service in `domain/runner`)
- **Job:** turn "run this step on a source of group G" into a placed, recorded, executed and classified result.
- **Flow:**
  1. Read the step identity from the context; if it is missing, fail with a typed error.
  2. Place the step. If it must wait, publish execution status and block until pool state changes, honouring context cancellation (so pause and cancel work).
  3. Write the job row.
  4. Invoke the chosen runner's `StepRunner`.
  5. On a classified outcome:
     - **exhausted / logged out:** transition the source (and alert), then re-place with generation + 1. This does **not** consume `retries`.
     - **transient / busy:** bounded backoff on the same source; then an ordinary error.
     - **other:** an ordinary error.
  6. Close the job row and return the result plus the placement facts used for the trace.
- **Idempotency key:** (run, step, attempt). This lets Phase 3 adopt live jobs after a restart.
- **Depends on:** registry, ledger, placement, the `StepRunner` port, the event bus and a clock.

### 3.6 Runner-routing selector decorators (`domain/runner`, wired in `cmd/arbitron`)
- **Job:** wrap the three existing selector ports.
  - A decorated `ChatProviderFor` / `AgentProviderFor` / `StreamProviderFor` returns a provider whose `Chat` / `StreamChat` / `RunAgent` derives the **model group** from the `ResolvedBinding`:
    - `ConnectionRef == ""` with the claude backend: Anthropic subscription.
    - With the codex backend: OpenAI subscription.
    - LunaRoute provider: LunaRoute.
    - Otherwise: server-connection `<ref>`.
  - It then delegates to the `StepDispatcher`.
- **Phase 1 affinity:** chat and agent calls are pinned to the built-in runner because no other runner exists yet. In Phase 2, chat calls become hermetic and placeable anywhere.
- **Consumers unchanged apart from stamping:** the executors and the spawn input preparer need no code changes. The two non-executor callers (`conversationHandler` turns and `GroupResolutionBridge.StartResolution`) gain only the step-identity stamp (§5).
- **Group resolution:** this is an `interactive_agent` stream that edits a server-local staging directory. It is routed with built-in-runner affinity in every phase, never as hermetic chat.
- **Exempt paths:** probe and validation paths (`adapter/probe`, `adapter/provider/path_inventory.go`) keep the undecorated selectors. They are not steps, carry no step identity, and must not consume slots.

### 3.7 ProcessRunner port (`domain/engine`) and its routing
- **Job:** "run this rendered command with this env, stdin, working directory and timeout; return stdout, stderr and exit code."
- **Callers:** `ScriptExecutor` and `ToolCallExecutor` call it instead of `adapter/process/runner.go:run`. The adapter implementation is today's `run()`.
- **Routing:** the routing wrapper sends it through the `StepDispatcher` with **no model group**. A process step needs only a slot on the workspace's home, which in Phase 1 is the built-in runner.

### 3.8 Built-in runner (`adapter/runner/builtin`)
- **Job:** implement the domain `StepRunner` port in-process.
  - Chat, agent and stream jobs call the **inner** (undecorated) providers from the existing factory.
  - Process jobs call the `ProcessRunner` adapter.
  - Classified outcomes from the adapters are returned to the dispatcher.
- **What does not change:** working directories (the in-place server workspace) and env construction.

### 3.9 Pool event log (`domain/runner`, SQLite adapter)
- **Job:** a bounded, persisted record of pool events with their cause, for the Phase 5 pool view and for MCP reads. Each event names the cause: a source state change and its classified outcome, an operator action and its actor, or a placement re-route.
- **Extension:** Phases 2–4 add connects and disconnects, moves, drains and session moves.
- **Fed by:** the dispatcher, the health monitor and the runner service, which publish to the event bus and append here through one recorder.

### 3.10 Harness outcome classification (`adapter/claudecli`, `adapter/codex`)
- **Job:** produce a domain `SourceOutcome`: exhausted (with window and reset), logged out, transient, or other. It is built **from structured fields only**, never prose, and is attached to the error before `SanitizeProcessError`.
  - **Claude:** parse `rate_limit_event`, the assistant `error` kind, `api_error_status` and `quotaLimits` / `rate_limit_info`. `authentication_failed` means logged out. `overloaded`/529, and the 429 "temporarily limiting requests" without `quotaLimits` rejected, are transient. `rate_limit_event` with `allowed_warning` is recorded as a headroom hint for placement.
  - **Codex:** after a failed turn, read the rollout file (located through `findRollout`) for `codex_error_info`:
    - `usage_limit_exceeded`: exhausted.
    - `unauthorized`: logged out.
    - `server_overloaded`, `rate_limit_exceeded` and `response_stream_disconnected`: transient.
    - The reset time comes from `resets_at`.
  - **Windows:** keyed by length (5-hour, 7-day), not primary or secondary.
- **Supported-harness-version table:** owned by these adapters, next to their fixtures. It seeds codex from `minCodexVersion` and adds a Claude floor.

### 3.11 Source health monitor (`domain/runner` service, probe adapters)
- **Job:** keep source health current and return sources to service automatically.
  - **Periodic probe:** about every 60 seconds per source, plus on demand through `runner_source_probe`:
    - Claude: an auth-status check.
    - Codex: `codex.Readiness` plus `codex app-server` `account/rateLimits/read`, which spends no tokens.
    - LunaRoute: the existing connection probe.
  - **Exhausted until T:** moves to probation at T (plus jitter); the first successful step restores available.
  - **Exhausted with unknown reset:** re-checked periodically.
  - **Logged out:**
    - On entry: raise a notification of a new type (sibling to `TypeProfileRefusesTriggers`, Teams included).
    - On a successful probe: return to available and record the event.
- **Depends on:** the registry, a `SourceProber` port per credential kind, the notification service, and the event log.

### 3.12 Execution-status projection
- **Job:** per run, expose the active job's status (queued · slot, waiting · capacity with reason and reset, running on runner X) through `InstanceEnricher`, MCP and REST instance reads, and `execution.status` events. Phase 3 adds moving workspace and recovering.
- **Unchanged:** the run's `InstanceState` (D6).

### 3.13 Trace placement fields
- **Job:** `NodeExecution` gains runner, source and placement reason, set from the dispatcher's placement facts through `nodeResultToExecution`. They are exposed by `trace_get` / `trace_get_node` and the HTTP trace endpoints.
- **Event:** the `node.started` payload (`engine.go:1238`) also carries the runner.

### 3.14 Runner service and operator surface (`domain/runner` service, `adapter/mcp`, `adapter/http`)
- **Job:** the authorized boundary for pool reads and administration.
  - **Reads:** `runner_list`, `runner_get` (sources, health, slots, execution counts), and `runner_events` (the pool event log, newest first, bounded).
  - **Admin-only, audited:** `runner_update` (step limit and labels), `runner_cordon`, `runner_uncordon`, `runner_source_probe`.
  - **Pattern:** MCP tools and `/api/v1/runners` REST routes stay thin over the service, following the `instance_pause` pattern.
- **Visibility (D12):** run-scoped data inside runner reads (which runs are on a runner) is filtered by the existing run-visibility predicate.

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

| Input | Consumer | Producer (existing unless marked new) |
|---|---|---|
| Step identity (run, step, attempt) | Dispatcher, ledger | Three producers, each stamping onto ctx in this phase: `domain/engine/engine.go:Engine.executeWithRetry` (attempt loop at :1507) for node steps; `domain/engine/conversation.go:conversationHandler.HandleMessage` (:221) for later interactive turns; `domain/engine/group_resolution.go:GroupResolutionBridge.StartResolution` (:237) for resolution sessions |
| Model group: credential kind | Decorators, placement | `domain/provider/provider.go:ResolvedBinding.ConnectionRef` / `.Provider` / `.Backend`, produced by the resolver chain (`adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`, pinning resolver) |
| Model group: family | Placement | `domain/modelidentity` family classifier applied to `ResolvedBinding.Model` |
| Effective agent backend | Decorators | `domain/engine/executor_agent.go` effective-backend computation (:84–85), written into the binding before `AgentProviderFor` |
| Built-in sources: Anthropic subscription | Registry | Presence of the claude CLI binary as configured for `adapter/claudecli` in `cmd/arbitron/main.go` (:813–816) |
| Built-in sources: OpenAI subscription | Registry | `adapter/codex/readiness.go:Readiness` |
| Built-in sources: LunaRoute / server connections | Registry | `domain/provider.ConnectionStore` list (`adapter/sqlite/connection_repo.go`) |
| Step limit default (8) | Registry | **New** server config default in `domain/config` (no existing producer; this phase adds it) |
| Exhausted, logged-out and transient outcome (Claude) | Dispatcher, monitor | Claude CLI stream-json `rate_limit_event`, assistant `error`, `quotaLimits`, parsed by the extended `adapter/claudecli/streamjson.go:parseEvent` |
| Exhausted, logged-out and transient outcome (Codex) | Dispatcher, monitor | Codex rollout `codex_error_info`, located by `adapter/codex/rollout.go:findRollout` |
| LunaRoute busy plus Retry-After | Dispatcher backoff | `domain/provider/lunaroute_classify.go:ClassifyLunaRoute` (`RetryAfter` filled for Busy) |
| Codex proactive headroom | Monitor, placement | **New** `codex app-server` `account/rateLimits/read` client in `adapter/codex` (the vendor capability is verified in `research-exhaustion.md`; no production caller today) |
| Claude headroom hint | Placement | `rate_limit_event` status (`allowed_warning`) from the extended parser |
| Claude logged-out probe | Monitor | **New** auth-status probe in `adapter/claudecli` that runs the CLI's own status command; no existing producer |
| Codex minimum version | Version table | `adapter/codex/version.go:minCodexVersion` |
| Claude minimum supported version | Version table | **UNRESOLVED DEPENDENCY:** no Claude Code version floor exists in the repo, and the structured fields are evidenced only for 2.1.328. The plan must pin the floor from real fixtures. |
| Claude exhausted with unknown reset: re-check | Monitor | **UNRESOLVED DEPENDENCY:** Claude has no free usage endpoint the terms allow. The re-check must be a minimal probe job that spends tokens; the plan must define and bound it. |
| Logged-out alert delivery | Operator | `domain/notification` service plus the Teams notifier (`adapter/notification`); new type sibling to `TypeProfileRefusesTriggers` |
| Actor for audit | Audit | `domain/identity` context identity (`WithIdentity`), consumed by `domain/audit/service.go:Service.Record` |
| Run visibility for runner reads | Runner service | Existing run-visibility predicate used by `adapter/http/system_stream_handler.go:canSee` (:119) / `domain/engine/authz.go` |

### Deferred to plans
- **Composition-root wiring** in `cmd/arbitron/main.go`:
  - Construct the registry, ledger, event log, dispatcher, health monitor and built-in runner.
  - Wrap the three selectors (factory at :1033, interactive selector at :1376) before they reach executors, the spawn preparer and the group resolver.
  - Hand the inner selectors to the built-in runner and to probe/validation paths.
  - Pass the routed `ProcessRunner` to `NewScriptExecutor` / `NewToolCallExecutor` (:1082–1083).
  - Start the health monitor in `domainStack.startRuntime`.
- **SQLite schema and migrations** (after `039_…`) for runners, sources, the job ledger, workspace-home records and the pool event log, plus `NodeExecution` placement columns. sqlc queries included.
- **Built-in runner registration and source population** at boot from the binary checks and the connection store.
- **Config:** the step-limit default, probe interval, transient backoff bounds, and the event-log retention bound.
- **Execution-status fields** on enriched instance summaries, MCP/REST instance reads and the dashboard API types. Phase 5 renders them.
- **New notification type** and its Teams payload mapping (`adapter/notification/teams_payload.go`).
- **Fixtures** of real Claude stream-json and Codex rollout output per pinned version, using the real Codex 0.170 text (U+2019, `chatgpt.com/settings/usage`), replacing the inaccurate ASCII fixtures noted in the idea doc.

## 6. Integration seams
- **Decorator boundary:** consumers of the selector ports keep their contract. Only the composition root knows about routing.
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
- **Secrets:** the outcome class and reset time are safe to surface. Message text, credentials and tool output are never carried, which preserves the existing redaction rules on pinned and LunaRoute runs.

## 8. Testing — STRICT TDD (all five categories required)
Tests are written first for every component. Each category below is required.
- **Happy path:** in local mode, an existing workflow suite produces the same results and outputs as today, with every step recorded in the ledger on the built-in runner.
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
  - The probe and validation paths consume no slots.
- **Fixtures:** classifier tests run against captured real CLI output for each pinned harness version.
