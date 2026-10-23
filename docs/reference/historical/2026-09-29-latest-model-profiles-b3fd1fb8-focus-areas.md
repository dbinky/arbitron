# Focus Areas — Latest Model Profiles Review Plan

Single-system review plan for branch `dev-latest-model-profiles-2026-10-01-b3fd1fb8`.
Each row is one **behaviour** the spec promises, traced from its entry to its sink
across every layer. It is not a layer, a file or a task. Review 1 is the **wiring
lens** (does the path exist and is it fed?). Review 2 is the **failure lens** (how
does it fail?). Pass criteria live in `RALPH.md`. Seams between areas live in
`docs/reference/paired-reviews.md`.

**State at seeding:** only the earlier slices exist in code: settings (P1T1–P1T2),
profile selection fields (P2T1), the raw guarded fetcher (part of P3T2), and
connection revisions plus the model-state key (part of P3T3). Owner decisions D1–D7
(2026-10-01) unblocked everything else, so the rest of each path is an **open build
gap** (B1–B15 in `gaps-identified.md`), not a Won't Fix. Where an area's path is
unbuilt, the review fails the wiring lens. The "most important issue" is then the
earliest open B-gap on that path, in task-graph order (see `RALPH.md`).

**Budget:** 9 single-system areas × 2 + 16 paired seams = **34 minimum passes**;
500 × 0.35 = **175** available. 34 ≤ 175; no budget deferrals.

Symbols in unbuilt B-gap paths below are planned review targets, not claims that
implementation exists. Existing code evidence is under **Seed evidence**; absence
greps refer to production Go under `domain/`, `adapter/` and `cmd/`, excluding tests
and documents.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | a post-migration run executes every node, retry, resume and child-input preparation on the model versions pinned at trigger, while a NULL-pin legacy run keeps live resolution | high | [x] | [x] |
| 2 | an interactive run's follow-up turns resume on its pinned model and refuse a resumed session recorded on a different model | high | [x] | [x] |
| 3 | startup, manual and four-hour refreshes inventory actual paths, reject stale evidence, atomically advance eligible profiles and confirm publication; failures retain working models | high | [ ] | [ ] |
| 4 | an admin's catalog URL and Teams webhook edits persist through HTTP, MCP and dashboard, the webhook defaulting empty and redacted on every public read while internal readers get the raw value | high | [ ] | [ ] |
| 5 | Claude Code and Codex probes mark a model usable on a path only when the harness's own record shows that exact model ran | high | [ ] | [ ] |
| 6 | pi, OpenCode and direct-connection probes mark a model usable on a path only when the assistant response names the requested provider and model | high | [ ] | [ ] |
| 7 | the catalog at the configured URL is fetched through the guarded client and classified into provider, tier and numeric version by the persisted admin-editable D2 policy | high | [ ] | [ ] |
| 8 | an admin creates or edits a pinned or following profile through MCP or YAML seed, validated against version and path evidence and initialized from the newest eligible candidate when no model is given | high | [ ] | [ ] |
| 9 | each newly usable model gets one durable event, classified by the webhook setting at discovery, and is delivered at least once to Teams with its original profile changes | med | [ ] | [ ] |

---

### Area 1 — a post-migration run executes on its trigger-time pinned models; a NULL-pin legacy run resolves live

- **Promise:** New workflow runs use the updated model, and runs already underway keep the model they started with. Runs created before the pin migration (NULL pin record) keep live resolution until they finish (spec; D1).
- **Entry:** `domain/engine/engine.go:Engine.TriggerWithDepartment` (also `Engine.Rerun` and the spawn-child trigger path).
- **Sink:** the model in the outgoing provider request or harness argv: `adapter/claudecli/claude.go:buildArgs` (`--model`), `adapter/codex/args.go:buildArgs` (`-m`), `adapter/pi/args.go:buildArgs`, `adapter/opencode/args.go:buildArgs`, and HTTP adapter request bodies. The persisted `instances.model_pins` / `model_pins_version` columns are also a sink.
- **Files:** planned/changed first: `adapter/sqlite/migrations/020_instance_model_pins.{up,down}.sql`, `domain/instance/instance.go`, `domain/engine/engine.go`, `adapter/sqlite/instance_repo.go`, `adapter/sqlite/queries/instances.sql`, `adapter/sqlite/generated/*` (instances conversions), `domain/provider/model_snapshot.go`, `adapter/provider/model_snapshot.go`, `adapter/provider/cache.go` (`SnapshotProfiles`), `domain/provider/pinning.go`, `adapter/provider/pin_lookup.go`, `domain/provider/model_version.go`, `cmd/arbitron/main.go`, `domain/engine/model_pins_test.go`, `adapter/sqlite/instance_model_pins_test.go`, `cmd/arbitron/model_pins_wiring_test.go`. Unchanged files the path crosses: `domain/engine/executor_llm.go`, `domain/engine/executor_agent.go`, `domain/engine/child_input_preparer.go`, `domain/engine/executor_timer.go`, `adapter/provider/dynamic.go`, `adapter/provider/factory.go`, `domain/queue/manager.go`, `adapter/claudecli/claude.go`, `adapter/claudecli/streamjson.go`, `adapter/codex/args.go`, `adapter/pi/args.go`, `adapter/pi/parse.go`, `adapter/opencode/args.go`, `adapter/opencode/parse.go`.
- **Folded in:**
  1. Migration 020 (`adapter/sqlite/migrations/`): applies on a fresh DB and on a DB that already records 019/021/023/024 with 020 and 022 absent. Down pair present. No backfill of historical pins (D1).
  2. sqlc regeneration for `queries/instances.sql`: the existing hand-maintained `sqlc.slice` query at `instances.sql:86` breaks whole-project generation. Regenerate scoped and hand-check every Get/Create conversion.
  3. `Engine.SetModelSnapshotter` and `Engine.SetPinCacheRelease`: both non-nil when pins are configured. Release is composed with `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)` (`cmd/arbitron/main.go:911`) and does not replace it.
  4. Executed-model evidence on non-interactive calls (P1T4 consuming P3T4 parsers). Claude stream result `modelUsage` must contain X; Codex must correlate its completed turn's `turn_context.model` with X. Pi `message_end` provider/model and OpenCode exported providerID/modelID must match.
  5. `ExecutionGate.WithRead` wraps snapshot capture (Area 3 owns the gate).
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — wraps the live `providersadapter.NewDynamicProfileResolver(profileCache, connStore)` (`main.go:753`) in a `PinningResolver`. It injects that resolver into `engine.NewLLMCallExecutor` (`main.go:841`), `engine.NewAgentCallExecutor` (`main.go:842`) and `engine.NewChildInputPreparer` (`main.go:991`). Workflow validation and the snapshotter keep the live resolver. Prove it with `grep -rn NewPinningResolver` outside `*_test.go`.
  2. `domain/engine/engine.go:Engine.TriggerWithDepartment` — `Engine.TriggerWithDepartment` collects every non-empty `NodeConfig.ModelProfile` from the same serialized workflow that will execute: branches, loops, interactive nodes and the spawn input-preparer alias. It captures them through one `ProfileCache.SnapshotProfiles` generation and the P3T1 `VersionResolver`. `e.instances.Create` (`engine.go:554`) persists `ModelPins`/`ModelPinsVersion=1` before `queueManager.Enqueue` (`engine.go:564`).
  3. `adapter/sqlite/instance_repo.go:InstanceRepo.Create` — `adapter/sqlite/instance_repo.go:InstanceRepo.Create`/`Get` round-trip the JSON map and version. `adapter/provider/pin_lookup.go` reads through `InstanceRepository.Get`, not a cache-only source.
  4. `domain/engine/engine.go:Engine.run` — `Engine.run` attaches `ContextWithInstanceID` (`engine.go:920`), and that ID reaches `PinningResolver.Resolve`. `Engine.Rerun` and spawned children go through the trigger path and get fresh pins. The parent's child-input preparation uses the parent's pins.
  5. `domain/provider/pinning.go:PinningResolver.Resolve` — Only `binding.Model` is replaced. Credentials, connection, backend, axes and tools still resolve live. A credential rotation during a run is used on the next call.
  6. `cmd/arbitron/model_pins_wiring_test.go:TestModelPinsWiringAllConsumers` — uses actual `initDomain` and migrated SQLite. It triggers on X, edits the live profile to Y, and asserts the final argv/body is X for the old run and Y for a fresh run. Removing the decorator must make it fail.
