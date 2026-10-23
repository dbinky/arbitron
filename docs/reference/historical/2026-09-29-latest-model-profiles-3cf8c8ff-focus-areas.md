# Focus Areas — Latest Model Profiles Review Plan

Single-system review plan for branch `dev-latest-model-profiles-2026-10-18-3cf8c8ff`
(`0ce2359..978362e`, 20 commits).

- **Rows.** Each row is one **behaviour** the branch promises, traced from its entry to its sink across every layer. A row is never a layer, a file or a plan task.
- **Lenses.** Review 1 is the **wiring lens**: does the path exist and is it fed? Review 2 is the **failure lens**: how does it fail?
- **Other files.** Pass criteria are in `RALPH.md`. Seams between areas are in `docs/reference/paired-reviews.md`. Open gaps are in `docs/reference/gaps-identified.md`; the `G` numbers below refer to it.
- **Line numbers** are at seeding HEAD `978362e`. They drift, so re-grep before citing them.

**Budget.** 10 areas × 2 + 13 paired = **33** minimum passes, against 500 × 0.35 = **175**.

**Boundaries.** Areas 1 and 2 split ordinary exact pins from native opaque-route pins; Area 6 owns the follow-up entry. Areas 4 and 5 own independent probe lanes, with real-run launch parity as a folded check; pin-to-adapter propagation belongs to Areas 1 and 6. Area 9 owns YAML seed persistence and startup/shutdown ordering; Area 3 owns each coordinated refresh cycle. Shared validators, DTOs, migrations and root construction remain inside these paths.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | an ordinary exact-model post-migration run executes every batch node, retry, resume and child-input preparation on the exact models pinned at trigger from its own captured workflow; rerun and spawn re-pin; a NULL-pin legacy run keeps live resolution | high | [x] | [x] |
| 2 | a pinned native Azure or Bedrock run executes its configured deployment or model ID through the route exception persisted at trigger, and no later live route edit can relax a strict pin or move an opaque one | high | [x] | [x] |
| 3 | a startup, manual or four-hour refresh discovers every catalog model on every compatible configured path, persists confirmations durably, and atomically advances eligible following profiles to a capability-valid newer model for new runs, recovering failed publication on the next cycle and keeping the working model on any failure | high | [x] | [x] |
| 4 | a Claude Code or Codex discovery probe on exact model X is accepted only when the harness's own record shows X ran, through the same launch path real runs use | high | [x] | [x] |
| 5 | a pi, OpenCode or direct-connection discovery probe on exact model X is accepted only when the assistant event, session export or response names the requested model and provider, and Azure or Bedrock usability is refused | high | [x] |[x] |
| 6 | an interactive run's follow-up turns resume on its pinned model with live tools and credentials, and refuse a resumed session that ran a different model | high | [x] | [x] |
| 7 | an admin creates or edits a pinned or following profile through MCP, validated against version and route evidence, a following profile without a model starting on the newest confirmed candidate, and a stale revision rejected | high | [x] | [x] |
| 8 | the guarded fetch of the admin-configured catalog URL and the admin-editable D2 policy decide which catalog models are followable, in which tier and order, under a canonical identity that stays stable across routing prefixes, and model_catalog_list shows it without secrets | med | [x] | [x] |
| 9 | the first serve or MCP boot seeds the YAML profiles before serving, following entries starting on a candidate confirmed by durable prospective discovery, then runs publication recovery and the four-hour loop, and Close joins all of it before the database closes | med | [x] | [x] |
| 10 | each newly usable model gets one durable event classified by the webhook setting at discovery, delivered at least once to Teams with its original profile changes, while the webhook stays empty by default and redacted on public reads | med | [x] | [x] |

---

### Area 1 — an ordinary exact-model post-migration run executes on its trigger-time exact pins; rerun and spawn re-pin; legacy runs resolve live

- **Promise:** New runs use the updated model, and runs already underway keep the model they started with (spec Outcome 2, D1). The trigger captures pins from its own workflow only (commit `013bcda`). Missing or corrupt pins are errors, never a live fallback.
- **Entry:** `domain/engine/engine.go:Engine.TriggerWithDepartment` (:480). It calls `captureModelPins` (:208, called at :654) before `instances.Create` (:685). `Engine.Rerun` (:1811) re-enters through `Trigger`, and `domain/engine/engine_spawn.go:Spawn` re-pins at :92.
- **Sink:** The provider or harness invoked with the pinned model, through `domain/provider/pinning.go:PinningResolver.Resolve` (:74) and `adapter/provider/factory.go:ChatProviderFor` (:187) / `AgentProviderFor` (:258). The executor call sites are:
  - `domain/engine/executor_llm.go` (:63/:78)
  - `domain/engine/executor_agent.go` (:73/:87)
  - `domain/engine/child_input_preparer.go` (:98/:110)
- **Files:**
  - **Changed:**
    - `domain/engine/engine.go`
    - `domain/instance/instance.go`
    - `domain/provider/model_snapshot.go`, `domain/provider/pinning.go`, `domain/provider/provider.go`, `domain/provider/selection.go` (`ValidatePinRoutes`)
    - `adapter/provider/model_snapshot.go`, `adapter/provider/path_inventory.go` (`VersionRequestsForWorkflow`), `adapter/provider/pin_lookup.go`
    - `adapter/sqlite/instance_model_pins.go`, `adapter/sqlite/instance_repo.go`, `adapter/sqlite/instance_spawn_rows.go`, `adapter/sqlite/queries/instances.sql`, `adapter/sqlite/generated/instances.sql.go`, `adapter/sqlite/generated/queue.sql.go`
    - `cmd/arbitron/main.go`
  - **Unchanged but crossed:**
    - `cmd/arbitron/model_consumers.go`
    - `domain/engine/engine_spawn.go`, `domain/engine/executor_llm.go`, `domain/engine/executor_agent.go`, `domain/engine/child_input_preparer.go`, `domain/engine/executor_timer.go`
    - `domain/provider/model_version.go`, `domain/provider/execution_gate.go`, `domain/modelidentity/version.go`
    - `adapter/provider/factory.go`, `adapter/provider/cache.go` (`SnapshotProfiles`)
    - `adapter/claudecli/model_probe.go`, `shared.go`, `adapter/codex/provider.go`, `rollout.go`
    - `adapter/pi/model_probe.go`, `adapter/opencode/model_probe.go`, `adapter/anthropicapi/model_probe.go`, `adapter/openrouter/model_probe.go`
