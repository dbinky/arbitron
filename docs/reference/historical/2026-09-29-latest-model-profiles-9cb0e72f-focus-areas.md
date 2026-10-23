# Focus Areas — Latest Model Profiles Review Plan

This is the single-system review plan for branch `dev-latest-model-profiles-2026-10-16-9cb0e72f`.

- **Rows.** Each row is one **behaviour** the spec promises, traced from its entry to its sink across every layer. A row is never a layer, a file or a plan task.
- **Lenses.** Review 1 is the **wiring lens**: does the path exist, and is it fed? Review 2 is the **failure lens**: how does it fail?
- **Where things live.** Pass criteria are in `RALPH.md`. Seams between areas are in `docs/reference/paired-reviews.md`. Open gaps are in `docs/reference/gaps-identified.md`.

**State at seeding (HEAD `7ec204d`).**

- Areas 1, 3, 4 and 5 run in production today.
- Areas 2 and 8 lack their production refresh/delivery feed. Area 6 serves pinned edits but following creation lacks candidate injection. Area 7 serves admin settings reads/writes; its background consumers remain unwired. These wiring lenses retain the corresponding B-gap checks.
- Seed evidence below comes from production-only greps (excluding `*_test.go`) at seeding time.

**Budget.** 8 areas × 2 + 14 paired seams = **30** minimum passes, against 500 × 0.35 = **175** available.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | a post-migration run executes every node, retry, resume and child-input preparation on the model versions pinned at trigger, reruns and spawned children re-pin, and a NULL-pin legacy run keeps live resolution | high | [x] | [x] |
| 2 | a startup, manual or four-hour refresh safely fetches and classifies the catalog under the editable D2 policy, discovers usable models on every configured path and atomically advances eligible following profiles for new runs, rejecting stale evidence and keeping the working model on any failure | high | [x] | [x] |
| 3 | an interactive run's follow-up turns resume on its pinned model with live tools and credentials, and refuse a resumed session that ran a different model | high | [x] | [x] |
| 4 | a Claude Code or Codex invocation on exact model X is accepted only when the harness's own record shows X ran | high | [x] | [x] |
| 5 | a pi, OpenCode or direct-connection invocation on exact model X is accepted only when the assistant response or session export names the requested model and provider | high | [x] | [x] |
| 6 | an admin creates or edits a pinned or following profile through MCP or YAML seed, validated against version and path evidence and initialized from the newest eligible candidate when no model is given | high | [x] | [x] |
| 7 | an admin's catalog URL and Teams webhook edits persist through HTTP, MCP and dashboard, the webhook empty by default and redacted on every public read while internal readers get the raw value | med | [x] | [x] |
| 8 | each newly usable model gets one durable event classified by the webhook setting at discovery, and is delivered at least once to Teams with its original profile changes | med | [x] | [x] |

---

### Area 1 — a post-migration run executes on its trigger-time pins; reruns and children re-pin; legacy runs resolve live

- **Promise:** New runs use the updated model, and runs already underway keep the model they started with (spec Outcome 2, D1). Missing or corrupt pins are errors, never a live fallback.
- **Entry:** `domain/engine/engine.go:Engine.TriggerWithDepartment`. It calls `captureModelPins` before `instances.Create` and `Enqueue`. `Engine.Rerun` and `engine_spawn.go:Spawn` re-enter the same trigger.
- **Sink:** Two places:
  - The provider or harness invoked with the pinned model: `domain/engine/executor_llm.go:LLMCallExecutor.Execute` (Chat), `executor_agent.go:AgentCallExecutor.Execute` (RunAgent) and `child_input_preparer.go:LLMChildInputPreparer.Prepare`.
  - The pinned binding produced by `domain/provider/pinning.go:PinningResolver.Resolve`.
- **Files:**
  - **Changed:**
    - `domain/engine/engine.go`, `domain/engine/spawn_wake.go`, `domain/engine/ports.go`
    - `domain/instance/instance.go`, `domain/instance/execution_snapshot.go`
    - `domain/provider/pinning.go`, `domain/provider/model_snapshot.go`, `domain/provider/model_version.go`, `domain/provider/provider.go`, `domain/provider/execution_gate.go`
    - `domain/modelidentity/version.go`
    - `adapter/provider/pin_lookup.go`, `adapter/provider/pinned_route.go`, `adapter/provider/model_snapshot.go`, `adapter/provider/path_inventory.go` (`VersionRequests`), `adapter/provider/cache.go` (`SnapshotProfiles`), `adapter/provider/factory.go`
    - `adapter/sqlite/instance_model_pins.go`, `adapter/sqlite/instance_repo.go`, `adapter/sqlite/instance_spawn_rows.go`
    - `adapter/sqlite/migrations/020_instance_model_pins.up.sql`, `adapter/sqlite/queries/instances.sql`, `adapter/sqlite/generated/instances.sql.go`, `adapter/sqlite/generated/queue.sql.go`
    - `cmd/arbitron/main.go`
  - **Unchanged but crossed:**
    - `domain/engine/executor_llm.go`, `domain/engine/executor_agent.go`, `domain/engine/child_input_preparer.go`, `domain/engine/executor_spawn.go`, `domain/engine/engine_spawn.go`, `domain/engine/executor_timer.go` (`ContextWithInstanceID`)
    - `adapter/sqlite/queue_repo.go`, `domain/queue/manager.go`