- **Review 2 — failure lens:**
  1. `adapter/sqlite/instance_repo.go:InstanceRepo.Get` — Discriminator corruption: NULL with version 1, non-NULL with version 0, invalid JSON or an unsupported version each fail before any provider call. Only NULL/0 is legacy, and a legacy run deliberately follows live edits. Test `adapter/sqlite/instance_model_pins_test.go:TestPinsSQLiteRoundTrip` plus corrupt-combination cases.
  2. `adapter/provider/pin_lookup.go:PinLookup` — A PinLookup repository fault is not cached and never falls back to live Y. The 1024-entry cache evicts and reloads from SQLite after restart.
  3. `adapter/provider/cache.go:ProfileCache.SnapshotProfiles` — Concurrent advancement during trigger: every alias comes from one cache generation, and returned maps cannot mutate the cache (`TestTriggerPinsCoherentGeneration`).
  4. `domain/engine/engine.go:Engine.TriggerWithDepartment` — A version, snapshot or Create failure leaves no queued instance and makes no provider call. An unknown alias, an empty model or an unresolvable mutable alias (`sonnet`, `latest`) fails before `Create` with an actionable error that asks for a fixed version.
  5. `domain/engine/model_pins_test.go:TestRetryResumeRerunAndSpawnPins` — Retry, pause/resume and restart reuse the persisted pins (`TestRetryResumeRerunAndSpawnPins`). A no-alias workflow persists `{}`/1, not NULL.
  6. `adapter/claudecli/streamjson.go:parseEvent` — Wrong executed-model evidence (Claude `modelUsage` without X, Codex `turn_context.model` for another model/turn, a pi/OpenCode provider/model mismatch) fails the node before its output is accepted. A sink fake rejects an empty model.
- **Seed evidence:**
  - `grep -rn 'PinningResolver\|PinLookup\|model_pins\|ModelSnapshotter'` matches nothing in the repo (unbuilt: B8, B9).
  - `cmd/arbitron/main.go:753` constructs `NewDynamicProfileResolver` and injects it raw at `main.go:841`, `:842`, `:991`, `:1089` and `:1096`.
  - `adapter/sqlite/migrations/` jumps from 019 to 021; there is no 020.
  - `engine.go:554` calls Create before Enqueue at `:564`, which is the correct order for pins.
  - `adapter/claudecli/claude.go:buildArgs` emits `--output-format stream-json` and `--model`, with no `--fallback-model`. `grep -rn modelUsage` finds nothing, so normal Claude runs never verify the executed model.

### Area 2 — an interactive run's follow-up turns resume on its pinned model and refuse a mismatched resumed session

- **Promise:** Every interactive turn of an active post-migration run keeps its starting model, including follow-up turns, retries and resumes (design phase 1; D1, D3–D6 "a pinned run's harness must be invoked with the pinned exact model and no fallback flag").
- **Entry:** `domain/engine/conversation.go:conversationHandler.HandleMessage`. The first turn starts at `domain/engine/executor_interactive.go:InteractiveExecutor.Execute`.
- **Sink:** the resumed harness invocation. That is `adapter/codex/exec_session.go:buildInteractiveExecArgs` argv plus a verified rollout `turn_context.model`, and `adapter/claudecli/claude.go:buildArgs` with `--resume` plus `--model`, checked against the result `modelUsage`.
- **Files:** `domain/engine/conversation.go`, `domain/engine/executor_interactive.go`, `adapter/provider/interactive_stream_selector.go`, `adapter/codex/exec_session.go`, `adapter/codex/parse.go`, the Codex rollout reader (P3T4, under `adapter/codex/`), `adapter/claudecli/stream_agent_adapter.go`, `adapter/claudecli/claude.go`, `adapter/claudecli/streamjson.go`, `domain/provider/pinning.go`, `adapter/provider/pin_lookup.go`, `cmd/arbitron/main.go`, `cmd/arbitron/model_pins_wiring_test.go`. Unchanged files crossed: `domain/engine/executor_timer.go`, `adapter/provider/factory.go`.
- **Folded in:**
  1. Pin-cache release on authorized `EndSession`/completion routes through the same `SetPinCacheRelease` function as Area 1.
  2. The OpenCode interactive gap: `InteractiveStreamSelector` has no opencode stream adapter, so it must fail explicitly. It must never route to an unpinned path.
- **Review 1 — wiring lens:**
  1. `domain/engine/conversation.go:conversationHandler.HandleMessage` — `conversationHandler.HandleMessage` attaches `ContextWithInstanceID(ctx, instanceID)` after authorization and instance loading, before `h.resolver.Resolve` (`conversation.go:207`).
  2. `cmd/arbitron/main.go:initDomain` — `cmd/arbitron/main.go` injects the same `PinningResolver` into `engine.NewInteractiveExecutor` (`main.go:1089`) and `engine.NewConversationHandler` (`main.go:1096`).
  3. `adapter/codex/exec_session.go:execSession.Start` — Codex resume locates that thread's stored rollout and verifies `turn_context.model == pin` before resuming and again for the new turn. The thread ID comes from `adapter/codex/parse.go:foldEvent` `thread.started`. Verification reuses the P3T4 rollout reader rather than a second parser.
  4. `adapter/claudecli/claude.go:buildArgs` — Claude resume carries `--model X` alongside `--resume` and checks the turn result's `modelUsage`.
  5. `adapter/provider/interactive_stream_selector.go:InteractiveStreamSelector.StreamProviderFor` — `InteractiveStreamSelector.StreamProviderFor` receives the pinned binding per turn. No cached provider or session default overrides the pinned model.
  6. `cmd/arbitron/model_pins_wiring_test.go:TestModelPinsWiringAllConsumers` — A wiring test drives `HandleMessage` after a live edit to Y and asserts that the resumed argv/session model is X. Removing the continuation context must make it fail.
