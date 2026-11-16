# Gaps identified — LunaRoute provider

> **Rule:**
> - Each record is at most ten lines.
> - When an item is closed, move it under `## Closed` with a one-line note.
> - The open list is what a pass reads, so keep it short.
> - An unfed or inert production path is never "acceptable". Fix it, or escalate it under `Won't Fix` with the ruling that
>   covers it.

## Open

- [x] **G14 (Area 1): closed by approver at 142dcc5.** `go test -count=1 -timeout 40m ./cmd/arbitron` → `ok` (315s,
  exit 0), including `TestLunaRouteProfileScopeWiring` with the corrected Azure destination key.
- **[ESCALATION] Area 1 scope ruling (approver, 142dcc5): G12/G13 do not gate review2 — box ticked.** Review-2 checks
  1–5 are all met at the domain/repo sink. G12 (MCP `""` means keep, shipped for every provider) needs an operator ruling;
  G13 (operator explicitly pairs an `lr_` key with a foreign provider) is not among checks 1–5 → rule-5 sibling. Both stay
  open as their own gaps below; do not reopen Area 1 review2 for them.

- **[ESCALATION] G12 (Area 1): should MCP `api_key: ""` on a same-provider lunaroute update be refused or mean "keep"?**
  Approver ruling: not a review2 blocker — `connectionUpdateHandler` maps "" to omission for every provider (shipped
  behaviour) and no invalid key is persisted; an operator must decide whether to narrow that. G13 (explicit `lr_` key on
  a foreign provider) is a sibling defect under rule 5 — fix it as its own gap, but it does not gate this box.

- [ ] **G3 (Area 6): classifier boundaries are untested.**
  The `harden:domain/provider/lunaroute_classify.go` survivors sit at lines 118, 137, 141, 189, 217, 225, 229, 232, 234 and
  236. They cover:
  - the redaction pair sizing;
  - the 256-byte snippet boundary;
  - the Retry-After digit test;
  - transport-before-code precedence;
  - verified-code-before-status precedence.

  Add a table test per boundary that fails under each mutant.
- [ ] **G4 (Area 6): StreamChat may not classify SSE error frames.**
  The `harden:adapter/openrouter/provider.go` survivors are at lines 239–240: negating
  `p.classify != nil && len(frame.Choices) == 0 && frame.Usage == nil` survives, and so does the scanner buffer at line 209.
  A structured LunaRoute error frame mid-stream may produce empty content instead of a classified failure. Prove it is
  caught, or fix it.
- [ ] **G6 (Area 7): a 3xx listing status may count as success.**
  `harden:adapter/probe/lunaroute.go` survives at line 76 (`resp.StatusCode >= 300`). Add a 300 response at the fake
  gateway: it must classify as a failure, never as a listing.
- [ ] **G10 (Areas 4, 5): executed-identity safety checks are untested.**
  The survivors are `harden:adapter/codex/lunaroute.go` (line 97, `safeLunaRouteIdentity`) and
  `harden:adapter/pi/model_probe.go` (lines 60 and 62). Reported model strings with quotes, whitespace, control characters,
  `lr_` text or more than 256 bytes must be rejected as evidence. Add a case per clause.
  Progress (single:4/review1): codex half pinned by `adapter/codex/lunaroute_identity_test.go:TestSafeLunaRouteIdentity_EachClause`
  (dropping the quote/backslash clause fails it). The pi half (`adapter/pi/model_probe.go` 60/62) remains open for Area 5.
- [ ] **G11 (Areas 3, 7): text survivors need a decision.**
  The survivors `harden:adapter/mcp/tools_connection_models.go`, `harden:adapter/mcp/lunaroute_backend_hint.go` and
  `harden:domain/provider/model_discovery.go` are string-concatenation mutants in LLM-facing text. Pin the load-bearing
  phrases in tests (unknown is not a guarantee; codex or pi only; pinned only), or record each mutant as equivalent with a
  reason.

- [ ] **G12 (Area 1): MCP accepts an explicit empty key on an existing LunaRoute connection.**
  `adapter/mcp/tools_connection.go:connectionUpdateHandler` turns `api_key: ""` into nil; the repo preserves the old key
  and increments `ConfigRevision`, violating failure-lens check 1's empty-key refusal/no-row-change requirement.
  Reproduced through `cmd/arbitron/lunaroute_connection_wiring_test.go:lunaRouteMCP` in a temporary test overlay:
  create lunaroute with `lr_original`, update same provider with `api_key: ""` → success and revision 1 → 2.
  The temporary `TestArea1EmptyKeyRefused` failed (exit 1); no failing test or overlay remains in the worktree.