- **Folded in:**
  1. `adapter/provider/model_snapshot.go:SnapshotModels` (:32) now takes the captured `wf`. `VersionRequestsForWorkflow` (`path_inventory.go:173`) must constrain only that workflow's routes. Check that an unrelated saved or active workflow can neither block nor relax this trigger.
  2. The `snapshotVersionResolver` (`model_snapshot.go:95-110`) requires every strict route of an alias to resolve to one model. Check that this refusal happens before `Create`.
  3. `test/acceptance/harness_test.go` (6 lines changed): check that the acceptance harness still drives the real snapshotter rather than a stub.
  4. `workflowModelAliases` (`model_snapshot.go:190`) pins the `model_profile` of every node type, while `path_inventory.go:addConsumers` (:422) adds routes only for model node types. Check that the two agree for every node type.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go` builds the pin path, and none of these receives nil:
     - `NewPinLookup` (:789) and `NewPinningResolver` (:790).
     - `newRunModelConsumers` (`model_consumers.go:18`) passes `pins.ForRun()` (:21) to the LLM executor, the agent executor and the child-input preparer.
     - `eng.SetModelPinGate` (:1019) and `eng.SetModelSnapshotter(NewModelSnapshotter(profileCache, connStore, NewVersionResolver(), pathInventory))` (:1194).
  2. `engine.go:captureModelPins` is fed by the real `ProfileCache.SnapshotProfiles` under the gate's `WithRead` (:222). A workflow without aliases persists `{}` with version 1, never NULL. Tests: `domain/engine/model_pins_test.go:TestTriggerNoAliasPersistsEmptySnapshot`, `adapter/provider/model_snapshot_test.go:TestModelSnapshotter_EmptyAliasesYieldsEmptyNonNilMap`.
  3. A pinned run reaches the sink carrying the pinned model after its profile advances, on all three batch consumers: `cmd/arbitron/model_pins_all_consumers_wiring_test.go:TestModelPinsWiringAllConsumers` and `model_pins_sink_wiring_test.go`.
  4. Retry and resume reuse the persisted pins, while rerun and spawn re-pin fresh: `cmd/arbitron/model_pins_lifecycle_test.go:TestRetryResumeRerunAndSpawnPins`.
  5. Only the captured workflow constrains the trigger: `cmd/arbitron/model_snapshot_d8_test.go:TestSnapshotUsesOnlyCapturedWorkflow`. A NULL-pin legacy row resolves live through the same root (`pinning.go:93-97`, and the legacy case in `model_pins_sink_wiring_test.go`).
  6. `domain/provider/pinning.go:Resolve` must carry `Pinned=true` and the captured `Model` through `adapter/provider/factory.go:ChatProviderFor`/`AgentProviderFor` to `adapter/claudecli/model_probe.go:requiredExecutedModel` and `adapter/codex/provider.go:RunAgent`; trace `agent_call`, `llm_call` and `domain/engine/child_input_preparer.go:Prepare` to the harness record.
  7. Trace that same binding through `adapter/pi/model_probe.go:verifyExecutedModel`, `adapter/opencode/model_probe.go:checkModelExecution`, `adapter/anthropicapi/model_probe.go:messageIdentity.verify` and `adapter/openrouter/model_probe.go:verifyExecutedModel`; the batch result must be gated by the actual response/export identity.

- **Review 2 — failure lens:**
  1. `engine.go:TriggerWithDepartment`: a snapshot, gate or `Create` fault must create and enqueue nothing. Check `domain/engine/model_pins_test.go:TestTriggerIncompleteSnapshotFails`, and show whether any test faults `InstanceRepository.Create` itself.
  2. `pinning.go:Resolve`: a corrupt pin payload or discriminator fails the run without a provider call (`cmd/arbitron/model_pins_failure_test.go`). One corrupt row must not abort `adapter/sqlite/instance_repo.go:ListExecutionSnapshots` (:1001) for every other trigger.
  3. `pinning.go:ForRun` (:67): a batch consumer that loses its run context must fail rather than resolve live.
  4. `adapter/provider/model_snapshot.go:SnapshotModels`: each of these is refused before enqueue (`cmd/arbitron/model_pins_route_wiring_test.go:TestModelPinsWiringRejectsIncompatibleLiveRoute`):
     - an alias the workflow uses but no profile provides;
     - an unproved mutable alias;
     - an incompatible live route;
     - strict routes that resolve to different models.
  5. `domain/provider/execution_gate.go:WithRead`: an advancement concurrent with a trigger yields one coherent snapshot, and the read lease is never re-entered (`cmd/arbitron/model_pins_gate_cancellation_test.go`).
  6. Nil collaborators: `engine.go:SetModelSnapshotter` (:153) accepts a typed-nil, unlike `SetModelPinGate` (:167), and `NewModelSnapshotter` (:26) does not check its cache (G17).
  7. At `adapter/claudecli/shared.go:collectResult` and `adapter/codex/rollout.go:afterTurn`, inject a fallback or missing identity after a real trigger; prove the executor records no successful result. Rebuilding a binding in `factory.go` must not erase `Pinned` or `Model`.
  8. Make pi/OpenCode report a different provider/model and Anthropic/OpenAI-compatible HTTP responses report Y; `domain/engine/executor_agent.go:Execute` and `executor_llm.go:Execute` must reject the result before success persistence, using the production pin resolver and factory.

- **Seed evidence:**
  - `grep -rn ModelPinSnapshot --include=*.go | grep -v _test.go`: defined at `domain/provider/model_snapshot.go:12`, produced at `adapter/provider/model_snapshot.go:33,50`, consumed at `engine.go:208-243`.
  - `VersionRequestsForWorkflow`: defined at `path_inventory.go:173` (port at `model_version.go:29`), called from `model_snapshot.go:66`. Neither symbol is test-only.
  - None of the pin wiring lines in `main.go` changed in this branch. Every batch consumer gets the `ForRun()` resolver through `model_consumers.go:21`.

### Area 2 — a pinned native Azure or Bedrock run executes its configured ID through the persisted route exception; live edits cannot relax or move it

- **Promise:** D8 (spec "Provider identity limit"). Native Azure and Bedrock paths are pinned-only. A pinned run preserves its configured deployment or model ID through a route exception persisted at run creation, so a later live route edit cannot weaken an ordinary exact-model pin. Commits `24463fa`, `013bcda` and `ac2ad72`.
- **Entry:** `domain/engine/engine.go:captureModelPins` (:208). There, `adapter/provider/model_snapshot.go:SnapshotModels` calls `domain/provider/selection.go:ValidatePinRoutes` (:127), which records the family `Canonicalize(request.RouteProvider)` (:162) into `ModelPinSnapshot.OpaqueRoutes` (`model_snapshot.go:79-85`). The engine re-checks it at `engine.go:239-243`.
- **Sink:** The native adapter called with the configured ID:
  - `adapter/azurefoundry/provider.go:Chat` / `StreamChat` (:91, :144);
  - `adapter/bedrock/provider.go:Chat` / `StreamChat` (:141, :187).

  Both are gated by `adapter/provider/pinned_route.go:validatePinnedRoute` (:17-27) and `domain/provider/provider.go:RequiresModelEvidence` (:133).
- **Files:**
  - **Changed:**
    - `adapter/sqlite/migrations/031_model_pin_routes.up.sql`, `adapter/sqlite/migrations/031_model_pin_routes.down.sql`
    - `adapter/sqlite/instance_model_pins.go` (`encodeModelPinRoutes` :101, `decodeModelPinRoutes` :115), `adapter/sqlite/instance_repo.go`, `adapter/sqlite/queries/instances.sql`, `adapter/sqlite/generated/instances.sql.go`, `adapter/sqlite/generated/models.go`, `adapter/sqlite/generated/queue.sql.go`
    - `domain/instance/instance.go` (`ModelPinRoutes`)
    - `domain/provider/model_snapshot.go`, `domain/provider/selection.go`, `domain/provider/pinning.go`, `domain/provider/provider.go`
    - `adapter/provider/model_snapshot.go`, `adapter/provider/pin_lookup.go`, `adapter/provider/pinned_route.go`
    - `adapter/azurefoundry/provider.go`, `adapter/bedrock/provider.go`
    - `domain/engine/engine.go`, `domain/engine/conversation.go`, `domain/engine/executor_interactive.go`
  - **Unchanged but crossed:**
    - `domain/provider/canonical.go`, `domain/modelidentity/version.go`
    - `adapter/provider/factory.go`, `adapter/provider/interactive_stream_selector.go`, `adapter/provider/path_inventory.go` (`routeBinding` :570, `profileBackend` :408)
    - `adapter/azurefoundry/url.go`
- **Folded in:**
  1. Migration 031 must not edit applied 019–030. Existing version-1 rows with NULL routes stay strict, and NULL/version-0 rows stay legacy: `adapter/sqlite/instance_model_pin_routes_test.go:TestPinsRoutesMigration031PreservesExistingRecords`.
  2. Every full-row read selects the new column: `TestPinsRoutesAllFullRowReads`.
  3. The Azure/Bedrock spelling lists are duplicated in three places (G19):
     - `domain/provider/canonical.go` (:26-34);
     - `domain/modelidentity/version.go` (:36-40), which omits a bare `azure`;
     - the hard-coded `"azure"` and `"bedrock"` in `instance_model_pins.go:141`.

     Check that all three agree.
  4. Codex is exempt because it uses its own account: `TestValidatePinRoutesCodexUsesSubscription`. An agent backend never takes an opaque exception (`pinned_route.go:22`).
- **Review 1 — wiring lens:**
  1. The new symbols are populated outside tests, and the review fails if any is test-only:
     - `Instance.ModelPinRoutes`: set at `engine.go:674` and `instance_repo.go:964`.
     - `PinSet.OpaqueRoutes`: set at `pin_lookup.go:90,132`.
     - `ResolvedBinding.OpaquePinRoute`: set at `pinning.go:112`.
  2. No D8-specific wiring was added. The opaque path rides the Area 1 root at `main.go:789-790,1194`. Check that the same `PinningResolver` instance reaches the native chat selector.
  3. A pinned Azure or Bedrock run executes its configured ID after a profile edit and a key rotation, after reopen, and on a fresh trigger. The test covers five spellings on both `llm_call` and `interactive_llm`: `cmd/arbitron/model_pins_d8_wiring_test.go:TestD8PinnedExecutionRoot`.
  4. Capture round-trips through persistence, and an invalid capture creates nothing: `cmd/arbitron/model_snapshot_d8_test.go:TestD8SnapshotRoutesRoundTrip` and `TestD8SnapshotInvalidCaptureCreatesNothing`.
  5. The batch D8 sinks have no root coverage of a cross-family edit or of corrupt 031 data (only `HandleMessage` is covered). The child-input preparer and Rerun/Spawn re-capture also have no root coverage. Add or cite coverage (G18).
- **Review 2 — failure lens:**
  1. Same-family live edits: `pinned_route.go:21-25` compares families only. For a serverless Azure deployment, `adapter/azurefoundry/url.go` (:66-71) builds the URL from `BaseURL` and ignores `b.Model`. A connection edit can therefore move a "pinned" run to another deployment (G3).
  2. Corrupt or mismatched route data: `TestD8PinnedExecutionRootRejectsInvalidBinding` covers `{}`, a wrong family, `openai`, `not-json` and a missing pin. Check that the batch `llm_call` path refuses each one too. `pinning.go:86-91` rejects every alias of the run if any single exception is invalid; check that this is the intended blast radius.
  3. Strict-to-opaque escalation: a run captured strict, whose connection is later edited to Azure, must be refused at the selector (`cmd/arbitron/model_pins_route_wiring_test.go`, `adapter/provider/pinned_d8_test.go:TestPinningD8Selectors`).
  4. Bedrock is always opaque, even when the model matches `bedrockFixedRE` (`version.go:31`; see `selection.go:166-172`). Pin this with a test either way.
  5. A run created before migration 031 on an Azure route stays strict and is refused (`instance_model_pins.go:116-117`). Check that the refusal is explicit, not a live fallback.
- **Seed evidence:**
  - `grep -rn OpaquePinRoute --include=*.go | grep -v _test.go`:
    - set at `pinning.go:112`;
    - cleared by the probe lanes at `adapter/probe/model_usability.go:230,304` and `interactive_usability.go:61,155`;
    - read at `pinned_route.go`, `azurefoundry/provider.go`, `bedrock/provider.go` and `provider.go:134`.
  - `ValidatePinRoutes` is called from both `adapter/provider/model_snapshot.go:79` (trigger) and `domain/provider/selection.go:200` (write). See Area 7 wiring check 5.

### Area 3 — a startup, manual or four-hour refresh discovers every catalog model on every path and atomically advances eligible following profiles

- **Promise:** Spec Features 1, 2 and 4, Outcomes 1 and 2, the D3–D6 probe policy, and the D9 ordering:
  - Every four hours, Arbitron distinguishes cataloged from confirmed-usable models on this installation's paths.
  - By the next successful refresh, eligible following profiles select the newer usable model for new runs.
  - Failure or no eligible replacement keeps the last known working model.
  - Pinned profiles never move.
  - The full source candidate set is probed on every compatible path, including no-follower paths. Confirmed evidence is never re-probed.

  Commits `394d50d`, `8917329`, `012d764`, `75ed381` and `55e798b`.
- **Entry:** Three entries, all through `domain/modelcatalog/loop.go:RefreshCoordinator.Refresh` (:45) into `domain/modelcatalog/refresh.go:RefreshService.Refresh` (:349):
  - the startup pass, `cmd/arbitron/model_dependencies.go:startRuntime` (:75);
  - the four-hour tick, `loop.go:RefreshLoop.RunScheduled` (:127), started by `model_refresh.go:startScheduled` (:182, called at `model_dependencies.go:122`);
  - the admin `model_catalog_refresh` tool, `domain/modelcatalog/admin.go:AdminService`.
- **Sink:** Three stages:
  - The durable cycle: `adapter/sqlite/model_catalog_repo.go:CommitCycle` (:31). It writes catalog, evidence, cursor, events and report, and runs `adapter/sqlite/profile_advance.go:commitAdvanceTx` (:106) for each proposal.
  - Publication: `adapter/provider/cache.go:ProfileCache.PublishCommitted` (:136), then `ModelCatalogRepo.MarkPublished` (:495) / `Acknowledge` (:541).
  - The observable result: the model a new run executes.
- **Files:**
  - **Changed:**
    - `domain/modelcatalog/refresh.go` (`DiscoveryPipeline.Collect` :102, `RefreshService.Refresh`/`commit`/`publish`/`Recover` :349/:389/:413/:425)
    - `domain/modelcatalog/candidates.go` (`AdvanceSelector.selectWithin` :71, `check` :181, `evidenceMatches` :233, `currentIdentity` :269, `newerCandidates` :290)
    - `domain/modelcatalog/probe_schedule.go` (`probeQueue` :30, `runProbeQueue` :122)
    - `domain/modelcatalog/loop.go`, `domain/modelcatalog/public.go`
    - `domain/provider/advance.go` (`AdvanceValidator`), `domain/provider/advance_service.go` (`ValidateAdvance`), `domain/provider/profile_service.go`
    - `adapter/provider/cache.go`, `adapter/provider/path_inventory.go`, `adapter/provider/harness_fingerprint.go`
    - `adapter/sqlite/model_catalog_repo.go`
    - `cmd/arbitron/model_refresh.go` (`newModelRefresh` :94), `cmd/arbitron/model_dependencies.go`, `cmd/arbitron/main.go`
  - **Unchanged but crossed:**
    - `adapter/sqlite/profile_advance.go` (`checkExecutionRevisionTx` :72)
    - `adapter/sqlite/migrations/022_model_catalog.up.sql`, `027_*` (profile revision floor)
    - `domain/provider/execution_gate.go`
    - `domain/modelcatalog/admin.go`
    - `adapter/probe/model_usability.go` (as the `UsabilityProbe` port only; the lanes are Areas 4 and 5)
- **Folded in:**
  1. The publication-reconciliation change (`8917329`): `PublishCommitted` now skips an alias that Load confirmed deleted (`cache.go:146`). The revision floor (migration 027 `profile_revision_floor`) keeps a recreated alias from matching an old CAS. Check that both hold together.
  2. `CommitCycle` refuses confirmed evidence that matches no catalog model (`model_catalog_repo.go:79-85`, `catalogEvidenceIdentified`), and the refusal fails the whole cycle. Check that one bad confirmation cannot starve every later cycle.
  3. `ValidateAdvance` (`advance_service.go:44`) is mandatory: `NewDiscoveryPipeline` refuses a nil `AdvanceValidator` (`refresh.go:74`). The selector skips a capability-invalid Y and continues to Z (`candidates.go:132-137`).
  4. Test-only lifecycle helpers: `cmd/arbitron/model_refresh.go:start` (:177) and `loop.go:RefreshLoop.Run` (:120) have no production caller (G12).
  5. Opaque D8 paths are enqueued for source-wide discovery and recorded unknown every cycle (`probe_schedule.go:85-100`, `candidates.go:193-198`; G22).
  6. The evidence written on the probe-error branch can carry an empty `native_model` (`candidates.go:199,208`; G14).
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go`, from :1150, builds the refresh graph with no nil collaborator:
     - `NewExecutionPathInventory` (:1150, error-checked at :1163);
     - `executionGate.SetCurrentRevision(pathInventory.Revision)`;
     - `newModelRefresh(refreshDeps)` (:1178), with `Settings`, `Policy`, `Profiles`, `Inventory`, `Chats`/`Agents`/`Streams`, `Store`, `Gate`, `Publisher: profileCache` and `DB`.

     `newModelRefresh` refuses missing dependencies (`model_refresh.go:95-100`).
  2. `refresh.go:DiscoveryPipeline.Collect` (:102) is fed by real producers, cited at `model_refresh.go:138-141`:
     - `settings.Service.ReadInternal`;
     - the guarded fetcher;
     - the normalizer and `IdentityMapper` over the persisted policy;
     - `ExecutionPathInventory.Snapshot`;
     - the four-lane router;
     - `ProfileService` as `AdvanceValidator`.

     Production passes no `Fetcher` or `FetchTransport` (`main.go:425` uses `ModelDependencies{}`).
  3. Full-source discovery: every source candidate is probed on every compatible path, including paths without followers and native-only, older or unknown-tier IDs. Tests: `cmd/arbitron/model_catalog_all_discovery_test.go:TestDiscoveryAllSourceCandidatesOnEveryPathRoot`, `model_profiles_discovery_test.go:TestDiscoveryBeyondFollowingTargetsRoot`, and `domain/modelcatalog/discovery_all_test.go:TestCandidateSelectionDoesNotHideFourthEligibleRelease`.
  4. Advancement reaches the executed model. The advanced model executes for a new run while an old run keeps X, driven by the real four-hour ticker seam: `cmd/arbitron/model_refresh_tick_test.go:TestRefreshAdvancesBySuccessfulFourHourTick`, `model_profiles_e2e_test.go:TestModelProfilesE2ESinks`. A capability-invalid Y is skipped and a compatible Z executes: `model_refresh_capability_test.go:TestAutomaticAdvanceUsesConfiguredCapabilities`.
  5. Publication is recovered by the next coordinated cycle without a restart, for both cycle reports and direct advancements with no report: `cmd/arbitron/model_refresh_recovery_test.go:TestRefreshRepairsPublicationWithoutRestart`, `model_profiles_e2e_test.go:TestNextTickPublicationRecoveryRoot`, `profile_publication_reconciliation_test.go:TestDirectAdvancePublicationRecoveryWithoutReport`. That last test drives the test-only `ds.modelRefresh.start()` at :159; retarget it at `startScheduled` (G12).
  6. The startup pass runs before crash recovery (`model_dependencies.go:75` precedes :87), and the ticker starts only afterwards (:122). Show a test that drives the production `startScheduled`, not `start()`.
