# Latest Model Profiles — Design Phase 1: Settings & Run Pinning

> **Current alignment (2026-10-19):** D1–D11 and the current closure map in Phase 3 govern implementation. Historical completion/blocker records are not a present inventory. Existing runtime producers are extended in place; upstream producer completion never waits for downstream acceptance. D8 permits native Azure/Bedrock configured-ID pinned execution while rejecting following/usability claims.

Spec: `docs/specs/2026-09-29-latest-model-profiles-spec.md`

## Goal

Persist editable catalog and model-announcement settings, and freeze the specific
model version selected at run creation. Later automatic advancement must affect
new runs while every node and interactive turn of an active run retains its
starting model. Infrastructure errors must never substitute a newer live model.

## Architecture

Follow the existing domain ports, SQLite adapters, MCP/HTTP handlers, dashboard,
and `cmd/arbitron/main.go` composition root.

### System settings

| Unit | Responsibility |
|---|---|
| New `domain/settings.SystemSettings` port and SQLite `SettingsRepo` | Persist named settings in a new `system_settings` table. |
| New `domain/settings.Service` | Validate values, enforce admin access on public reads/writes, return a redacted public view, and provide a separate internal raw-value read for background services. |
| New MCP `system_settings_get` / `system_settings_update` and HTTP `GET` / `PUT /system/settings` | Delegate to the service; never expose the raw webhook. Extend `adapter/mcp/tools_system.go` and the HTTP system routes. |
| Dashboard settings card | Extend `dashboard/src/routes/Settings.tsx` and `dashboard/src/api/client.ts:api.system` to edit the two settings. |

- `model_catalog.url`: default `https://models.dev/api.json`; absolute HTTP(S)
  URL, with fetch-time SSRF enforcement in Phase 3.
- `model_announcements.teams_webhook_url`: default **empty**, disabling model
  announcements. Validate non-empty values with
  `domain/config/teams.go:ValidateTeamsWebhookURL`; redact public responses with
  `RedactWebhookURL`. No webhook is supplied during this work.
- Updates distinguish omission (retain), empty webhook (clear), and a supplied
  replacement. The redacted placeholder is never saved as a URL. Saving only
  the catalog URL must retain a configured webhook.

The existing run-notification `domain/config/settings.go:SettingsStore` remains
separate. An internal read error is an error, not an empty configured value.

### Run pinning

| Unit | Responsibility |
|---|---|
| Instance model pins | New immutable alias-to-specific-model-version map (`model_pins` nullable JSON; NULL/version 0 legacy, non-NULL/version 1 including `{}` new) plus a pin-format/version discriminator on `domain/instance/instance.go:Instance`, persisted in the creation transaction. Distinguish legacy instances from a valid empty map on a new run. |
| Trigger stamping | Extend `domain/engine/engine.go:Engine.TriggerWithDepartment` with a separate profile-model snapshot dependency. Its existing `resolver` is a workflow ID resolver, **not** a `ProfileResolver`. Capture one coherent profile snapshot and pin each non-empty model-profile alias in the workflow, including spawn input preparation. Pass the exact captured workflow to SnapshotModels and VersionRequestsForWorkflow; unrelated saved/active workflows are not trigger constraints. Persist before enqueue. |
| New `PinningResolver` | Implement `provider.ProfileResolver`; resolve the live binding for credentials and other fields, but replace its model from this instance's pin. A missing required pin or pin-read error returns an error before provider invocation. |
| New `PinLookup` adapter | Read immutable pins through the instance repository; cache only successful reads, release cache entries on run completion, and reload persisted pins after restart. |
| Interactive continuation integration | In `domain/engine/conversation.go:conversationHandler.HandleMessage`, attach the authorized, loaded instance ID to context before resolving the profile. Wiring a decorator alone does not supply this ID. |

The requested model ID and any D8 opaque-route exception are pinned; credentials, connection metadata, effort,
context window and tool scope retain existing live-resolution semantics. If a
live routing edit cannot accept the pinned model, fail explicitly rather than
fall back to another version. Mutable harness aliases such as `sonnet` or `latest` are not specific versions.
P3T1 recognizes supported explicit version IDs; mutable aliases/deployments with
no proved fixed executable referent are rejected for new runs. No automatic alias
recovery producer is assumed or required. An admin can provide a specific version.

