# Latest Model Profiles — Phase 1 Task 4: Pin resolver and every production execution consumer Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Pin resolver and every production execution consumer.

**Architecture:** Domain ports own contracts; adapters produce real inputs; the shared `cmd/arbitron/main.go:initDomain` composition root connects them. Execution order follows the dependency graph, not numeric phase order.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP/HTTP; React/Vitest for dashboard boundaries.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; `docs/designs/2026-09-29-latest-model-profiles-design-phase-1.md`. Read the Phase 3 execution/coverage map and this task's upstream Interfaces before coding.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** P1T3 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-3.md`) and P3T1 version contract, P3T3 execution gate and P3T4 exact-model parsers/launch helpers; legacy (pre-migration) runs follow decisions D1.

**Files:** Create `domain/provider/pinning.go`, `adapter/provider/pin_lookup.go`, associated tests, `cmd/arbitron/model_pins_wiring_test.go`. Modify `domain/engine/conversation.go`, `domain/engine/engine.go` terminal lifecycle hook, `cmd/arbitron/main.go`, `adapter/provider/interactive_stream_selector.go`, and the invocation/session parsers and builders in `adapter/{claudecli,codex,pi,opencode}` as needed for exact-model enforcement. P3T4 owns shared exact-model evidence parsers; reuse them here instead of duplicating parsers.

## Interfaces (owned here unless explicitly consumed)

Produce `provider.PinSet{Version int; Models map[string]string; OpaqueRoutes map[string]string}`, `PinLookup.Lookup(context.Context,uuid.UUID) (PinSet,error)` and `Release(uuid.UUID)`. `PinningResolver` implements existing `ProfileResolver.Resolve(context.Context,string) (ResolvedBinding,error)`; constructor takes live resolver, PinLookup and `func(context.Context)(uuid.UUID,bool)` supplied by `engine.InstanceIDFromContext`. SQLite-backed lookup consumes `InstanceRepository.Get`. Add explicit terminal release callback on engine; continuation completion also releases. Cache successful reads only; bound cache to 1024 entries with eviction and persisted reload.

## Data producers and composition-root wiring

P1T3 repository fields feed lookup; `Engine.run` supplies instance context. After authorization and instance loading, `conversationHandler.HandleMessage` attaches `ContextWithInstanceID(ctx,instanceID)` before resolution. Construct live resolver, snapshotter, lookup and decorator in `initDomain`; inject decorator into `LLMCallExecutor`, `AgentCallExecutor`, `InteractiveExecutor`, `NewConversationHandler`, `NewChildInputPreparer`. Validation checker and snapshotter keep the live resolver/cache. Wire lifecycle release without replacing spawn terminal hooks.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

- [x] Independent cache-lifecycle tests: write `TestPinLookupReleaseDuringRead` against migrated SQLite with only repository I/O return delayed, covering two concurrent old reads, repeated release, unrelated reads, retry reload before/after old completions and owned returned maps. Write `TestModelPinsWiringTerminalCacheLifecycle` through real initDomain/SQLite/profile edits/HTTP sink for completed, failed, cancelled and paused runs.

- [x] Independent live-route preflight: write `TestPinningSelectorsRejectIncompatibleLiveRoute`, `TestModelPinsWiringRejectsIncompatibleLiveRoute` and `TestModelPinsWiringRejectsIncompatibleLiveBackend`. Use real initDomain, migrated SQLite, admin profile edits and HTTP/process sinks; assert no request/spawn or accepted output/transcript on incompatible live routing, restored compatible route uses X, and a new run uses Y.

- [x] Independent live-binding portion: write `TestModelPinsWiringInteractiveLiveBinding` and `TestModelPinsWiringCodexInteractiveLiveBinding` through real initDomain, migrated SQLite, profile/connection services, engine and subprocess sinks. Cover initial/resumed turns, live credential rotation/endpoint/axes/tools/effort, X/Y persisted pins, credential-read failure with no spawn/transcript, and corrupted Codex rollout rejected before resume.

