# Gaps identified — LunaRoute provider

> **Rule:**
> - Each record is at most ten lines.
> - When an item is closed, move it under `## Closed` with a one-line note.
> - The open list is what a pass reads, so keep it short.
> - An unfed or inert production path is never "acceptable". Fix it, or escalate it under `Won't Fix` with the ruling that
>   covers it.

## Open

- [ ] **G20 (blocking verification, Go suite at ddb27e4): baseline-green suite exits nonzero.**
  `go test -count=1 -timeout 30m ./...` exited 1: `github.com/arbitron/arbitron/test/lunaroute` failed at
  `test/lunaroute/codex_controls_test.go:TestCodexConfigControlsExperimentToPrivateSink/held-pipe` (line 136): query marker absent.
  Supplied `test-baseline.json` reports the Go suite green. The exact isolated subtest rerun exited 0; intermittency is observed,
  not proof of a change-caused defect. Its context expires after 500 ms, including fixture startup; investigate under suite load.
  Rule 1 blocks paired:1. Next pass needs a clean full Go suite or measured baseline-equivalence, not only this isolated rerun.

- [ ] **G17 (Area 8, sibling documentation): evidence README retains obsolete blockers.**
  `docs/evidence/lunaroute-provider/README.md:Released scope` says the legacy MCP-matrix assertion remains and deferred harness tests are red.
  `test/lunaroute/register_test.go:TestReleaseGate` has no matrix assertion; its explicit release-mode run passed, as did the ordinary evidence suite.
  Update both the introduction and `README.md:Release enforcement` to match the current gate and R7. This does not fail Review 1 checks 1–6.

- **single:8/review1 — wiring-lens evidence (recommend approve).**
  C1 `register.yaml` LR-01..08, LR-05/06 `deferred` empty applied_to; `TestRegisterDeferredScope`. C2 all 20 `applied_to` pointers grepped as real
  declarations; the helper matched by substring only, so a renamed target (`Probe`→`ProbeX`) kept a stale pointer green. Fixed:
  `test/lunaroute/evidence_helpers_test.go:declaresSymbol` (func/method/type/var/const or YAML key); new case `symbol only in a comment`
  in `TestRegisterRejectsBadEvidence` was red before, green after. C3 overlays on `capabilities.default.yaml` (anchor typo line 55, chat evidence ->
  docs page, agent evidence -> missing file) and `version.go:lunaRouteReviewedIDs` anchor typo each failed `TestProductionCapabilitySnapshot`/
  `TestProductionCapabilityEvidence`/`TestProductionR7CompletionApplication`/`TestLunaRouteProductionIdentityEvidence`. C4 operator observation pinned by
  `TestProductionR7CompletionApplication`. C5 examples codex/pi only. C6 `TestProductionR7*` run in the ordinary suite; flipping LR-04 to pending and a stale pointer both fail them.
  Verified: `go test -count=1 ./test/lunaroute/ ./domain/modelidentity/ ./adapter/mcp/` ok.
  Recommend: approve single:8/review1.

- **[RESOLVED at approval of d9544b4] Area 7 / single:7/review2 — APPROVED (independent failure review).**
  Ran `go test ./domain/provider/... ./adapter/probe/... ./adapter/mcp/... ./cmd/arbitron/... ./domain/modelidentity/...`; all passed, `go build ./...` clean.
  Mutant 1: disabling the `ConfigRevision` comparison in `domain/provider/probe.go:ProbeBindingFromConnection` made 10/12 `TestLunaRouteDiscovery_ConcurrentConnectionChange` cases fail (both surfaces). The 2 delete cases still pass because Get fails.
  Mutant 2: changing `>= 300` to `> 300` in `adapter/probe/lunaroute.go:Probe` made `TestLunaRouteProbe_RedirectNotFollowed` fail, so G6 is killed. Both files were restored.
  C1 `TestDiscover_AuthzAndNotFound`, C2 `TestLunaRouteProbe_MalformedListingIsNeverAnEmptyCatalog`/`_OversizedBody`/`_EmptyListIsReachable`, C4 `TestConnectionModels_LogsNeverEchoUnclassifiedErrors`, C5 `TestDiscover_LunaRouteView` (table-only id excluded, unlisted ids unknown), C6 `TestDiscover_RevokedAliasFamily`.

- **[RESOLVED at approval of b03188d] Area 6 / single:6/review2 — G3 boundary fixtures supplied and independently mutation-verified.**

- **[RESOLVED at approval of d11128a] Area 5 / single:5/review2 — exact remaining approval conditions.**
  Per the approver at 868c0b0, the missing fixture was a direct boundary table for
  `adapter/pi/model_probe.go:safeModelIdentity`; this pass supplies `TestSafeModelIdentity_EachClause`.
  To tick this box, independent review must verify rejection cases and mutation failures for each nonredundant guard,
  and accept a reason for each equivalent removal. No external credential, dependency or operator decision is required.
  G15 remains a separate cleanup-error defect, explicitly ruled non-blocking for this review by that approver.
- [ ] **G15 (Area 5): private-directory removal errors are discarded.**
  `adapter/pi/lunaroute.go:prepareLunaRoutePiLaunchWithFS` defines `cleanup := func() { _ = fs.removeAll(dir) }`;
  both preparation rollback and `adapter/pi/provider.go:RunAgent`'s deferred cleanup lose failures.
  Temporary overlay `TestArea5RemovalFailureSurfaced` injected chmod plus removal failures: exit 1, removal error absent
  from the local error chain and directory still present. Overlay removed; fixture directories cleaned by `t.TempDir`.
  Preserve secret-free cleanup failure alongside the primary error and cover removal failure after otherwise successful runs.

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

- [ ] **G11 (Areas 3, 7): text survivors need a decision — Area 7 half pinned, Area 3 half open.**
  Area 7 done (single:7/review1): per-segment phrases pinned in `adapter/mcp/tools_connection_models_test.go:TestConnectionModels_DescriptionGuidesTheLLM`
  and `domain/provider/model_discovery_test.go:TestDiscover_LunaRouteView`. Still open: `harden:adapter/mcp/lunaroute_backend_hint.go`
  (Area 3: pin "codex or pi only", "pinned only", or record as equivalent). Area 7 independently verified at approval of 28da84b: all eight single-segment-removal overlays fail the named tests.

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

- **paired:1 — WITHHELD at ddb27e4 solely by the Go suite gate (G20); both seam directions independently verified.**
  SHARED: `cmd/arbitron/main.go:initDomainWithModelDependencies` loads once and feeds ProfileService and Factory;
  `adapter/provider/factory.go:agentProviderFor` passes the same rules into pi. No production table replacement was found.
  Save→launch: `domain/provider/profile_service.go:write` validates before persistence through `selection.go:checkLunaRouteProfile`
  and `domain/provider/lunaroute_validate.go:checkLunaRouteRoute`; `adapter/provider/dynamic.go:Resolve` preserves effort.
  Launch→save: `adapter/pi/lunaroute.go:prepareLunaRoutePiLaunchWithFS` rechecks that rule before reveal/files;
  `adapter/pi/lunaroute.go:lunaRouteModelsJSON` emits table limits/reasoning and `adapter/pi/args.go:thinkingLevel` preserves levels.
  TRACE `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiWiringEffortReachesArgv` passes (4096; none/low/high, real child).
  Independent overlays removing the reasoning guard, maxTokens←context, low→off, or flipping reasoning each exit 1; sources untouched.
  Targeted domain/pi/root tests and `go build ./...` exit 0; full Go exits 1 (G20). No direction/BREAK evidence remains missing.