- **Review 2 — failure lens:**
  1. `domain/provider/pinning.go:PinningResolver.Resolve` — A missing instance context at the resolver boundary inside a run is an error, not silent live resolution.
  2. `adapter/codex/exec_session.go:execSession.Start` — A rollout that is missing, unreadable, over 1 MiB, from the wrong session or from an earlier turn refuses the turn before output is accepted.
  3. `domain/engine/conversation.go:conversationHandler.HandleMessage` — Concurrent turns on one instance stay authorized and serialized. A turn after `EndSession` reloads pins from SQLite.
  4. `adapter/provider/pin_lookup.go:PinLookup` — A legacy NULL-pin conversation resolves live (D1). That path is tested, and an empty or corrupt new snapshot never enters it.
  5. `adapter/provider/interactive_stream_selector.go:InteractiveStreamSelector.StreamProviderFor` — A backend that cannot honour the pin, such as opencode interactive, fails explicitly with no spawn. Preserve `cmd/arbitron/main.go:initDomain` opt-in registration: disabled Codex refuses before spawn; enabled Codex reaches the pinned resume path.
- **Seed evidence:**
  - `grep -n ContextWithInstanceID domain/engine/*.go` (non-test) finds only `engine.go:920`. `conversation.go:207` resolves the profile without it.
  - The resume branch of `adapter/codex/exec_session.go:160 buildInteractiveExecArgs` builds argv without `modelFlag`.
  - `grep -rn turn_context` finds nothing.

### Area 3 — refreshes reject stale evidence, atomically advance eligible profiles and confirm publication at startup and every four hours

- **Promise:** Startup, manual and four-hour refreshes discover actual execution paths, reject stale evidence, and atomically advance only eligible following profiles before confirming publication to new runs. Failures retain working models, pending publication recovers, and admins receive a redacted catalog.
- **Entry:** `domain/modelcatalog/loop.go:RefreshLoop.Run`, admin `domain/modelcatalog/refresh.go:RefreshService.Refresh` and trusted `RefreshInternal`; the direct `domain/provider/advance_service.go:ProfileService.AdvanceModel` path shares the same gate, transaction and publisher. Admin connection/profile/workflow/policy mutations feed the revision checks.
- **Sink:** The durable `profiles.model`/`revision`, `profile_advancement_journal` and `RefreshReport` committed by `adapter/sqlite/model_catalog_repo.go:CatalogStore.CommitCycle`, the verified cache generation and `MarkPublished` acknowledgement, plus `RefreshService.List` public catalog. Inventory tokens, key bootstrap and gate checks are intermediate steps, not separate behaviours.
- **Files:** `domain/provider/advance.go` (exists), `domain/provider/advance_service.go`, `domain/provider/profile_service.go`, `domain/provider/profile_store.go`, `adapter/sqlite/profile_advance.go`, `adapter/sqlite/profile_repo.go`, `adapter/sqlite/queries/profiles.sql`, `adapter/sqlite/migrations/021_following_profiles.up.sql` (exists), `domain/modelcatalog/candidates.go`, `domain/modelcatalog/refresh.go`, `domain/modelcatalog/store.go`, `domain/modelcatalog/report.go`, `adapter/sqlite/model_catalog_repo.go`, `adapter/provider/cache.go`, `cmd/arbitron/main.go`, `cmd/arbitron/logging_reloader.go`, `cmd/arbitron/profile_advance_wiring_test.go`. Unchanged files crossed: `domain/provider/capability.go`, `domain/modelidentity/identity.go` (`Compare`). `domain/provider/execution_gate.go`, `domain/modelcatalog/paths.go`, `adapter/provider/path_inventory.go`, `domain/instance/execution_snapshot.go`, `adapter/sqlite/instance_repo.go` (`ListExecutionSnapshots`), `adapter/filesystem/workflow_repo.go` (`ListScopes`), `domain/workflow/service.go`, `domain/provider/connection_service.go`, `domain/provider/connection.go`, `domain/provider/advance.go`, `adapter/sqlite/connection_repo.go`, `adapter/sqlite/queries/connections.sql`, `adapter/sqlite/migrations/023_execution_revisions.up.sql`, `adapter/sqlite/migrations/024_connection_revision_floor.up.sql`, `adapter/crypto/installation_secret.go`, `adapter/crypto/installation_secret_unix.go`, `adapter/crypto/installation_secret_windows.go`, `cmd/arbitron/model_state.go`, `cmd/arbitron/main.go`. Unchanged files crossed: `domain/workflow/local_registry.go`, `domain/workflow/registry.go`, `adapter/provider/factory.go`, `adapter/provider/interactive_stream_selector.go`, `domain/engine/executor_agent.go` (backend precedence), `install.sh`, `install.ps1`. `domain/modelcatalog/loop.go`, `domain/modelcatalog/refresh.go` (`DiscoveryPipeline`, `RefreshCoordinator`, `RefreshService`), `domain/modelcatalog/report.go`, `domain/modelcatalog/public.go`, `domain/settings/settings.go` (`InternalReader`), `adapter/mcp/tools_model_catalog.go`, `adapter/mcp/deps.go`, `adapter/mcp/register.go`, `cmd/arbitron/main.go` (`domainStack.Close` `:246`, `initDomain` `:382`), `cmd/arbitron/model_catalog_wiring_test.go`, `cmd/arbitron/model_profiles_e2e_test.go`. Unchanged pattern reference: `domain/schedule/evaluator.go`.
- **Folded in:**
  1. G12: `ProfileCache.Load` ordering.
  2. Best-effort `ProfileService.recache` is kept for admin edits, and automatic changes use `PublishCommitted`.
  3. Request-ID replay returns the original journal change.
  4. The pinned-profile guard.
  5. The no-downgrade rule.
  6. Carried key-bootstrap gaps G4, G6, G7, G8 and G9.
  7. `initModelState` HMAC fingerprint closure (P3T3 keyed-fingerprint producer).
  8. `WorkflowScopeSource.ListScopes`.
  9. Migration 024 revision floor.
  10. Interactive-route enumeration matching `InteractiveStreamSelector`, including the actual `ARBITRON_CODEX_INTERACTIVE_VERIFIED` registration flag in `cmd/arbitron/main.go:initDomain`.
  11. The `Clock`/`Ticker` seam.
  12. The staged `DeferredAnnouncer`, which preserves durable events.
  13. `initDomainWithModelDependencies`, which may substitute only I/O leaves.
  14. Limits: 3-hour cycle and 4 workers.
  15. Startup cleanup for each constructed resource.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `initDomain` calls `ProfileService.SetAdvancementDependencies(AdvanceStore, AdvancementPublisher, ExecutionGate)` with the real `ProfileRepo`, `ProfileCache` and shared gate, all non-nil. The automatic path never goes through `loggingReloader` (`cmd/arbitron/logging_reloader.go`).
  2. `domain/modelcatalog/candidates.go:CandidateService` — The expected profile comes from `ProfileService.ListInternal`. The candidate comes from the eligibility helper: same follow provider/tier, strictly newer by `modelidentity.Compare`, confirmed on every consuming path, with one common executable spelling. Show the call site.
  3. `adapter/sqlite/model_catalog_repo.go:CatalogStore.CommitCycle` — `CatalogStore.CommitCycle` calls `commitAdvanceTx` for each proposal inside the same SQL transaction as events and the report. The journal row comes from that transaction, not from a separate write.
  4. `adapter/provider/cache.go:ProfileCache.PublishCommitted` — `PublishCommitted` reloads the cache and verifies the committed revisions are present. `MarkPublished` clears `PublicationPending` only after that. Startup `Recover` republishes unacknowledged reports.
  5. `cmd/arbitron/profile_advance_wiring_test.go:TestAdvanceWiringPublishesNewRun` — asserts that the new-run provider model is Y and the old run stays X. The sink is the request body or argv, not "Reload was called".
  6. `cmd/arbitron/main.go:initDomain` — `initDomain` builds one shared `ExecutionGate`, late-binds the inventory fingerprint callback after inventory construction, and injects the gate into the connection, profile, workflow and identity-policy services. Every create/update/delete/seed/rename hook calls `WithMutation`; `grep -rn WithMutation` shows the callers.
  7. `adapter/provider/path_inventory.go:ExecutionPathInventory.Snapshot` — The inventory enumerates registry scopes plus `WorkflowRepo.ListScopes` (shared and `users/<owner>` directories), active `InstanceRepo.ListExecutionSnapshots`, and configured connections and harnesses with no following profiles. It applies node > profile > deployment backend precedence exactly as `AgentCallExecutor.Execute` does.
  8. `cmd/arbitron/model_state.go:initModelState` — `cmd/arbitron/model_state.go` exposes a fingerprint closure built from the same persisted key as `initModelStateCrypto`. `main.go:418` passes that closure to the inventory. The key is never exported or logged.
  9. `adapter/provider/path_inventory.go:ExecutionPathInventory.Snapshot` — `ExecutionPath.ConfigRevision` reads `Connection.ConfigRevision` together with the binary, account-file and launch-config fingerprints. `PathSnapshot.ExecutionRevision` adds profile, policy and workflow-hash state.
  10. `adapter/sqlite/profile_advance.go:commitAdvanceTx` — `commitAdvanceTx`/`CommitCycle` validate the token's policy and connection revisions inside the SQL transaction. `WithCurrent` rechecks external fingerprints immediately before the transaction starts.
  11. `cmd/arbitron/model_profiles_e2e_test.go:TestModelProfilesE2ESinks` — A root test edits a real connection after a probe and asserts that the stale candidate never reaches the durable profile.
  12. `cmd/arbitron/main.go:initDomain` — `initDomain` constructs the collaborators in the P3T7 order and starts the loop only after seed, publication recovery and cache load. Both `runServe` and `runMCP` start it, and `domainStack.Close` cancels and joins it before closing the DB.
  13. `domain/modelcatalog/loop.go:RefreshLoop.Run` — The loop runs a startup pass and then ticks every `4*time.Hour` from the real clock. A test drives a second tick through the clock seam rather than calling `RefreshInternal` directly.
  14. `domain/modelcatalog/refresh.go:RefreshCoordinator.Do` — Manual refresh, the ticker and `CandidateService` share one `RefreshCoordinator` instance.
  15. `domain/modelcatalog/refresh.go:RefreshService.List` — `model_catalog_list` returns `ProjectCatalog` output, and both catalog tools authorize in the service. The internal refresh does not impersonate an admin.
  16. `cmd/arbitron/model_profiles_e2e_test.go:TestModelProfilesE2ESinks` — `TestModelProfilesE2ESinks` runs actual ticks with no Claude connection and ends at the executed model: X for the old run, Y for the new run.