- [ ] **G13 (Area 1, high): explicitly supplied LunaRoute keys can still be paired with a foreign provider.**
  `domain/provider/lunaroute_connection.go:PrepareLunaRouteCreate` / `PrepareLunaRouteUpdate` pass through foreign-provider
  keys unchecked. G1 now requires a nonempty replacement, but resubmitting `lr_original` on switch-away is accepted.
  Real-root temporary `TestArea1ForeignProviderRejectsLunaRouteKey` failed (exit 1) for both openrouter create with
  `lr_mispaired` and lunaroute → openrouter update with `lr_original`; persisted provider became openrouter.
  Refuse `lr_` keys at foreign-provider writes with static secret-free guidance; test create, update and switch-away sinks.

## Review evidence

- **Area 3 / single:3/review2 — failure assessment (G7).** Initial targeted tests built and passed; no baseline failure.
  Checks 1–4: root `TestLunaRouteProfileScopeWiring`, `TestLunaRouteAgentProfileSaveWiring`, `TestLunaRoutePiEffortEchoWiring`,
  `TestLunaRouteDeferredBackendsSaveWiring` and domain `TestCheckLunaRouteRoute_*` pass: following/backend/axis/limit refusals precede writes.
  Check 5: `domain/provider/lunaroute_profile_policy_test.go:TestLunaRouteMixedRouteBackendsPreserveAxes` uses real version grammar;
  both route orders/create/update refuse different axes without row changes; equal axes persist/echo. Negating the backend branch fails all four refusal cases (overlay exit 1).
  Folded G7: `adapter/config/capabilities_lunaroute_test.go:TestLoadCapabilityRules_LunaRouteEmptyMapsReplaceKnownFacts` proves omitted/null/empty maps replace known facts with unknown.
  Three loader `>0`→`>=0` mutants only allocate empty maps: getters expose identical unknown facts; output `>0`→`>=0` is redundant because Known() implies a positive limit; sort `<`→`<=` is equivalent for distinct loaded ids. All five overlays pass.
  Check 6: `profileUsesLunaRoute` gates axes; native Profile/LoadCapabilityRules tests pass; `rg -n 't.Skip'` in LunaRoute domain/profile MCP tests returns none. Checks 7–9: existing identity/root/sample tests pass, but G8's qualifier/alias-hint distinctions remain unproved.
  Validation: `go test -count=1 -timeout 5m ./domain/provider ./adapter/config ./adapter/mcp -run 'LunaRoute|LoadCapabilityRules|Profile|AuthoringGuidance_ModelDiscoveryAndExplicitSelection'` exits 0; targeted root stale-admin/profile tests and identity/sample tests also exit 0. No production code or approval boxes changed.
  Recommend: no approval for single:3/review2 — G8 still lacks discriminating contradictory vendor/Bedrock/reviewed-alias fixtures named in the escalation above.