- **paired:1 — Area 3 save ↔ Area 5 pi launch evidence (recommend approve).**
  SHARED: `main.go:810` one `capabilityRules` → `NewProfileService` (:994) and factory Deps → `factory.go:356` `pi.Config.CapabilityRules`
  → `pi/provider.go:154/168`; save and launch both call `lunaroute_validate.go:checkLunaRouteRoute`; rules load once, no drift.
  A→B: save's exact effort/limits/reasoning now imply pi's emission (G19 closed). B→A: pi trusts no default — `lunaRouteModelsJSON`
  refuses unknown facts, `args.go:thinkingLevel` is lossless. Root TRACE: `TestLunaRoutePiWiringEffortReachesArgv` (override output
  4096, efforts none/low/high, child checks models.json + argv). Mutants killed: guard removed, maxTokens←context, low→off.

- **Area 8 / single:8/review2 — APPROVED at 7847169 (independent failure review).**
  C3 now holds: `evidence_helpers_test.go:unsafeManualObservation` replaces the credential-only scan for the R7 note; redacted note keeps every result row.
  Restoring the HEAD~1 note (with the prompt) fails `TestReleaseGate` (release mode) and always-on `TestProductionR7RegisterApplication` (LR-02/07/08 unsafe).
  Swapping the guard back to `unsafeText` turns all 11 `TestReleaseGateRejectsManualPromptPayloads` subtests red. Sources restored.
  C1/C2/C4 unchanged since the 6523e3c review; `go test ./test/lunaroute/`, release-gate run and MCP `LunaRoute|AuthoringGuidance|Initialize` all pass.
  G17 remains an open sibling documentation gap.

- **Area 8 / single:8/review2 — failure-lens verification after G18 repair.**
  C1 `evidence_helpers_test.go:releaseProblems`: required LR-01–04/07/08 remain verified-only; `TestRegisterDeferredScope` excludes only LR-05/06. Real LR-04 pending/escalated/deferred mutations each fail explicit release mode.
  C2 `register_test.go:TestRegisterRejectsBadEvidence` covers absent/empty/unreadable/unsafe evidence; a real capability-anchor mutation fails `TestProductionCapabilityEvidence`; `TestLunaRouteProductionIdentityEvidence` passes.
  C3 `evidence_helpers_test.go:unsafeManualObservation` rejects quoted/fenced/indented or labeled prompt/body/message payloads after decoding, alongside credentials; errors retain fixed labels only.
  `register_test.go:TestReleaseGateRejectsManualPromptPayloads` was red for all 11 formats, then green before/after the genuine note; restoring its original prompt fails `TestReleaseGate`, and removing the guard makes the new fixture red.
  `TestObservationBoundaryRedaction`/`TestObservationPrivateSink` pass: rejected payloads cause zero writes, private/exclusive publication refuses overwrites and symlinks. The gate reads local evidence synchronously; live timeout/helper collectors remain deferred by R7.
  C4 `docs_scope_test.go:TestLunaRouteAuthorSurfacesRejectContradictoryProse` passes all 18 controls; contradictory claims injected into each real guide/instructions/hint input fail `TestLunaRouteAuthorSurfacesR7`.
  Verified targeted `go test -count=1 -timeout 30m` runs for release/register/observation/production/docs tests, MCP hints/initialize guidance, identity evidence, and explicit `ARBITRON_LUNAROUTE_RELEASE_GATE=1` `TestReleaseGate`; all pass. Nine mutation controls rejected; sources restored.
  Recommend: approve single:8/review2 — all four failure checks pass, including real prompt reinsertion and guard-removal controls; G17 remains a sibling documentation gap.

- **Area 8 / single:8/review2 — WITHHELD at 6523e3c (independent failure review).**
  Re-ran `go test -count=1 ./test/lunaroute/` and MCP `LunaRoute|AuthoringGuidance|Initialize` (both pass). C1, C2, C4 accepted: `docs_scope_test.go:validateLunaRouteAuthorScope` whitelists exact refusal clauses, and `TestLunaRouteAuthorSurfacesRejectContradictoryProse` mutates the real guide/instructions/hint inputs.
  C3 fails: `observations/operator-manual-agent-tasks-2026-11-12.md` line 7 holds the verbatim task prompt, and `evidence_helpers_test.go:validateRegisterAt` checks that note only with `unsafeText` (credentials, not prompts) — G18 confirmed.
  The "[ESCALATION]" label on G18 is wrong: no human ruling is needed. To tick next time: redact the prompt (keep the result table), add a Markdown prompt rejection to `validateRegisterAt`, and add a negative fixture that goes red when the prompt is re-added.

- **Area 8 / single:8/review2 — failure-lens review; no approval.**
  C1 `register_test.go:TestReleaseGateRejectsPending`/`TestRegisterDeferredScope` pass; explicit-gate LR-04 pending/escalated/deferred overlays each fail; LR-05/06 stay excluded.
  C2 `TestRegisterRejectsBadEvidence` covers missing/empty/unreadable/symlink evidence; a real `capabilities.default.yaml` wrong-anchor overlay fails `TestProductionCapabilityEvidence`.
  C3 `TestObservationBoundaryRedaction`/`TestObservationPrivateSink` pass (zero writes on rejection, exclusive private sink); Markdown prompt rejection is missing (G18).
  C4 `docs_scope_test.go:TestLunaRouteAuthorSurfacesRejectContradictoryProse` was red with the no-op validator, then green for 18 mutations; real guide/instructions/hint support-claim overlays each fail `TestLunaRouteAuthorSurfacesR7`.
  Verified `go test -count=1 -timeout 30m ./test/lunaroute -run 'Test(Register|VerifiedEvidenceReferences|ReleaseGate|Observation|ProductionR7|ProductionCapability|ProductionIdentity|ProductionGateway|ProductionDiscovery|LunaRouteGuide|LunaRouteDoc|ParseLunaRouteDoc|LunaRouteAuthorSurfaces)'` (pass).
  Verified MCP `TestLunaRouteBackendHint*`, `TestAuthoringGuidance_ModelDiscoveryAndExplicitSelection`, `TestServer_InitializeCarriesAuthoringInstructions` and explicit `ARBITRON_LUNAROUTE_RELEASE_GATE=1` `TestReleaseGate` (pass); all overlays restored.
  Recommend: no approval for single:8/review2 — G18 allows raw prompt text in accepted operator Markdown evidence; redact and add a release-validator rejection fixture.

- **Area 8 / single:8/review1 — APPROVED at bf04b3f (independent wiring review).**
  C1 `test/lunaroute/register_test.go:TestRegisterDeferredScope` and the checked-in register confirm exactly eight items, only LR-05/06 deferred.
  C2 `test/lunaroute/evidence_helpers_test.go:validateRegisterAt`: independently grepped all 21 application pointers as declarations; reverting the fix makes `TestRegisterRejectsBadEvidence/symbol_only_in_a_comment` fail.
  C3 `test/lunaroute/production_evidence_test.go:TestProductionCapabilitySnapshot`/`TestProductionCapabilityEvidence` and `domain/modelidentity/version_lunaroute_internal_test.go:TestLunaRouteProductionIdentityEvidence` consume all 19 capability refs and three identity anchors; broken-anchor overlays fail both consumers.
  C4 `test/lunaroute/production_release_test.go:TestProductionR7CompletionApplication` binds supported facts to committed operator observations; docs-page agent evidence and empty embedded models each turn it red. Real loader feed: `cmd/arbitron/main.go:initDomainWithModelDependencies` calls `LoadCapabilityRules` and `NewModelDiscoveryService`.
  C5 `test/lunaroute/docs_examples_test.go:TestLunaRouteGuideExamplesAreValid` parses chat/codex/pi examples; `adapter/mcp/lunaroute_backend_hint_test.go:TestLunaRouteBackendHint_ReachesProfileTools` checks tools/list, and `adapter/mcp/server.go:NewServer` installs `AuthoringGuidance`.
  C6 `test/lunaroute/production_release_test.go:TestProductionR7RegisterApplication` fails for stale application and pending LR-04; the explicit `ARBITRON_LUNAROUTE_RELEASE_GATE=1` `TestReleaseGate` passes without the descoped matrix.
  Verification: `go test -count=1 -timeout 30m ./test/lunaroute ./adapter/mcp ./adapter/config ./domain/modelidentity`, explicit gate, and `go build ./...` all exit 0; seven negative controls exit 1, then all inputs restored. Happy/success, failure, read-error and edge cases run in these suites, including `TestObservationBoundaryRedaction`/`TestObservationPrivateSink`.
  Only Review 1 ticked; G16 belongs to Review 2, and G17 is a separate stale-documentation gap.