- [x] Independent portion: write `TestModelPinsWiringInteractiveResumeLiveTools` through real `initDomain`, migrated SQLite, profile service/cache, engine and subprocess argv. Verify initial profile tools, live edited tools on both old/new continuations, persisted X/Y pins, and no stale/global tool fallback. Only the CLI I/O leaf is substituted.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestModelPinsWiringAllConsumers` uses actual stack construction and real migrated DB, triggers on X, edits live profile to Y, drives each of the five consumers and checks final HTTP model body/subprocess argv/interactive resumed session is X; fresh run is Y.
- Success: credential rotation is live while model is fixed; cache eviction/restart reloads X; terminal completion releases entries.
- Failure: missing required pin, corrupt/unsupported pin version or incompatible live route produces no request/process spawn; legacy NULL/0 deliberately resolves live; out-of-run checker remains live.
- Error: repository fault is not cached and cannot fall back to Y; corrupt pins fail before sink.
- Edge: pause/retry/continuation and spawned child behavior; concurrent turns remain authorized and serialized; returned map is immutable to callers.
- Input guard: resolver-boundary guard verifies instance context; sink fake rejects an empty model and validates argv/env/body, including continuation. Removing decorator or continuation context must fail the wiring test. A resolver-only assertion is insufficient.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, "version-X", oldInvocation.Model)
assert.Equal(t, "version-Y", newInvocation.Model)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).

- [x] Independent live-binding portion: run each new root test before its implementation. Claude's subprocess rejects the missing credential/axes environment; Codex rejects absent model_reasoning_effort. Both real runs end failed instead of waiting. Observe green after per-turn binding construction.

- [x] Independent portion: observe behavioral red in both resumed subprocess invocations: `--allowedTools Read,Bash` (global fallback) instead of the live profile's `Glob`; the model/persistence and initial-turn assertions already pass.

- [x] Independent cache-lifecycle red: after correcting the test's owner fixture, observe behavioral red: both pre-release SQLite reads repopulate the released cache, and the next lookup bypasses the repository. Real-root lifecycle cases already pass and will serve as wiring regression controls.

- [x] Independent live-route preflight red: observe selector failures and real root batch completion/continuation transcript acceptance after a live edit to an opaque Azure deployment. For the additional backend sink guard, disable the new preflight temporarily and observe both batch and continuation spawn Codex with the old Claude pin; restore the guard before verification. Observe a separate behavioral red for a chat binding incorrectly receiving the Codex backend exception.

- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

- [x] Independent cache-lifecycle implementation: track active cache-miss read generations under the existing mutex. Release invalidates only that run's in-flight cache writes, without blocking on repository I/O or retaining permanent terminal tombstones. Earlier callers still receive their immutable persisted pins; post-release retries may reload/cache them. Existing initDomain lookup, terminal hooks and real instance repository remain the production wiring/feed.

- [x] Independent live-route preflight implementation: reuse P3T1's policy-independent version resolver in the existing provider factory and per-binding interactive agent selector, after actual backend precedence. Validate the pinned model against the actual live routing provider before constructing an adapter/session. Chat ignores the backend axis; Codex uses its subscription account independently of connection provider. Keep legacy/unpinned behavior and existing adapter evidence checks unchanged; add no option, parser, discovery call or credential read.

- [x] Independent live-binding portion: replace singleton agent stream selection with per-binding factories at initDomain; construct Claude streams with current credentials/axes via the existing fail-closed agent environment helper, and Codex interactive providers with current binding/effort. Resolve the actual default backend before construction. Keep model pinning, authorization, tool scopes, sandbox policy and existing Codex opt-in unchanged.

- [x] Independent portion: set continuation `ChatRequest.ToolScope` from the freshly resolved binding's `AllowedTools`, matching the existing initial interactive executor. The model remains trigger-pinned; tools retain live-profile semantics.

Only replace binding.Model; keep live credentials, connection/backend/tool/axes semantics. In-run missing/corrupt new pins error before execution; legacy NULL/0 and outside-run calls use live resolver (D1). Enforce pinned model at the provider/harness invocation and resumed session, including cached interactive providers: changing routing/session defaults cannot override the pin. If a backend cannot honor it, fail explicitly. A legacy run intentionally follows live profile edits; an empty or corrupt new snapshot must never enter that branch.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

- [x] Independent live-binding portion: pass the focused task gate, related provider/engine pin and interactive tests, full provider/Claude/Codex/root packages, and focused root race verification after ordered review. The broad engine suite retains unrelated snapshotter-fixture failures; record these separately rather than weakening the fail-closed engine.

- [x] Independent portion: the new root test and the task's focused gate pass after the minimal fix. The focused regex has no matching domain/engine or adapter/provider tests, so also run the actual pin resolver/lookup and conversation/interactive tests explicitly (see execution evidence below).

- [x] Independent cache-lifecycle green: pass the exact focused gate, explicit pin/lookup/inventory/interactive gates, full adapter/provider and cmd/arbitron suites, and focused adapter/root race tests after ordered review. No schema/query/UI changes require regeneration.

- [x] Independent live-route preflight green: pass the exact task focused gate, full adapter/provider and cmd/arbitron suites, related pin/lookup/inventory/interactive tests, and root/selector race verification after the ordered review. No schema/query/UI changes require regeneration.

**Focused gate:** `go test ./domain/engine ./adapter/provider ./cmd/arbitron -run "Pinning|ModelPins|PinsWiring" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