- **Area 3 / single:3/review1 — APPROVED independently at refinement 0f37e73.** Only Review 1 ticked; all eight wiring checks verified.
  Check 1: `cmd/arbitron/main.go:initDomainWithModelDependencies` feeds the same loaded `capabilityRules` to ProfileService, Factory, discovery and snapshotter; `cmd/arbitron/lunaroute_capabilities_wiring_test.go:TestLunaRouteCapabilityTableWiring` and `TestLunaRouteEmbeddedOutputLimitWiring` verify override facts and embedded bounds at real sinks.
  Checks 2–3: `domain/provider/profile_service.go:write`/`revalidatePrepared` pass `s.rules` through selection and route validation; `domain/modelcatalog/candidate_source.go:ConnectionProvider` forwards to `adapter/provider/path_inventory.go:ConnectionProvider`, which reads `conn.Provider` from the real store. Root `main.go:initDomainWithModelDependencies` injects `refresh.candidates`; `TestLunaRouteProfileScopeWiring` verifies stored-provider changes and no discovery I/O.
  Check 4: `cmd/arbitron/lunaroute_agent_wiring_test.go:TestLunaRoutePiEffortEchoWiring` persists none/low/high/max and asserts both stored and MCP effective effort. An approver-only Go overlay replacing the root ProfileService rules with `provider.CapabilityRules{}` made this test fail on missing pi effort support (exit 1); unmodified production passes.
  Check 5: `domain/provider/selection.go:ValidatePinRoutes` preserves LunaRoute codex routing; `domain/provider/lunaroute_codex_routes_test.go:TestValidatePinRoutes_LunaRouteCodexKeepsProviderAndGrammar` verifies acceptance and route-specific refusals; its non-LunaRoute companion preserves the subscription carve-out.
  Check 6 / folded loader: `adapter/config/capabilities.go:LoadCapabilityRules`/`parseLunaRouteModels` preserve unknown versus known-empty, replace per model and retain embedded defaults; `capabilities_lunaroute_test.go:TestLoadCapabilityRules_LunaRouteSection`, `TestLoadCapabilityRules_LunaRouteRejectsBadFacts`, `capabilities_lunaroute_internal_test.go:TestLoadCapabilityRules_LunaRouteOverrideReplacesPerModel` and `capabilities_test.go:TestLoadCapabilityRules_MissingOverrideIgnored` cover those paths. `domain/provider/lunaroute_capability_test.go:TestCapabilityRules_CarriesLunaRouteTable` asserts sorted ids.
  Check 7: `domain/provider/lunaroute_validate.go:checkLunaRouteRoute` calls real `NewVersionResolver` and `ClassifyFamily`; `domain/modelidentity/version.go:lunaRouteReviewedIDs` has exactly the three reviewed ids and zhipuai/deepseek evidence. `version_lunaroute_internal_test.go:TestLunaRouteProductionIdentityEvidence` verifies vendors and live document anchors.
  Check 8 / folded guidance: `samples/family_test.go:familyOfNode` uses shared `ClassifyFamily`; `TestJudgePairingProblems` rejects LunaRoute GLM versus direct GLM and accepts DeepSeek. No private modelFamily remains. `adapter/mcp/lunaroute_backend_hint_test.go:TestLunaRouteBackendHint_ReachesProfileTools` verifies registered descriptions; `authoring_guidance.go:AuthoringGuidance` states pinned-only and explicit codex/pi.
  Validation: `go test -count=1 ./cmd/arbitron ./samples ./domain/provider ./domain/modelidentity ./adapter/config ./adapter/mcp ./adapter/sqlite ./adapter/provider` exited 0, with no build failure; six targeted root wiring tests also exited 0. Happy/success persistence and echo, typed refusals, source errors and edge axes are covered by the cited root tests and `domain/provider/lunaroute_profile_policy_test.go:TestLunaRouteMixedRoutesAndSourceErrors`; native-provider tests ran without LunaRoute skips.
  Other lens: G7/G8/G11 remain open for boundary/mutation and text-failure review; they reveal no missing producer or sink in this wiring assessment. No new wiring gap found.

- **Area 2 / single:2/review2 — APPROVED by independent approver at 65fd03a.** All twelve failure checks demonstrated; only Review 2 ticked.
  Checks 1–6/12: `adapter/provider/lunaroute_agent_test.go:TestLunaRouteAgent_EffectiveRouteRefusalsBeforeRevealAndRun` counts reveal + inner launch (zero on every refusal incl. nil caps → `ErrSelectionPortsMissing`, alias/unreviewed/empty pins, pi limits/efforts narrowed, tampered endpoint); `TestLunaRouteAgent_SwitchedProviderPinRefusedBeforeRevealAndRunner` traces `dynamic.go:Resolve` → `pinned_route.go:validatePinnedRoute`; root override/legacy tests pass.
  Checks 7–11: `lunaroute_agent.go:diagnose`/`lunaRouteDiagnosable` covered by `TestLunaRouteAgent_DiagnosisObservations`, `_NoDiagnosisForLocalOrCancelled`, `_NilProbesIsUncheckedNotPanic`, `_StalledDiagnosisHonorsDeadline`; `domain/provider/lunaroute_diagnosis_test.go` covers redaction + nil original.
  Approver mutations of `lunaroute_agent.go` (local probe error → Checked; listed → credentials rejected; preflight removed; timeout removed) each exit 1. `go test` of adapter/provider, domain/provider, adapter/probe, domain/engine, cmd/arbitron (-run LunaRoute) exit 0; `go build ./...` clean.
  Note (non-blocking): `_StalledDiagnosisHonorsDeadline/parentBound=false` waits the real 20 s; consider an injectable timeout to keep the suite fast. The `[ESCALATION]` entry above is resolved by this approval.

- **[ESCALATION] Area 2 / single:2/review2 acceptance boundary.** Approval requires committed fixtures at
  `adapter/provider/lunaroute_agent_test.go` proving refusal before both reveal/run counters, pi table narrowing,
  switched-provider pin rejection, and a stalled diagnostic HTTP request ending at the effective deadline without retry.
  G5 additionally needs exact `Checked`/`Observation`/text/identity assertions and nil-original coverage at
  `domain/provider/lunaroute_diagnosis_test.go:TestLunaRouteDiagnosis`. These are missing fixtures, not an external
  dependency or an undecided policy. All required fixtures now pass as recorded below; no external blocker remains.