- **Review 2 — failure lens:**
  1. Stale evidence is refused at commit:
     - `refresh.go:commit` (:389) holds `Gate.WithCurrent(PathRevision)`. On `ErrExecutionRevisionStale` it drops the proposals and still records evidence.
     - An effective harness-config edit must change `ConfigRevision` (`cmd/arbitron/harness_effective_config_wiring_test.go:TestPathInventoryCodexEffectiveConfigRejectsStaleCASRoot`).
     - Changed config refuses discovery (`model_catalog_durable_bootstrap_test.go:TestCatalogDiscoveryRejectsChangedConfigRoot`).
  2. CAS conflict: an expected-field mismatch in `commitAdvanceTx`, including delete-and-recreate (revision floor 027), returns `ErrAdvanceConflict` with no row or journal change, while other proposals proceed. A journal or event write fault rolls back the model update.
  3. `refresh.go:Refresh` (:361-364) returns before `Collect` whenever `Recover` fails. Show that no single unpublishable row can stop discovery and advancement indefinitely, or record the gap.
  4. These never advance (`candidates.go:currentIdentity`, `newerCandidates`; `discovery_all_test.go:TestCandidateSelectionClassifiesUnresolvedCurrentAndRoutes`):
     - a pinned profile;
     - an unknown, mutable or ambiguous current model;
     - equal versions with missing or equal dates;
     - a candidate that is not confirmed on every consuming path.
  5. Fetch or parse failure keeps the stored catalog, and the cycle commits a degraded report (`refresh.go:collect` `keep`, :146-160). A probe error is recorded unknown, never usable. Budget exhaustion keeps completed evidence (`domain/modelcatalog/collection_budget_test.go`, `cmd/arbitron/model_refresh_budget_test.go:TestModelRefreshBudgetRestartsReachTailExecution`).
  6. Concurrency and single flight:
     - A manual refresh that overlaps a tick gets `ErrRefreshBusy` (`loop.go:72`), and the tick is skipped until the next interval.
     - A panic is contained (`loop.go:exec`).
     - `Close` cancels and joins (`domain/modelcatalog/coordinator_do_test.go:TestCoordinatorDoSharesRefreshAdmissionAndClose`).
     - The four probe workers merge in dispatch order (`probe_workers_test.go:TestDiscoveryUsesFourBoundedProbeWorkers`).
  7. Policy revision: `collect` refuses known entries whose `PolicyRevision` differs from the path snapshot (`refresh.go:207-211`). Discovery-only commits run under `WithRead` with no policy recheck (`refresh.go:396-397`; G8).