- [x] Independent live-binding portion: review spec compliance, then anti-green-theater, then code quality. Restrict scope to per-turn bindings; subprocess fixtures refuse missing live env/effort and assertions require X/Y at argv/persistence. Reuse existing credential/axis and Codex rollout helpers; update affected selector tests and format Go. Commit only this verified slice and its plan evidence.

- [x] Independent portion: complete spec-compliance, anti-green-theater and code-quality reviews in that order, then rerun the focused and related/race tests. Scope is one missing request field, matching initial-turn behavior. The real root feeds edited tools to subprocess argv; nil/empty tools or absent pin/context wiring cannot satisfy the old-run live-tool/X-model assertions. Commit only this verified slice; leave full prerequisite-dependent steps unchecked.

- [x] Independent cache-lifecycle review/commit: spec first (only concurrent terminal-release correctness in the existing bounded pin cache), anti-green-theater second (actual migrated rows, exact HTTP model/auth/route/prompt inputs, durable state/output/pins, root callback-removal mutation fails completed/failed/cancelled cases), quality third (mutex-protected read generations, no I/O under lock, no permanent terminal tombstones, named test cases and formatting). Restore root wiring, rerun tests/race gates green and commit only this verified slice plus plan evidence.

- [x] Independent live-route preflight review/commit: review spec compliance first (only the required incompatible-live-route failure; no new option or guessed equivalence), anti-green-theater second (real root negative and positive sinks; missing decorator/guard/model/auth fails; process log records even version probes), and code quality third (reuse existing pure grammar and factory precedence, retain legacy behavior, format Go). Commit only the verified slice and this plan's evidence.

## Execution evidence (cache lifecycle, 2026-10-16)