- **Area 2 / single:2/review2 — failure-lens assessment.** Pre-change targeted tests in adapter/provider, domain/provider and cmd/arbitron passed; no baseline failure was found.
  Checks 1–3/5/6/12: `adapter/provider/lunaroute_agent_test.go:TestLunaRouteAgent_EffectiveRouteRefusalsBeforeRevealAndRun` runs the real preflight with saved pi facts, removed limits/efforts, node overrides, legacy codex default, nil capabilities, empty/unreviewed/alias pins and counts both reveal and inner launch; every refusal counts zero, while the valid route counts one each.
  Check 4: the same test rejects tampered hosted endpoints before both sinks; `TestLunaRouteAgent_SwitchedProviderPinRefusedBeforeRevealAndRunner` follows `dynamic.go:Resolve` → `factory.go:AgentProviderFor` → `pinned_route.go:validatePinnedRoute` for pi/codex with an incompatible retained pin and counts zero store reveals/runner calls.
  Check 7: `TestLunaRouteAgent_NoDiagnosisForLocalOrCancelled` and root `TestLunaRouteCodexWiringLocalFailureNeverProbes` pass, including every local sentinel and cancellation; root legacy/default/deferred/explicit-node refusal tests also pass.
  Checks 8–10/G5: `TestLunaRouteAgent_DiagnosisObservations` now asserts exact observation identity, checked/listed flags, current-observation versus unchecked text, original identity, partial-result preservation, one mission call and at most one probe; `domain/provider/lunaroute_diagnosis_test.go:TestLunaRouteDiagnosis_NilOriginalAndObservationPrecedence` covers nil-original formatting and redacted observation precedence.
  Check 11: `TestLunaRouteAgent_StalledDiagnosisHonorsDeadline` drives the real probe HTTP sink until its 20-second deadline (and separately a 50ms parent deadline), preserving original/deadline error identities and partial result with exactly one request/run; nil probes remain explicitly unchecked.
  Ordering/concurrency: `adapter/provider/lunaroute_agent.go:RunAgent` validates before dispatch and `diagnose` allocates its result locally without changing wrapper fields; no rollback state or shared diagnosis result is introduced. Success, local faults, opaque failures, empty listings and nil ports are covered.
  Verification: `go test -timeout 2m ./adapter/provider ./domain/provider ./adapter/probe ./cmd/arbitron -run 'Test(LunaRouteAgent_|LunaRouteDiagnosis|CheckLunaRouteLaunch|CheckLunaRouteAdapterLaunch|SnapshotLunaRouteCapabilityPolicy|ValidatePinnedRoute_LunaRouteCodexKeepsProvider|RouteBinding_NonLunaRouteCodexNeverReveals|LunaRoute(LegacyDefaultBackendRefused|DeferredNodeOverridesRefused|ExplicitNodeOverrideChecked|CodexWiringOpaqueFailure|CodexWiringLocalFailureNeverProbes|Probe_CancellationAndTimeout))' -count=1` exited 0; six temporary Go-overlay mutations each exited 1 on assertions (classified flag, local-error guard, nil-original guard, observation guard, nil capability input, removed timeout); overlays were removed.
  Recommend: approve single:2/review2 — all twelve failure checks demonstrated; G5 closed, including nil/empty, refusal ordering, deadline, partial-result and no-retry evidence; approval boxes untouched.