- **Area 7 / single:7/review2 — failure-lens checks.**
  C1: `domain/provider/model_discovery_test.go:TestDiscover_AuthzAndNotFound` verifies forbidden/unauthenticated before any store or selector read, and unknown connection before probing; `TestDiscover_NilPortsAreWiringFaults` covers missing dependencies.
  C2: `adapter/probe/lunaroute_test.go:TestLunaRouteProbe_MalformedListingIsNeverAnEmptyCatalog`, `OversizedBody`, `ReadDiagnosticsDropIncompleteBodies` and `EmptyListIsReachable` distinguish invalid/partial/oversized responses from a reachable empty list; valid rows followed by malformed rows return no partial catalog.
  C3: `adapter/probe/lunaroute_test.go:TestLunaRouteProbe_RedirectNotFollowed` covers 300/302/307 with valid-looking catalogs and exactly one request; `CancellationAndTimeout` preserves cancellation/deadline identity.
  C4: `adapter/mcp/tools_connection_models_test.go:TestConnectionModels_LogsNeverEchoUnclassifiedErrors` verifies generic output and a connection-name-only log; `domain/provider/model_discovery_test.go:TestDiscover_RevealFailureDoesNotDiscloseReturnedKey` pins exact-secret redaction when Reveal returns both key and error.
  C5: `domain/provider/model_discovery_test.go:TestDiscover_LunaRouteView` verifies sorting/deduplication, exclusion of table-only models and unknown facts for listed-only models; `NonLunaRouteProviderIsAllUnknown` and `ListingUnsupported` cover folded contracts.
  C6: `domain/provider/model_discovery_test.go:TestDiscover_RevokedAliasFamily` now checks both contradictory vendor prefixes, Claude-shaped/opaque aliases and `glm-5.3` at the discovery view sink.
  Concurrency: `domain/provider/probe.go:ProbeBindingFromConnection` checks the production `Connection.ConfigRevision` after Reveal; repository-wide `grep -rn 'ProbeBindingFromConnection' --include='*.go' .` finds real callers in `model_discovery.go:Discover` and `cmd/arbitron/connection.go:bindingFromConnection`.

- **Area 7 / single:7/review2 — verification.**
  Before edits, targeted discovery/probe/MCP/CLI tests passed in all four packages, with no baseline failure found.
  `cmd/arbitron/lunaroute_discovery_concurrency_test.go:TestLunaRouteDiscovery_ConcurrentConnectionChange` failed before implementation: switch-to-LunaRoute returned a successful catalog through the stale foreign route; all twelve cases now reject with no HTTP and no success output. The returned-key reveal-error regression was also observed red before its redaction fix.
  `go test -timeout 30m ./domain/provider ./adapter/probe ./adapter/mcp ./cmd/arbitron -run 'Test(Discover_|LunaRouteProbe_|ConnectionModels_|LunaRouteDiscovery|RunConnectionValidate|ConnectionValidate|BindingFromConnection|FormatProbeResult)' -count=1` passed (four packages).
  `go test -timeout 30m ./adapter/sqlite -run '^TestConnectionExecutionRevision' -count=1` passed, including concurrent increments and deletion/recreation revision monotonicity used by the guard.
  `go test -race -timeout 30m ./cmd/arbitron -run '^TestLunaRouteDiscovery_ConcurrentConnectionChange$' -count=1` passed after the final code change (29.533s).

Recommend: approve single:7/review2 — all six failure checks pass; `domain/provider/probe.go:ProbeBindingFromConnection` has two production callers confirmed by grep, and `TestLunaRouteDiscovery_ConcurrentConnectionChange` was red before the fix and now passes all twelve real-SQLite cases with zero HTTP; targeted discovery/probe/MCP/CLI, revision and race checks pass.

- **Area 7 / single:7/review1 — APPROVED at 28da84b (independent wiring review).**
  C1: `cmd/arbitron/main.go:initDomainWithModelDependencies` feeds one shared-client ProbeFactory into discovery and Factory.Probes; `mcpDeps` and `adapter/mcp/tools_connection.go:RegisterConnectionTools` expose the tool. `cmd/arbitron/lunaroute_discovery_wiring_test.go:TestLunaRouteDiscoveryWiring` checks the actual HTTP and MCP JSON sinks.
  C2: `domain/provider/model_discovery.go:Discover` gates before Get/Probe and wraps the stored binding with GuardLunaRouteReveal; `domain/provider/model_discovery_test.go:TestDiscover_AuthzAndNotFound` checks zero store/selector reads, and `TestDiscover_GuardAppliesToDiscoveryBinding` checks the guard.
  C3: `adapter/probe/lunaroute.go:Probe` sends exactly GET https://gw.lunaroute.com/v1/models with the revealed Bearer key through NoRedirectClient, feeding Code/RetryAfter/Secrets to ClassifyLunaRoute; `adapter/probe/lunaroute_test.go:TestLunaRouteProbe_ListsModels`, `TestLunaRouteProbe_VerifiedCodeFromHTTP` and `TestLunaRouteProbe_StatusClassification` exercise these inputs.
  C4: `cmd/arbitron/main.go:initDomainWithModelDependencies` loads capabilityRules through `adapter/config/capabilities.go:LoadCapabilityRules`; `cmd/arbitron/lunaroute_discovery_wiring_test.go:TestLunaRouteDiscoveryWiringEmbeddedDefaults` checks embedded limits, evidence and known/unknown facts at MCP JSON.
  C5: `cmd/arbitron/connection.go:bindingFromConnection` guards the stored binding; `cmd/arbitron/lunaroute_cli_validate_test.go:TestConnectionValidate_LunaRouteProductionWiring` exercises Cobra -> encrypted SQLite -> real factory -> HTTP/output; `lunaroute_cli_binding_test.go:TestBindingFromConnection_GuardsLunaRoute` verifies the guard.
  C6: `domain/provider/model_discovery.go:Discover` calls `domain/modelidentity/family.go:ClassifyFamily(route,id)` independently of listing/capabilities; `domain/provider/model_discovery_test.go:TestDiscover_LunaRouteView` and `TestDiscover_RevokedAliasFamily` verify known and unresolved vendors.
  Folded/scenarios: `domain/provider/model_discovery_test.go:TestDiscover_NonLunaRouteProviderIsAllUnknown`, `TestDiscover_ListingUnsupported`, `TestDiscover_EmptyListing`, `TestDiscover_ProbeFailurePropagates` and `adapter/mcp/tools_connection_models_test.go:TestConnectionModels_LogsNeverEchoUnclassifiedErrors` cover unknown/unsupported, edge, failure and error paths alongside root success. No wiring gap found; Area 3 G11 remains separate.
  Verification: `go build ./...` and `go test -count=1 ./domain/provider ./domain/modelidentity ./adapter/probe ./adapter/mcp ./cmd/arbitron ./test/lunaroute` all exit 0 (CLI package 321s); supplied measurement fields were unresolved, so these are fresh local results.
  Independent red checks: temporary Go overlays removing each of five connectionModelsDescription segments and three DiscoveryNote segments fail `TestConnectionModels_DescriptionGuidesTheLLM` / `TestDiscover_LunaRouteView`; >=300 -> >300 fails `TestLunaRouteProbe_RedirectNotFollowed`; a nil root capability feed fails `TestLunaRouteDiscoveryWiring`. All ten exit 1 on assertions; overlays removed, production files unchanged.