- **Folded in:**
  1. Migration 020 `model_pins` nullable JSON. NULL with version 0 is legacy; non-NULL with version 1, including `{}`, is new. Check that every generated row conversion selects both columns.
  2. `TestPinsAllFullRowReads` covers `NextEligible`, `ListQueued` and `ListWaiting*`.
  3. `modelidentity/version.go:NewVersionResolver` refuses mutable or unproved aliases for new runs. Azure and litellm deployments are refused until B5. Check that refusal happens before `Create`.
  4. `pinned_route.go:validatePinnedRoute` rejects incompatible live routes.
  5. The pin-cache release is `Engine.SetPinCacheRelease`.
  6. The live model is shown on `node.started` and the queue enricher for pinned runs (D16).
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomainWithModelStateSecret` builds `NewPinLookup` (`:779`) and `NewPinningResolver` (`:780`). It injects them into the LLM and agent executors (`:867`, `:868`) and the child-input preparer (`:1036`), and calls `eng.SetModelSnapshotter` (`:1160`) and `eng.SetModelPinGate` (`:1007`). None of these receive nil.
  2. `engine.go:captureModelPins` is fed by `adapter/provider/model_snapshot.go:ModelSnapshotter.SnapshotModels`, which reads the real `ProfileCache.SnapshotProfiles` under the gate's `WithRead`. A workflow without aliases persists `{}` with version 1, never NULL.
  3. A pinned run reaches the sink carrying the pinned model in argv or the request body after the profile is advanced: `cmd/arbitron/model_pins_sink_wiring_test.go:TestModelPinsWiringLLMCallSink` and `model_pins_agent_child_sink_wiring_test.go:TestModelPinsWiringAgentCallSink` / `ChildInputSink`.
  4. Retry and resume reuse the persisted pins, while rerun and spawn re-pin fresh: `cmd/arbitron/model_pins_lifecycle_test.go:TestRetryResumeRerunAndSpawnPins`.
  5. A NULL-pin legacy row resolves live through the same root: the legacy case in `TestModelPinsWiringLLMCallSink`, plus `domain/provider/pinning_test.go:TestPinningResolverLegacyAndOutOfRunResolveLive`.
  6. `ResolvedBinding.Pinned` is set by `PinningResolver.Resolve` (`pinning.go:102`) and reaches every per-binding adapter built by `factory.go:ChatProviderFor` / `AgentProviderFor`. Row 1↔4 and row 1↔5 cover what the adapters then do with it.
- **Review 2 — failure lens:**
  1. `domain/engine/engine.go:TriggerWithDepartment`: A snapshot, gate or `Create` fault creates nothing and enqueues nothing. `TestTriggerSnapshotFailureCreatesNothing` and `TestTriggerGateFailureCreatesNothing` exist, but no test faults `InstanceRepository.Create` itself (gap D9).
  2. A corrupt pin payload or discriminator fails the run without a provider call and frees capacity (`TestModelPinsCorruptionFailsRunAndFreesCapacity`). The same corruption also stalls `queue_repo.go:NextEligible`, aborts `RecoverCrashedInstances` at startup, and blocks every trigger through `ListExecutionSnapshots` (gaps D6, D7).
  3. `domain/provider/pinning.go:ForRun`: A lost run context on a batch consumer must fail rather than resolve live. Today the LLM, agent and child-input consumers get the plain resolver, not `ForRun()` (gap D8).
  4. `adapter/provider/model_snapshot.go:SnapshotModels`: An alias the workflow uses but no profile provides, an unproved mutable alias, and an incompatible live route are each refused before enqueue (`TestModelPinsWiringRejectsIncompatibleLiveRoute`).
  5. `domain/provider/execution_gate.go:WithRead`: Concurrent advancement during a trigger yields one coherent snapshot (`TestTriggerPinsCoherentGeneration`; the gate's read lease is not re-entered recursively).
  6. Per-trigger cost and coupling: `path_inventory.go:VersionRequests` scans every manifest and active run under the read lease. One unreadable unrelated manifest must not fail this trigger (gap D10).
- **Seed evidence:**
  - Two calls carry no run context:
    - `engine.go:641` calls `captureModelPins`; `Create` is at `:671` and `Enqueue` at `:681`.
    - `main.go:1167` hands only the interactive consumers `pinningResolver.ForRun()`.
  - `instance_repo.go:996` (`ListExecutionSnapshots`) returns an error for the whole list on one corrupt row.
  - `PinLookup.Cached` has no production caller. Its production fields are read only by tests: `domainStack.pinLookup` and `domainStack.profileResolver`.

### Area 2 — refreshes fetch and classify the catalog, discover usable models and atomically advance following profiles

- **Promise:** The spec and D2 require:
  - Every four hours, Arbitron distinguishes cataloged from confirmed-usable models on this installation's paths.
  - By the next successful refresh, eligible following profiles select the newer usable model for new runs.
  - Refresh failure or no eligible replacement keeps the last known working model.
  - Pinned profiles never move.
  - The configured catalog is fetched safely and classified by the persisted admin-editable D2 full-match policy; unmatched models remain visible but ineligible.
- **Entry:** Scheduled/manual refresh, plus the admin policy edit that controls classification:
  - Planned (B2/B3): `domain/modelcatalog/loop.go:RefreshLoop.Run` (startup pass plus four-hour ticker) and the admin `model_catalog_refresh` tool → `domain/modelcatalog/refresh.go:RefreshService.Refresh`, both through one `RefreshCoordinator`.
  - Policy edit: `adapter/mcp/tools_model_identity_policy.go:modelIdentityPolicyHandler` → `domain/modelidentity/policy_service.go:PolicyService.Update` → `adapter/sqlite/model_identity_policy_repo.go:Replace`; the next refresh must consume that persisted revision.
  - Existing direct entry: `domain/provider/advance_service.go:ProfileService.AdvanceModel`.
- **Sink:** Three stages:
  - The durable cycle: `adapter/sqlite/model_catalog_repo.go:ModelCatalogRepo.CommitCycle` writes catalog slices, per-path evidence, `ProbeCursor`, events and report. It also runs `adapter/sqlite/profile_advance.go:commitAdvanceTx` for every proposal, writing `profiles.model`/`revision` and the journal.
  - The confirmed publication: `adapter/provider/cache.go:ProfileCache.PublishCommitted`, then `ModelCatalogRepo.MarkPublished` / `Acknowledge`.
  - The observable result: the new-run executed model, read back through `model_catalog_list`.
- **Files:**
  - **Changed:**
    - `domain/modelcatalog/catalog.go`, `paths.go`, `store.go`, `report.go`, `usability.go`, `public.go`. Planned: `candidates.go`, `refresh.go`, `loop.go` (B2).
    - `adapter/sqlite/model_catalog_repo.go`, `adapter/sqlite/migrations/022_model_catalog.up.sql`
    - `domain/provider/advance.go`, `advance_service.go`, `publication.go`, `execution_gate.go`, `profile_service.go`
    - `adapter/sqlite/profile_advance.go`, `adapter/sqlite/profile_repo.go`, `adapter/sqlite/queries/profiles.sql`, `adapter/sqlite/migrations/021_following_profiles.up.sql`
    - `adapter/provider/path_inventory.go`, `adapter/provider/harness_fingerprint.go`, `adapter/provider/cache.go`
    - `domain/instance/execution_snapshot.go`, `adapter/sqlite/instance_repo.go` (`ListExecutionSnapshots`)
    - `adapter/filesystem/workflow_repo.go` (`ListScopes`, `LoadManifests`)
    - `domain/workflow/service.go`, `admin_ops.go`, `delete.go`, `rename.go`
    - `domain/provider/connection_service.go`, `domain/provider/connection.go`, `adapter/sqlite/connection_repo.go`, `adapter/sqlite/queries/connections.sql`
    - `adapter/sqlite/migrations/023_execution_revisions.up.sql`, `024_connection_revision_floor.up.sql`
    - `adapter/crypto/installation_secret.go`, `installation_secret_unix.go`, `installation_secret_windows.go`
    - `cmd/arbitron/model_state.go`, `cmd/arbitron/main.go`
    - Planned: `adapter/probe/model_usability.go` (B1), `adapter/mcp/tools_model_catalog.go` (B3)
    - `adapter/modelcatalog/fetcher.go`, `normalizer.go`, `testdata/models.dev.json`, `testdata/models.dev-provenance.md`; `adapter/outbound/guarded_client.go`
    - `domain/modelidentity/identity.go`, `policy.go`, `policy_seed.go`, `policy_service.go`; `adapter/sqlite/model_identity_policy_repo.go`, `migrations/025_model_identity_policy.up.sql`; `adapter/mcp/tools_model_identity_policy.go`, `register.go`, `deps.go`
  - **Unchanged but crossed:**
    - `adapter/provider/factory.go`, `adapter/provider/interactive_stream_selector.go`
    - `domain/engine/executor_agent.go` (backend precedence)
    - `cmd/arbitron/logging_reloader.go`
    - `domain/schedule/evaluator.go` (ticker pattern)
    - `install.sh`, `install.ps1`
    - `domain/identity`; `adapter/notification/ssrf_guard.go` (guard pattern)
- **Folded in:**
  1. The `ExecutionGate` and `ExecutionRevision`, with the revision bumps from connection, profile, workflow and policy mutations (`WithMutation` at `connection_service.go:57`, `profile_service.go:198`, `workflow/service.go:115`, `policy_service.go:66`). Three mutations are not gated: `SaveNodePositions`, workflow files edited on disk, and harness binary or account changes. They must still change the revision through the rescan.
  2. The migration 024 connection revision floor.
  3. The `initModelState` HMAC fingerprint key, carrying gaps G4, G6, G7, G8 and G9.
  4. The probe policy (D3–D6): probe once per path revision; re-probe failed or unknown results; never re-probe a confirmed one (`ProbeCursor`).
  5. Never downgrade. Equal versions with missing or equal dates never advance.
  6. The pinned-profile guard.
  7. `ProjectCatalog`, which keeps secret-bearing proof paths out of `model_catalog_list`.
  8. Startup-cleanup gap G16.
  9. Catalog ingestion and editable D2 policy (former Area 7): `Fetcher.Fetch` → `Normalizer.Normalize` → `IdentityMapper.Map` → `CommitCycle`; these are inputs to this behaviour, not a separate acceptance sink.
  10. Fetch limits: 20 MiB, 30 s, 3 redirects. Migration 025 and `policy_seed.go:InitialRules` parity (`TestPolicySeedMatchesInitialRules`); `policy.go:CompileRules` rejects empty rules.
  11. Former paired row 10: tier ordering, policy edits during a cycle and `UnmatchedTargets` are internal selector/commit checks (wiring checks 4, 7, 8; failure checks 4, 8, 9).
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomainWithModelStateSecret`: The refresh orchestration must exist and run from the composition root:
     - `RefreshLoop`, `RefreshCoordinator`, `RefreshService` and `DiscoveryPipeline` exist (B2).
     - `cmd/arbitron/main.go:initDomainWithModelStateSecret` builds the dependency graph and injects the real candidate source before `SeedProfiles`; it starts the loop only after seed, publication recovery and cache load in both `runServe` and `runMCP`.
     - `domainStack.Close` cancels and joins the loop before closing the DB (B3).
  2. `domain/modelcatalog/refresh.go:DiscoveryPipeline.Collect (planned B2)`: `DiscoveryPipeline.Collect` is fed by real producers:
     - `settings.Service.ReadInternal` for the URL and webhook.
     - `adapter/modelcatalog/fetcher.go:NewFetcher` with the guarded client.
     - `NewNormalizer`, `IdentityMapper.Map` with the persisted policy, and `ExecutionPathInventory.Snapshot`.
     - The B1 probe router.

     The pipeline produces a `CycleCommit` that `ModelCatalogRepo.CommitCycle` writes. Show each production call site with `grep -rn`; today all of them are test-only.
  3. `adapter/provider/path_inventory.go:Snapshot`: The inventory enumerates every configured path:
     - providers and harnesses with no following profiles;
     - node backend overrides and the deployment-default harness (node > profile > deployment precedence, as in `AgentCallExecutor.Execute`);
     - interactive routes as `InteractiveStreamSelector` actually supports them.

     `harness.Binaries` is hardcoded at `main.go:1138–1155` to claude-code only, with the rest found on PATH. Confirm that matches the real launchers.
  4. Candidate selection (`domain/modelcatalog/candidates.go:CandidateService`, B2):
     - The expected profile comes from `ProfileService.ListInternal`.
     - The candidate must be in the same follow provider and tier, strictly newer by `modelidentity.Compare`, confirmed on **every** path that consumes the alias, and share one executable spelling.
     - The result reaches `commitAdvanceTx` inside the same transaction as events and the report.
  5. `adapter/provider/cache.go:PublishCommitted`: Publication is confirmed and recovered:
     - `PublishCommitted` verifies the committed revisions are in the cache before `MarkPublished`.
     - At startup, `ModelCatalogRepo.Unpublished` is replayed before serving. Today `Recover`, `Unpublished` and `MarkPublished` have no production caller.
     - A root test ends at the executed model: old run X, new run Y.
  6. `domain/modelcatalog/refresh.go:RefreshService.Refresh/List (planned B2/B3)`: The admin catalog tools: `model_catalog_list` returns `ProjectCatalog` output, and `model_catalog_refresh` uses the same coordinator. Both authorize in the service, and the internal refresh never impersonates an admin (B3).
  7. `adapter/sqlite/model_catalog_repo.go:catalogAdvanceEvidenced`: Revision freshness at the sink: `catalogAdvanceEvidenced` and `checkExecutionRevisionTx` consult the path's current `ConfigRevision` and workflow hashes, not only `CanonicalID` and `NativeModel` (gaps D1, D2).
  8. `PolicyService.Update` is authorized in the service, gated at `main.go:initDomainWithModelStateSecret`, and registered by `adapter/mcp/register.go:RegisterAllTools`. `IdentityMapper.Map` reads that persisted policy on each cycle; an MCP replacement changes stored `Entry.Tier`, `OrderKey` and `PolicyRevision`, then affects the durable selection. Extend `cmd/arbitron/model_identity_policy_wiring_test.go:TestIdentityPolicyRootWiring` beyond the policy row.
  9. `adapter/modelcatalog/normalizer.go:Normalize`: `Fetcher.Fetch` reads the current `settings.Service.ReadInternal().CatalogURL`; `Normalizer.Normalize` carries all provider slices, raw evidence and configured `SourceRef` into `CommitCycle`. `ProjectCatalog` exposes unmatched follow targets and cataloged unknowns without source URLs or proof paths. No-followers discovery still reaches durable observations.