- Work is limited to P1T4's independent concurrent lookup/terminal-release obligation. Do not retry or certify the remaining upstream Azure/Bedrock proof producers. The already-shipped canonical Anthropic route correction closes that older blocker.
- Red: `go test ./adapter/provider -run '^TestPinLookupReleaseDuringRead$' -count=1` observes both old SQLite reads caching after release (`Cached=true`, expected false), then a subsequent lookup bypassing the persisted row. Fixture setup first required the migrated default owner; those fixture errors were not counted as behavioral red.
- Minimal fix: share a short-lived active-read generation per run. Release detaches/invalidates that generation; old reads can return the immutable pin but cannot populate the cache. A later lookup has its own generation, so late old completions cannot invalidate the retry's cache. Unrelated run reads continue caching normally. Completed/failed/missing reads remove their tracking, and Release retains no terminal-ID history.
- Real-root test feeds persisted connections/profiles and a trigger snapshot into the existing lookup/decorator/engine. HTTP checks exact X, bearer credential, route, POST, non-stream mode and nonempty prompt; live profile edits to Y cannot change old-run HTTP calls, persisted X or a later lookup. Completed/failed/cancelled transitions release cache entries; pause retains them. Accepted node outputs/statuses are checked; cancellation interrupts the first request before a node record is persisted.
- Ordered self-review: spec compliance, then anti-green-theater, then code quality. Temporarily remove only `eng.SetPinCacheRelease(pinLookup.Release)` from initDomain: all three terminal root cases fail at the cache-release sink. Restore it before further verification; main.go has no final diff. Strengthened node-output assertions then corrected the cancelled-row expectation to the actual interrupted-write contract; no engine behavior was changed.
- Post-review gates (all exit 0): exact task focused gate; `go test ./domain/provider ./adapter/provider -run 'Pinning|PinLookup|InteractiveStreamSelector|Inventory' -count=1`; `go test ./domain/engine -run 'Conversation|Interactive|PinCache|ModelPin|ResolveNodeBinding' -count=1`; `go test ./adapter/provider ./cmd/arbitron -count=1`; `go test -race ./adapter/provider ./cmd/arbitron -run 'PinLookup|ModelPins|PinsWiring|RetryResumeRerunAndSpawnPins' -count=1`; `git diff --check`. The task regex matches no engine tests here, so the explicit dependent engine gate is also required and green.
- Full P1T4 remains blocked as recorded above; this change makes no discovery, model-equivalence or cross-backend completion claim.

## Execution evidence (live-route preflight, 2026-10-16)

- Selection: first unfinished task P1T4; earlier shared-parser work now makes this independent preflight step possible. Do not rerun or re-fail the remaining upstream Azure/Bedrock/canonical-route blockers.
- Red: new selector assertions return adapters instead of fixed-version errors. The real root sends X as `/openai/deployments/X/chat/completions` after a profile routing edit, completes the batch run and appends an interactive transcript. Both negative sinks fail before the implementation.
- Additional anti-green-theater red: disabling the preflight makes both backend-root cases create the Codex process log (even though the leaf exits with failure); restored guard rejects before any spawn. The chat/backend-axis assertion separately fails before restricting the subscription exception to actual agent dispatch.
- Green: `go test ./adapter/provider ./cmd/arbitron -run 'TestPinningSelectorsRejectIncompatibleLiveRoute|TestModelPinsWiringRejectsIncompatibleLive' -count=1`; exact focused gate; full `go test ./adapter/provider ./cmd/arbitron -count=1`; `go test ./domain/provider ./adapter/provider -run 'Pinning|PinLookup|InteractiveStreamSelector|Inventory' -count=1`; `go test ./domain/engine -run 'Conversation|Interactive|PinCache|ModelPin|ResolveNodeBinding' -count=1`.
- Race green: `go test -race ./adapter/provider ./cmd/arbitron -run 'Pinning|ModelPins|PinsWiring|Usability.*Root|RetryResumeRerunAndSpawnPins' -count=1`.
- Wiring/feed: existing initDomain constructs the actual factory and interactive selector; real SQLite profile/connection metadata plus immutable run pins feed the new preflight at those production selection seams. The HTTP leaf records every attempt, including malformed requests. Restoring the compatible route yields old X/new Y at the request sink; process-negative cases assert no spawn and unchanged transcript/no output. This is not catalog/discovery confirmation or full P1T4 completion.

## Execution evidence (independent portion only)

- Red: `go test ./cmd/arbitron -run '^TestModelPinsWiringInteractiveResumeLiveTools$' -count=1` fails at both continuation argv sinks on global `Read,Bash` instead of `Glob`; green after forwarding `binding.AllowedTools`.
- Green after ordered review: task focused gate; `go test ./domain/provider ./adapter/provider -run 'Pinning|PinLookup' -count=1`; `go test ./domain/engine -run 'Conversation|Interactive|PinCache' -count=1`; `go test ./adapter/claudecli -count=1`.
- Race green: `go test -race ./cmd/arbitron -run 'ModelPins|PinsWiring|RetryResumeRerunAndSpawnPins' -count=1`.
- No new component, fake route producer, parser duplication, schema change or UI asset change. Full task completion is not claimed.