A spawned child is a **new workflow run** and captures fresh pins at child
creation; it does not inherit stale parent pins. The parent's child-input
preparation call still uses the parent's pins. Retry, pause/resume and restart
reuse the same persisted pins; `Engine.Rerun` creates fresh pins.

## Data flow

```
trigger -> coherent live profile snapshot -> specific version pins
        -> Instance.Create (snapshot + pins together) -> enqueue
node / spawn input preparation / interactive continuation
        -> context with authorized instance ID -> PinningResolver
        -> live binding with pinned Model -> provider or harness
spawned child / rerun -> new trigger -> fresh pins
```

The resolution consumers are `AgentCallExecutor.Execute`,
`LLMCallExecutor.Execute`, `InteractiveExecutor.Execute`,
`conversationHandler.HandleMessage`, and `LLMChildInputPreparer.Prepare` in
`domain/engine/`. Inject the decorated resolver into all five; workflow
validation and trigger snapshot capture use undecorated sources.

## Data Provenance

All paths below are relative to the repository root. Existing sources are
`file:symbol`; planned producers name their implementation task; unresolved external evidence or design policy is marked separately. These are plans, not claims of shipped code.

| Leaf input | Existing source or unresolved producer |
|---|---|
| Admin identity | `domain/identity/identity.go:FromContext`, `Identity.IsAdmin`; service-gating pattern in `domain/provider/authz.go:requireAdmin` (package-private, not callable from `domain/settings`). |
| Submitted settings | Existing boundary patterns: `adapter/mcp/tools_system.go:systemUpdateTeamsConfigHandler`, `dashboard/src/routes/Settings.tsx:Settings`, `dashboard/src/api/client.ts:api.system`; **PLANNED PRODUCER P1-SETTINGS (P1T1–P1T2)**: new handlers, persisted settings/default constants and separate internal raw reader do not exist. |
| URL validation/redaction | `domain/config/teams.go:ValidateTeamsWebhookURL`, `RedactWebhookURL`. Catalog URL validation is part of P1-SETTINGS. |
| Workflow aliases | `domain/workflow/workflow.go:NodeConfig.ModelProfile`, loaded and serialized by `domain/engine/engine.go:Engine.TriggerWithDepartment`. |
| Live profile models/bindings | `adapter/provider/cache.go:ProfileCache.get`, `ProfileCache.Load`; `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve`. Existing P1-PINS sources include `adapter/provider/cache.go:ProfileCache.SnapshotProfiles`, `adapter/provider/model_snapshot.go:ModelSnapshotter.SnapshotModels`, `adapter/provider/pin_lookup.go:PinLookup.Lookup`, `domain/provider/pinning.go:PinningResolver.Resolve` and `domain/engine/conversation.go:conversationHandler.HandleMessage`. P1T3–P1T4 extend fail-closed creation/effective-route validation and complete exact execution/root tests. |
| Immutable referent of a mutable model alias | **PLANNED PRODUCER P1-VERSION (P3T1 + P1T3; policy source P3T8; D2)**: `DynamicProfileResolver.Resolve` copies `Profile.Model` verbatim; `adapter/claudecli/claude.go:buildArgs` forwards it. Neither resolves `sonnet` to a fixed version. Plans must provide verified alias-to-version evidence or require a specific-version starting model; never claim the string alias is a version pin. |
| Instance ID at node execution | `domain/engine/executor_timer.go:ContextWithInstanceID`, `InstanceIDFromContext`, called by `domain/engine/engine.go:Engine.run`. For continuation, `domain/engine/conversation.go:conversationHandler.HandleMessage` receives the ID but needs the P1-PINS context change. |
| Persisted instance/restart state | `adapter/sqlite/instance_repo.go:InstanceRepo.Create`, `InstanceRepo.Get`; new pin fields remain P1-PINS. |
| Existing active runs without pins | **RESOLVED P1-LEGACY** (decisions D1: a NULL pin record marks a legacy run, which keeps live resolution until it finishes): `domain/instance/instance.go:NodeExecution.LLMModel` records some executed calls (some adapters echo the requested alias), but no existing producer records every alias's immutable starting version. D1 explicitly accepts live resolution for NULL-pin legacy runs; never invent historical pins. New runs persist non-NULL JSON/version=1 and cannot fall back to this branch. |

## Integration and plan obligations