- **Review 2 — failure lens:**
  1. `adapter/sqlite/model_catalog_repo.go:catalogAdvanceEvidenced`: Stale evidence is rejected:
     - A probe confirms Y at path revision r. Then the connection's credential is rotated through the real admin service, or by a second DB connection.
     - The stale proposal must leave `profiles.model` and the journal unchanged.
     - A test must go red if the `PathKey`/`ConfigRevision` comparison is removed (gaps D1, D2).
  2. `adapter/sqlite/profile_advance.go:commitAdvanceTx`: CAS conflict: any expected-field mismatch, including an ABA edit or delete-and-recreate (no profile revision floor, gap D4), returns `ErrAdvanceConflict` with no row or journal change, while other proposals proceed. A journal or event write fault rolls back the model update. The current fixture drops the table before the UPDATE (gap D11).
  3. `adapter/provider/cache.go:PublishCommitted`: A reload fault returns the committed record plus a pending publication. Repair publishes Y without a second journal row, and no report claims success before `MarkPublished`. Request-ID replay must return only that alias's original change (gaps D5, G18).
  4. `domain/modelcatalog/candidates.go:CandidateService (planned B2)`: These never advance:
     - a pinned profile;
     - an unknown or unmappable current version;
     - equal versions with missing or equal dates.

     `CandidateService` must compare verified current identity, including historical metadata when absent from the latest slice: disappearance alone never authorizes replacement or downgrade (phase 3 design). The sink does not enforce ordering today, so the selector must (B2).
  5. `adapter/sqlite/model_catalog_repo.go:CommitCycle`: A fetch failure keeps the catalog and profiles, and the loop continues. A malformed slice isolates only that slice and blocks only its advances. Probe failure means unknown, never usable. A corrupt active-run row must not block every advancement (`ListExecutionSnapshots`, gap D6).
  6. `domain/modelcatalog/refresh.go:RefreshCoordinator.Do` / `loop.go:RefreshLoop.Run` (B2) single-flight: a manual refresh overlapping a tick gets an explicit busy result. Cancelling mid-cycle joins without using the DB after close. A degraded or budget-exhausted scan commits completed evidence, so repeated failures cannot starve new models.
  7. `adapter/crypto/installation_secret.go:LoadOrCreateInstallationSecret` integrity and startup cleanup: G6, G7, G8, G9 and G4 for the model-state key, and G16 for `initDomainWithModelStateSecret` returning without closing `db` and `logCloser`.
  8. `domain/modelidentity/policy_seed.go:InitialRules` must exclude variants such as `claude-opus-5-5-fast` and dated snapshots such as `claude-opus-4-20250514`, which currently ranks as `4.20250514` (D3). `IdentityMapper.Map` must leave ambiguous matches ineligible; `modelidentity.Compare` orders numeric tuples first and release dates only on ties; equal versions with missing/tied dates remain unorderable. A policy replacement during normalization/probing cannot mix revisions at `catalogAdvanceEvidenced`; a removed target remains unchanged and appears in `CycleCommit.UnmatchedTargets` / `ProjectCatalog`.
  9. `adapter/outbound/guarded_client.go:NewGuardedClient` and `Fetcher.Fetch` reject unsafe DNS/dial targets, credential-bearing URLs, private redirects and oversized bodies; errors expose no URL query token (D17). `Normalizer.Normalize` rejects a foreign top-level shape without replacement and isolates a malformed slice. A `PolicyService.ReadInternal` fault propagates without committing replacement catalog or profile state. `PolicyService.Update` refuses unauthorized, invalid and stale replacements; `ModelIdentityPolicyRepo.Replace` allows exactly one concurrent CAS winner and preserves edits on restart.