- **Area 6 / single:6/review2 — approver APPROVED at b03188d (independent review).**
  Ran `go build ./...` and `go test -count=1 ./domain/provider ./adapter/openrouter ./adapter/provider ./domain/modelidentity ./cmd/arbitron`: all ok.
  Re-applied mutants myself: `lunaroute_classify.go:RedactLunaRoute` `<=`→`<` fails `TestRedactLunaRoute_ByteLimit` (panic at s[256]);
  `NormalizeLunaRouteRetryAfter` `> '9'`→`>= '9'` fails `TestNormalizeLunaRouteRetryAfter/9` and `/19`. Reverted; tree clean.
  C1–C3, C5–C7 accepted at 0bb44e3; G3 equivalences (L118, L141, L229) accepted. All seven failure checks now demonstrated.

- **Area 6 / single:6/review2 — G3 boundary audit and final failure assessment.** Pre-change targeted domain/provider and adapter/provider tests passed; no baseline failure.
  C1: `domain/provider/lunaroute_classify_internal_test.go:TestLunaRouteDocumentedCodeTable` and `lunaroute_classify_test.go:TestClassifyLunaRoute_StatusMapping` prove code/status precedence and exactly one gateway sentinel.
  C2–C3: `adapter/provider/lunaroute_chat_stream_failure_test.go:TestLunaRouteChat_StreamFailureBoundaries` and `adapter/openrouter/hooks_test.go:TestClassifyError_ChatReadAndDecode`/`NoPartialSecretAtBodyLimit` pass; partial output terminates once and incomplete bodies never become diagnostic text.
  C4: new `domain/provider/lunaroute_classify_test.go:TestRedactLunaRoute_ByteLimit` preserves 256 bytes and truncates 257; `TestNormalizeLunaRouteRetryAfter` preserves 9 and 19. Both recorded mutants passed before these fixtures; temporary Go overlays now exit 1 (256-byte panic; 9/19 become empty). Overlays removed; production unchanged.
  C5–C7: `adapter/provider/lunaroute_chat_policy_test.go:TestLunaRouteChat_RequestPolicyJSONAndStream`/`FixedIdentityJSONAndStream` and existing preflight tests pass in both modes: zero HTTP/reveals on policy refusal, absent/different identities fail before content, accepted body fields equal validated settings.
  Fresh `go test -race -count=1 -timeout 30m ./domain/provider ./adapter/openrouter ./adapter/provider -run 'ClassifyLunaRoute|RedactLunaRoute|NormalizeLunaRouteRetryAfter|LunaRouteErrorCode|LunaRouteDocumentedCodeTable|LunaRouteCodesAreOperationScoped|CheckLunaRoute|ClassifyError|AcceptExecutedModel|Preflight|^TestLunaRouteChat_'` passes all three packages, including cancellation/deadlines and concurrent credential rotation.
  Production `rg -n 'ClassifyError:|Preflight:|AcceptExecutedModel:' adapter --glob '*.go' --glob '!**/*_test.go'` finds all three hooks at `adapter/provider/lunaroute_chat.go:lunaRouteChat`; no new exported collaborator/config. Five categories: happy completion, successful SSE, gateway refusal, transport/read error, boundary/empty inputs.
  Prior G3 equivalences retained: L118 capacity-only allocation; L141 `cut >= 0` cannot overrun because scrubbed UTF-8 has a rune start at index 0; L229 empty-code map lookup is nil. Other cited classifier survivors were killed and accepted at 0bb44e3. Approval boxes untouched.
Recommend: approve single:6/review2 — all seven failure checks pass; the last two G3 mutants now fail discriminating boundary fixtures, and targeted race checks pass.

- **Area 6 / single:6/review2 — approver WITHHELD at 0bb44e3 (independent review).**
  Ran `go build ./...` and `go test -count=1 ./adapter/openrouter ./adapter/provider ./domain/provider ./domain/modelidentity ./cmd/arbitron`: all pass.
  C7 now demonstrated: `adapter/provider/lunaroute_chat_policy_test.go:TestLunaRouteChat_RequestPolicyJSONAndStream` (both modes, zero reqs, zero reveals on refusal, exact body on accept); G17 closure accepted. C6 reinforced by `TestLunaRouteChat_FixedIdentityJSONAndStream`.
  C1–C3, C5 stand as accepted at 28b8bd2. **C4 FAILS** only on two G3 survivors (L137 256-byte boundary, L189 digit 9) — see G3 for the exact fixtures; other cited survivors are killed or accepted equivalent.
  Next pass: add those two table rows to `domain/provider/lunaroute_classify_test.go`; nothing else is needed for this box.

- **Area 6 / single:6/review2 — G17 offline fixture closure.** Pre-change `go test -count=1 -timeout 30m ./adapter/provider -run '^TestLunaRouteChat_'` passes; no baseline failure.
  C1/C4: classifier/redaction/code/Retry-After targeted tests pass; G3's per-survivor boundary proof remains outstanding.
  C2/C3: `adapter/provider/lunaroute_chat_stream_failure_test.go:TestLunaRouteChat_StreamFailureBoundaries` and `adapter/openrouter/hooks_test.go:TestClassifyError_ChatReadAndDecode`/`NoPartialSecretAtBodyLimit` pass, including cancellation, deadlines and credential rotation coverage.
  C5–C7: `adapter/provider/lunaroute_chat_policy_test.go:TestLunaRouteChat_RequestPolicyJSONAndStream` loads YAML output limit 1000 and independent schema/temperature policy; both modes refuse schema/MaxTokens 2000 before reveal and HTTP, and emit exact accepted `response_format`, temperature and tokens.
  `TestLunaRouteChat_FixedIdentityJSONAndStream` selects pinned glm-5.3; absent/different ids yield nil JSON response or exactly one SSE error, no token/Done/retry; exact-id controls succeed.
  Temporary Go overlays emptying settings/MaxTokens/capabilities, omitting schema, changing temperature/tokens or disabling identity verification each fail the new fixtures (exit 1); overlays removed, production unchanged.
  Fresh `go test -race -count=1 -timeout 30m ./adapter/provider -run '^TestLunaRouteChat_'` passes; targeted `./domain/provider` classifier tests and `./adapter/openrouter` ClassifyError/AcceptExecutedModel/Preflight tests pass. Five categories include accepted schemas, successful streams, policy refusal, identity error and omitted-setting/limit edges.
  `rg -n 'ClassifyError:|Preflight:|AcceptExecutedModel:' adapter --glob '*.go' --glob '!**/*_test.go'` still finds the three production hooks at `adapter/provider/lunaroute_chat.go:lunaRouteChat`; no new collaborator/config was introduced.
Recommend: no approval for single:6/review2 — G17 is covered; G3 classifier survivor boundaries still require discriminating fixtures or accepted equivalence arguments.