- **Seed evidence:**
  - `grep -rn 'startScheduled\|\.start()' cmd/arbitron --include=*.go | grep -v _test.go`: production calls only `startScheduled` (`model_dependencies.go:122`). `start()` is called from `model_refresh_wiring_test.go:258`, `model_refresh_tick_test.go:77` and `profile_publication_reconciliation_test.go:159` only.
  - `SetAnnouncer` is called at `model_refresh.go:159`, and `RefreshService.validate` (`refresh.go:332`) refuses a nil `Announcer`.
  - `probe_schedule.go:85-100` maps codex paths to catalog provider `openai` and `claude-cli` to `anthropic`. Every other path uses `RouteProvider`, so Azure paths draw from the catalog's `azure` slice.

### Area 4 — Claude Code and Codex discovery probes accept exact model X only on the harness's own record, through the real launch path

- **Promise:** D3/D4 probe acceptance and parity with pinned-run evidence helpers, plus D9 (P3T4 owns a tool-disabled probe built on real adapters):
  - A Claude call with `--model X` and no `--fallback-model` succeeds only if `modelUsage` has key X.
  - A Codex `-m X` turn succeeds only if the rollout's `turn_context.model` is X, and only models in `~/.codex/models_cache.json` are probed.

  Commits `6a598a6` and `901d87a`.
- **Entry:** Discovery probe, with the real run as the launch-parity reference:
  - Probe: `adapter/probe/model_usability.go:UsabilityRouter.Check` (:97), through `AgentUsabilityLane.Check` (:298) to `adapter/claudecli/agent_adapter.go:ProbeExecutedModel` (:138) or `adapter/codex/provider.go:ProbeExecutedModel` (:272). The interactive lanes are at `adapter/probe/interactive_usability.go:149`.
  - Real-run parity reference (propagation reviewed in Areas 1 and 6): `claudecli/agent_adapter.go:RunAgent` (:81), `chat_adapter.go:Chat`/`StreamChat` (:110/:140) and `codex/provider.go:RunAgent` (:148).
- **Sink:**
  - Claude: `adapter/claudecli/shared.go:collectResult`, with `streamjson.go:executedModelMatches` (:186) and `streamstate.go:streamState.Handle` (:62-86).
  - Codex: `adapter/codex/rollout.go:rolloutVerifier.afterTurn` (:183) / `readRollout` (:82).
  - The externally consumed probe result is `adapter/probe/model_usability.go:UsabilityRouter.Check` returning confirmed or unknown evidence; Area 3 owns durable consumption.
- **Files:**
  - **Changed:**
    - `adapter/claudecli/agent_adapter.go`
    - `adapter/codex/provider.go`, `adapter/codex/interactive_provider.go`, `adapter/codex/testdata/probe-restrictions.md`
    - `adapter/probe/model_usability.go`, `adapter/probe/interactive_usability.go`
    - `adapter/provider/harness_fingerprint.go`, `adapter/provider/path_inventory.go`
    - `cmd/arbitron/model_refresh.go`, `cmd/arbitron/main.go`
  - **Unchanged but crossed:**
    - `adapter/claudecli/model_probe.go` (`requiredExecutedModel` :14), `claude.go` (`buildArgs`), `shared.go`, `streamjson.go`, `streamstate.go`, `chat_adapter.go`, `stream_agent_adapter.go`, `env.go`
    - `adapter/codex/account_models.go` (`accountListsModel` :24), `args.go` (`buildExecArgs` :195, `buildProbeArgs` :210), `rollout.go`, `exec_session.go`, `env.go`
    - `adapter/provider/factory.go`, `adapter/provider/interactive_stream_selector.go`
- **Folded in:**
  1. `--fallback-model` appears nowhere in production `.go` files, only in test scripts and assertions.
  2. `adapter/codex/testdata/probe-restrictions.md` records an unresolved contract: `--sandbox read-only` does not disable Codex tools, MCP servers or plugins (G1).
  3. `harness_fingerprint.go:harnessInputs` (:74-118) hashes content, now with no size cap. It hashes `$HOME/.claude.json` even when `CLAUDE_CONFIG_DIR` is set (:88; G4).
  4. The doc mismatch "D3 says `--output-format json`, code uses `stream-json`" (`claude.go:56`) is recorded under Won't Fix.
  5. `adapter/claudecli/agent_adapter.go:ProbeExecutedModel` and `adapter/codex/provider.go:ProbeExecutedModel` share launch/evidence helpers with real runs. Review that parity here; batch and continuation pin propagation are Areas 1 and 6, not extra paired approvals.

- **Review 1 — wiring lens:**
  1. One `interactiveStreamSel` is built from `providerFactory` (`main.go:1142`). It is shared by the inventory (:1157-1158), the refresh lanes (:1170) and the live interactive executors (:1203, :1210). Probes and real runs therefore build adapters through the same factory instances.
  2. The probe and the pinned run share the argv builder and the evidence reader:
     - Claude: `buildArgs` and `collectResult` (`agent_adapter.go:154,160` versus `:66,108`).
     - Codex: `buildExecArgs` and `afterTurn` (`provider.go:320` versus `:245`).

     Each divergence must be a deliberate tool restriction only: `SuppressTools`, read-only sandbox, timeout source. Name each one.
  3. D4's account-list filter runs on both the batch and the interactive probe: `accountListsModel` at `codex/provider.go:283` and `interactive_provider.go:161`. It does not run on pinned runs.
  4. Root tests that end at the parsed harness record:
     - `cmd/arbitron/model_usability_native_harness_wiring_test.go`;
     - `TestModelRefreshRootClaudeCodeAgentRouteProvesCandidateFromModelUsage`;
     - `TestModelRefreshRootCodexAgentRouteProvesCandidateFromRollout`;
     - `model_refresh_codex_interactive_agent_wiring_test.go`;
     - `TestUsabilityConfirmedExactModel`.
  5. The interactive Codex lane works only when `ARBITRON_CODEX_INTERACTIVE_VERIFIED` is set. Check that it is reported unknown, never usable, when unset.
- **Review 2 — failure lens:**
  1. Fallback detected: `modelUsage` without X fails, while extra helper models are tolerated (`streamjson.go:executedModelMatches`). Codex fails on each of: a missing rollout, another session, an incomplete turn, an oversized rollout, or a `turn_context` with no model.
  2. Tool restriction (G1): a Codex probe that can run tools or MCP servers violates D9's tool-disabled probe. Fix it with a verified restriction recipe, or keep the escalation open.
  3. Fingerprint churn (G4): content-hashed files the harness itself rewrites (`.credentials.json` OAuth refresh, `$HOME/.claude.json` counters, `models_cache.json`) would make `confirmCurrentBinding` (`model_usability.go:328`) report "configuration changed" on every probe. Show evidence that they are stable, or narrow the inputs.
  4. The lane `== model` comparisons are tautological, because every `ProbeExecutedModel` returns the requested model (`agent_adapter.go:163`, `codex/provider.go:323`). `ProofRef` is also built from the request values (G13). Show that a forced mismatch fails inside the adapter, not in the lane.
  5. Errors stay secret-free. Pinned Codex errors embed CLI output (`codex/provider.go:227` `failureCause`, `exec_session.go:316`), whereas pi suppresses it (G21).
  6. The router's fixed 30 s timeout (`model_usability.go:18`) versus a slow Codex turn: show that a path cannot stay unknown forever (G16).
- **Seed evidence:**
  - `grep -rn models_cache --include=*.go | grep -v _test.go`: `adapter/codex/account_models.go` and `adapter/provider/harness_fingerprint.go`.
  - `grep -rn 'fallback-model' --include=*.go | grep -v _test.go`: no hits.
  - `main.go:1159-1161` supplies only the claude-code binary path. codex, pi and opencode resolve through `harness_fingerprint.go:findBinary` (:120), matching the factory, which never sets their binary paths.

### Area 5 — pi, OpenCode and direct-connection discovery probes accept exact model X only when the response names it; Azure/Bedrock usability is refused

- **Promise:** D5/D6 probe acceptance and parity with pinned-run evidence helpers on the harness and HTTP lanes:
  - The assistant `message_end` (pi) or the session export (OpenCode) must name the requested provider and model.
  - The Anthropic, OpenAI-compatible and OpenRouter responses must name the model.
  - D8: confirming usability on an Azure or Bedrock path is refused.
- **Entry:** Discovery probe, with the real run as the launch-parity reference:
  - Probe: `adapter/probe/model_usability.go:UsabilityRouter.Check` (:97). The D8 refusal is at :109-116. Lanes: `ChatUsabilityLane.Check` (:224), `AgentUsabilityLane.Check` (:298) → `adapter/pi/provider.go:ProbeExecutedModel` (:257) / `adapter/opencode/model_probe.go:ProbeExecutedModel` (:157), and `interactive_usability.go:InteractiveChatUsabilityLane.Check` (:55).
  - Real-run parity reference (propagation reviewed in Areas 1 and 6): `adapter/pi/provider.go:RunAgent` (:143), `adapter/opencode/provider.go:RunAgent` (:98), `adapter/anthropicapi/provider.go:Chat`/`StreamChat` (:88/:152) and `adapter/openrouter/provider.go:Chat`/`StreamChat` (:78/:138).