- **Seed evidence:**
  - Constructed at the root but only partly consumed:
    - `ModelCatalogRepo.CommitCycle` (`model_catalog_repo.go:31`), `Recover` (`:428`), `MarkPublished` (`:447`), `ProfileService.AdvanceModel` (`advance_service.go:50`) and `ProjectCatalog` (`public.go:42`) are called only from tests.
    - `main.go:1002` constructs the catalog repo, but only `SetPublicationJournal` (`:1004`) consumes it.
    - `pathInventory` (`main.go:1138`) is used only for `Revision` (`:1157`) and the snapshotter (`:1160`). `Snapshot` is called only from tests.
  - Absent everywhere, tests included: `RefreshLoop`, `RefreshService`, `RefreshCoordinator`, `DiscoveryPipeline`, `CandidateService`, `model_catalog_list` and `model_catalog_refresh`.
  - `catalogAdvanceEvidenced` (`model_catalog_repo.go:201`) matches evidence on `CanonicalID`, `NativeModel` and `Confirmed()` only.
  - `checkExecutionRevisionTx` (`profile_advance.go:72`) checks the policy and connection revisions but ignores `WorkflowHashes`.
  - `Fetcher.Fetch` and `Normalizer.Normalize` have no production construction/caller. `Normalizer.normalizeSlice` feeds `IdentityMapper.Map`; `persistCatalogTx` stores its entries inside the same `CommitCycle` transaction as advancement.
  - `PolicyService.Update` / `ModelIdentityPolicyRepo.Replace` are reachable through MCP, but classification is not: the root passes policy only to inventory today. `policy_seed.go:InitialRules` admits the major-only dated snapshot (D3); `fetcher.go:Fetch` wraps `url.Error` (D17).

### Area 3 — interactive follow-up turns resume on the pinned model

- **Promise:** Runs already underway keep their model, including interactive follow-up turns (design phase 1). Credentials, tools and connection metadata stay live. A resumed harness session recorded on another model is refused.
- **Entry:** `domain/engine/conversation.go:conversationHandler.HandleMessage`, which attaches the run ID before resolving. The first turn enters through `domain/engine/executor_interactive.go:InteractiveExecutor.Execute`.
- **Sink:** The harness or provider stream invoked with the pinned model and `RequireExecutedModel`:
  - `adapter/claudecli/stream_agent_adapter.go:StreamAgentAdapter.StreamChat`, with `streamstate.go:streamState.Finalize`.
  - `adapter/codex/exec_session.go:execSession.Start`, with `rollout.go:rolloutVerifier.beforeResume` / `afterTurn`.
  - A chat-kind `ChatProviderFor` stream.