- **Area 2 / single:2/review1 — APPROVED by independent approver at f890e22.** All eleven wiring checks demonstrated; only Review 1 ticked.
  Check 1: `adapter/provider/dynamic.go:Resolve` (both arms), `adapter/provider/path_inventory.go:routeBinding`, `domain/engine/executor_agent.go:Execute` and `domain/provider/pinning.go:PinningResolver.Resolve` preserve author intent; `TestDynamicProfileResolver_BackendExplicitProvenance`, `TestAgentCallExecutor_BackendExplicitProvenance` and `cmd/arbitron/lunaroute_reveal_wiring_test.go:TestLunaRouteBackendProvenanceWiring` pass. Each dynamic inversion independently fails the new tests (exit 1).
  Checks 2/5/6: `adapter/provider/factory.go:AgentProviderFor` rejects deferred backends before guard/construction; constructor inventory finds batch codex/pi/claude/opencode binding constructors only in its private delegate. `adapter/provider/path_inventory.go:validateRoute` and `adapter/probe/model_usability.go:AgentUsabilityLane.Check` use that factory; root `TestLunaRouteDeferredNodeOverridesRefused` and `TestLunaRouteInteractiveAgentRefusedAtRoot` pass, with `adapter/provider/interactive_stream_selector.go:StreamProviderFor` refusing before defaults.
  Checks 3/4: `cmd/arbitron/main.go:initDomainWithModelDependencies` loads real capability rules into Factory and `NewModelSnapshotter`; `adapter/provider/lunaroute_agent.go:wrapLunaRouteAgent` preflights before inner launch; `adapter/provider/model_snapshot.go:SnapshotModels` feeds `domain/provider/selection.go:ValidatePinRoutes`. `TestLunaRouteAgent_LaunchRefusalBeforeLaunch` and `TestSnapshotLunaRouteCapabilityPolicy` pass; passing nil caps to the production wrapper makes the root opaque-failure test fail (exit 1).
  Checks 7/9: root `Probes: probeFactory` reaches `adapter/provider/lunaroute_agent.go:diagnose` and `adapter/probe/lunaroute.go:Probe`; `cmd/arbitron/lunaroute_codex_wiring_test.go:TestLunaRouteCodexWiringOpaqueFailure` records exactly one authenticated hosted GET. `adapter/provider/lunaroute_agent_test.go:diagnosisProbes.Probe` asserts every binding field, revealed key and deadline <=20s; `TestLunaRouteAgent_PostFailureHTTPSink` passes.
  Checks 8/10: `adapter/provider/lunaroute_agent_test.go:TestLunaRouteAgent_SuccessIsPassThrough` preserves result/mission and counts zero probes; `TestLunaRouteAgent_DiagnosisObservations` proves original and observation identities through `domain/provider/lunaroute_diagnosis.go:LunaRouteDiagnosis.Unwrap`.
  Check 11: `domain/provider/pinning.go:PinningResolver.Resolve` replaces Model before `domain/provider/lunaroute_validate.go:checkLunaRouteBinding` feeds route/identity checks. A temporary root test with live glm-5.3, pinned glm-5.3-flash and flash marked agent-unsupported refused flash before corrupt-cipher reveal; dropping the production pin assignment makes it fail (exit 1). No temporary source changes remain.
  Folded/scenarios: R7's `domain/provider/lunaroute_validate.go:lunaRouteEnabledBackends` permits codex/pi only; codex reveal/pin carve-outs verified by `TestLunaRouteCodexWiring`, `TestRouteBinding_NonLunaRouteCodexNeverReveals` and `TestValidatePinnedRoute_LunaRouteCodexKeepsProvider`. `TestLunaRouteAgent_NilProbesIsUncheckedNotPanic`, diagnosis observation/local-error tables, and root codex/pi completed outputs cover happy/success/failure/error/edge categories with argument-checking sinks.
  Verification: `go test -timeout 30m ./...` exited 0 (52 packages); uncached focused tests in adapter/provider, domain/provider, domain/engine, adapter/probe and cmd/arbitron exited 0. Verification inputs were unsubstituted, so no supplied measurement was assumed. G5 remains an open Review-2 failure-lens gap and does not withhold this box.

- **Area 1 / single:1/review2 — APPROVED by approver at 142dcc5.** `go build ./...` ok; `go test -count=1` ok for
  `domain/provider`, `adapter/sqlite`, `adapter/mcp`, and the full `cmd/arbitron` package (exit 0). Check 1:
  `TestConnectionRepo_LunaRouteCreateRejectsForeignKeyAndEndpoint`, `..._LunaRouteUpdateRules`, `TestValidLunaRouteKey`,
  `TestConnectionLunaRoutePersistenceErrorsDoNotDiscloseKeys`. Check 2: `..._LunaRouteMetadataUpdateIsGuardedAgainstConcurrentSwitch`.
  Check 3 (G1): `PrepareLunaRouteUpdate` → `ErrLunaRouteDestinationKeyRequired`; `..._SwitchAwayFromLunaRouteRequiresNewKey`
  (omitted/empty/seal/SQL/cancel/deadline), `..._ConcurrentSwitchDoesNotLeakKeyToForeignProvider`. Check 4:
  `TestNormalizeLunaRouteBaseURL_RejectsOtherDestinations`. Check 5: `"lr_ab cd"` case; `lrWriteFailure` exec error.