- **Area 6 / single:6/review2 — approver WITHHELD at 28b8bd2 (independent review).**
  Ran `go build ./...` and `go test -count=1 ./adapter/openrouter ./adapter/provider ./domain/provider ./domain/modelidentity`: all pass; no regression.
  C1–C3, C5 demonstrated (`TestClassifyLunaRoute_StatusMapping`, `TestLunaRouteChat_Stream` frames, `TestLunaRouteChat_StreamFailureBoundaries`, `TestLunaRouteChat_PreflightRefusesBeforeIO` both modes, zero reqs). G4 and stream cancellation closures accepted.
  C6 is acceptable: JSON real-factory `TestLunaRouteChat_ExecutionEvidence` (drift + absent), SSE `hooks_test.go:TestAcceptExecutedModel_ChatAndStream` (drift/empty → no Done, empty output), exact-match `glm-5.3` FixedTarget in `modelidentity/version.go:LunaRouteExecutedModelAccepted`.
  **C7 FAILS:** `TestLunaRouteChat_RequestSettings` refuses `output_schema` only via `Chat`; there is no `StreamChat` schema-refusal case and no accepted case
  captures `response_format`/`temperature`/`max_tokens` to compare with what `lunaroute_chat.go:lunaRouteChat` Preflight inspected (no LunaRoute test mentions `response_format`).
  Next pass: add G17's table, using both modes, output limit 1000 + MaxTokens 2000 + unsupported schema (zero reqs), and accepted schema/temperature cases asserting captured body fields.
  C4/G3 is still open, as the refiner reported: no per-boundary fixture yet for Retry-After digits, snippet cut or transport precedence (`TestNormalizeLunaRouteRetryAfter` exists; I did not mutation-test it).

- **Area 6 / single:6/review2 — failure checks and bounded recommendation.**
  C1: `domain/provider/lunaroute_classify_test.go:TestClassifyLunaRoute_StatusMapping`/`RetryGuidance` and adapter classified/header tests pass; code overrides status and refusal omits retry guidance.
  C2: new `adapter/provider/lunaroute_chat_stream_failure_test.go:TestLunaRouteChat_StreamFailureBoundaries` proves empty frames and mid-stream errors terminate once, with no Done/retry/later content or gateway prose; G4 closed.
  C3: `adapter/openrouter/provider.go:statusError` discards incomplete/oversized bodies; `hooks_test.go:TestClassifyError_ChatReadAndDecode`/`NoPartialSecretAtBodyLimit` cover real reads, unlike the fake-only root body-read fixture.
  C4: classifier transport/TLS/deadline tests pass; new `TestLunaRouteChat_StreamCancellationOutcome` was red (zero errors) for cancellation/deadlines/backpressure and is green after terminal delivery repair. G3 mutation boundaries remain unproved.
  C5: `lunaroute_chat_test.go:TestLunaRouteChat_PreflightRefusesBeforeIO`/`EmptyTableDoesNotAuthorizeEffort` pass in both modes, with zero HTTP calls and preflight before reveal. C6–7: G17 records exact missing factory-level fixtures.
  Production grep `rg -n 'ClassifyError:|Preflight:|AcceptExecutedModel:' adapter --glob '*.go' --glob '!**/*_test.go'` finds all three at `adapter/provider/lunaroute_chat.go:lunaRouteChat`; no new exported collaborator was introduced.
  Targeted race runs: `go test -race -count=10 -timeout 30m ./adapter/provider -run '^TestLunaRouteChat_Stream'` and `go test -race -count=1 -timeout 30m ./adapter/openrouter` pass; classifier targeted tests pass. No whole suites run.
  Five categories: happy/existing HappyPath, success/70 KiB SSE, failure/mid-stream refusal, error/cancellation and timeout, edge/empty frames and scanner ceiling. Temporary overlays negating/removing the empty guard, removing Scanner.Buffer, ignoring error frames and restoring old cancellation each fail; overlays removed.
Recommend: no approval for single:6/review2 — G3 classifier mutation boundaries and G17 JSON/SSE identity/request-policy fixtures remain incomplete; approval boxes untouched.

- **Area 6 / single:6/review1 — approver TICKED at 4d5c1e6 (independent review).**
  C1: `cmd/arbitron/main.go:initDomainWithModelDependencies` wires the loaded rules, factory and pinning resolver through `cmd/arbitron/model_consumers.go:newRunModelConsumers`; `adapter/provider/dynamic.go:Resolve` guards the stored binding. `cmd/arbitron/lunaroute_chat_wiring_test.go:TestLunaRouteChatWiring` proves exact /v1 URL, credential, prompt, axes and persisted output/model.
  C2: `adapter/provider/lunaroute_chat.go:lunaRouteChat` forwards real status/body/transport/header/secrets and parsed code; `adapter/openrouter/hooks_test.go:TestClassifyError_ChatStatus` checks the HTTP producer; `adapter/provider/lunaroute_chat_test.go:TestLunaRouteChat_ClassifierReceivesHeaderStatusAndCode` checks the real classifier sink.
  C3: `adapter/provider/lunaroute_chat.go:lunaRouteChat` preflights actual MaxTokens and emitted setting names against root rules before reveal/I/O; `adapter/provider/lunaroute_chat_test.go:TestLunaRouteChat_PreflightRefusesBeforeIO` and `TestLunaRouteChat_RequestSettings` prove refusal and accepted emission.
  C4: `adapter/openrouter/request.go:newRequest` emits no OpenRouter headers; `adapter/procenv/httpx.go:NoRedirectClient` refuses redirects. `adapter/provider/lunaroute_chat_test.go:TestLunaRouteChat_HappyPath` and `TestLunaRouteChat_RedirectNotFollowed` check both.
  C5: `domain/provider/lunaroute_validate.go:checkLunaRouteRoute` requires exact axes; `cmd/arbitron/lunaroute_chat_wiring_test.go:TestLunaRouteChatWiringEffectiveAxesNeverFloor` and `TestLunaRouteChatWiringMissingEffortFactsRefuse` demonstrate high stays high and absent/unsupported facts refuse.
  C6: `adapter/provider/lunaroute_chat.go:lunaRouteChat` injects `domain/modelidentity/version.go:LunaRouteExecutedModelAccepted`; `adapter/openrouter/provider.go:Chat`/`StreamChat` call `verifyExecutedModel`, exercised in both modes by `adapter/openrouter/hooks_test.go:TestAcceptExecutedModel_ChatAndStream`.
  Five categories: happy = root wiring; success = adapter HappyPath/Stream; failure = ClassifiedErrors; error = ProtocolFailures; edge = PreflightRefusesBeforeIO/RedirectNotFollowed (all adapter tests in `adapter/provider/lunaroute_chat_test.go`). Strict gateway fakes compare complete request bodies before scripted responses.
  Fresh `go test -count=1 -timeout 30m ./...` exits 0 (cmd 350.639s); no affected-suite regression/build break. Own temporary Go overlays dropping RetryAfter, status, code, transport, rules, validated MaxTokens/settings, or executor prompt each exit 1 with assertion failures; no production files changed.
  G16 verified closed. G3/G4 remain Review-2 boundary questions, not wiring blockers; the fake-only gateway mutation/body-read tests do not establish production classifier coverage.

- **Area 5 / single:5/review2 — approver TICKED at d11128a (independent re-run).**
  Own overlays on `adapter/pi/model_probe.go:safeModelIdentity`, `go test -count=1 ./adapter/pi`: dropping nonempty, space or
  redaction clause, `return true ||`, or `<=`→`<` each exit 1 (killed by `lunaroute_identity_test.go:TestSafeModelIdentity_EachClause`).
  Length-only and control-only removals exit 0 — accepted as equivalent: `domain/provider/lunaroute_classify.go:RedactLunaRoute`
  maps controls to space and truncates at `lunaRouteSnippetMax`=256, so `RedactLunaRoute(s)==s` already rejects both.
  Uncached `go test -count=1 ./adapter/pi ./cmd/arbitron ./domain/provider ./domain/engine ./adapter/procenv`: all ok.
  Checks 1–6/8 undisputed since 868c0b0; check 7 now demonstrated. G15 stays open as a sibling (rule 5), not gating.