- **Files:**
  - **Changed:** `domain/engine/conversation.go`, `domain/engine/executor_interactive.go`, `domain/provider/pinning.go` (`ForRun`), `adapter/provider/pin_lookup.go`, `adapter/provider/interactive_stream_selector.go`, `adapter/provider/pinned_route.go`, `adapter/claudecli/stream_agent_adapter.go`, `adapter/claudecli/streamjson.go`, `adapter/claudecli/streamstate.go`, `adapter/codex/exec_session.go`, `adapter/codex/turn_session.go`, `adapter/codex/interactive_provider.go`, `adapter/codex/rollout.go`, `cmd/arbitron/main.go`.
  - **Unchanged but crossed:** `adapter/provider/factory.go`, `domain/engine/executor_timer.go`.
- **Folded in:**
  1. The `ARBITRON_CODEX_INTERACTIVE_VERIFIED` opt-in at `main.go:1122`.
  2. The pin-cache release at `completeSession`.
  3. Root-test gap A2l (one combined SQLite continuation fixture).
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomainWithModelStateSecret`: `main.go:1167` builds `pinningResolver.ForRun()` and passes it to both the interactive executor (`:1169`) and the conversation handler (`:1176`). The per-binding stream factories are built at `:1113–1124`.
  2. `domain/engine/conversation.go:HandleMessage` attaches the authorized, loaded instance ID before `Resolve`. It sets `ChatRequest.RequireExecutedModel = binding.Pinned` (`conversation.go:248`) and takes `ToolScope` from the live profile. Paired Area 3 ↔ Area 6 verifies actual admin edits, including an explicit empty scope and a changed connection, at the resumed invocation.
  3. After the profile advances, a resumed turn carries the pinned model in argv: `cmd/arbitron/model_pins_interactive_wiring_test.go:TestModelPinsWiringInteractiveResumeSink` / `ResumeLiveTools`.
  4. `domain/engine/conversation.go:completeSession`: Session end releases the pin cache (`TestModelPinsWiringEndSessionReleasesPinCache`). A reopened process reloads the persisted pins.
- **Review 2 — failure lens:**
  1. `domain/provider/pinning.go:ForRun`: A lost run context is refused before streaming (`TestConversationHandler_HandleMessage_LostRunContextRefusesBeforeStream`). A legacy NULL-pin run resolves live.
  2. `adapter/codex/rollout.go:beforeResume`: A Codex resume against a session recorded on another model is refused before spawning (`TestRolloutVerifier_BeforeResume`). Claude has no pre-resume check, so a mismatch surfaces only at the final result, after the stream has reached the user. Show that this result fails the turn and is not recorded as success.
  3. `domain/engine/executor_interactive.go:Execute`: First-turn persistence failure: `executor_interactive.go:234` only logs the `NodeExecution.Create` error, then moves the run to waiting (gap D12).
  4. `domain/engine/executor_interactive.go:Execute`: A nil or stalled first-turn stream: `executor_interactive.go:159` ranges over `stream` without a nil guard or a cancellation check (gap D13). The conversation path is already guarded (`conversation_stream_failure_test.go`).
  5. `domain/engine/conversation.go:HandleMessage/EndSession`: A concurrent follow-up turn and session end respect `conversation_ordering_test.go`. A release racing a turn never yields a live-resolved model.
- **Seed evidence:**
  - `interactive_stream_selector.go:70` (`StreamProviderFor`): chat-kind delegates to `ChatProviderFor`, and agent-kind goes through `agentStreamFor` with `validatePinnedRoute`.
  - `main.go:1187–1191`: the pin-cache release is set via a type assertion, and its failure branch leaks `db` and `logCloser` (G16).

### Area 4 — Claude Code and Codex accept exact model X only on harness evidence

- **Promise:** D3 and D4, and pinned-run enforcement. A Claude call with `--model X` and no `--fallback-model` succeeds only if `modelUsage` has key X. A Codex `-m X` turn succeeds only if the rollout's `turn_context.model` is X. Errors are secret-free.
- **Entry:**
  - Claude: `adapter/claudecli/agent_adapter.go:AgentAdapter.RunAgent`, `chat_adapter.go:ChatAdapter.Chat` / `StreamChat` and `stream_agent_adapter.go:StreamAgentAdapter.Chat` / `StreamChat`.
  - Codex: `adapter/codex/provider.go:Provider.RunAgent` and `interactive_provider.go:startTurn`.
- **Sink:**
  - Claude: `adapter/claudecli/shared.go:collectResult` with `streamjson.go:executedModelMatches`, plus `streamstate.go:streamState.Handle` / `Finalize`.
  - Codex: `adapter/codex/rollout.go:rolloutVerifier.afterTurn` / `beforeResume`.
- **Files:**
  - **Changed:**
    - `adapter/claudecli/model_probe.go`, `agent_adapter.go`, `chat_adapter.go`, `stream_agent_adapter.go`, `streamjson.go`, `streamstate.go`, `shared.go`, `provider.go`, `testdata/model-evidence.md`
    - `adapter/codex/rollout.go`, `exec_session.go`, `interactive_provider.go`, `parse.go`, `provider.go`, `turn_session.go`, `testdata/model-evidence.md`
    - `adapter/provider/factory.go`, `adapter/provider/pinned_route.go`
  - **Unchanged but crossed:** `adapter/claudecli/claude.go` (`buildArgs`), `adapter/codex/env.go`, `adapter/codex/args.go`.
- **Folded in:**
  1. `claudecli.Provider` is never constructed in production, so its edits are dead code. Delete them or justify them.
  2. The source-evidence notes in `testdata/model-evidence.md` must match the code.
  3. `--fallback-model` appears nowhere in production. Only tests assert that it is absent.
- **Review 1 — wiring lens:**
  1. `adapter/claudecli/model_probe.go:requiredExecutedModel`: Batch: `requiredExecutedModel(mission.Model, false, a.binding)` reads `binding.Pinned` on every Claude `RunAgent` and `Chat`. Codex `RunAgent` verifies when `p.binding.Pinned` is set. Both bindings come from `factory.go` per-binding construction at the root.
  2. `adapter/codex/interactive_provider.go:startTurn`: Streams: Claude `StreamChat` and Codex `startTurn` take the requirement only from `req.RequireExecutedModel`, never from `binding.Pinned`, and never compare `req.Model` with `binding.Model` (gap D14). Name the producer that always sets it.
  3. Tests that end at the parsed harness record: `cmd/arbitron/model_usability_claude_wiring_test.go`, `model_usability_codex_wiring_test.go`, `claudecli/model_probe_test.go:TestModelProbeClaudeNonStreamingExactExecution` and `codex/model_probe_test.go:TestModelProbeCodexExactExecution`.
  4. `adapter/provider/harness_fingerprint.go:NewHarnessFileFingerprinter`: D4's account-model filter: nothing reads `~/.codex/models_cache.json` `.models[].slug` today; `harness_fingerprint.go:86` only hashes the file. It belongs to the B1 discovery probe. Keep account-list filtering in Area 2; paired row 9 verifies the shared launch and execution-evidence contract.
- **Review 2 — failure lens:**
  1. `adapter/claudecli/streamjson.go:executedModelMatches`: Fallback detected: `modelUsage` without X fails, while extra helper models are tolerated. Unsupported suffixes are rejected (`model_probe_suffix_test.go`).
  2. Codex cases fail: missing rollout, another session, a failed or incomplete turn, an oversized rollout, or a `turn_context` with no model (`rollout.go:readRollout` records `""`; guarded only because the argument builders refuse an empty model).
  3. Errors stay secret-free (`model_probe_error_test.go`, `model_probe_stream_error_test.go`). `exec_session.go:execSession.Start` echoes untrusted stream thread IDs in its mismatch error; confirm they carry no secret, or redact them.
  4. A duplicate or out-of-order result event in `streamstate.go:Handle` cannot turn a mismatch into success, the way `collectResult` already refuses one.
- **Seed evidence:**
  - `claudecli/streamjson.go:pinnedModel` reads `req.RequireExecutedModel` only.
  - `codex/interactive_provider.go:startTurn` sets `VerifyModel: req.RequireExecutedModel`.
  - `grep -rn 'models_cache' --include=*.go` finds production hits only in `adapter/provider/harness_fingerprint.go`.

### Area 5 — pi, OpenCode and direct connections accept exact model X only when the response names it

- **Promise:** D5, D6, and pinned-run enforcement on HTTP lanes. The assistant `message_end` (pi) or the export (OpenCode) must name the requested provider and model. The Anthropic, OpenAI-compatible and OpenRouter responses must name the model. Bedrock and Azure refuse until an exact-execution producer exists (B5).
- **Entry:**
  - Harness lanes: `adapter/pi/provider.go:Provider.RunAgent` and `adapter/opencode/provider.go:Provider.RunAgent`.
  - HTTP lanes: `adapter/anthropicapi/provider.go:Chat` / `StreamChat`, `adapter/openrouter/provider.go:Chat` / `StreamChat` and `adapter/bedrock/provider.go:Chat` / `StreamChat`.
- **Sink:**
  - Harness lanes: `adapter/pi/model_probe.go:parsed.verifyExecutedModel`, and `adapter/opencode/model_probe.go:modelProof` + `verifyModelExport` (via `checkModelExecution`).
  - HTTP lanes: `adapter/anthropicapi/model_probe.go:messageIdentity.verify` and `adapter/openrouter/model_probe.go:verifyExecutedModel`.
- **Files:**
  - **Changed:** `adapter/pi/model_probe.go`, `parse.go`, `provider.go`; `adapter/opencode/model_probe.go`, `parse.go`, `provider.go`, `testdata/model-evidence.md`; `adapter/anthropicapi/model_probe.go`, `provider.go`, `response.go`, `testdata/model-evidence.md`; `adapter/openrouter/model_probe.go`, `provider.go`, `response.go`, `testdata/model-evidence.md`; `adapter/bedrock/provider.go`.
  - **Unchanged but crossed:** `adapter/pi/args.go`, `adapter/pi/env.go`, `adapter/opencode/config.go`, `adapter/azurefoundry/*`, `adapter/provider/factory.go`.
- **Folded in:**
  1. `pi/args.go:buildArgs` passes `binding.Model` verbatim rather than `<provider>/X` as D5 states. Show that verification fails closed, or align argv with D5.
  2. OpenCode export bounds: 30 s and 1 MiB.
  3. Bedrock always returns `ErrModelExecutionEvidenceUnavailable`.
- **Review 1 — wiring lens:**
  1. `adapter/pi/provider.go:RunAgent` and `adapter/opencode/provider.go:RunAgent` verify whenever `binding.Pinned` is set. The HTTP lanes verify when `Pinned` or `RequireExecutedModel` is set. Every binding is built per run by `factory.go` from the `PinningResolver` output.
  2. `adapter/provider/factory.go:ChatProviderFor/AgentProviderFor`: Root tests end at the parsed response: `cmd/arbitron/model_usability_pi_wiring_test.go`, `model_usability_opencode_wiring_test.go`, `model_usability_http_wiring_test.go` and `model_usability_bedrock_wiring_test.go`.
  3. `adapter/provider/interactive_stream_selector.go:StreamProviderFor` routes interactive chat-kind bindings to `anthropicapi` or `openrouter` `StreamChat` through `InteractiveStreamSelector` → `ChatProviderFor`, with `RequireExecutedModel` set (row 3↔5).
  4. `adapter/azurefoundry/provider.go:Chat` has no evidence check. Pinned Azure runs are blocked only because `validatePinnedRoute` treats Azure as opaque (B5).
- **Review 2 — failure lens:**
  1. `adapter/pi/model_probe.go:verifyExecutedModel`: pi rejects:
     - a wrong provider;
     - a user-message echo;
     - delta-only or transcript-only evidence;
     - a fallback before or after the requested model;
     - malformed JSON.

     Slash-bearing native IDs still work (`pi/model_probe_test.go`).
  2. `adapter/opencode/model_probe.go:checkModelExecution`: OpenCode rejects:
     - an unrelated session or turn;
     - trailing JSON;
     - an oversized or failed export;
     - an errored assistant message.

     The export deadline derived from `runCtx` can falsely reject a long mission near its deadline. Give the export its own bound.
  3. `adapter/openrouter/model_probe.go:verifyExecutedModel`: HTTP lanes compare the model only. Show that the OpenRouter response's provider cannot silently differ from the requested one, or record the gap. A stale binding or an empty pin never sends (`TestModelProbeHTTPStaleBindingDoesNotSend`, `EmptyPinDoesNotSend`).
  4. `adapter/pi/model_probe.go:verifyExecutedModel`: Every evidence error is secret-free (`TestModelProbePiFailureErrorsAreSecretFree`).
- **Seed evidence:**
  - `openrouter/model_probe.go:10` (`verifyExecutedModel(expected, actual)`) and `anthropicapi/model_probe.go:16` (`messageIdentity.verify`) take no provider argument.
  - `opencode/model_probe.go:113` (`checkModelExecution`) reuses `claudecli.RunSpec`.

### Area 6 — admins and YAML seed create and edit pinned or following profiles with evidence

- **Promise:** An admin can create a profile that is pinned to a specific version, or one that follows the latest in a chosen provider and tier. A following profile is initialized from the newest eligible candidate when no model is supplied. Validation and authorization failures persist nothing, and stale edits are rejected by revision.
- **Entry:**
  - MCP: `adapter/mcp/tools_profile.go:RegisterProfileTools` → `profileCreateWriteHandler` / `profileUpdateHandler` → `patchFromArgs`.
  - YAML: `cmd/arbitron/profile_seed.go:SeedProfiles`.
- **Sink:**
  - `adapter/sqlite/profile_selection.go:CreateValidated` / `UpdateValidated` and `adapter/sqlite/profile_mutation.go:UpdateExpected`, reached through `domain/provider/profile_service.go:ProfileService.write` → `selection.go:ValidateSelection`.
  - Then `profile_get` / `profile_list` and the cache.
- **Files:**
  - **Changed:** `domain/provider/profile_service.go`, `selection.go`, `advance.go` (`EligibleCandidateSource`), `profile_patch.go`, `profile_store.go`, `provider.go`; `adapter/sqlite/profile_repo.go`, `profile_mutation.go`, `profile_selection.go`, `migrations/021_following_profiles.up.sql`, `queries/profiles.sql`, `generated/profiles.sql.go`; `adapter/config/profiles.go`; `domain/config/config.go`; `adapter/mcp/tools_profile.go`; `cmd/arbitron/profile_seed.go`; `adapter/provider/cache.go`; `cmd/arbitron/main.go`.
  - **Unchanged but crossed:** `adapter/config/loader.go`, `domain/provider/capability.go`, `domain/provider/authz.go`.
- **Folded in:**
  1. Old rows default to pinned (migration 021).
  2. Sample YAML comments.
  3. Error mapping in `profileErrorResult` for `ErrAdvanceConflict`, `ErrSelectionPortsMissing` and `ErrNoEligibleCandidate`.
  4. G10: `Create` returns revision 0 while the stored row is 1.
  5. G14: partial seed persistence.
- **Review 1 — wiring lens:**
  1. `domain/provider/profile_service.go:SetModelSelection`: `ProfileService.SetModelSelection(VersionResolver, EligibleCandidateSource)` is called in production with the real `CandidateService` before `SeedProfiles` runs (B2, B3). Today it is called only in tests, so every following create, and every edit that changes the selection, returns `ErrSelectionPortsMissing` (`selection.go:91`).
  2. `adapter/mcp/tools_profile.go:RegisterProfileTools`: The MCP profile tools accept and return `selection_mode`, `follow_target` and `revision`. Patch omission and clear are honoured. Today the schema has none of these fields (B6).
  3. `cmd/arbitron/profile_seed.go:SeedProfiles`: `SeedProfiles` uses the same prepare / revalidate / `CreateValidated` flow, with the candidate source injected. Today it runs only `NormalizeSelection` at `main.go:754`, before the cache (`:767`), the catalog store (`:1002`) and the inventory (`:1138`).
  4. `domain/provider/selection.go:ValidateSelection`: Pinned creation checks the explicit-version grammar used at trigger (`modelidentity.NewVersionResolver`). `ValidateSelection` currently returns early for pinned mode (gap D15; row 1↔6).
  5. A root test runs registered MCP → durable row and revision → executed model: extend `cmd/arbitron/profile_following_wiring_test.go:TestFollowingProfileUpdateThroughMCPPreservesSelection` to cover create without a model.
- **Review 2 — failure lens:**
  1. A non-admin write, unknown mode, missing target, target set on a pinned profile, unsupported axes, or no available starting version persists nothing (`following_service_test.go:TestFollowingCreationFailuresWriteNothing`, `TestFollowingMissingPortsFailClosed`).
  2. `adapter/sqlite/profile_mutation.go:UpdateExpected`: Concurrent admin patches, or an admin patch racing an advancement CAS: the loser gets `ErrAdvanceConflict` and no field is lost (`TestProfileMutationFollowingToolsRace`, `TestFollowingProfileRevisionABAAndConcurrentEdits`).
  3. A prospective proof that changes between prepare and the lease is rejected (`following_preparation_test.go:TestFollowingWriteRejectsChangedProspectiveProof`). Preparation never holds the mutation lease during network discovery.
  4. `cmd/arbitron/profile_seed.go:SeedProfiles`: A seed persistence fault after partial creates must not strand the remaining candidates on the next boot (G14). A discovery fault during seeding leaves existing rows untouched.
  5. `domain/provider/profile_service.go:Create`: `Create` returns the stored revision (G10).
- **Seed evidence:**
  - `grep -rn SetModelSelection --include=*.go` finds only `profile_service.go:44`, the definition.
  - `grep -n 'selection_mode\|follow_target' adapter/mcp/tools_profile.go` finds nothing.
  - `cmd/arbitron/profile_seed.go:19` (`SeedProfiles`) takes no candidate port.

### Area 7 — catalog URL and Teams webhook settings persist, redacted publicly and raw internally

- **Promise:** Arbitron has an editable Teams webhook setting that starts empty (spec). The catalog URL defaults to `https://models.dev/api.json`.
  - **Updates:** a public update distinguishes omission (retain), an empty value (clear) and a replacement. The redacted placeholder is never saved.
  - **Reads:** internal reads return the raw value. A read error is never treated as an empty value.
- **Entry:** `adapter/http/server.go` (`GET` / `PUT /system/settings`, `:263–264` → `GetSettings` / `UpdateSettings`), `adapter/mcp/tools_system.go:systemSettingsHandler`, and `dashboard/src/routes/Settings.tsx:ModelAnnouncementSettings` → `dashboard/src/api/client.ts:api.system`.
- **Sink:** `adapter/sqlite/settings_repo.go:SettingsRepo.Update`, then `domain/settings/service.go:Service.Get` (redacted) and `Service.ReadInternal` (raw).
- **Files:**
  - **Changed:** `domain/settings/settings.go`, `service.go`; `adapter/sqlite/settings_repo.go`, `queries/settings.sql`, `generated/settings.sql.go`, `migrations/019_system_settings.up.sql`; `adapter/http/server.go`; `adapter/mcp/tools_system.go`, `deps.go`; `dashboard/src/api/client.ts`, `dashboard/src/routes/Settings.tsx`, `Settings.module.css`; `adapter/dashboard/dist/*`; `cmd/arbitron/main.go`.
  - **Unchanged but crossed:** `domain/config/teams.go` (`ValidateTeamsWebhookURL`, `RedactWebhookURL`), `adapter/http/errors.go`.
- **Folded in:**
  1. `make ui` embedded assets match the source.
  2. The run-notification `SettingsStore` stays separate.
  3. `UpdateSettings` lacks a `MaxBytesReader`, and `UpdateSystemSetting` ignores rows affected (gap D18).
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomainWithModelStateSecret`: `main.go:990` builds `settingsdomain.NewService`. It is placed on `domainStack` (`:1336`) and passed to the MCP deps (`:323`) and the HTTP deps (`:360`).
  2. A dashboard edit sends a sparse patch, and the stored value survives a restart: `cmd/arbitron/model_settings_wiring_test.go:TestModelSettingsWiringRoundTrip` and `TestModelSettingsMCPHTTPRouteWiring`, plus `dashboard/src/routes/Settings.test.tsx`.
  3. `domain/settings/service.go:ReadInternal`: `ReadInternal` has a production consumer for each value: the catalog URL is read by the B2 `DiscoveryPipeline`, and the webhook by discovery classification and the B4 announcement drain. Today `AnnouncementService.Drain` and `deliver` call `ReadInternal`, but that service is not constructed at the root; discovery is absent, so neither background use is reachable.
- **Review 2 — failure lens:**
  1. `domain/settings/service.go:Get/Update`: A non-admin read or write is refused. An invalid webhook or catalog URL persists nothing (`TestSettingsRejectsNonAdminAndInvalidURLs`).
  2. `domain/settings/service.go:Update`: Saving only the catalog URL keeps a configured webhook. Saving the redacted placeholder is rejected (`TestSettingsPatchOmissionAndClear`).
  3. `domain/settings/service.go:Get/ReadInternal`: A repository read fault surfaces as an error to both public and internal readers, never as an empty value (`TestSettingsServiceReadAndWriteFaults`).
  4. `domain/settings/service.go:publicValues`: Neither the dashboard nor any HTTP or MCP response or error echoes the raw webhook (`Settings.test.tsx`, "surfaces load and save faults without echoing secrets").
- **Seed evidence:**
  - `domain/settings/service.go:ReadInternal` is called by `domain/modelcatalog/announcements.go:Drain` and `deliver`; `NewAnnouncementService` has no production caller. Public `Service.Get` / `Update` are wired through HTTP and MCP.

### Area 8 — each newly usable model is announced at least once to Teams with its original changes

- **Promise:** When the Teams setting is configured, each newly usable model produces at least one announcement that identifies the following profiles it changed, if any (spec, D7).
  - **Webhook state at discovery:** empty suppresses the announcement for good. A read fault leaves it `awaiting_setting`.
  - **Delivery:** a rare duplicate after an ambiguous send is accepted.
- **Entry:** The event is created in `adapter/sqlite/model_catalog_repo.go:observeCatalogTx` / `discoveryEventState` (inside `CommitCycle`). It is drained by `domain/modelcatalog/announcements.go:AnnouncementService.Drain`, which runs at startup and after every refresh result, including a discovery failure (B4).
- **Sink:** `adapter/notification/model_announcement_sender.go:TeamsModelSender.SendPart`, which POSTs to the receiver, then `adapter/sqlite/model_announcement_repo.go:Complete` / `CompletePart`, which set the state to `announced`.
- **Files:**
  - **Changed:** `domain/modelcatalog/announcements.go`, `announcement_sender.go`, `report.go`, `store.go`; `adapter/sqlite/model_announcement_repo.go`, `model_catalog_repo.go`, `migrations/026_model_announcement_retries.up.sql`, `migrations/022_model_catalog.up.sql`; `adapter/notification/model_announcement_sender.go`, `model_announcement_payload.go`; `adapter/outbound/guarded_client.go`; `domain/settings/service.go`; `cmd/arbitron/main.go`.
  - **Unchanged but crossed:** `adapter/notification/teams_payload.go`, `domain/config/teams.go`.
- **Folded in:**
  1. `PartitionAnnouncement` multipart plan.
  2. The 60 s lease and the 4 h → 24 h backoff.
  3. A 2xx with an unreadable body counts as confirmed.
  4. Proof paths are excluded from cards.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomainWithModelStateSecret`: `NewModelAnnouncementRepo`, `NewTeamsModelSender` and `NewAnnouncementService` are constructed at the root with the real DB, the internal settings reader and an injected clock. The sender gets the **guarded** client, not `newOutboundHTTPClient` (`main.go:387`), which is unguarded and honours environment proxies. Today all three are called only in tests (B4).
  2. `domain/modelcatalog/refresh.go:SetAnnouncer (planned B2/B4)`: The service is installed through `SetAnnouncer` before the seed bootstrap, recovery or any loop starts. It drains at startup and after every cycle, including a failed one, and no second scheduler exists (B3, B4).
  3. `adapter/sqlite/model_catalog_repo.go:discoveryEventState`: The refresh caller supplies `CycleCommit.WebhookConfigured` from a fresh `ReadInternal`: nil on a read fault, true if configured, false if empty (row 7↔8).
  4. `domain/modelcatalog/announcements.go:Drain`: A root test runs the real stack and a non-deduplicating receiver: one card containing the original changes, the event `announced`, and a later drain sending nothing (`TestModelAnnouncementWiringToReceiverSink`, B4).
- **Review 2 — failure lens:**
  1. `adapter/sqlite/model_announcement_repo.go:Complete/RecoverClaims`: A non-2xx response returns the event to `pending`. A timeout or reset leaves it `delivery_unknown`. Both retry with persisted backoff. A crash after acceptance recovers through an expired claim (`TestAnnouncementClaimsBackoffReopenAndRecovery`).
  2. `adapter/sqlite/model_announcement_repo.go:ClaimPending`: A corrupt stored plan or JSON row must not block every later event. Today `ClaimPending` returns `ErrAnnouncementState` without moving `next_attempt_at`, so the row stays at the head of the queue (gap D19).
  3. `domain/modelcatalog/announcements.go:deliver`: A settings fault in the middle of delivery is treated as a definite failure, which delays delivery by at least 4 h (`announcements.go:108–120`). Classify it as transient (gap D20).
  4. `domain/modelcatalog/announcements.go:deliver`: Destination changes: clearing the webhook pauses pending events. Changing it redirects only later parts. Suppressed events never replay (`TestAnnouncementMultipartPauseRestartAndCurrentDestination`).
  5. `adapter/notification/model_announcement_sender.go:SendPart`: Errors and logs carry no raw webhook URL or response body (`TestModelAnnouncementTimeoutAndSecretFreeErrors`).
- **Seed evidence:**
  - `NewAnnouncementService` (`announcements.go:39`), `NewTeamsModelSender` (`model_announcement_sender.go:27`) and `NewModelAnnouncementRepo` (`model_announcement_repo.go:25`) have no production caller.
  - `discoveryEventState` derives the state from the caller's `*bool`. `observeCatalogTx` reclassifies older `awaiting_setting` rows when the bool is non-nil (`model_catalog_repo.go:275–278`).