- **Review 2 — failure lens:**
  1. `adapter/sqlite/profile_advance.go:commitAdvanceTx` — A CAS mismatch on any expected field (selection, target, model, kind, connection, backend, axes, tools, revision; ABA included) returns `ErrAdvanceConflict` with no row or journal change. Other proposals in the cycle proceed (`TestAdvanceCASAndJournal`).
  2. `adapter/sqlite/model_catalog_repo.go:CatalogStore.CommitCycle` — A journal or event write fault rolls back the model update.
  3. `adapter/provider/cache.go:ProfileCache.PublishCommitted` — A reload fault returns the committed record plus `ErrPublicationPending`. Repair publishes Y without a second journal row, and no report claims success before `MarkPublished`.
  4. `adapter/provider/cache.go:ProfileCache.Load` — Two reloads that complete in reverse order cannot leave the cache at the older snapshot (G12).
  5. `domain/modelcatalog/candidates.go:CandidateService` — A pinned profile, an unknown current version, the current model missing from the latest catalog, or equal versions with missing or equal dates never advance. A newer current model is never downgraded.
  6. `adapter/sqlite/profile_advance.go:commitAdvanceTx` — A duplicate request ID replays the original advancement without a new revision.
  7. `adapter/provider/path_inventory.go:ExecutionPathInventory.Snapshot` — A registry, scope, manifest, instance or fingerprint read fault reports incomplete inventory. It never produces vacuous zero-path eligibility.
  8. `domain/modelcatalog/paths.go:ExecutionPath.ConfigRevision` — Revision scope: an edit that changes only the model, the profile revision or the tier policy keeps `ConfigRevision`, so there is no re-probe. A credential, account, binary or launch-config change alters it.
  9. `adapter/crypto/installation_secret.go:LoadOrCreateInstallationSecret` — Key integrity and durability: a same-length corrupted key is refused after DB-verifier enrollment (G6); one-time legacy enrollment has no earlier verifier to compare. Publication completes on retry before the key is returned (G7). On Windows only service, SYSTEM and Administrators can read the key (G8). Lock waits are bounded or cancellable (G9). The data directory outside `/var/lib` is writable (G4).
  10. `domain/provider/execution_gate.go:ExecutionGate.WithCurrent` — A mutation that races `WithCurrent` is blocked or conflicts. The CAS uses `commitAdvanceTx` directly and never re-enters the mutation lock, so there is no recursive deadlock.
  11. `adapter/sqlite/profile_advance.go:commitAdvanceTx` — A revision bumped by a second process is caught in the transaction even when the in-process digest is still green.
  12. `domain/modelcatalog/refresh.go:RefreshCoordinator.Do` — When a manual refresh overlaps a tick, it gets an explicit busy result and no work is duplicated.
  13. `domain/modelcatalog/refresh.go:RefreshService.RefreshInternal` — A fetch failure keeps the catalog and profiles, the loop continues, and `Announcer` still drains durable events.
  14. `domain/modelcatalog/refresh.go:DiscoveryPipeline.Collect` — An incomplete scan is reported as degraded and blocks the affected advances. It is never reported as full success. A cycle cancelled at 3 hours does the same.
  15. `cmd/arbitron/main.go:domainStack.Close` — Cancellation mid-cycle joins without any DB use after close.
  16. `domain/modelcatalog/public.go:ProjectCatalog` — Secret-bearing proof and route fixtures never reach `model_catalog_list`.
  17. `cmd/arbitron/main.go:initDomain` — A nil required collaborator fails stack construction and cleans up resources already built.
  18. `adapter/sqlite/model_catalog_repo.go:CatalogStore.CommitCycle` — after a probe confirms Y at revision r, rotate the connection through the real admin service or update it from a second DB connection; the stale proposal must leave the durable model and journal unchanged. Recheck external workflow/account/binary fingerprints through `domain/provider/execution_gate.go:ExecutionGate.WithCurrent` immediately before SQL.