- **Area 5 / single:5/review2 — direct identity boundaries and mutation evidence.**
  `adapter/pi/lunaroute_identity_test.go:TestSafeModelIdentity_EachClause` calls the real predicate, independent of exact-model matching.
  21 cases cover empty/provider/model inputs, ASCII/UTF-8 256/257/258-byte boundaries, whitespace, controls and redacted auth text.
  Temporary `go test -overlay <json> -count=1 -timeout 30m ./adapter/pi -run '^TestSafeModelIdentity_EachClause$'` runs:
  removing nonempty, whitespace or redaction guards, always accepting, and excluding exactly 256 bytes each fail (exit 1).
  Removing length alone is equivalent (exit 0): `domain/provider/lunaroute_classify.go:RedactLunaRoute` caps output at 256 bytes.
  Removing control alone is equivalent (exit 0): that same helper replaces every Unicode control before equality with input.
  All overlays were outside the worktree and removed; production source is unchanged. G15 remains open per the prior ruling.

- **Area 5 / single:5/review2 — failure-lens checks in order.**
  1. `adapter/pi/lunaroute_test.go:TestLunaRoutePi_RefusalsBeforeIO` refuses empty limits/unknown reasoning before reveal or mkdir.
  2. `TestLunaRoutePi_FilesystemFailures` injects mkdir/chmod/partial-write failures, asserts rollback and the local marker.
  3. `lunaroute_cancellation_test.go:TestLunaRoutePi_CancelDuringPreparation`, `CancelWithCleanExit` and `DeadlineCleansDirectory` pass.
  4. `TestLunaRoutePi_CleanupAndSafeErrors` preserves `ErrTurnFailed` after exit 0 and removes directories on errors/panic.
  5. `TestLunaRoutePi_ConcurrentDistinctDirs` passes under the race detector with distinct directories and no leftovers.
  6. `procenv/credential.go:CredentialEnv` starts from allowlisted `BaseEnv`; `TestLunaRoutePi_HappyPath` rejects ambient env/config.
  7. `TestSafeModelIdentity_EachClause` and the mutation evidence above cover every safety requirement without a matching-model veto.
  8. `model_probe_test.go:TestModelProbePiLunaRouteReportedModelEvidence` rejects empty/mismatched earlier evidence despite later matches;
  `TestLunaRoutePi_ReportedEvidenceEveryEntry` rejects foreign provider/model evidence; `NoUsabilityProbeAndDirectEnv` refuses probing.

- **Area 5 / single:5/review2 — completed targeted verification.**
  Before changes, `go test -count=1 -timeout 30m ./adapter/pi -run 'TestLunaRoutePi|TestModelProbePiLunaRoute|TestRunAgent'` passed (0.654s).
  After changes, the same command with `TestSafeModelIdentity_EachClause|` prepended passed (0.623s); no new failure observed.
  `go test -count=1 -timeout 30m ./cmd/arbitron -run '^TestLunaRoutePi(WiringMissingMetadata|WiringFailures)$'` passed (2.139s), including zero successful persisted nodes on failures.
  `go test -race -count=1 -timeout 30m ./adapter/pi -run '^TestLunaRoutePi_(CancelDuringPreparation|CancelWithCleanExit|DeadlineCleansDirectory|ConcurrentDistinctDirs)$'` passed (1.470s).
  `rg -n 'safeModelIdentity|prepareLunaRoutePiLaunchWithFS|ctx.Err\(\)|CredentialEnv|BaseEnv|IsBaseName|ErrTurnFailed|executedModels' adapter/pi adapter/procenv --glob '*.go' --glob '!**/*_test.go'` confirms fail-closed guards and env filtering.
  `TestLunaRoutePi_RefusalsBeforeIO`'s empty-rules case fails if production stops requiring metadata; happy/subprocess tests require the actual definition/env/argv.

Recommend: approve single:5/review2 — all eight failure checks pass; direct identity tests kill nonredundant guard mutations, with length/control equivalence documented.

- **Area 5 / single:5/review2 — approver withheld at 868c0b0 on check 7 only (G10 pi half).**
  Overlays on `adapter/pi/model_probe.go:safeModelIdentity`: dropping the nonempty, length, space, control or redaction clause,
  or `return true ||` for the whole function, all leave `go test -count=1 ./adapter/pi` and root `-run LunaRoutePi` at exit 0.
  `lunaroute_test.go:TestLunaRoutePi_ReportedEvidenceEveryEntry` cases are rejected by `LunaRouteExecutedModelAccepted` instead.
  Next pass: add `TestSafeModelIdentity_EachClause` calling `safeModelIdentity` directly (mirror codex
  `TestSafeLunaRouteIdentity_EachClause`), one case per clause; record any clause proven equivalent with the reason.
  Checks 1–6/8 not disputed: `./adapter/pi` passes uncached; new cancellation tests cover check 3 (`ctx.Err()` after write, clean exit).
  G15 (removal-error surfacing) is a sibling defect, not required by check 2. It does **not** gate this box (rule 5).

- **Area 5 / single:5/review2 — cancellation fix; approval withheld.**
  Baseline `go test -timeout 30m ./adapter/pi -run 'TestLunaRoutePi|TestModelProbePiLunaRoute' -count=1` passed (0.719s).
  Strict TDD: new cancellation tests failed at mkdir/chmod/write and clean probe/turn exits; all pass after context checks
  at `adapter/pi/lunaroute.go:prepareLunaRoutePiLaunchWithFS` and `adapter/pi/provider.go:RunAgent` (no success after cancel).
  Checks 1–6/8: targeted adapter tests plus `TestRunAgent` passed (0.740s); root `TestLunaRoutePiWiringMissingMetadata`
  and `TestLunaRoutePiWiringFailures` passed (2.141s), covering refusal, failed turn, wrong definition and cleanup.
  `go test -race -timeout 30m ./adapter/pi -run '^TestLunaRoutePi_(CancelDuringPreparation|CancelWithCleanExit|DeadlineCleansDirectory|ConcurrentDistinctDirs)$' -count=1` passed (1.528s).
  `rg -n 'safeModelIdentity|prepareLunaRoutePiLaunchWithFS|CredentialEnv|BaseEnv|IsBaseName|probeOnce' adapter/pi adapter/procenv --glob '*.go'`
  confirms allowlisted env construction and identity guards; check 7 remains G10, and removal-error injection exposes G15.
  Recommend: no approval for single:5/review2 — G15 removal failures remain silent and G10 pi guard boundaries lack isolated tests.