- **Area 1 / single:1/review2 — fixture correction and convergence escalation.**
  Arrival regression reproduced: `go test -timeout 30m ./cmd/arbitron -run '^TestLunaRouteProfileScopeWiring$' -count=1` exited 1 with `ErrLunaRouteDestinationKeyRequired`; this predates this pass's change, despite the green branch-point baseline.
  Check 1: explicit-key shape/refusal and secret-free storage-error tests pass; `adapter/mcp/tools_connection.go:connectionUpdateHandler` still maps explicit empty to omission (G12).
  Checks 2–3: `TestConnectionRepo_LunaRouteMetadataUpdateIsGuardedAgainstConcurrentSwitch`, `TestConnectionRepo_LunaRouteConcurrentSwitchDoesNotLeakKeyToForeignProvider` and `TestConnectionRepo_SwitchAwayFromLunaRouteRequiresNewKey` pass, including cancellation/deadline/rollback; G13's explicit-key pairing remains.
  Checks 4–5: endpoint edge tables, embedded-space `TestValidLunaRouteKey`, and `TestConnectionRepo_LunaRoutePersistenceErrors/conditional_SQL` pass; the fault double checks SQL arguments.
  Grep `rg -n 'conn(Svc|Store)\.Update|PrepareLunaRouteUpdate|ErrLunaRouteDestinationKeyRequired' . --glob '*.go'` identifies the corrected fixture, real stored.Provider input, and static MCP sentinel mapping; no new production field was added.
  Completed: focused Area 1 tests in `domain/provider`, `adapter/sqlite`, `adapter/mcp` exited 0, covering canonicalization, stored-root echo and key-safe failures too.
  Completed: `go test -timeout 30m ./cmd/arbitron -run '^TestLunaRoute(ProfileScopeWiring|Connection.*)$' -count=1` exited 0; the corrected fixture retains the profile-policy assertions and no-network check.
  Incomplete: `go test -timeout 30m ./cmd/arbitron -count=1` stopped at assessment (exit 143); broader package failures remain unverified (G14).
  Recommend: no approval for single:1/review2 — G14 needs completed package evidence; G12/G13 still conflict with the written empty-key/no-foreign-key contract, requiring the escalation above.

- **Area 1 / single:1/review2 — withheld by approver against f0690de.** Full `go test ./cmd/arbitron -count=1` FAILs
  `TestLunaRouteProfileScopeWiring` (G14); it passes at f0690de~1. `domain/provider`, `adapter/sqlite`, `adapter/mcp` ok;
  `go build ./...` ok. Otherwise checks 2–4 look covered (`TestConnectionRepo_SwitchAwayFromLunaRouteRequiresNewKey`,
  `..._LunaRouteConcurrentSwitchDoesNotLeakKeyToForeignProvider`, endpoint `%2F` case). Next approver: re-run the full
  package, then judge checks 1–5; G12/G13 are not grounds to withhold.

- **Area 1 / single:1/review2 — failure-lens refinement (no approval).**
  Baseline: the initial focused provider/sqlite/mcp/cmd tests passed before edits; no pre-existing area failure observed.
  Check 1: key-shape/storage-error coverage passes, but real-root empty-key reproduction violates no-row-change (G12).
  Checks 2–3: `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_LunaRouteConcurrentSwitchDoesNotLeakKeyToForeignProvider` catches a missing foreign-provider CAS; `TestConnectionRepo_SwitchAwayFromLunaRouteRequiresNewKey` covers nil/empty, success, seal/SQL failure, cancellation and deadline without partial writes. Both defect tests were red before the fix; G13 remains for explicit lr_ resubmission.
  Checks 4–5: `domain/provider/lunaroute_endpoint_test.go:TestNormalizeLunaRouteBaseURL_RejectsOtherDestinations` includes `%2F`; removing the whitespace scan makes `TestValidLunaRouteKey` fail for `lr_ab cd`; inverting the generated conditional-query error guard makes `TestConnectionRepo_LunaRoutePersistenceErrors/conditional_SQL` fail (both temporary mutations exit 1).
  Folded items: canonicalization and stored-root/secret-free response tests pass; new sentinel is consumed by production `adapter/mcp/tools_connection.go:connErrorResult`, whose wrapped-secret test passes. `rg -n 'PrepareLunaRouteUpdate|ErrLunaRouteDestinationKeyRequired' . --glob '*.go' --glob '!**/*_test.go'` confirms real stored.Provider input and error consumer.
  Verification (exit 0 in all four packages): `go test -timeout 30m ./domain/provider ./adapter/sqlite ./adapter/mcp ./cmd/arbitron -run '^Test(ValidLunaRouteKey|PrepareLunaRoute.*|NormalizeLunaRouteBaseURL.*|IsLunaRoute|Canonicalize.*|ConnectionRepo_.*|ConnectionLunaRoute.*|Connection(Create|Update|Get|List|Delete)_.*|ConnErrorResult_.*|LunaRouteConnection.*)$' -count=1`.
  Recommend: no approval for single:1/review2 — G12 accepts an explicit empty key and writes a revision; G13 accepts lr_ keys on foreign providers, both reproduced through real MCP/SQLite.