- **Seed evidence:**
  - `domain/provider/advance.go:31` declares `AdvanceStore` and `:40` declares `EligibleCandidateSource`. `grep -rn 'CommitAdvance('` finds no implementation.
  - `profile_advancement_journal` appears only in `021_following_profiles.up.sql:11`, with no Go writer.
  - `domain/provider/profile_service.go:174 recache` discards the `Reload` error.
  - `adapter/provider/cache.go:75 Load` calls `store.List` before taking `c.mu.Lock`.
  - `Connection.ConfigRevision` (`domain/provider/connection.go:14`) is read only by the row conversions at `adapter/sqlite/connection_repo.go:202,222`. It has no consumer.
  - `cmd/arbitron/model_state.go:17 initModelStateCrypto` returns only `provider.Crypto`. `main.go:418` builds `modelStateCrypto`, and nothing reads it.
  - `grep -rn 'ExecutionGate\|ListScopes\|ExecutionPathInventory'` finds nothing (B4).
  - `grep -rn 'RefreshLoop\|RefreshService\|model_catalog_'` finds nothing (B11, B12).
  - `domainStack.Close` (`main.go:246`) has no loop join yet.
  - `domain/schedule/evaluator.go:115` is the existing ticker pattern.

### Area 4 — an admin's catalog URL and Teams webhook edits persist, defaulting empty and redacted on public reads, raw for internal readers

- **Promise:** Arbitron has an editable Teams webhook setting that starts empty. The catalog source URL is admin-configurable. No webhook is supplied during this work (spec).
- **Entry:** `PUT /system/settings` in `adapter/http/server.go`, MCP `system_settings_update` in `adapter/mcp/tools_system.go`, and the dashboard card `dashboard/src/routes/Settings.tsx`.
- **Sink:** the `system_settings` rows (migration 019) through `adapter/sqlite/settings_repo.go:SettingsRepo`, and the raw values returned by `domain/settings/service.go:Service.ReadInternal`.
- **Files:** `domain/settings/settings.go`, `domain/settings/service.go`, `adapter/sqlite/settings_repo.go`, `adapter/sqlite/generated/settings.sql.go`, `adapter/sqlite/migrations/019_system_settings.up.sql`, `adapter/mcp/tools_system.go`, `adapter/mcp/deps.go`, `adapter/http/server.go`, `cmd/arbitron/main.go`, `dashboard/src/api/client.ts`, `dashboard/src/routes/Settings.tsx`, `cmd/arbitron/model_settings_wiring_test.go`. Unchanged files crossed: `domain/config/teams.go`.
- **Folded in:**
  1. The `settings.InternalReader` interface added by P3T6.
  2. Dashboard build and `make ui` embedding.
  3. G15, the stdio MCP identity question, is under Won't Fix.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `main.go:961` constructs `settingsdomain.NewService(adaptsqlite.NewSettingsRepo(db))`. It is projected into `mcpDeps()` (`main.go:304`), `httpDeps()` (`:340`) and the stack (`:1247`).
  2. `domain/settings/service.go:Service.ReadInternal` — Once built, the production readers of `ReadInternal` are `DiscoveryPipeline` (catalog URL) and `AnnouncementService` (webhook). Both take `settings.InternalReader` satisfied by this `Service`, never the public `Get`.
  3. `dashboard/src/routes/Settings.tsx:ModelAnnouncementSettings` — The dashboard omits the webhook when it is untouched and sends an explicit empty value to clear it.
  4. `cmd/arbitron/model_settings_wiring_test.go:TestModelSettingsWiringRoundTrip` — `TestModelSettingsWiringRoundTrip` goes from HTTP update to MCP read to `ReadInternal` after restart.
- **Review 2 — failure lens:**
  1. `domain/settings/service.go:Service.ReadInternal` — An internal read error is returned as an error and never as an empty value. Area 9's `awaiting_setting` depends on this.
  2. `domain/settings/service.go:Service.Update` — A redacted placeholder is rejected as input, so it is never saved as a URL.
  3. `domain/settings/service.go:requireAdmin` — A missing or non-admin identity is rejected on every surface without DB mutation.
  4. `adapter/sqlite/settings_repo.go:SettingsRepo.Update` — Concurrent independent patches lose neither value.
  5. `domain/settings/service.go:publicValues` — Webhook secrets are absent from errors, HTTP logs and dashboard state.
- **Seed evidence:**
  - `domain/settings/service.go:87 ReadInternal` has no production caller yet (B11, B15).
  - `main.go:961` is the single construction site, and `adapter/mcp/tools_system.go:109` guards a nil service.

### Area 5 — Claude Code and Codex probes mark a model usable only when the harness's own record shows it ran

- **Promise:** Arbitron separates cataloged models from models confirmed usable through Claude Code and Codex. A model is usable only after a real probe that proves the requested model executed (spec; D3, D4).
- **Entry:** the probe stage of `domain/modelcatalog/refresh.go:DiscoveryPipeline.Collect`, which calls the router `adapter/probe/model_usability.go:UsabilityProbe.Check`.
- **Sink:** persisted `modelcatalog.UsabilityEvidence` (`confirmed`/`unavailable`/`unknown`, keyed by `CanonicalID`, `PathKey` and `ConfigRevision`) in `CatalogStore`, with the executable spelling from `UsabilityMatcher.Match`.
- **Files:** `domain/modelcatalog/usability.go`, `adapter/probe/model_usability.go` (router and matcher, shared by every lane), `adapter/claudecli/model_probe.go`, `adapter/codex/model_probe.go` (account `models_cache.json` reader and session-rollout reader), `adapter/claudecli/claude.go`, `adapter/claudecli/runner.go`, `adapter/claudecli/env.go`, `adapter/claudecli/chat_adapter.go`, `adapter/codex/args.go`, `adapter/codex/env.go`, `adapter/codex/parse.go`, `adapter/codex/exec_session.go`, `adapter/probe/factory.go`, `adapter/probe/claudecli.go`, `cmd/arbitron/main.go`.
- **Folded in:**
  1. Limits: 30 s per check, 1 MiB output/rollout cap, at most 4 parallel checks.
  2. Probes run in an isolated temp workspace with tools, MCP and plugins disabled.
  3. Confirmed evidence is reused with no time TTL (D3–D6).
  4. Source-evidence records in `docs/designs/latest-model-profiles-source-evidence.md`.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `initDomain` constructs the router with the real Claude and Codex probe adapters. The Codex account-cache and rollout readers are fed the same resolved home and environment as `adapter/codex/env.go:codexEnv`, never a test root.
  2. `adapter/claudecli/model_probe.go:UsabilityProbe.Check` — The Claude probe runs `-p --model X --output-format json` with no `--fallback-model`, through the production launcher and env. It records confirmed only when X is a key of `modelUsage`.
  3. `adapter/codex/model_probe.go:UsabilityProbe.Check` — The Codex probe considers only `.models[].slug` from the account's `models_cache.json`. It runs `-m X` with production flags, correlates the `thread.started` ID (`adapter/codex/parse.go:112`) to that session's rollout, and confirms only when the probe turn's `turn_context.model == X`.
  4. `domain/modelcatalog/refresh.go:DiscoveryPipeline.Collect` — `Collect` probes every catalog entry with a `NativeID` on every configured path, including unknown-tier models and models with no followers. It skips only `(CanonicalID, PathKey, ConfigRevision)` already confirmed.
  5. `adapter/probe/model_usability.go:UsabilityMatcher.Match` — `UsabilityMatcher.Match` returns the spelling the adapter actually executes, and that spelling is what Area 3 writes to `Profile.Model`.
  6. `adapter/probe/model_usability_test.go:TestUsabilityConfirmedExactModel` — `TestUsabilityConfirmedExactModel` uses fake executables that check model argv, env, disabled tools and workdir, and emit wrong-model evidence when any input is missing.