P1-SETTINGS migration 019 is already implemented. P1-PINS preserves existing migration
020, with upgrade coverage on a DB already recording 021/023/024; update actual
instance repository SQL and all sqlc queries/generated conversions. Do not edit
applied migrations or backfill historical pins. Wire both resolver forms explicitly
in `cmd/arbitron/main.go`. Phase 2 and Phase 3 depend on P3T1 explicit-version recognition and P1T3/P1T4
pin implementation; D1 has resolved the legacy policy. Active-run stability
applies to post-migration runs, with the spec's explicit legacy rollout exception. No automatic refresh rewrites pins.

## Error handling

Invalid values and non-admin access fail before persistence. Settings/storage
errors surface as service errors. Unresolvable required aliases or versions
block creation before enqueue; a pin-read error, missing required pin, or corrupt
pin payload fails the call without invoking a live replacement. A workflow with
no model aliases legitimately has an empty map. Legacy runs (NULL pin record)
resolve live, per decisions D1; new runs never do. Calls outside a run, such as workflow
validation, continue to use live resolution.

## Testing — strict TDD

For each change, write a failing behavioral test, run it and observe the expected
failure, implement the minimum change, then refactor with the test green.

- **Happy path:** a run started on version X keeps X after advancement to Y;
  a new run uses Y, including interactive follow-up turns.
- **Success:** settings survive restart, public reads redact and internal reads
  return the real webhook; child input preparation uses X while a new child
  pins Y; all five resolution consumers use the decorator.
- **Failure:** invalid settings, non-admin access, unresolved alias/version and
  missing required pins are rejected without writes or provider calls.
- **Error:** pin/settings repository faults and corrupted pins produce explicit
  errors with no live-model fallback; creation failure never enqueues.
- **Edge cases:** retry/resume/restart retain pins, rerun re-pins; empty-map versus
  legacy discriminator; partial setting updates preserve the webhook; concurrent
  advancement during trigger uses one profile snapshot; mutable alias recovery
  and legacy rollout follow the resolved P1-VERSION/P1-LEGACY contracts.

Use domain fakes, SQLite tests on a migrated database, MCP/HTTP and dashboard
settings tests, and engine/continuation integration tests. Do not substitute
fixture-only alias claims for real producer verification.

## Implementation alignment (2026-09-29)

- P1T1: [2026-09-29-latest-model-profiles-implementation-phase-1-task-1](../plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md).
- P1T2: [2026-09-29-latest-model-profiles-implementation-phase-1-task-2](../plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-2.md).
- P1T3: [2026-09-29-latest-model-profiles-implementation-phase-1-task-3](../plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-3.md).
- P1T4: [2026-09-29-latest-model-profiles-implementation-phase-1-task-4](../plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-4.md).

The execution/coverage map in `2026-09-29-latest-model-profiles-design-phase-3.md` is authoritative for cross-phase ordering. A planned producer closes task ownership; its implementation and final-sink tests remain required before release. Every task has named happy/success/failure/error/edge tests and a final-sink wiring obligation.

New-run legacy fallback is forbidden even if the snapshotter/gate was omitted at construction: P1T3 replaces the existing captureModelPins nil fallback with an error, and all properly wired no-alias runs save `{}`/1. Existing SnapshotProfiles takes []string and returns map[string]Profile. Snapshot version checks use the coherent captured Profile and actual route providers/backend precedence, through P3T3's policy-independent route-only VersionRequests. P1T4/P3T7 inject these real inputs; the shared engine read lease is not recursively reacquired. Reuse already existing migration 020, pin resolver/lookup/continuation context and root wiring; implement remaining sink/enforcement tests without duplicating them.

## D8 pin-route persistence

P1T3 adds ModelPinSnapshot{Models,OpaqueRoutes}, Instance.ModelPinRoutes and migration 031_model_pin_routes without editing applied 019–030. SnapshotModels(ctx,aliases,wf) returns that coherent record. OpaqueRoutes maps an alias to its existing provider.Canonicalize native family (azure or bedrock, including all documented aliases), only for a pinned selection whose actual workflow routes share that family. Existing version-1 rows with no route exceptions stay strict; NULL/0 stays legacy. P1T4 carries the exception as internal ResolvedBinding.OpaquePinRoute while retaining Pinned and captured Model. Actual factory/selector routes must match the persisted family before native adapters accept configured-ID execution without proof. Missing/corrupt data cannot relax a strict pin; explicit probe evidence requirements still reject opaque paths. Tests drive both positive native pinned calls and negative following/probe/live-route changes through root, persistence, restart and output/continuation sinks.