- **Area 5 / single:5/review1 — approved at refinement 9c8f26c.** All six wiring checks independently verified; no wiring gap found.
  Check 1: `cmd/arbitron/main.go:initDomainWithModelDependencies` loads real rules via `adapter/config/capabilities.go:LoadCapabilityRules`; `adapter/provider/factory.go:agentProviderFor` feeds them into pi. `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiWiring` checks the subprocess-read file against distinctive table limits for three models.
  Check 2: `adapter/pi/lunaroute.go:lunaRouteModelsJSON` writes the literal `$LUNAROUTE_API_KEY`; `prepareLunaRoutePiLaunchWithFS` injects the guarded key and private directory. `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiRootChild` checks actual file/env/argv, 0700/0600 permissions, cwd and stdin before emitting success.
  Check 3: `adapter/pi/args.go:thinkingLevel` maps none to off and other supported levels verbatim; `domain/provider/lunaroute_validate.go:checkLunaRouteRoute` validates exact support. `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiWiringEffortReachesArgv` saves none/high profiles and checks the real child argv.
  Checks 4/6: `adapter/pi/parse.go:foldMessage` supplies reported identities to `adapter/pi/model_probe.go:verifyExecutedModel` / `modelEvidence`, using `domain/modelidentity/version.go:LunaRouteExecutedModelAccepted`; `domain/engine/executor_agent.go:Execute` forwards them. `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiWiring` reads persisted SQLite evidence with source pi_message_end; selected bindings cannot supply it.
  Check 5: `adapter/pi/lunaroute_test.go:TestLunaRoutePi_NonLunaRouteUnchanged` passes and checks the existing argv/env path with no private directory.
  Folded: `adapter/pi/provider.go:ProbeExecutedModel` refuses LunaRoute; `adapter/pi/lunaroute.go:localError` / `prepareLunaRoutePiLaunchWithFS` mark local failures. `adapter/pi/lunaroute_test.go:TestLunaRoutePi_NoUsabilityProbeAndDirectEnv`, `TestLunaRoutePi_RefusalsBeforeIO` and `TestLunaRoutePi_FilesystemFailures` pass.
  Scenarios: root `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiWiring`, `TestLunaRoutePiWiringFailures`, `TestLunaRoutePiWiringMissingMetadata` cover happy/success/failure/error/edge at checked sinks; missing/drifting identity is covered by `adapter/pi/lunaroute_test.go:TestLunaRoutePi_ReportedEvidenceEveryEntry`. G10 remains a Review 2 gap.
  Temporary Go overlays: emptying factory pi rules fails all three root wiring cases; dropping none→off fails the new effort test (both exit 1). Unmodified root wiring/effort tests pass again (exit 0); production files were never edited.
  Verification (exit 0; tests uncached): `go build ./...`; `go test -count=1 -timeout 30m ./adapter/pi ./adapter/provider ./domain/provider ./domain/engine ./domain/modelidentity`; same flags for `./adapter/config ./adapter/procenv` and full `./cmd/arbitron` (323.257s). Unsubstituted measurement fields were replaced by these direct checks.

- **Area 4 / single:4/review2 — failure-lens evidence.** Baseline `go test -count=1 -timeout 30m ./adapter/codex -run 'LunaRoute|SafeLunaRoute'` passed before changes.
  Checks 1–2: `lunaroute_test.go:TestLunaRouteCodex_CredentialFailures`, `HomeFailures`, `MkdirFailureBeforeLaunch`, root `TestLunaRouteCodexWiringLocalFailureNeverProbes`, and `domain/provider/lunaroute_reveal_test.go:TestGuardLunaRouteReveal_NilRevealFailsClosed` verify no partial launch/home and local markers.
  Check 3: `lunaroute_lifecycle_unix_test.go:TestLunaRouteCodex_RealProcessCancellationCleanup` checks cancel/deadline against the production runner, stopped process, removed read-only home, nil result and preserved context identity.
  Checks 4–5/8: `lunaroute_test.go:TestLunaRouteCodex_PreflightBeforeIO`, `ExactEffort`, `EvidenceRequiredOnEverySuccess`, `FinalTurnMustComplete`, `lunaroute_identity_test.go:TestSafeLunaRouteIdentity_EachClause` and root `TestLunaRouteCodexWiringRejectsRolloutDrift` pass; empty/drift/unsafe evidence is refused.
  Checks 6–7: `lunaroute_test.go:TestLunaRouteCodex_ConcurrentHomes`, `ProcessErrorsSafeAndClean`, `VersionFailureIsLocalAndClean` pass; version env is key-free and stderr/errors/logs do not disclose the key.
  Cleanup regression: `lunaroute_cleanup_test.go:TestLunaRouteCodex_ReadOnlyHomeCleanup` failed on four exit paths before `lunaroute.go:removeLunaRouteHome`; permission repair now removes them without following operator symlinks. `CleanupFailureIsLocalAndPreservesCause` failed before `provider.go:RunAgent` joined cleanup errors; unrecoverable OS refusal now yields no successful result and never widens temp-parent permissions.
  Folded: `rollout.go:lunaRouteAfterTurn` verifies every identity before cleanup; `Provider.ProbeExecutedModel` refuses usability probes; `ensureLunaRouteCompatible` memoizes the key-free probe, matching `adapter/mcp/lunaroute_backend_hint.go:LunaRouteBackendHint`.
  Commands passed (exit 0): `go test -count=1 -timeout 30m ./adapter/codex` (5.154s); `go test -count=1 -timeout 30m ./cmd/arbitron -run '^TestLunaRouteCodexWiring'` (3.317s); `go test -count=1 -timeout 30m ./domain/provider -run '^TestGuardLunaRouteReveal'` (0.285s).
  Race command passed (2.685s): `go test -race -count=1 -timeout 30m ./adapter/codex -run 'TestLunaRouteCodex_(ConcurrentHomes|RealProcessCancellationCleanup|ReadOnlyHomeCleanup|CleanupFailureIsLocalAndPreservesCause)'`. Production `CredentialSpec.Reveal=nil` overlay fails `TestLunaRouteCodex_HappyPath` (exit 1); overlay removed. Grep confirms `prepareLunaRouteLaunch` → `removeLunaRouteHome` and deferred `RunAgent` cleanup; no new exported collaborator.
  Recommend: approve single:4/review2 — all eight failure checks, red/green cleanup regressions, real teardown, race and root sink tests pass; no approval boxes edited.

- **Area 4 / single:4/review1 — approved wiring evidence.** Checks 1–6 traced from production constructors to sinks.
  Check 1: `cmd/arbitron/main.go:initDomainWithModelDependencies` → `cmd/arbitron/model_consumers.go:newRunModelConsumers` feeds the real factory/rules; `adapter/provider/dynamic.go:Resolve` supplies stored endpoint/reveal, and `adapter/provider/factory.go:AgentProviderFor` guards the binding before codex construction.
  Checks 2–3: `adapter/codex/lunaroute.go:lunaRouteExecExtra` derives `/v1` once; `prepareLunaRouteLaunchWithFS` injects only the guarded key/private 0700 home. `adapter/codex/lunaroute_test.go:TestLunaRouteCodex_HappyPath` checks exact argv/env and cleanup at the runner.
  Checks 4/6: `adapter/codex/provider.go:RunAgent` verifies before deferred cleanup; `adapter/codex/rollout.go:lunaRouteAfterTurn` checks every observed model with `domain/modelidentity/version.go:LunaRouteExecutedModelAccepted`; `domain/engine/executor_agent.go:Execute` persists evidence.
  Sink proof: `cmd/arbitron/lunaroute_codex_wiring_test.go:TestLunaRouteCodexWiring` checks subprocess routing, untouched operator login, removed homes and persisted conversation for three models. Emptying factory codex rules or returned evidence independently makes all three cases fail (overlay exit 1); unmodified focused runs pass (exit 0).
  Check 5: `adapter/codex/lunaroute_test.go:TestLunaRouteCodex_SubscriptionUnchanged` asserts unchanged subscription argv/env and zero reveal.
  Folded: `adapter/codex/lunaroute.go:ensureLunaRouteCompatible` probes once without the key; `TestLunaRouteCodex_NoUsabilityProbe` requires ErrModelEvidence/no runner calls; `adapter/mcp/lunaroute_backend_hint.go:LunaRouteBackendHint` matches the ungated execution path.
  Scenarios: `TestLunaRouteCodex_HappyPath`/`RealProcessSink`, root `TestLunaRouteCodexWiringOpaqueFailure`/`LocalFailureNeverProbes`, and `TestLunaRouteCodex_EvidenceRequiredOnEverySuccess` cover happy/success/failure/error/edge with argument-checking sinks. G10's codex equivalents are documented above; pi remains Area 5.
  Verification: `go test -timeout 30m ./adapter/codex ./adapter/provider ./domain/engine ./cmd/arbitron` passes (exit 0; root 325.986s); uncached codex LunaRoute and root codex-wiring reruns also pass. Measurement placeholders were unresolved, so these commands supplied direct evidence.

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