- **Review 2 — failure lens:**
  1. `adapter/claudecli/model_probe.go:UsabilityProbe.Check` — A Claude fallback (X absent from `modelUsage`) records unavailable. Extra helper models in `modelUsage` are tolerated.
  2. `adapter/codex/model_probe.go:UsabilityProbe.Check` — A missing, malformed or truncated `models_cache.json` gives unknown and never admits every catalog model.
  3. `adapter/codex/model_probe.go:UsabilityProbe.Check` — A rollout from the wrong session or an earlier turn, or a missing `turn_context`, never confirms.
  4. `adapter/probe/model_usability.go:UsabilityProbe.Check` — A timeout, a 1 MiB overflow, or an unsupported binary version gives unknown. Credentials never appear in proof, output or logs.
  5. `domain/modelcatalog/refresh.go:DiscoveryPipeline.Collect` — Failed and unknown results are re-probed on a later refresh. Confirmed results survive restart without a re-probe.
  6. `adapter/probe/claudecli.go:claudeCLIProbe.Probe` — An installed binary, `--version` (`adapter/probe/claudecli.go:34`) or a listing alone never confirms.
- **Seed evidence:**
  - `adapter/probe/claudecli.go:34` runs `--version` and returns `ModelsUnsupported` (`:44`).
  - `grep -rn 'modelUsage\|models_cache\|turn_context'` finds nothing (B7).
  - The correlation key exists at `adapter/codex/parse.go:112`, where `thread.started` sets `sessionID`.

### Area 6 — pi, OpenCode and direct-connection probes mark a model usable only when the assistant response names the requested provider and model

- **Promise:** Usability through pi, OpenCode and configured connections is confirmed by a real exact-model probe, never by a listing (spec; D5, D6; design phase 3 "HTTP provider success must map actual returned model").
- **Entry:** the same `DiscoveryPipeline.Collect` probe stage, through the router into the pi, OpenCode and HTTP lanes.
- **Sink:** persisted `UsabilityEvidence` for those paths.
- **Files:** `adapter/pi/model_probe.go`, `adapter/opencode/model_probe.go`, `adapter/pi/args.go`, `adapter/pi/env.go`, `adapter/pi/parse.go`, `adapter/opencode/args.go`, `adapter/opencode/config.go`, `adapter/opencode/env.go`, `adapter/opencode/parse.go`, `adapter/probe/anthropic.go`, `adapter/probe/openai_compat.go`, `adapter/probe/bedrock.go`, `adapter/probe/ollama_native.go`, `adapter/probe/httpshared.go`, `adapter/probe/factory.go`, `domain/provider/probe.go`. Area 5 reviews the router `adapter/probe/model_usability.go`; read it here only for lane registration.
- **Folded in:**
  1. pi may run uncatalogued IDs (D5).
  2. OpenCode and pi listings are diagnostics only and are not prerequisites.
  3. Azure deployment and Bedrock opaque IDs need an evidenced mapping or are unknown.
- **Review 1 — wiring lens:**
  1. `adapter/pi/parse.go:foldMessage` — The pi probe uses the real launch mode: prompt on stdin, `--print --mode json --no-session --model <provider>/X`, env from `adapter/pi/env.go:piEnv`. It confirms only when the `message_end` provider and model equal the requested ones (`adapter/pi/parse.go:55`).
  2. `adapter/opencode/config.go:buildConfig` — The OpenCode probe injects the real config from `adapter/opencode/config.go:buildConfig` and runs `opencode run --format json -m <provider>/X`. It captures that run's `sessionID`, runs bounded `opencode export <sessionID>` with the same env, and matches the assistant `providerID`/`modelID`.
  3. `adapter/probe/factory.go:ProbeFactory.ProbeFor` — HTTP connection lanes confirm from the model or deployment the response actually returned, through an evidenced equivalence. `ProbeFactory.ProbeFor` listings only supply candidates.
  4. `cmd/arbitron/main.go:initDomain` — The router registers the pi, OpenCode and HTTP lanes at `initDomain` with real binaries, connections and the `Reveal` key path.
  5. `adapter/probe/model_usability_test.go:TestUsabilityConfirmedExactModel` — A test confirms an uncatalogued pi ID and rejects an OpenCode run whose export names another model.
- **Review 2 — failure lens:**
  1. `adapter/opencode/model_probe.go:UsabilityProbe.Check` — An `opencode export` failure, timeout or 1 MiB overflow gives unknown and never confirms.
  2. `adapter/pi/parse.go:foldMessage` — A `message_end` or export naming a different provider or model gives unavailable.
  3. `adapter/probe/model_usability.go:UsabilityProbe.Check` — A failed optional listing does not block a successful exact probe (D5/D6).
  4. `domain/modelcatalog/public.go:ProjectCatalog` — Injected connection keys never appear in proof, output, logs or catalog DTOs.
  5. `adapter/probe/model_usability.go:UsabilityMatcher.Match` — An opaque Azure deployment or Bedrock ID with no evidenced mapping gives unknown. Only a recognized routing prefix is stripped from slash-bearing IDs.
- **Seed evidence:**
  - `adapter/pi/parse.go:55` folds `message_end` for output text but never compares provider or model.
  - `grep -rn '"export"' adapter/opencode` finds nothing.
  - `adapter/probe/*` probes list models only (`domain/provider/probe.go` `ProbeResult.Models`).

### Area 7 — the configured catalog is fetched through the guarded client and classified by the persisted admin-editable D2 policy