- **Area 1 / single:1/review1 — approved independently against refinement 3f59d42.**
  Check 1: `cmd/arbitron/main.go:initDomain` builds SQLite/AES-GCM → ConnectionService → `domainStack.mcpDeps`; `adapter/sqlite/connection_repo.go:Create` prepares before Seal, and `ConnectionRepo.Update` prepares after metadata read; `cmd/arbitron/lunaroute_connection_wiring_test.go:TestLunaRouteConnectionWriteWiring` reaches stored rows and revealed keys through real MCP.
  Check 2: `adapter/sqlite/connection_repo.go:Update` is the sole production `PrepareLunaRouteUpdate` caller and passes `stored.Provider`; `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_SwitchToLunaRouteRequiresNewKey` verifies the foreign-provider source and refusal/success sinks.
  Check 3: `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_LunaRouteAcceptedSpellingsPersistHostedRoot` passes all nine spelling classes for Create and Update, asserting canonical provider/root through fresh Get reads.
  Check 4: `adapter/mcp/tools_connection.go:connErrorResult` maps all three write sentinels to actionable guidance; `adapter/mcp/tools_connection_lunaroute_test.go:TestConnErrorResult_LunaRouteSentinels` proves wrapped errors cannot echo submitted secrets.
  Check 5: repository-wide production Go/SQL searches found no connection-table writers outside `adapter/sqlite/connection_repo.go:Create` and `ConnectionRepo.Update` calling generated queries; `cmd/arbitron/connection.go:newConnectionCmd` exposes validation only, and seeds add no connection writer.
  Folded items: `domain/provider/canonical.go:Canonicalize` trims/lowercases without a LunaRoute alias; `adapter/mcp/tools_connection.go:RegisterConnectionTools` documents the provider/root/lr_ key; `metaOf`, `connectionGetHandler` and `connectionListHandler` emit stored metadata without a key field. A temporary test overlay exercised both reads through the real root successfully.
  Happy/success/failure/error/edge coverage uses real SQLite/AES-GCM in `adapter/sqlite/connection_repo_lunaroute_test.go:TestConnectionRepo_LunaRouteCreateNormalizesAndSeals`, `TestConnectionRepo_LunaRouteUpdateRules`, `TestConnectionRepo_LunaRouteCreateRejectsForeignKeyAndEndpoint`, `TestConnectionRepo_LunaRoutePersistenceErrors` and the spelling table; fault fakes assert their key/SQL arguments.
  Verification: `go test -timeout 30m ./...` exited 0 (52 packages passed); uncached focused tests in sqlite/mcp/provider/cmd also exited 0. A temporary production overlay replacing `stored.Provider` with empty made `TestConnectionRepo_LunaRouteUpdateRules` fail (exit 1), proving that input is load-bearing; repository source stayed intact.
  Failure-lens gaps G1 and G9 remain open: `domain/provider/lunaroute_connection.go:PrepareLunaRouteUpdate` still permits switching away while retaining a key; key-shape/query-error mutation coverage belongs to Review 2 and does not withhold this wiring approval.

## Closed

- [x] **Area 3 / single:3/review2 ticked by approver at 75b7d6a.** Hand mutants re-measured independently: `family.go:ClassifyFamily` 89 (`!=`→`==`), 98 (`lab==""`, `hint!=""`, `hint!=lab`) and 110 (`hint!=lab`) and `version.go:192` (lunaroute case) each fail `./domain/modelidentity` (98/hint!=lab via `family_test.go:TestClassifyFamily`; 110 via `family_internal_test.go:TestReviewedAliasVendorPrefixConsistency`). Restored tree: `go test ./domain/modelidentity ./domain/provider ./adapter/config ./adapter/mcp ./samples` ok; root ProfileSave/ProfileScope/AgentProfileSave/PiEffortEcho/DeferredBackendsSave wiring tests pass; no LunaRoute `t.Skip` in domain/provider or adapter/mcp.
- **[ESCALATION] Area 3 / single:3/review2 acceptance boundary resolved:** approval required contradictory/matching prefix and reviewed-alias fixtures at `domain/modelidentity/family_test.go:TestClassifyFamily` / `family_internal_test.go:TestReviewedAliasVendorPrefixConsistency`, seeded-mutation failures and all nine failure checks passing; this pass supplies them without a policy decision or external dependency.
- **G8 (Area 3) closed:** `family_test.go:TestClassifyFamily` and `family_internal_test.go:TestReviewedAliasVendorPrefixConsistency` kill family.go:89/98/110 negations; existing version tests kill version.go:192, with restored-code targeted tests passing.