- **G19 (paired:1, Area 3 ↔ Area 5): pi reasoning effort saved on a reasoning-unsupported model was silently dropped.**
  Save validated `effort ∈ efforts.pi` only; `adapter/pi/lunaroute.go:lunaRouteModelsJSON` then wrote `reasoning:false`,
  and pi clamps every `--thinking` to `off` for non-reasoning models (pi docs `extensions.md`, "non-reasoning models always
  use off"). Fixed in `domain/provider/lunaroute_validate.go:checkLunaRouteRoute` (shared by save and pi launch preflight):
  pi + reasoning `unsupported` + effort other than default/none → `ErrLunaRouteAxisUnsupported`. Tests:
  `TestCheckLunaRouteRoute_PiReasoningEffortNeedsReasoningModel` (red→green), root `TestLunaRoutePiWiringReasoningEffortNeedsReasoningModel`.
  The embedded table marks all three ids reasoning `supported`, so only override files could reach it.

- **G18 (Area 8):** redacted the operator task prompt while preserving every result row; `test/lunaroute/evidence_helpers_test.go:unsafeManualObservation` and `register_test.go:TestReleaseGateRejectsManualPromptPayloads` now reject reinsertion through the release validator (11 formats, red → green).

- **G16 (Area 8):** `test/lunaroute/docs_scope_test.go:TestLunaRouteAuthorSurfacesR7` rejects contradictory guide/MCP support claims; 18 mutation cases and all three real-source overlays verified, with no approval box changed.
- Area 7 discovery credential race: `domain/provider/probe.go:ProbeBindingFromConnection` now rejects revision drift after decryption for discovery and CLI; twelve real-SQLite interleavings prove zero outgoing requests and no partial success.

- G6 (Area 7): closed single:7/review1 — `adapter/probe/lunaroute_test.go:TestLunaRouteProbe_RedirectNotFollowed` now includes 300 with a valid body; `>= 300`→`> 300` mutant verified to fail it.

- [x] **G3 (Area 6):** `domain/provider/lunaroute_classify_test.go:TestRedactLunaRoute_ByteLimit` and `TestNormalizeLunaRouteRetryAfter` kill the exact-256-byte and digit-9 survivors (both exit 1); real-code targeted race tests pass.
- [x] **G17 (Area 6):** `adapter/provider/lunaroute_chat_policy_test.go:TestLunaRouteChat_RequestPolicyJSONAndStream`/`FixedIdentityJSONAndStream` prove loaded request policy and pinned identity at JSON/SSE sinks; seven production mutations fail.
- [x] **G4 (Area 6):** `adapter/provider/lunaroute_chat_stream_failure_test.go:TestLunaRouteChat_StreamFailureBoundaries`/`StreamFrameSizeBoundary` distinguish empty-frame, structured-error and scanner-buffer mutations through the real factory/classifier.
- [x] **Area 6 silent cancellation:** `adapter/openrouter/provider.go:StreamChat` retains one terminal error after cancellation/deadline, releases blocked consumers and closes the body; `TestLunaRouteChat_StreamCancellationOutcome` proves red/green with cause identity and secret-free errors.

- [x] **G10 (Areas 4, 5):** guard boundaries covered by `adapter/codex/lunaroute_identity_test.go:TestSafeLunaRouteIdentity_EachClause` and `adapter/pi/lunaroute_identity_test.go:TestSafeModelIdentity_EachClause`; redundant length/control removals are documented above.

- [x] **Area 5 cancellation boundaries:** `adapter/pi/lunaroute_cancellation_test.go` now proves rollback during reveal/mkdir/chmod/write, no mission after probe cancellation, no success after clean-exit cancellation, and cleanup on mission/default deadlines.

- [x] **G14 (Area 5, review1): root pi test never ran a non-default effort to argv.** Closed:
  `cmd/arbitron/lunaroute_pi_wiring_test.go:TestLunaRoutePiWiringEffortReachesArgv` saves a profile with effort
  `none`/`high`, runs it through the real engine and fake-pi subprocess; the child fails unless argv ends in
  `--thinking off`/`--thinking high` (mutating `thinkingLevel` none→"" fails it). Checks 1-6 otherwise verified:
  `factory.go` pi arm passes `Deps.CapabilityRules` (root `main.go:1076`); the models.json the child read equals the table;
  `$LUNAROUTE_API_KEY` literal, key only in child env; evidence from `pi_message_end` persisted at `executor_agent.go:128`.
  Still open for review2: G10 pi half (`safeModelIdentity` per-clause tests).
- [x] **Area 4 / single:4/review2 ticked by approver at 450a269.** The verify-tests placeholders were not substituted, so the approver ran the suites itself: `go build ./...`, `go test ./adapter/codex ./domain/provider ./domain/engine ./adapter/provider`, `./cmd/arbitron -run LunaRouteCodex` and `-race -count=3 ./adapter/codex -run TestLunaRouteCodex_` all pass.
  Hand mutants, each reverted: skipping the `lunaroute.go:removeLunaRouteHome` repair → `ReadOnlyHomeCleanup` and `RealProcessCancellationCleanup` fail; dropping the deferred `launch.cleanup()` in `provider.go:RunAgent` → `RealProcessCancellationCleanup` and `ProcessErrorsSafeAndClean` fail; swallowing `cleanupErr` → `CleanupFailureIsLocalAndPreservesCause` fails; `rollout.go:lunaRouteAfterTurn` checking selected-vs-selected → `EvidenceRequiredOnEverySuccess`, `TestRolloutLunaRouteReportedEvidence` and root `TestLunaRouteCodexWiringRejectsRolloutDrift` fail.
  Checks 1, 2 and 4–7 rely on the cited tests that already exist (`CredentialFailures`, `HomeFailures`, `MkdirFailureBeforeLaunch`, `ExactEffort`, `SafeLunaRouteIdentity_EachClause`, `ConcurrentHomes`, `VersionFailureIsLocalAndClean`), and every one of them passes.

- **[ESCALATION] Area 4 / single:4/review2 acceptance boundary resolved:** approval required read-only-home removal, visible irrecoverable cleanup errors, real cancel/deadline teardown fixtures and all eight failure checks; `adapter/codex/lunaroute.go:removeLunaRouteHome`, `provider.go:RunAgent` and the cleanup/lifecycle tests now supply them without an operator key or policy dependency.
- **Area 4 cleanup gap closed:** `lunaroute_cleanup_test.go:TestLunaRouteCodex_ReadOnlyHomeCleanup` and `TestLunaRouteCodex_CleanupFailureIsLocalAndPreservesCause` fail before the fix and pass afterward; private permission repair and joined local errors replace silently discarded deletion failures.

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

- **G16 (Area 6, review1, closed): `ClassifyError` closure's Retry-After/Status/Code inputs were unpinned.**
  `adapter/provider/lunaroute_chat.go:lunaRouteChat` mutants `RetryAfter: ""`, `Status: 0`, `Code: ""` all passed the
  suite (`TestLunaRouteChat_ClassifiedErrors` uses no header; the `openrouter` hook tests use a fake classifier). Added
  `TestLunaRouteChat_ClassifierReceivesHeaderStatusAndCode`; all three mutants now fail it (3/5/3 subtests red).
  Not for this lens: `TestLunaRouteGatewayRejectsMutatedInputs` (cmd) exercises the fake gateway, not production — it
  pins the fake's strictness only; Review 2 may weigh whether it should stay named as coverage for check 2.

Recommend: approve single:7/review1 — wiring verified: main.go:1030 probeFactory (shared httpClient) → NewModelDiscoveryService(main.go:1078) → ds.modelDiscovery in MCP deps (main.go:462); TestLunaRouteDiscoveryWiring drives the real root and asserts the sink (one GET https://gw.lunaroute.com/v1/models, Bearer lr_, no key in output, authz before gateway); embedded-defaults, CLI guard and probe tests pass. G6 closed (300 mutant killed); G11 Area 7 half pinned.