- **Promise:** Arbitron keeps an internal catalog from the configured source (default models.dev). Tier membership and order come from the shipped, admin-editable full-match pattern table with numeric version order and a `release_date` tie-break. Unmatched models stay cataloged but are never followable (spec; D2).
- **Entry:** `DiscoveryPipeline.Collect` reads `domain/settings/service.go:Service.ReadInternal().CatalogURL`. The second entry is MCP `model_identity_policy_update` → `domain/modelidentity/policy_service.go:PolicyService.Update`.
- **Sink:** classified catalog entries (tier, `OrderKey`, canonical ID, `PolicyRevision`, raw source) persisted by `CatalogStore.CommitCycle` and visible in `CatalogStore.Snapshot`. Also the persisted policy rows and revision (migration 025).
- **Files:** `adapter/modelcatalog/fetcher.go` (exists), `adapter/outbound/guarded_client.go` (exists), `domain/modelcatalog/catalog.go` (exists), `adapter/modelcatalog/normalizer.go`, `adapter/modelcatalog/identity.go`, `adapter/modelcatalog/testdata/`, `domain/modelidentity/identity.go`, `domain/modelidentity/policy.go`, `domain/modelidentity/policy_service.go`, `adapter/sqlite/model_identity_policy_repo.go`, `adapter/sqlite/migrations/025_model_identity_policy.{up,down}.sql`, `adapter/mcp/tools_model_identity_policy.go`, `adapter/mcp/deps.go`, `adapter/sqlite/model_catalog_repo.go` (catalog portion), `docs/designs/latest-model-profiles-source-evidence.md`, `cmd/arbitron/main.go`. Unchanged files crossed: `domain/settings/service.go`, `adapter/notification/ssrf_guard.go`.
- **Folded in:**
  1. Migration 025 seeds every D2 row exactly and never reseeds over edits.
  2. Go regexp translation of `\d` and full-match anchoring.
  3. Parsing of `release_date` and `canonical_model_id`.
  4. Retention of historical current-model metadata.
  5. `VersionResolver.SpecificVersion` uses the shipped grammar, independent of policy reads.
  6. The source-evidence document.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `initDomain` constructs `NewFetcher` (`adapter/modelcatalog/fetcher.go:26`, which already calls `outbound.NewGuardedClient`) and the planned normalizer. Today the fetcher has no root caller and the normalizer is absent. `Collect` passes the settings `CatalogURL` to both `Fetch` and `Normalize`, with no hardcoded default or `SourceRef`.
  2. `domain/modelidentity/policy_service.go:PolicyService.Update` — `PolicyService` is built on the SQLite repo, gets `SetMutationGate` before any write, has its MCP tools registered through `mcpDeps()`, and is passed as `PolicySource` to the mapper. It is not a static fixture.
  3. `domain/modelidentity/identity.go:IdentityMapper.Map` — The normalizer fills `CatalogModel.ReleaseDate` and `CanonicalModelID`, and `IdentityMapper.Map` consumes them. `TestIdentityReleaseDateIsFed` changes the order when only the date changes.
  4. `domain/modelidentity/policy_test.go:TestIdentityPolicySeedsD2` — `TestIdentityPolicySeedsD2` covers every row and every worked example: `claude-opus-5-5` > `claude-opus-5`; `gpt-6.1-sol` > `gpt-6-sol`; `kimi-k3` is the newest kimi; `claude-opus-5-5-fast` and `deepseek-v4-flash:0731` are excluded.
  5. `cmd/arbitron/model_profiles_e2e_test.go:TestModelProfilesE2ESinks` — A root test edits the policy through registered MCP so that a previously unmatched newer model becomes followable, and the change reaches the durable following profile and the new-run model.
- **Review 2 — failure lens:**
  1. `adapter/modelcatalog/normalizer.go:Normalize` — A malformed top-level shape replaces nothing. A malformed slice keeps the prior slice and inhibits only that slice's advances.
  2. `adapter/modelcatalog/fetcher.go:Fetcher.Fetch` — A timeout, a body over 20 MiB (chunked included), a fourth redirect, a private or rebinding target, or a non-2xx response keeps the prior catalog.
  3. `domain/modelidentity/policy_service.go:PolicyService.Update` — Policy edits are rejected without change on an invalid regex, a bad capture index, an empty provider or tier, a contradictory duplicate rule or a stale `expected_revision`. A nil gate rejects the update rather than succeeding as a no-op. A catalog ID that matches two rules is ambiguous and ineligible.
  4. `domain/modelidentity/identity.go:Compare` — Equal numeric versions with missing or equal dates are not orderable. Different versions are ordered without dates.
  5. `domain/modelidentity/identity.go:IdentityMapper.Map` — Unknown-tier entries are retained with `NativeID` and source provider. A policy edit does not create rediscovery events.
  6. `domain/modelidentity/identity.go:VersionResolver.SpecificVersion` — A policy-read failure does not block new runs on an already-working explicit model, while advancement stays put.
- **Seed evidence:**
  - `adapter/modelcatalog/fetcher.go:NewFetcher` already calls `outbound.NewGuardedClient`; production root code never calls `NewFetcher`. The chain is present but unfed.
  - `Service.ReadInternal` (`domain/settings/service.go:87`) has no production caller.
  - `grep -rn 'IdentityPolicy\|PatternRule\|Normalize('` finds nothing.
  - There is no migration 025 (B1, B2, B6).

### Area 8 — an admin creates or edits a pinned or following profile, validated and initialized from the newest eligible candidate

- **Promise:** An admin can create a profile for a specific model version or one that follows the latest version in a chosen provider and tier. Creation never guesses a model (spec; design phase 2).
- **Entry:** `adapter/mcp/tools_profile.go:RegisterProfileTools` (`profile_create`, `profile_update`) and boot-time YAML `cmd/arbitron/profile_seed.go:SeedProfiles` (`cmd/arbitron/main.go:734`).
- **Sink:** the `profiles` row (`model`, `selection_mode`, `follow_provider`, `follow_tier`, `revision`) through `adapter/sqlite/profile_repo.go:ProfileRepo`, the reloaded `ProfileCache`, and the MCP response.
- **Files:** `domain/provider/profile_service.go`, `domain/provider/provider.go`, `domain/provider/profile_patch.go`, `domain/provider/selection.go`, `domain/provider/profile_store.go`, `adapter/mcp/tools_profile.go`, `cmd/arbitron/profile_seed.go`, `cmd/arbitron/main.go`, `domain/config/config.go`, `adapter/config/profiles.go`, `adapter/sqlite/profile_repo.go`, `adapter/sqlite/queries/profiles.sql`, `domain/modelcatalog/candidates.go` (`Newest`/`ValidateStart`), `domain/provider/following_service_test.go`, `adapter/mcp/tools_profile_following_test.go`. Unchanged files crossed: `domain/provider/capability.go`, `domain/provider/authz.go`, `adapter/sqlite/migrations/021_following_profiles.up.sql`.
- **Folded in:**
  1. G10: the returned revision is stale.
  2. G11: concurrent sparse edits, solved through `UpdateExpected`.
  3. G14: seed atomicity and retry.
  4. MCP schema fields `selection_mode`, `follow_target` and read-only `revision`.
  5. Sample YAML comments.
  6. `ListInternal` must stay unregistered publicly.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `initDomain` calls `ProfileService.SetModelSelection(VersionResolver, CandidateService)` before `SeedProfiles` at `main.go:734`. `SeedProfiles` takes the same ports (new signature) and the shared `ValidateSelection` helper, never public `Create`.
  2. `adapter/mcp/tools_profile.go:RegisterProfileTools` — The MCP create and update schemas accept `selection_mode` and nullable `follow_target{provider,tier}` and return read-only `revision`, sparse patches keep omission and clear distinct.
  3. `domain/modelcatalog/candidates.go:CandidateService.Newest` — Creating a following profile with no model calls `CandidateService.Newest` with the complete prospective profile (kind, route, backend, target, axes, tools) and persists the returned model (`TestFollowingCreationFromCatalog`).
  4. `domain/modelcatalog/candidates.go:CandidateService.ValidateStart` — Switching to following keeps the evidenced model. Route, model or target edits call `ValidateStart` on the prospective profile, and metadata-only patches do not.
  5. `domain/provider/profile_service.go:ProfileService.UpdatePatch` — Admin `Update`/`UpdatePatch`/`Delete` persist through `ProfileMutationStore.UpdateExpected`/`DeleteExpected` under the gate's mutation lease. The unconditional `ProfileStore.Update` is no longer the production path.
  6. `cmd/arbitron/profile_seed.go:SeedProfiles` — A cold empty DB seed initializes through bounded prospective discovery on the shared coordinator.