## Execution evidence (live-binding portion, 2026-10-16)

- Red: `go test ./cmd/arbitron -run '^TestModelPinsWiringInteractiveLiveBinding$' -count=1` fails because the initial subprocess receives no connection/axes binding. Green after Claude per-binding construction and shared credential environment injection.
- Red: `go test ./cmd/arbitron -run '^TestModelPinsWiringCodexInteractiveLiveBinding$' -count=1` fails because singleton Codex construction loses effort. Green after supplying the freshly resolved binding to each provider.
- The fixtures reject absent/stale load-bearing environments or effort rather than emitting a success unconditionally. Old/new model argv, persisted X/Y pins, resumed session IDs, no-spawn credential faults and no-spawn corrupted-session rejection are asserted at the sinks.
- Full `adapter/provider`, `adapter/claudecli`, `adapter/codex` and `cmd/arbitron` suites pass. The broad `domain/engine` suite fails in existing trigger/schedule fixtures omitting snapshotter/gate (or returning empty required snapshots); engine production code is unchanged in this slice. Focused engine pin/interactive tests remain the task gate.
- Post-review green: `go test ./domain/engine ./adapter/provider ./cmd/arbitron -run 'Pinning|ModelPins|PinsWiring' -count=1`; `go test ./domain/provider ./adapter/provider -run 'Pinning|PinLookup|InteractiveStreamSelector|Inventory' -count=1`; `go test ./domain/engine -run 'Conversation|Interactive|PinCache|ModelPin|ResolveNodeBinding' -count=1`; `go test ./adapter/provider ./adapter/claudecli ./adapter/codex ./cmd/arbitron -count=1`; `go test -race ./cmd/arbitron -run 'ModelPins|PinsWiring|RetryResumeRerunAndSpawnPins' -count=1`.
- Full cross-backend acceptance remains blocked above; this does not complete P1T4.

## Provenance / release gate (resolved 2026-10-01)