- **Sink:**
  - `adapter/pi/model_probe.go:verifyExecutedModel` (:22).
  - `adapter/opencode/model_probe.go:checkModelExecution` (:196) / `verifyModelExport` (:69).
  - `adapter/anthropicapi/model_probe.go:messageIdentity.verify`.
  - `adapter/openrouter/model_probe.go:verifyExecutedModel` (:10).
  - `adapter/probe/model_usability.go:UsabilityRouter.Check` returns confirmed or unknown evidence; Area 3 owns its durable consumption at `domain/modelcatalog/candidates.go:check`.
- **Files:**
  - **Changed:**
    - `adapter/pi/provider.go`
    - `adapter/opencode/model_probe.go`
    - `adapter/probe/model_usability.go`, `adapter/probe/interactive_usability.go`
    - `adapter/azurefoundry/provider.go`, `adapter/bedrock/provider.go`
    - `adapter/provider/harness_fingerprint.go`
  - **Unchanged but crossed:**
    - `adapter/pi/model_probe.go`, `args.go` (:76), `env.go`, `parse.go`
    - `adapter/opencode/args.go`, `config.go`, `providers.go`, `provider.go`
    - `adapter/openrouter/provider.go`, `model_probe.go`, `response.go`
    - `adapter/anthropicapi/provider.go`, `model_probe.go`
    - `adapter/provider/factory.go`
    - `domain/provider/canonical.go`
- **Folded in:**
  1. pi receives `--model <model>` without the `<provider>/` prefix that D5 describes (`pi/args.go:76`); the expected provider comes from the binding. `cmd/arbitron/model_refresh_harness_slash_wiring_test.go:TestModelRefreshHarnessSlashSpellingProbeMatchesPinnedRun` shows that slash-bearing IDs fail closed.
  2. The OpenCode export is bounded at 30 s and 1 MiB (`opencode/model_probe.go:22`).
  3. The Azure and Bedrock adapters refuse `RequireExecutedModel` (`azurefoundry/provider.go:91`, `bedrock/provider.go:141`).
  4. The pi and OpenCode fingerprint inputs: `~/.pi/agent/` `auth.json`, `models.json`, `settings.json`, `mcp.json`; `~/.local/share/opencode/auth.json`; and `~/.config/opencode/` `config.json`, `opencode.json`, `opencode.jsonc` (`harness_fingerprint.go:74-118`).
  5. `adapter/pi/provider.go:ProbeExecutedModel` and `adapter/opencode/model_probe.go:ProbeExecutedModel` share real-run helpers. Review lane parity here; pin propagation into batch and streaming HTTP adapters belongs to Areas 1 and 6.

- **Review 1 — wiring lens:**
  1. `chatLaneProviders` (`model_usability.go`) lists the routes whose adapters prove the executed model: `anthropic-api`, `openai`, `openrouter`, `litellm`, `ollama`, `ollama-cloud` and `claude-cli`. `agentLaneBackends` lists `claude-code`, `pi`, `opencode` and `codex`. Every other route must be reported unknown, never usable.
  2. pi and OpenCode probes share the run's `buildArgs` and verifier (`pi/provider.go:261,304` versus `:154,231`; `opencode/model_probe.go:165,196` versus `provider.go:117,187`). The only deliberate differences are the tool restriction (`--no-tools --no-mcp`, `Posture{}`); show that nothing else differs.
  3. Pinned runs verify whenever `binding.RequiresModelEvidence()` holds; the HTTP lanes also verify on `RequireExecutedModel`. Every binding is built per run by `factory.go` from the `PinningResolver` output.
  4. Root tests:
     - `cmd/arbitron/model_usability_native_wiring_test.go`;
     - `TestUsabilityPiExactModelRoot`, `TestUsabilityOpenCodeExactModelRoot`, `TestUsabilityHTTPExactModelRoot`;
     - `model_usability_d8_wiring_test.go:TestUsabilityD8AndUnknownMappingRoot`;
     - `model_usability_bedrock_wiring_test.go:TestUsabilityBedrockPinnedAndLegacyExecutionRoot`.
- **Review 2 — failure lens:**
  1. pi rejects each of the following, and every error is secret-free (`TestUsabilityPiSecretFreeErrorsRoot`):
     - a wrong provider;
     - a user-message echo;
     - delta-only evidence;
     - a fallback before or after the requested model;
     - malformed JSON.
  2. OpenCode rejects an unrelated session, trailing JSON, an oversized or failed export, and an errored assistant message. The router's 30 s budget also covers the OpenCode run plus its own 30 s export (G16).
  3. D8: the router's Azure/Bedrock refusal reaches `candidates.go:check` as an error and is stored as **unknown** (:193-198). `evidenceMatches` (:233-242) also refuses opaque paths, so stored proof can never be reused: `discovery_all_test.go:TestDiscoveryDoesNotReuseMismatchedOrOpaqueStoredProof`, `cmd/arbitron/model_catalog_durable_bootstrap_test.go:TestCatalogOpaquePathsNeverPersistConfirmationRoot`.
  4. The lane `== model` comparisons are tautological (`pi/provider.go:307`, `opencode/model_probe.go:199`, `interactive_usability.go:103`), and `ProofRef` comes from request values (G13). Show that a response naming Y fails in the adapter.
  5. A stale binding or an empty pin never sends a request. `confirmCurrentBinding` (`model_usability.go:328`) refuses a route that changed during I/O.