- **G7 (Area 3) closed:** `TestLunaRouteMixedRouteBackendsPreserveAxes` kills the backend negation; `TestLoadCapabilityRules_LunaRouteEmptyMapsReplaceKnownFacts` pins unknown semantics; five boundary survivors are equivalent for valid loaded tables (proof and overlay results above).
- **G5 (Area 2) closed:** `adapter/provider/lunaroute_agent_test.go:TestLunaRouteAgent_DiagnosisObservations` and `domain/provider/lunaroute_diagnosis_test.go:TestLunaRouteDiagnosis_NilOriginalAndObservationPrecedence` discriminate checked/observation/text/identity and nil-original branches; four corresponding branch/flag mutations fail assertions.
- **G2 (Area 2) closed:** `adapter/provider/dynamic_backend_explicit_test.go` (`TestDynamicProfileResolver_BackendExplicitProvenance`, `..._BackendProvenanceReachesLaunchGate`) drives the real `DynamicProfileResolver` on both arms into `CheckLunaRouteLaunch`; negating `dynamic.go:89` or `:124` now fails them (verified by hand-mutation).
- **G1 (Area 1) closed:** `domain/provider/lunaroute_connection.go:PrepareLunaRouteUpdate` requires a replacement on switch-away; `adapter/sqlite/connection_repo.go:Update` guards every retained-key write; red/green repo and real-root MCP tests prove refusal and concurrent-switch safety.
- **G9 (Area 1) closed:** embedded-space coverage added to key-shape/create/update tests; temporary whitespace and generated-query guard mutations both fail, with `TestConnectionRepo_LunaRoutePersistenceErrors` exercising meta-read and conditional-SQL errors.

- **W3-1 (Area 3, review 1) closed:** wiring checks 4 and 8 had no root/sample test. Added `cmd/arbitron/lunaroute_agent_wiring_test.go:TestLunaRoutePiEffortEchoWiring` (embedded table, real MCP root: pi `glm-5.3` none/low/high/max persist and echo `effective.effort` unfloored; `medium` refused, nothing persisted) and `samples/family_test.go:TestJudgePairingProblems` cases (LunaRoute `glm-5.3` doer vs direct `ollama/glm-5.1` judge fails; vs `deepseek-4.1-flash` passes) plus `TestModelFamily` rows for the three reviewed ids. Open G7/G8/G11 are boundary/failure-lens items, not wiring.

## Won't Fix

- **Dormant claude-code and opencode LunaRoute emitters. Escalated and decided by operator ruling R7 (2026-11-12).**
  `adapter/claudecli/lunaroute_env.go` and the LunaRoute branches of `adapter/opencode/*` stay in the tree but are refused
  at `domain/provider/lunaroute_validate.go:lunaRouteEnabledBackends`, and at `Factory.AgentProviderFor` and the
  interactive selector. Their emission is not refined. Area 2 verifies only that they are unreachable. Re-enabling one
  takes a gate change plus a live operator pass (LR-05/LR-06 are `deferred`).
  The survivors left unrefined are `harden:adapter/claudecli/streamjson.go`, `harden:adapter/claudecli/stream_agent_adapter.go`,
  `harden:adapter/opencode/config.go` and `harden:adapter/opencode/model_probe.go`.
- **Live verification and the full MCP acceptance matrix.** Ruling R6 makes these operator-run, and R7 descopes them:
  P4-T7's matrix, P2-T8's manual acceptance, the P3-T5 harness, and the forced-helper collectors from P2-T9 step 3 and
  P4-T1 steps 2–3. Agents hold no `lr_` key, so a skipped live test is expected and never counts as readiness.
- **Codex warns "Model metadata for glm-5.3 not found".** R7 records this as a known, non-fatal degradation.
- **Repository content outside the LunaRoute scope.** The squashed single-commit branch makes the whole repo look changed;
  see `RALPH.md` → Scope. Layout, dashboard, spawn, queue, teams and other features are not this branch's work.