Provenance gates resolved 2026-10-01 (D1, D3–D6 in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`): implement and test the chosen rules exactly as recorded there; do not substitute synthetic evidence for the real probe/catalog/delivery behavior they define.

## Exact-model execution and session obligations

P3T4 defines the shared exact-model checks; this task applies them to actual calls and continuations, not only probes. Claude always gets the fixed `--model X`, no fallback flag, and its result `modelUsage` must contain X. Codex fresh execution gets `-m X`; resume currently omits `-m` (`adapter/codex/exec_session.go:buildInteractiveExecArgs`), so locate the stored session and verify its `turn_context.model` before resuming and the new turn afterwards. Reject a mismatched/missing session model instead of trusting the cached provider. Pi assistant provider/model and OpenCode exported assistant providerID/modelID must match the pinned path/model before accepting outputs. HTTP adapters retain the pinned outgoing model and reject contradictory actual-model evidence using the same evidenced equivalences as P3T4. All five consumers supply the pin in the binding and request; instance context is checked at the resolver boundary, not required as a fictitious subprocess argument.

Add red/green tests for correct argv with a wrong actual model, corrupted resumed-session model and a cached interactive provider bound to Y while the run is pinned to X. Assert failure before accepting output; for preflight mismatch assert no spawn. Reconstruct per-turn bindings for live credentials/axes/tools; factory caches cannot override the pinned model. Compose pin-cache release with existing child-terminal hooks for completed, failed, cancelled and interactive completion/EndSession; retry reloads persisted pins and pause retains them. Calls to a mutable alias without a specific referent fail explicitly outside the D8 native pinned-only exception; P3T1 does not manufacture such evidence.

Define `Engine.SetPinCacheRelease(func(uuid.UUID))` for the terminal cleanup callback; reject nil when pin lookup is configured. Invoke it after successful durable terminal transitions without replacing queueMgr.SetChildTerminalHook or spawn wakeups. Authorized conversation EndSession/completion routes through the same release function. Eviction is still safe for paused/retryable instances because repository pins are immutable and reloadable.

Extend the existing root snapshotter constructor call to pass P3T3 inventory as provider.WorkflowVersionRequestSource (P3T1 owns that narrow port), after constructing inventory and before serving/recovery. Keep one shared engine pin gate; do not inject a second read lock inside the snapshotter. Add TestModelPinsDefaultBackendWiring for an agent profile with empty Backend on a Codex-default installation and assert the persisted pin and final Codex `-m` value. Add TestModelPinsNoSnapshotterWiring for a no-model workflow as well as a model workflow: omitted snapshotter/gate must never create a new NULL/0 legacy row. These extend existing pin root tests rather than recreate their wiring.

## Current D8 execution enforcement and pin consumers

Consume P1T3 ModelPinSnapshot/Instance.ModelPinRoutes/PinSet.OpaqueRoutes. Extend `provider.ResolvedBinding` with internal `OpaquePinRoute string`, populated only by PinningResolver from persisted exception metadata; Model and Pinned remain set for every post-migration alias. Deep-copy/cache/release the exception map with Models. Pinned with an empty OpaquePinRoute retains strict exact-model evidence. The actual factory/interactive selector validates the final effective route against a nonempty saved OpaquePinRoute before constructing an adapter: only the same native D8 family through its native chat/stream adapter (never a Claude/Codex/Pi/OpenCode backend) may execute without an exact-version claim. A live route/backend change cannot turn a strict pin into an exception or reuse an opaque pin on a different family. Credentials/endpoints/axes/tools otherwise remain live.

**Files:** extend existing resolver, lookup, factory, interactive selector, `adapter/azurefoundry/{provider,stream}.go`, `adapter/bedrock/provider.go` and root pin tests. P3T4 owns probes, which always require exact evidence and never receive this execution exemption.

- [x] Write `TestD8PinnedExecutionRoot` through initDomain, registered profile service, migrated SQLite, trigger, retry/reopen and continuation. Native pinned Azure (azure-openai/azure-foundry/azure aliases) and Bedrock (bedrock/amazon-bedrock aliases) runs send the captured deployment/model ID, accept ordinary successful output and keep that ID after profile.Model changes; fresh runs capture the changed ID. Verify Azure request route/body/auth and Bedrock signed request/model path plus durable output/transcript. These are configured-ID assertions, not actual-version claims.
- [x] Write failure/error/edge controls: strict X pin edited to Azure/Bedrock fails before I/O; opaque route edited to another family fails; missing exception/pin or corrupt storage fails; rotating credentials stays live; following creation and discovery confirmation on D8 paths remain rejected. Explicit `RequireModelEvidence` still refuses these adapters even on an exempt pinned binding. Legacy D1 calls retain their existing behavior.
- [x] Observe behavioral red before extending factory/adapter guards; replace old all-pinned-refusal expectations with the D8 positive/negative distinction. Run the task gate plus explicit Azure/Bedrock packages and root D8 tests, then race tests. Remove the exception producer or resolver input and require positive root sinks to fail. Preserve all five existing execution-only resolver/context injections.

## Completion evidence (D8 execution, 2026-10-18)

This closes P1T4's remaining local obligations; historical blocked/full-task statements above no longer describe its status. No discovery identity or immutable underlying Azure/Bedrock version is claimed.

- Red before implementation: `go test ./cmd/arbitron -run '^TestD8PinnedExecutionRoot' -count=1` fails at the HTTP/durable execution sinks: native requests never arrive and interactive runs fail instead of persisting successful output. The cache-copy test independently fails because persisted route exceptions disappear, and `TestPinningD8Selectors` rejects valid native configured IDs. The malformed empty-alias resolver control also fails before its validation fix. Fixture cleanup/retry-history corrections were not treated as production behavioral failures.
- Implementation/feed: existing initDomain constructs the repository-backed lookup, execution-only decorator and native factory/stream selector. P1T3's real persisted `ModelPinRoutes` now feeds copied `PinSet.OpaqueRoutes` and `ResolvedBinding.OpaquePinRoute`. The final effective native family must match; harness dispatch never receives an exemption. Initial/continued interactive requests distinguish configured-ID execution from explicit exact-evidence requests. Strict pins, probes and genuine NULL/0 legacy calls retain their respective contracts. All five existing resolver/context injections and terminal-release hooks remain intact.
- Root acceptance covers all five documented provider aliases, signed Bedrock model paths and secrets/session tokens, Azure deployment path/body/api-version/auth, rotated credentials, failed-call retry after actual root reopen, old X/new Y pins, cache-map ownership, durable output/transcripts, and legacy live edits/continuations. Invalid/missing exceptions, missing pins, corrupt storage and cross-family edits leave both request count and transcript unchanged. Strict-to-native route edits are covered for Azure and Bedrock; existing root profile-selection and real probe/adapter controls keep following and confirmation rejected. `RequireExecutedModel` is the existing concrete exact-evidence API (the plan's `RequireModelEvidence` terminology); it still rejects even exempt native bindings before credential access/I/O.
- Named acceptance gates `TestModelPinsWiringAllConsumers` and `TestModelPinsDefaultBackendWiring` consolidate existing real-root sink scenarios without adding production behavior. Cache release/race fixtures now assert persisted exception maps as well as models.
- Ordered review: **spec first**, confined to the specified D8 execution exception and existing pin consumers; **anti-green-theater second**, temporarily remove the snapshot exception producer, then the resolver exception input, independently. Both mutations fail Azure/Bedrock interactive root tests at failed state/no durable accepted output; restore both before verification. TLS leaves validate real model, route, credentials/signature and nonempty prompt, and positive responses deliberately contain no model identity. Harness builders cannot run on an opaque exemption, even with otherwise supported fixed-model spellings. **quality third**, reuse canonical provider vocabulary, deep-clone both maps with `maps.Clone`, keep successful-read-only bounded caching/release behavior, validate exception shape, format Go and preserve native adapters' explicit proof refusal. No schema/query/UI changes require regeneration.
- Final green gates (each exit 0): exact task focused gate; `go test ./domain/provider ./adapter/provider -run 'Pinning|PinLookup|InteractiveStreamSelector|Inventory' -count=1`; `go test ./domain/engine -run 'Conversation|Interactive|PinCache|ModelPin|ResolveNodeBinding' -count=1`; full Azure, Bedrock and probe packages; root `D8|UsabilityBedrock|RejectsIncompatibleLiveRoute|RetryResumeRerunAndSpawnPins`; full `go test ./cmd/arbitron -count=1`; `git diff --check`. The task regex still matches no engine tests, so the explicit engine gate was also run.
- Race green: full Azure/Bedrock packages; `go test -race ./domain/provider ./domain/engine ./adapter/provider ./cmd/arbitron -run 'Pinning|PinLookup|ModelPins|PinsWiring|D8|UsabilityBedrock|RetryResumeRerunAndSpawnPins|Conversation|Interactive' -count=1 -timeout=5m`. Verification used an isolated `GOCACHE=/tmp/arbitron-p1t4-go-cache-3cf8c8ff` after shared-cache missing-file errors. A later short-timeout repeat under extreme host load timed out in the existing agent sink fixture; the final broad race command above passed (root 115.914s). No assertions were weakened or production timeouts changed.
- Broader-suite concern, not a P1T4 blocker: full domain/provider and adapter/provider suites still fail in profile-service/cache fixtures lacking the required selection/version ports (`provider: model selection ports not configured: version resolver`). This pass repaired its own route-map cache-fixture mismatch; focused pin/lookup/inventory tests and the full root suite are green. No full-repository release gate is claimed.