- **Seed evidence:**
  - `openrouter/response.go:36,54` decodes only `model`; the upstream provider is not checked (Won't Fix with reason).
  - `grep -rn ProbeExecutedModel --include=*.go | grep -v _test.go`: implementations at `claudecli/agent_adapter.go:138`, `codex/provider.go:272`, `pi/provider.go:257` and `opencode/model_probe.go:157`; the caller is `model_usability.go`.

### Area 6 — interactive follow-up turns resume on the pinned model with live tools and credentials

- **Promise:** Runs already underway keep their model, including interactive follow-up turns (design phase 1). Credentials, tools and connection metadata stay live. A resumed harness session recorded on another model is refused.
- **Entry:** `domain/engine/conversation.go:conversationHandler.HandleMessage` (:162). It attaches the instance ID (:174), resolves (:232), selects a stream (:236) and sets `RequireExecutedModel: binding.RequiresModelEvidence()` (:248). The first turn is `domain/engine/executor_interactive.go:InteractiveExecutor.Execute` (:60, with `RequiresModelEvidence` at :136).
- **Sink:** `adapter/provider/interactive_stream_selector.go:StreamProviderFor` (:70) routes chat bindings to `factory.ChatProviderFor` (:73) and agent bindings through `validatePinnedRoute` (:103). The stream then reaches one of:
  - `adapter/claudecli/stream_agent_adapter.go:StreamChat` with `streamstate.go:Finalize`;
  - `adapter/codex/exec_session.go:Start` with `rollout.go:beforeResume`/`afterTurn`;
  - an HTTP `StreamChat`.
- **Files:**
  - **Changed:**
    - `domain/engine/conversation.go`, `domain/engine/executor_interactive.go`
    - `domain/provider/pinning.go`, `domain/provider/provider.go`
    - `adapter/provider/pin_lookup.go`, `adapter/provider/pinned_route.go`
    - `adapter/codex/interactive_provider.go`
    - `cmd/arbitron/main.go`
  - **Unchanged but crossed:**
    - `adapter/provider/interactive_stream_selector.go`, `adapter/provider/factory.go`
    - `adapter/claudecli/stream_agent_adapter.go`, `streamjson.go`, `streamstate.go`
    - `adapter/codex/exec_session.go`, `turn_session.go`, `rollout.go`
    - `domain/engine/executor_timer.go`
    - `adapter/anthropicapi/provider.go`, `model_probe.go`, `adapter/openrouter/provider.go`, `model_probe.go`
- **Folded in:**
  1. The `ARBITRON_CODEX_INTERACTIVE_VERIFIED` opt-in.
  2. Pin-cache release at session end (`main.go:1225`).
  3. `RequiresModelEvidence` (`provider.go:133-135`) equals `Pinned` for non-native pins. Check that the D8 interactive-chat case added by `ac2ad72` is the only behavioural difference.
- **Review 1 — wiring lens:**
  1. `main.go:1201` sets `interactiveResolver := pinningResolver.ForRun()`. It is passed to `NewInteractiveExecutor` (:1202) and `NewConversationHandler` (:1209), both of which share `interactiveStreamSel` (:1142).
  2. `HandleMessage` attaches the authorized, loaded instance ID before `Resolve`, and takes `ToolScope` from the live profile. Paired row 6 covers admin edits.
  3. After an advancement, a resumed turn carries the pinned model in argv, and a resume after restart reloads persisted pins:
     - `cmd/arbitron/model_pins_interactive_wiring_test.go:TestModelPinsWiringInteractiveResumeSink`;
     - `...ResumeRejectsFallback`;
     - `...ContinuationAfterRestart`.
  4. Session end releases the pin cache: `TestModelPinsWiringEndSessionReleasesPinCache`.
  5. Trace `conversation.go:HandleMessage` and `executor_interactive.go:Execute` setting `ChatRequest.RequireExecutedModel` and `Model` into `adapter/claudecli/streamjson.go:pinnedModel` and `adapter/codex/interactive_provider.go:startTurn`; these consumers use the request flag, not just `binding.Pinned`.
  6. Trace chat-kind turns through `adapter/provider/interactive_stream_selector.go:StreamProviderFor` → `factory.go:ChatProviderFor` → `adapter/anthropicapi/provider.go:StreamChat` and `adapter/openrouter/provider.go:StreamChat`; response-frame identity must gate `conversation.go:handleNormalResponse`/`handleSessionCompletion`.

- **Review 2 — failure lens:**
  1. A lost run context is refused before streaming. A legacy NULL-pin run resolves live.
  2. A Codex resume against a session recorded on another model is refused before spawning (`rollout.go:beforeResume`; `cmd/arbitron/model_pins_codex_resume_evidence_wiring_test.go`). Claude has no pre-resume check: show that a mismatched final result fails the turn and is not persisted as success.
  3. HTTP mid-stream identity: a stream whose frames name Y fails the turn (`cmd/arbitron/model_pins_interactive_http_midstream_wiring_test.go`).
  4. An interactive agent turn on an opaque route is always refused (`interactive_stream_selector.go:89-103`, `pinned_route.go:22`). Capture can never produce an agent exception (`path_inventory.go:408`). Check that the two stay consistent.
  5. A follow-up turn racing session end respects `conversation_ordering_test.go`, and a release racing a turn never yields a live-resolved model.
  6. A resumed Claude/Codex stream reporting Y must fail through `conversation.go:streamChatResponse` before appending a successful assistant turn; exercise both first-turn and follow-up request producers with X pinned.
  7. A late HTTP frame naming Y must propagate an error from `StreamChat` before `conversation.go:handleNormalResponse` or `handleSessionCompletion` persists success, including after a restart.

- **Seed evidence:**
  - `codex/interactive_provider.go:151` takes verification only from `req.RequireExecutedModel`, never from `binding.Pinned`.
  - `conversation.go:248` and `executor_interactive.go:136` are the only producers of `RequireExecutedModel` for interactive turns.

### Area 7 — admins create and edit pinned or following profiles through MCP with version and route evidence

- **Promise:** An admin can create a profile for a specific model version, or one that follows the latest in a chosen provider and tier (spec Feature 2):
  - A following profile with no model starts on the newest eligible candidate confirmed on its own routes.
  - D8: following on an Azure or Bedrock route is refused.
  - Validation and authorization failures persist nothing, and stale edits are rejected by revision.

  Commits `1fe44cb`, `394d50d` and `41ff3fa`.
- **Entry:** `adapter/mcp/tools_profile.go:profileCreateHandler` (:276) and `profileUpdateHandler` (:371), through `profileFromArgs` (:251) / `patchFromArgs` (:291) and `followTargetArg` (:200).
- **Sink:** `domain/provider/profile_service.go:Create` (:74) / `UpdatePatch` (:109), then `write` (:150) → `writeTrusted` (:160) → `persistPrepared` (:198) / `revalidatePrepared` (:205) / `storePrepared` (:225). The rows land in:
  - `adapter/sqlite/profile_selection.go:CreateValidated`/`UpdateValidated` (:17/:29), with `checkSelectionTx` (:61);
  - `adapter/sqlite/profile_mutation.go:UpdateExpected` (:48);
  - `adapter/sqlite/profile_repo.go:Create` (:118).

  The result is then visible through `profile_get` and the cache.
- **Files:**
  - **Changed:**
    - `domain/provider/profile_service.go`, `domain/provider/selection.go` (`ValidateSelection` :82, `ValidatePinRoutes` :127), `domain/provider/advance_service.go`
    - `domain/modelcatalog/candidate_source.go` (`Newest` :113, `ValidateStart` :117, `selectStart` :124, `RevalidateStart` :222)
    - `adapter/provider/path_inventory.go` (`VersionRequests` :133, `PathsForProfile` :112)
    - `cmd/arbitron/main.go`
  - **Unchanged but crossed:**
    - `adapter/mcp/tools_profile.go`, `adapter/mcp/register.go`
    - `adapter/sqlite/profile_selection.go`, `profile_mutation.go`, `profile_repo.go`
    - `domain/provider/profile_store.go`, `domain/provider/execution_gate.go`, `domain/modelidentity/version.go`
    - `adapter/provider/model_snapshot.go`, `adapter/provider/pinned_route.go`
    - `domain/modelcatalog/loop.go` (`Coordinator.Do`)
- **Folded in:**
  1. `profileErrorResult` (`tools_profile.go:150`) has no case for `ErrRefreshBusy`, `ErrExecutionRevisionStale`/`Unavailable` or `ErrInventoryIncomplete`, so they surface as a generic failure with the raw error chain (G10).
  2. `ProfileService.SeedCreate` (`profile_service.go:263`) is called only from domain tests. Delete it, or justify it.
  3. `TestD8ProfileSelectionRoot` asserts only `require.Error` for following on a native route. It should assert the error type.
- **Review 1 — wiring lens:**
  1. `main.go:1189` calls `profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)`. `newModelRefresh` returns an error rather than a nil `candidates` (`model_refresh.go:165-170`). `SetAdvancementDependencies` (:1015) and `SetPublicationJournal` (:1016) also receive real values. `ProfileAdmin: ds.profileSvc` is at `main.go:354`, and `RegisterProfileTools` is called from `register.go:15`.
  2. A following create without a model goes through `CandidateService.Newest` → `selectStart`:
     - It reads persisted proof first.
     - On a miss it runs `Coordinator.Do` → `Pipeline.Collect(ctx,&p)` → `CommitCycle` → `MarkPublished` (`candidate_source.go:137-160`).
     - It then re-reads, and the created profile executes the chosen model.

     Tests: `cmd/arbitron/following_profile_create_wiring_test.go:TestFollowingProfileRootCreateSelectsNewestUsableModel` and `TestFollowingProfileCreateWithoutModelThroughMCP`, `profile_create_execution_wiring_test.go:TestProfileCreatedThroughMCPExecutesSelectedModel`.
  3. MCP round-trips `selection_mode`, `follow_target` and `revision`, honouring patch omission and clear: `adapter/mcp/tools_profile_following_test.go`, `TestProfileTools_SelectionFieldsRoundTrip`, `TestProfileUpdate_RevisionAndFollowTargetPatch`.
  4. `ValidatePinRoutes` runs at write time on actual route-only version requests: `domain/provider/d8_selection_test.go:TestValidatePinRoutes`, `TestPinnedSelectionRechecksRoutesUnderMutationLease`, `TestPinnedSelectionRequiresRouteSource`, and `cmd/arbitron/profile_d8_selection_test.go:TestD8ProfileSelectionRoot`.
  5. `domain/provider/selection.go:ValidatePinRoutes` is the shared write/capture rule: through MCP, save native Azure/Bedrock configured IDs and trace to `adapter/provider/model_snapshot.go:SnapshotModels` and `pinned_route.go:validatePinnedRoute`; a following profile route edit to either native family must fail at the write. Keep underlying connection edits in Area 2's live-route failure checks.

- **Review 2 — failure lens:**
  1. Each of these persists nothing: a non-admin write, an unknown mode, a missing target, a target on a pinned profile, unsupported axes, no starting version, or following on Azure/Bedrock (`TestProfileCreate_FollowingFailsClosed`).
  2. A stale revision: the early check is at `profile_service.go:117`, and the SQL CAS at `profile_mutation.go:updateExpectedTx` (:66). Concurrent admin patches, or an admin patch racing an advancement CAS, give the loser `ErrAdvanceConflict` without losing any field.
  3. A proof that changes between prepare and the lease is rejected by `revalidatePrepared` (:205). Preparation never holds the mutation lease during network discovery.
  4. A create that overlaps a refresh gets `ErrRefreshBusy` (`loop.go:72`, via `candidate_source.go:137`). A scheduled cycle can hold the slot up to `DefaultCycleBudget` (`refresh.go:19`). The MCP caller sees a generic failure with no retry hint (G10).
  5. `domain/modelcatalog/candidate_source.go:CandidateService.read` calls `remapView` before comparing revisions. Old stored classifications alone must not block creation; a policy edit racing that read must return an explicit `ErrExecutionRevisionStale`, and a fresh retry must use the edited rules.
  6. `domain/modelidentity/version.go:explicitVersion` preserves `req.Model` on every successful route; `path_inventory.go:versionRequests` supplies `p.Model` to every request. The seeded differing-version claim is not supported (G11). Verify mutable aliases fail at write, mixed routes fail explicitly, and `VersionRequestsForWorkflow` intentionally ignores unrelated workflows; retain `snapshotVersionResolver` as defensive validation, not a demonstrated production mismatch.
- **Seed evidence:**
  - `grep -rn 'SetModelSelection(' --include=*.go | grep -v _test.go`: `main.go:1189` and the definition.
  - `grep -rn SeedCreate --include=*.go | grep -v _test.go`: only its definition at `profile_service.go:263`.
  - Every following create or seed with no persisted proof writes and publishes a full `CommitCycle` report (`candidate_source.go:156-160`), even when the fetch failed.

### Area 8 — the guarded catalog fetch and the editable D2 policy decide followability, tier, order and stable canonical identity

- **Promise:**
  - The catalog source is the admin-configured URL (default `https://models.dev/api.json`), fetched through the SSRF guard.
  - Membership and order come from the admin-editable D2 full-match policy. A model that matches no pattern stays catalogued but is never followable.
  - Canonical identity strips at most one recognized routing prefix and never depends on the policy (D2, source evidence "routing prefix", commit `465940d`).
  - `model_catalog_list` exposes the result without source URLs or proof paths.

  Commits `465940d`, `f7a0ccc` and `b50cbc3`.
- **Entry:** Two entries:
  - The refresh read: `domain/modelcatalog/refresh.go:DiscoveryPipeline.collect` (:123) reads `CatalogURL` (:130), calls `Fetcher.Fetch` (:157) and `Normalizer.Normalize` (:162), then `remapView` (:200; defined at `candidate_source.go:84`) with `IdentityMapper.Map`.
  - The admin policy edit: `adapter/mcp/tools_model_identity_policy.go` (:79) → `domain/modelidentity/policy_service.go:Update` (:54) → `adapter/sqlite/model_identity_policy_repo.go:Replace` (:50).
- **Sink:** The stored catalog entries' `Tier`, `OrderKey`, `CanonicalID` and `PolicyRevision`, as consumed by `candidates.go:newerCandidates`. The list view is `domain/modelcatalog/admin.go:List` (:35) → `public.go:ProjectCatalog` (:42) → `adapter/mcp/tools_model_catalog.go:modelCatalogHandler` (:50).
- **Files:**
  - **Changed:**
    - `domain/modelidentity/identity.go` (`stripRoutingPrefix` :111, `recognise` :129, `Map` :207, `canonicalID` :258)
    - `adapter/modelcatalog/fetcher.go`, `adapter/outbound/guarded_client.go`
    - `adapter/mcp/tools_model_identity_policy.go`
    - `domain/modelcatalog/public.go`, `domain/modelcatalog/refresh.go`, `domain/modelcatalog/candidate_source.go` (`remapView`)
    - `domain/settings/settings.go` (`InternalReader`)
    - `cmd/arbitron/model_refresh.go`
  - **Unchanged but crossed:**
    - `domain/modelidentity/policy.go`, `policy_seed.go`, `policy_service.go`
    - `adapter/sqlite/model_identity_policy_repo.go`, `adapter/sqlite/migrations/025_model_identity_policy.up.sql`, `019_system_settings.up.sql`
    - `adapter/modelcatalog/normalizer.go`
    - `adapter/mcp/tools_model_catalog.go`, `adapter/mcp/register.go`
    - `domain/modelcatalog/admin.go`
    - `domain/settings/service.go`
- **Folded in:**
  1. Catalog URL setting: admin edits persist through HTTP, MCP and the dashboard. `domain/settings/settings.go:InternalReader` is the port the refresh reads; check that it is implemented by `settings.Service`.
  2. Fetch limits: 20 MiB (Content-Length plus `LimitReader`), 30 s, at most 3 redirects (`fetcher.go:17-21`). Errors are sanitized (`fetchFailure`).
  3. `policy_seed.go:InitialRules` parity with D2, including the 1–3 digit Claude segment bound (`model_identity_policy_acceptance_test.go:TestIdentityPolicySeedsD2`).
  4. `parseRevision` (`tools_model_identity_policy.go:89`) loses precision above 2^53 (Won't Fix).
  5. D8: no D2 rule may admit `azure` or `bedrock` catalog providers. Check whether `PolicyService.Update` refuses such a rule, or whether `ValidatePinRoutes` alone keeps it harmless.
- **Review 1 — wiring lens:**
  1. `main.go:1003-1005` builds `PolicyService` over the SQLite repo and gives it the shared `executionGate`. `register.go:10-11` registers `RegisterModelIdentityPolicyTools` and `RegisterModelCatalogTools`, and `main.go:345-346` puts `IdentityPolicy` and `ModelCatalog` (the admin service) into the MCP deps.
  2. The fetcher is guarded in production. `model_refresh.go:134-137` calls `NewFetcherWithTransport(nil)`, and even an injected transport keeps the URL and redirect guards (`guarded_client.go:116`; `adapter/modelcatalog/fetcher_transport_test.go:TestFetchTransportPreservesGuards`).
  3. A policy edit through MCP changes the stored tier on the next cycle, and a fresh selection follows it: `cmd/arbitron/model_profiles_policy_proof_test.go:TestModelProfilesPolicyEditFeedsFreshSelectionRoot`, `model_identity_policy_wiring_test.go:TestIdentityPolicyRootWiring`, `adapter/mcp/tools_model_identity_policy_test.go:TestIdentityPolicyMCPRoundTrip`.
  4. Routing prefixes yield one canonical identity for the same model on deepseek, ollama-cloud and openrouter, and exactly one event: `cmd/arbitron/model_identity_prefix_wiring_test.go:TestIdentityRoutingPrefixRoot`, `domain/modelidentity/identity_test.go:TestIdentityRoutingPrefixes`, `TestIdentityForeignRoutingPrefixesRemainUnknown`.
  5. All provider slices reach the commit: `cmd/arbitron/model_catalog_normalization_wiring_test.go:TestFetchNormalizeAllProvidersRoot`.
- **Review 2 — failure lens:**
  1. The guard rejects each of these (`guarded_client_test.go`):
     - an internal or private dial, re-checked at connect time through `net.Dialer.Control` (:58-65) to defeat DNS rebinding;
     - an IPv4-transition target;
     - a private redirect;
     - a credential-bearing URL.

     Errors carry no URL query token.
  2. A policy edit during a cycle cannot mix revisions: `collect` refuses it (`refresh.go:207-211`; `TestIdentityPolicyChangeRejectsInFlightSelectionRoot`). Discovery-only commits do not recheck the policy (G8).
  3. A contradictory `canonical_model_id` raises `ErrMalformedEvidence` (`identity.go:265-266`), and `normalizer.go:112-113` then invalidates the whole provider slice. Decide whether one row should poison the slice.
  4. Ambiguity and exclusions: an ambiguous match is never first-wins (`TestIdentityAmbiguousIDIsNeverFirstWins`). Dated snapshots and the `-fast`/`-preview` variants are excluded by construction. An unmatched follow target appears in `UnmatchedTargets`.
  5. Upgrade identity: `465940d` changed prefix recognition. Any pre-branch canonical ID that differs now would create a second first-observation and a second announcement on an existing install (G6; row 11).
  6. `model_catalog_list` projects stored entries without remapping (`public.go:44-46`), so after a policy edit it shows the old tiers until the next refresh. Decide whether that is acceptable, and say so on the list.
- **Seed evidence:**
  - The routing-prefix helpers are unexported (`identity.go:111,129`). `NewFetcherWithTransport` is used in production at `model_refresh.go:136`.
  - `announcements.go:18` declares a private copy of the settings-reader interface instead of reusing `settings.InternalReader`.
  - `normalizer.go:104-109` keeps the full configured URL, query included, in `SourceRef`. It is persisted but not projected (Won't Fix with reason).

### Area 9 — the first boot seeds YAML profiles before serving, then recovery and the four-hour loop run, and Close joins them

- **Promise:** The first boot with an empty profiles table seeds the configured profiles:
  - Pinned entries are validated by route; D8 configured IDs are allowed.
  - Following entries start on a candidate confirmed by durable prospective discovery, and execute without a restart.
  - Interrupted seeds resume without overwriting existing rows.
  - Then the startup refresh, publication recovery and crash recovery run, then the four-hour loop. `Close` cancels and joins all of it before the database closes.

  Design phase 2 says "A successful first boot must not silently postpone configured following aliases to another boot." Commits `1fe44cb`, `41ff3fa`, `25ac15d` and `55e798b`.
- **Entry:** `cmd/arbitron/main.go:runServe` (:1328) / `runMCP` (:1441) → `cmd/arbitron/model_dependencies.go:domainStack.Start` (:31) → `ds.startWork` (`main.go:1311`) → `cmd/arbitron/profile_seed.go:SeedProfiles` (:27) → `domain/provider/seed_batch.go:SeedBatch` (:15).
- **Sink:**
  - `adapter/sqlite/profile_seed.go:PendingProfileSeed` (:16) / `CompleteProfileSeed` (:79), plus `CreateValidated` or `store.Create`, then a recache.
  - The startup `coordinator.Refresh` (`model_dependencies.go:75`) and `startScheduled` (:122).
  - On close, `main.go:close` (:276) joins and then closes the DB (:319).
- **Files:**
  - **Changed:**
    - `cmd/arbitron/main.go`, `cmd/arbitron/model_dependencies.go`, `cmd/arbitron/model_refresh.go`, `cmd/arbitron/profile_seed.go`
    - `domain/provider/seed_batch.go`, `domain/provider/profile_service.go`, `domain/provider/selection.go`
    - `domain/modelcatalog/candidate_source.go`, `domain/modelcatalog/loop.go`, `domain/modelcatalog/refresh.go`
    - `adapter/provider/cache.go`
  - **Unchanged but crossed:**
    - `adapter/sqlite/profile_seed.go`, `profile_selection.go`, `profile_repo.go`
    - `adapter/sqlite/migrations/030_*` (seed recovery)
    - `domain/provider/profile_store.go`, `domain/provider/execution_gate.go`
    - `adapter/config/profiles.go` (`ProfilesFromConfig`)
- **Folded in:**
  1. The `ModelDependencies` fields (`model_dependencies.go:17-25`) replace external I/O only. Production passes `ModelDependencies{}` (`main.go:425`). Check that no orchestration or persistence can be substituted through them.
  2. The `SeedProfiles` branch with a nil seeder (`profile_seed.go:31-48`) is unreachable in production. Keep it acknowledgement-only.
  3. A second `Start` returns the cached result, and a closed stack is refused (`model_dependencies.go:36-47`).
- **Review 1 — wiring lens:**
  1. `startWork` (`main.go:1311-1324`) calls `SeedProfiles(ctx, profileRepo, profileSvc, domainProfiles, …)` with the real service as seeder, after `SetModelSelection` (:1189). `SeedBatch` fails closed without the gate (`seed_batch.go:16`).
  2. Following entries prepare outside the lock through `Coordinator.Do` with prospective `Collect`. Under the lock they re-read pending membership, re-check the proof (:84), store (:93), recache (:114) and complete (:115). Tests: `cmd/arbitron/model_profiles_lifecycle_test.go:TestColdFollowingSeedFirstBootRoot` (seed, then 4 h tick, then execution) and `profile_seed_batch_test.go:TestBootSeedRoutesYAMLProfilesThroughProfileService`.
  3. Start ordering: seed, then the startup refresh (Recover and Collect), then the scheduler, crash recovery, timers and webhooks, then `startScheduled`. Both `runServe` (:1340) and `runMCP` (:1453) call `Start` before creating any server.
  4. Shutdown: `Close` during `Start` joins it before the DB closes (`TestModelProfilesBuildStartCancellationRoot`; `main.go:282-292`). The second `modelRefresh.stop()` (:292) covers a loop installed during the race.
- **Review 2 — failure lens:**
  1. Discovery unreachable on first boot: the entry is skipped with "skipped config profile" (`main.go:1313`) and stays pending. Nothing retries it until the next boot, because `PendingProfileSeed` has no caller outside `SeedBatch` (G2). This contradicts the design phase 2 rule quoted above.
  2. Partial persistence: a fault between `storePrepared` and `CompleteProfileSeed` (`seed_batch.go:93-115`) fails boot. The next boot acknowledges the existing alias without overwriting it (`TestSeedProfilesResumesAfterPartialPersistence`, `TestSeedProfilesRecoversAcknowledgementFailure`).
  3. A proof that changes between prepare and the lock is skipped and the entry stays pending (`TestFollowingSeedRejectsStaleProofAndRetainsOriginal`). A concurrent admin alias is preserved (`TestFollowingSeedPreservesConcurrentAdminAlias`).
  4. Startup refresh errors are only logged (`model_dependencies.go:75-80`), and `scheduler.Start` (:86) runs before crash recovery (:87). Show that a recovery failure stops the scheduler and fails `Start`.
  5. After a successful `Start`, cancelling the caller's ctx permanently closes the coordinator via `AfterFunc` (:55), and later following creates get `ErrRefreshClosed`. Confirm this is harmless for serve and MCP.
- **Seed evidence:**
  - `grep -rn 'PendingProfileSeed(' --include=*.go | grep -v _test.go`: `seed_batch.go:22,56` and `profile_seed.go:33` only. The refresh loop never revisits pending seeds.
  - `model_start_test.go` contains no tests, only the `initRecoveryTestDomain` helper.

### Area 10 — each newly usable model is announced at least once to Teams with its original changes; the webhook is empty by default and redacted publicly

- **Promise:** Spec Feature 5, Outcome 3, D7, and design phase 4 "Current root delivery completion":
  - Every newly confirmed canonical model can produce a card, including older, other-tier, unknown-tier and no-profile models.
  - The webhook state at discovery decides the event: empty suppresses it for good, and a read fault leaves it `awaiting_setting`.
  - Delivery is at least once, with a 4 h → 24 h backoff.
  - A multipart card re-reads the destination before each part, and clearing the destination pauses delivery.
  - The webhook is empty by default and redacted on every public read.

  Commits `01dac2f`, `50f3a26` and `978362e` (acceptance), plus `012d764`.
- **Entry:**
  - Event creation: `adapter/sqlite/model_catalog_repo.go:observeCatalogTx` (:271) / `discoveryEventState` (:330), inside `CommitCycle`. The state comes from the fresh post-collection read in `refresh.go:Collect` (:113-119).
  - Delivery: `domain/modelcatalog/announcements.go:AnnouncementService.Announce` (:46) → `Drain` (:50), called from `RefreshService.announce` after every cycle, including a failed one (`refresh.go:355,378`).
- **Sink:** `adapter/notification/model_announcement_sender.go:TeamsModelSender.SendPart` (:43) POSTs to the receiver. Then `adapter/sqlite/model_announcement_repo.go:Complete`/`CompletePart` (:181/:184) records the outcome.
- **Files:**
  - **Changed:**
    - `adapter/sqlite/model_catalog_repo.go`
    - `domain/modelcatalog/refresh.go`
    - `cmd/arbitron/model_refresh.go` (:148-162), `cmd/arbitron/main.go` (:1172-1176), `cmd/arbitron/model_dependencies.go`
    - `domain/settings/settings.go`
    - `adapter/outbound/guarded_client.go`
  - **Unchanged but crossed:**
    - `domain/modelcatalog/announcements.go`, `announcement_sender.go`
    - `adapter/sqlite/model_announcement_repo.go`, `migrations/022_model_catalog.up.sql`, `026_model_announcement_retries.up.sql`, `019_system_settings.up.sql`
    - `adapter/notification/model_announcement_sender.go`, `model_announcement_payload.go`
    - `domain/settings/service.go`, `adapter/mcp/tools_system.go`, `adapter/http/server.go`, `domain/config/teams.go`
- **Folded in:**
  1. Webhook setting: empty by default (`019_system_settings.up.sql:8`) and redacted on `Service.Get` (`service.go:44`), which is used by `tools_system.go:113` and `http/server.go:499`. Internal reads return the raw value, and a read error is never treated as empty.
  2. The `PartitionAnnouncement` multipart plan is persisted at first claim. The lease is 60 s. A 2xx with an unreadable body counts as confirmed, and proof paths are excluded from cards.
  3. Announcer ordering: `SetAnnouncer` (`model_refresh.go:159`) runs at build time, and `RefreshService.SetAnnouncer` refuses replacement after start (`refresh.go:306-317`; `domain/modelcatalog/refresh_lifecycle_test.go:TestRefreshRequiresAnnouncerAndRejectsLateReplacement`).
  4. Root announcement tests replace the guarded transport outright (`main.go:1175-1176`), so `validateURL` is never exercised at root (G20).
- **Review 1 — wiring lens:**
  1. `newModelRefresh` builds `NewModelAnnouncementRepo(d.DB)`, `NewTeamsModelSender(announceClient)` and `NewAnnouncementService(…, d.Settings, …, clockFunc(clock))` (`model_refresh.go:148-155`). `announceClient` defaults to `outbound.NewGuardedClient(15s, 0)`, never the proxy-honouring `newOutboundHTTPClient` (`main.go:409`).
  2. The refresh caller supplies `CycleCommit.WebhookConfigured` from a fresh `ReadInternal` after collection: nil on a fault, true when configured, false when empty (`refresh.go:113-119`; `TestCollectClassifiesWebhookFromFreshPostCollectionRead`).
  3. Source-wide discovery reaches the receiver through the real root, migrated SQLite and the four-hour ticker: `cmd/arbitron/model_announcement_acceptance_test.go:TestModelAnnouncementAllDiscoveredCandidatesRoot`, `model_announcement_wiring_test.go:TestModelAnnouncementWiringToReceiverSink`.
  4. The announcer is installed before startup work: `cmd/arbitron/model_profiles_announcement_test.go:TestModelProfilesAnnouncementInstalledBeforeStartupRoot`. Manual and ticker drains are joined at `Close` (`TestModelProfilesCloseJoinsManualAndTickerRoot`).
  5. The settings seam through HTTP: `TestModelAnnouncementSettingsSeamThroughHTTP`. A fault-time event awaits the setting: `TestModelAnnouncementAwaitsSettingWhenEligibilityReadFaults`.
- **Review 2 — failure lens:**
  1. A non-2xx response returns the event to pending. A timeout leaves it `delivery_unknown`. Both retry with persisted backoff (`model_announcement_repo.go:38-44`; `TestAnnouncementClaimsBackoffReopenAndRecovery`, `TestModelAnnouncementRetryReopenDespiteDiscoveryFailureRoot`).
  2. Clearing the webhook mid-multipart is recorded as `SendDefiniteFailure "destination_empty"` (`announcements.go:110`), which spends an attempt and adds at least 4 h of backoff. Design phase 4 says clearing pauses (G7).
  3. A corrupt row is deferred with backoff (`repo.go:163`; `TestAnnouncementCorruptRowsDoNotBlockReceiver`). A raw `Scan` type error is not `ErrAnnouncementState`, so it aborts `Drain` and leaves the head stuck (`announcements.go:84-85`; G15).
  4. Confirmed multipart indexes survive reopen and are not resent, and a destination change redirects only later parts (`TestModelAnnouncementMultipartPauseReopenRoot`). Errors and logs carry no raw webhook URL or response body (`sender.go:74-76`).
  5. The card's provider and tier come from the first historical entry per canonical, sorted by provider (`model_catalog_repo.go:272-277,308`), not from the confirming path. A model confirmed on ollama-cloud is announced as `deepseek` (G9).
- **Seed evidence:**
  - `grep -rn 'NewAnnouncementService\|NewTeamsModelSender\|NewModelAnnouncementRepo' --include=*.go | grep -v _test.go`: definitions plus `model_refresh.go:148-155`. All three are constructed in production.
  - `discoveryEventState` maps nil to `awaiting_setting`, false to `suppressed` and true to `pending`. `observeCatalogTx` reclassifies older `awaiting_setting` rows when the bool is non-nil (`model_catalog_repo.go:323-326`).