- **Review 2 — failure lens:**
  1. `domain/provider/selection.go:ValidateSelection` — An invalid mode, a target on a pinned profile, a following profile with no target, a tier unknown to the policy, a non-admin caller or incompatible axes each cause no write and no reload.
  2. `domain/provider/profile_service.go:ProfileService.SetModelSelection` — A nil version or candidate port when one is needed returns an explicit error. It never acts like an empty catalog or skips validation.
  3. `domain/provider/profile_service.go:ProfileService.UpdatePatch` — A catalog-down restart keeps persisted following rows. A metadata-only patch succeeds while discovery is down.
  4. `cmd/arbitron/profile_seed.go:SeedProfiles` — Seed partial failure: a retry after a storage fault seeds the remaining valid candidates (G14).
  5. `adapter/sqlite/profile_repo.go:ProfileRepo.UpdateExpected` — Concurrent model-only and tools-only patches both survive or one conflicts explicitly (G11). The returned revision equals the durable row (G10).
  6. `domain/provider/selection.go:ValidateSelection` — When the aggregator route provider differs from the catalog follow provider, both are preserved. Model spelling, including slashes, is preserved.
- **Seed evidence:**
  - `cmd/arbitron/profile_seed.go:19 SeedProfiles(ctx, store, rules, candidates)` has no version or candidate ports.
  - `grep -rn 'SetModelSelection\|ValidateSelection\|ListInternal\|UpdateExpected'` finds nothing (B3, B5).
  - `domain/provider/selection.go` exists from P2T1.

### Area 9 — each newly usable model gets one durable event and is delivered at least once to Teams with its original profile changes

- **Promise:** When the Teams setting is configured, each newly usable model produces at least one announcement that names the following profiles that changed, if any. A rare duplicate after an ambiguous send is accepted (spec; D7).
- **Entry:** event creation for the first confirmed observation inside `CatalogStore.CommitCycle`. Delivery runs through `domain/modelcatalog/announcements.go:AnnouncementService.Drain` at startup and on every cycle via `Announce`, including discovery failure.
- **Sink:** the Teams receiver POST from `adapter/notification/model_announcement_sender.go:TeamsModelSender.Send`/`SendPart`, and the event row's `announced` state.
- **Files:** `domain/modelcatalog/announcements.go`, `domain/modelcatalog/announcement_sender.go`, `adapter/notification/model_announcement_sender.go`, `adapter/notification/teams_payload.go`, `adapter/sqlite/model_announcement_repo.go`, `adapter/sqlite/model_catalog_repo.go` (event rows), `adapter/sqlite/migrations/022_model_catalog.{up,down}.sql` (026 if 022 has shipped), `cmd/arbitron/main.go`, `cmd/arbitron/model_announcement_wiring_test.go`. Unchanged files crossed: `adapter/outbound/guarded_client.go`, `adapter/notification/ssrf_guard.go`, `adapter/notification/teams_notifier.go` (must not be used), `domain/settings/service.go`.
- **Folded in:**
  1. `PartitionAnnouncement` for multipart cards.
  2. `RenewClaim`.
  3. Persisted backoff: 4 hours, doubling to 24 hours.
  4. Static diagnostic codes, never raw URLs.
  5. Replacing `DeferredAnnouncer` through `SetAnnouncer`.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `initDomain` constructs `AnnouncementService` with `settings.Service` (as `InternalReader`), the SQLite announcement repo, `NewTeamsModelSender(NewGuardedClient(...))` and the root clock. It injects the service through `RefreshService.SetAnnouncer` before recovery or the loop starts. `grep DeferredAnnouncer` shows no remaining production use.
  2. `adapter/sqlite/model_catalog_repo.go:CatalogStore.CommitCycle` — The frozen event payload is built in `CommitCycle` from the `Advancement` records that `commitAdvanceTx` actually returned. A model with no followers still gets an event with empty changes.
  3. `domain/modelcatalog/announcements.go:AnnouncementService.Drain` — `Drain` claims due `pending`/`delivery_unknown` events and sends with the current raw destination read for each attempt.
  4. `adapter/sqlite/model_announcement_repo.go:Complete` — A 2xx plus a saved marker marks the event `announced`, and a repeated drain sends nothing.
  5. `cmd/arbitron/model_announcement_wiring_test.go:TestModelAnnouncementWiringToReceiverSink` — `TestModelAnnouncementWiringToReceiverSink` asserts the receiver's card count and content alongside the executed model.
- **Review 2 — failure lens:**
  1. `adapter/sqlite/model_catalog_repo.go:CatalogStore.CommitCycle` — Discovery while the webhook is empty marks the event `suppressed` permanently. A settings read fault marks it `awaiting_setting`, and the next successful read classifies it with the payload preserved.
  2. `domain/modelcatalog/announcements.go:AnnouncementService.Drain` — Clearing the webhook pauses pending events. Reconfiguring it redirects only later attempts.
  3. `adapter/sqlite/model_announcement_repo.go:RecoverClaims` — A non-2xx response sets `pending` and a timeout or reset sets `delivery_unknown`, each with persisted backoff. A crash or marker failure after sending leaves `in_flight`, and lease expiry turns it into `delivery_unknown`, which is resent (D7 duplicate).
  4. `adapter/sqlite/model_announcement_repo.go:Complete` — A wrong `ClaimID` cannot complete another worker's event. Concurrent drains send each part once on the ordinary path.
  5. `adapter/sqlite/model_announcement_repo.go:CompletePart` — For multipart events, the receiver gets the union of every original change, and success on the first part alone cannot mark the event announced.
  6. `adapter/notification/model_announcement_sender.go:TeamsModelSender.Send` — The raw webhook URL never appears in errors, logs or the public catalog.
- **Seed evidence:**
  - `grep -rn 'AnnouncementService\|model_announcement\|ClaimPending'` finds nothing (B10, B13–B15).
  - `adapter/notification/teams_notifier.go:41` is asynchronous and unsuitable.
  - `adapter/notification/ssrf_guard.go:65` is package-private. The exported guard is `adapter/outbound/guarded_client.go`.
