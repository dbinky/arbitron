# Latest Model Profiles — Phase 3 Task 1: Canonical identity and immutable version evidence Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Canonical identity and immutable version evidence.

**Architecture:** Domain ports own contracts; adapters produce real inputs; the shared `cmd/arbitron/main.go:initDomain` composition root connects them. Execution order follows the dependency graph, not numeric phase order.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP/HTTP; React/Vitest for dashboard boundaries.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; `docs/designs/2026-09-29-latest-model-profiles-design-phase-3.md`. Read the Phase 3 execution/coverage map and this task's upstream Interfaces before coding.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** P3T8 policy schema/source (new task); P2T1 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-1.md`) defines follow target; independent of refresh/runtime pins.

**Files:** Create `domain/modelidentity/identity.go`, `adapter/modelcatalog/identity.go`, tests, `adapter/modelcatalog/testdata/identity-evidence.json`, `docs/designs/latest-model-profiles-source-evidence.md`. Create `domain/provider/model_version.go` to expose the domain version port alias for P2T2.

## Interfaces (owned here unless explicitly consumed)

Produce `modelidentity.CatalogModel{CatalogProvider,NativeID,CanonicalModelID,ReleaseDate,SourceRef string}` and `Entry{CanonicalID,Provider,NativeID,Tier,SpecificModel,OrderKey,ReleaseDate,SourceRef string; Immutable bool; PolicyRevision int64}`; missing Tier/OrderKey is explicit unknown, not guessed. `VersionRequest{Model,RouteProvider,ConnectionRef,Backend string}`; `VersionResolver.SpecificVersion(context.Context,VersionRequest) (string,error)`; `IdentityMapper.Map(context.Context,CatalogModel) (Entry,error)` takes parsed source provider/native ID plus optional canonical identity and release date; `Compare(Entry,Entry) (int,error)` compares evidenced same provider/tier chronology. `domain/provider/model_version.go` exposes `type VersionResolver = modelidentity.VersionResolver`; modelidentity imports no provider package. Callers build VersionRequest from a coherent Profile plus route metadata; no inconsistent Profile-taking overload. IdentityMapper requires a non-nil PolicySource; NewVersionResolver() takes no policy input. Identity mapping/version types are owned here; policy types/source are owned by P3T8. VersionRequest never requires evidence from a future refresh to recognize a supported explicit version.

## Data producers and composition-root wiring

P3T8 PolicySource.Snapshot supplies the persisted initial D2 pattern table and its admin edits; P3T2 supplies CatalogModel including `release_date` and `canonical_model_id`. The normalizer, persisted catalog and candidate service pass these fields intact. Direct mapper unit tests provide captured CatalogModel fixtures, keeping task construction acyclic. Record primary-source URLs, retrieval date, schema/binary versions, representative exact IDs and evidence in source-evidence doc/fixture. Current profiles from coherent cache supply VersionRequest; ConnectionStore.Get supplies route metadata. Explicit supported version IDs are recognized by the shipped provider-specific version grammar, separately from follow-tier membership (a dated specific snapshot may be pinned without being followable). Preserve executable spelling. Mutable aliases (`sonnet`, `latest`), Azure deployment names or opaque Bedrock IDs without a proved fixed executable spelling return ErrSpecificVersionUnavailable; no alias/deployment-mapping producer is assumed. A fixed native ID with documented mapping can be recognized by a source-recorded parser implemented here. Only the non-secret route provider, connection reference and backend reach VersionRequest; no credential-bearing Extras or unused evidence-revision field is carried in this explicit-version contract. P3T1 tests pass captured source entries directly; it must not depend on the later fetcher runtime, so P3T1 -> P3T2 stays acyclic. Wire mapper/version resolver at P3T7 and snapshotter at P1T4. Unknown mapping remains cataloged and blocks advancement/version claims.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

  - [x] Verify the existing named identity/order/date/error/edge tests and D2 source fixture; add numeric-alias/policy-cannot-certify tests and documented fixed-route successes.
  - [x] Add real-root trigger tests with HTTP and harness-argv positive controls, asserting rejected numeric aliases/opaque providers create no row and no provider request/process.
  - [x] Independent canonical-route slice: add fixed-version acceptance/rejection tests for the factory-supported `anthropic-api` spelling and extend the real-root HTTP execution/continuation matrix to use that persisted connection provider.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestIdentitySameTargetNewer` proves evidenced chronology and canonical identity across two native paths.
- Success: specific-version model and provider-prefixed routing spelling resolve without changing spelling needed by each adapter.
- Failure: unknown tier/version, mutable alias without evidence, wrong provider/target and current model missing from catalog do not authorize advancement.
- Error: malformed/source-mismatched evidence fails verification; loader errors propagate.
- Edge: numeric order (5-5 > 5, 6.1 > 6, k2.7 > k2), equal numeric version with later release_date, missing/tied date at equal version, excluded dated/fast/preview variants, multi-slash ID, unresolved deployment/alias rejection and aggregator/provider separation.
- Input guard: empty evidence map cannot confirm any version/tier; real-source fixture assertions verify mapping provenance and output. Hardcoded synthetic IDs only test algorithms and cannot satisfy source evidence gate.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, canonicalID, mapped.CanonicalID)
assert.Error(t, unknownOrderingErr)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
  - [x] Canonical-route red: run the new version and root cases; confirm supported fixed IDs are rejected by the resolver and trigger snapshotting before implementation.
  - [x] Routing-prefix follow-through red: `TestIdentityRoutingPrefixes`, `TestIdentityForeignRoutingPrefixesRemainUnknown` and `TestIdentityRoutingPrefixRoot` fail behaviorally. Own-provider Ollama prefixes split canonical identities; unrelated provider/lab prefixes and a newly added policy provider incorrectly authorize tier membership. The real-root durable canonical event is absent (count 0), including after reopen.
- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

  - [x] Replace the generic digit fallback in the existing version resolver with source-recorded full-match provider/backend grammars and the documented OpenAI snapshot; preserve executable spelling and policy-independent resolution.
  - [x] Define provider.VersionRequestSource using the shared modelidentity request type; observe the missing-port compile failure, then pass the compile-time contract. P3T3 implements the actual route producer; P1T3/P1T4 consume it.
  - [x] Correct the evidence fixture's unproved `-fast` immutability claim and use source-supported DeepSeek IDs in existing pin tests without weakening their final-sink assertions.
  - [x] Canonical-route implementation: recognize `anthropic-api` with the existing native Anthropic grammar, preserving executable spelling and all alias/deployment rejection rules. Document the existing factory alias, without changing catalog-provider identity or adding model equivalences.
  - [x] Routing-prefix implementation: use the same policy-independent, single-prefix rule for canonical identity and policy matching. Accept the offering's own catalog-provider prefix or a recorded carrying lab/family prefix; reject unrelated and repeated prefixes. Remove the global policy-derived prefix set, preserving native/executable spelling and catalog-provider follow targets. Both exact focused and new root gates pass.

Keep provider identity, stable follow tier, fixed version and routing spelling separate. Compare captured numeric versions first; only equal versions need evidenced release dates, and unresolved ties remain ineligible. Do not date-sort arbitrary strings or strip arbitrary slash prefixes. Recognize fixed native IDs with source-recorded parsers; reject mutable aliases and opaque deployment IDs lacking a specific executable referent, with an actionable request for a fixed starting model. No unspecified deployment metadata is assumed. Implement D2 from P3T8: anchored full matches over only a recognized provider prefix; compare captured numeric tuples first and release_date only when numeric tuples tie. Different numeric versions remain orderable even if dates are absent. Equal versions with missing/equal dates cannot advance. Unknown patterns stay cataloged, never followable. Use source canonical_model_id to unify recognized native offerings when present; otherwise use a stable catalog-provider/native-ID identity. Never merge unrelated providers by similar names or derive follow membership from optional family labels. Unknown tier must not prevent exact-version recognition or discovery.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

- [x] Canonical-route verification: pass the task focused gate, the complete HTTP root matrix, related version/pin root tests and race checks after spec-compliance, anti-green-theater and code-quality review, in that order. Commit only this slice and its evidence; final mapper/refresh acceptance remains blocked above.

**Focused gate:** `go test ./domain/modelidentity ./adapter/modelcatalog -run "Identity|Version" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Provenance / release gate (resolved 2026-10-01)

Provenance gates resolved 2026-10-01 (D2 in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`): implement and test the chosen rules exactly as recorded there; do not substitute synthetic evidence for the real probe/catalog/delivery behavior they define.

## Producer-to-sink verification

P3T7 injects the real persisted policy source into mapper/version resolver; P1T4 constructs the snapshotter with that version resolver. Add `TestIdentityReleaseDateIsFed` with equal numeric versions and distinct captured release dates, asserting ordering changes when the date changes; an ID-only mapper must fail. Add a root test editing policy through registered MCP, then fetching a previously unmatched newer model and reaching the durable following profile and new-run invocation. Empty/nil policy cannot make arbitrary IDs followable; unknown metadata remains visible. Preserve historical current-model metadata in P3T5 so a temporarily missing current catalog entry cannot cause a downgrade.

Retain Entry.NativeID and source provider for every catalog entry, including unknown tier/version, so P3T4 can attempt actual exact-model discovery independently of eligibility. Canonical identity must not depend on editable tier patterns: use verified source canonical_model_id where available, otherwise source-recorded recognized provider/native mappings (including the D2 families across their listed aggregator providers), and fall back to catalog-provider/native-ID identity only for unmapped offerings. Preserve the catalog provider as the follow target even when canonical identity refers to a lab model. P3T1 owns the mapping fixtures/parser; P3T2 supplies raw canonical metadata and P3T5 retains identity across refreshes. Test one DeepSeek model on deepseek, ollama-cloud and openrouter maps to one event while a profile's chosen catalog provider remains unchanged. Editing tier patterns must not manufacture rediscovery events for an already known model.

SpecificVersion recognizes supported explicit executable versions using the shipped immutable-version grammar independently of mutable tier-policy reads. A policy/catalog refresh read failure must not interrupt new runs on an already-working explicit model. Mapper/eligibility still require the persisted policy source and fail conservatively if it cannot be read. Test a known fixed starting profile during catalog/policy-read failure: new run pin and final model remain valid; following advancement stays put.

## Fixed-version grammar follow-through

Extend existing domain/modelidentity/version.go and its tests; do not recreate the mapper/policy implementation. Its current generic digit check accepts arbitrary proxy names such as `production-v2`; that is not evidence of a specific version. Replace that fallback with source-recorded provider/backend-specific full-match grammars or explicit documented native mappings. Unknown provider/opaque numeric alias returns ErrSpecificVersionUnavailable. D2 examples establish their chosen version semantics; dated fixed IDs may remain pinnable outside follow patterns. Admin tier regex edits alone cannot certify a new immutable version grammar.

Add red/green `TestVersionRejectsNumericAliases` (unknown-provider `production-v2`, numeric Azure/proxy deployments, mutable suffixed aliases), documented fixed-version successes for each supported route, and a P1T4 root trigger test asserting no row/process for an unproved numeric alias. Keep the input producer actual Connection.Provider/effective backend from P1T3/P3T3 VersionRequests; never use FollowTarget.Provider to bypass a deployment's opacity. P3T7's injection uses the existing NewVersionResolver() (no policy constructor argument); IdentityMapper alone consumes PolicySource.

Add `provider.VersionRequestSource` in domain/provider/model_version.go with `VersionRequests(context.Context,Profile) ([]modelidentity.VersionRequest,error)`. P3T3 inventory implements it for prospective selection and P3T6 delegates it on EligibleCandidateSource. Also preserve existing `provider.WorkflowVersionRequestSource.VersionRequestsForWorkflow(context.Context,Profile,*workflow.Workflow) ([]modelidentity.VersionRequest,error)` from domain/provider/model_version.go for P1T3/P1T4 snapshot capture; NewModelSnapshotter consumes this workflow-scoped port. This port describes actual routes only; it cannot read tier policy or return FollowTarget.Provider in place of Connection.Provider.

## Execution evidence (canonical-route slice, 2026-10-16)

- Selection: P1T4's remaining Azure/Bedrock evidence is still unavailable; P2T2 still lacks P2T3's validated selection store and P2T3 still depends on P2T2 preparation. Their prior blockers are not retried. P3T1 has an independent owning correction for the factory-supported `anthropic-api` route; this pass implements only that slice. P2T1's proof ports and P3T3's route producer now exist, despite older downstream blocker notes referring to their absence.
- Red: the exact Identity/Version focused gate fails on all three supported `anthropic-api` fixed-version assertions. `go test ./cmd/arbitron -run '^TestUsabilityHTTPExactModelRoot$/provider=anthropic-api' -count=1` fails all six cases at trigger snapshotting with `ErrSpecificVersionUnavailable`, before any HTTP execution.
- Green: the one existing native Anthropic grammar branch now accepts both factory-supported provider spellings. The complete HTTP root matrix passes, including persisted X, authenticated batch/stream request bodies, accepted output, continuation X after a live edit to Y, and wrong/missing actual-model rejection before output/transcript acceptance. No catalog identity, follow membership, model equivalence, adapter, flag or composition-root option was added.
- Ordered self-review before final verification: (1) spec compliance—only route alias parity with the existing factory; full mapper/advancement acceptance remains blocked above; (2) anti-green-theater—the real initDomain reads migrated SQLite connection/profile data, persists pins and sends non-empty model/auth/prompt fields to a validating HTTP leaf; absent snapshot wiring fails trigger, absent continuation pinning sends Y and fails the sink, wrong actual evidence leaves output/transcript unaccepted; (3) code quality—reuse the existing full-match grammar and existing root matrix, preserve executable spelling, retain malformed/foreign/repeated-prefix and numeric-alias rejection, document alias provenance, gofmt and diff check.
- Post-review green: exact task focused gate; full `go test ./domain/modelidentity ./domain/provider ./adapter/modelcatalog ./adapter/provider ./adapter/anthropicapi -count=1`; full `go test ./cmd/arbitron -count=1`; `go test ./domain/engine ./adapter/provider ./cmd/arbitron -run 'Pinning|ModelPins|PinsWiring' -count=1` (the engine package has no matching tests); `go test -race ./domain/modelidentity ./adapter/provider ./cmd/arbitron -run 'Identity|Version|UsabilityHTTPExactModelRoot|ModelPins|PinsWiring|Pinning' -count=1`; `git diff --check`. No schema/query/UI changes require regeneration. Commit only this verified slice and evidence; do not count it as completion of the unchecked final acceptance steps.

## Execution evidence (independent slice, 2026-10-16)

- Red: focused Identity/Version gate failed on numeric aliases, wrong providers/prefixes, unproved Bedrock IDs and policy-derived immutability. Root HTTP trigger test failed because invalid input was accepted. The new route-port signature check failed to compile because VersionRequestSource did not exist. Both root HTTP/argv tests also fail behaviorally against the untouched HEAD in an isolated temporary worktree (removed afterward).
- Green: `go test ./domain/modelidentity ./adapter/modelcatalog -run 'Identity|Version' -count=1`; `go test ./domain/modelidentity ./domain/provider ./adapter/modelcatalog ./adapter/provider -count=1`; `go test ./cmd/arbitron -run 'ModelPins|Version' -count=1`; `git diff --check` — all exit 0 after refactoring. No schema/query changes.
- Dependent pin tests initially exposed synthetic/unproved numeric fixture IDs; replaced only those IDs with D2-supported DeepSeek spellings, preserving exact HTTP/argv/persisted-pin/corruption assertions. They now pass rather than exempting unsafe fixture spellings in production.
- Self-review in required order: (1) scope is existing fixed-version grammar, shared route port, source evidence and direct-root rejection only; no candidate/refresh feature or new option; incomplete final mapper wiring/date provenance remain blocked above; (2) root positive controls require non-empty persisted models/connections and prove final HTTP body/argv, while rejection asserts no row/request/process; policy tests cannot manufacture immutable evidence; (3) reused existing resolver/fixtures/root helpers, full-match regexes and spelling preservation, gofmt, explicit errors and no nil/empty no-op fallback. Re-ran all gates after fixes.
- P2T1's previously unchecked-in-prose proof extension is now an explicit unchecked step in its own plan so the next pass can supply that independent prerequisite, rather than repeatedly blocking P2T2/P2T3.

## Current identity consumers

IdentityMapper is already constructed from the persisted PolicyService by `cmd/arbitron/model_refresh.go:newModelRefresh`; NewVersionResolver remains policy independent. P1T3 owns the workflow-scoped snapshot input and D8 configured-ID exception; do not broaden SpecificVersion to pretend opaque deployments are immutable. Discovery may use Entry.NativeID with unknown tier/order/immutability, whereas following selection still requires all three. P3T6 remaps retained CatalogModels under the same current policy as candidates, including prospective initialization/revalidation, and P3T7 owns policy-edit → refresh → persisted change → execution acceptance. Implement remaining mapper/source tests locally without waiting on that downstream acceptance.

## Completion evidence (routing-prefix follow-through)

- Selection: P2T2's recorded cold-bootstrap blocker is unchanged: CandidateDeps still lacks the prospective pipeline/coordinator, and Collect still has no prospective input. Do not retry/re-fail it or implement P3T6 inside the admin task. P3T1's remaining mapper/source obligations are independent and complete here; historical downstream-blocker statements above are not prerequisites or current absence claims.
- Strict red/green: add `TestIdentityRoutingPrefixes` and `TestIdentityForeignRoutingPrefixesRemainUnknown`, then run the exact task gate red. Own-provider Ollama prefixes split canonical IDs; foreign/lab-crossed and policy-added prefixes incorrectly grant tiers. Add/run `TestIdentityRoutingPrefixRoot` red: migrated SQLite persists the wrong canonical ID and foreign tier, and the required canonical event count is zero, also after reopen. Minimal shared prefix implementation makes both gates green without changing version grammars, policy rules or executable IDs.
- Spec review FIRST: finish only the mapper's recognized single-prefix, canonical/provider separation contract. Reuse the existing D2 fixture, recorded source capture, date/order/error tests and policy-independent version port/resolver. No new model family, source evidence claim, schema, configuration flag or alternate discovery path. P3T6 cold bootstrap/full-source scheduling and P3T7/P4T3 broader acceptance remain in their owning tasks, not prerequisites for this completed producer.
- Anti-green-theater SECOND: the actual `newModelRefresh` composition root consumes persisted settings/policy/connections/profiles, normalizes source-backed routing variants and probes the real chat adapter with nonempty model/auth/messages. A checking fetch leaf rejects the wrong configured URL. Assert the sink: three providers retain their targets/native IDs but share one canonical identity, confirmed nonempty exact proof and one durable event; foreign metadata stays unknown; the selected profile survives reopen. A real policy-table read fault stops advancement while a newly triggered run still pins and executes the already-published explicit ID, preserving spelling and accepting actual provider output. The pre-implementation root failure proves the durable sink detects the missing canonical mapping. Existing registered-MCP policy-edit → selected profile → invocation and numeric-alias rejection roots also pass freshly.
- Code quality THIRD: one small prefix helper shared by identity and matching replaces the global policy-derived prefix cache; preserve anchored matching, one-prefix-only semantics, source contradiction checks, cache synchronization and owned rules. Reuse neighboring root/SQLite fixtures and lifecycle helpers, validate load-bearing leaf inputs, gofmt and diff check. No production I/O or collaborator is substituted outside the test leaves.
- Fresh post-review verification (all exit 0): exact `go test ./domain/modelidentity ./adapter/modelcatalog -run 'Identity|Version' -count=1`; full `go test ./domain/modelidentity ./adapter/modelcatalog ./domain/modelcatalog -count=1`; full `go test ./cmd/arbitron -count=1 -timeout=8m`; `go test ./cmd/arbitron -run 'Identity|Version|UsabilityHTTPExactModelRoot' -count=1 -timeout=8m`; `go test ./adapter/provider ./adapter/sqlite -run 'Profile|Advance|ValidatedSelection|Inventory|ModelPins|Snapshot' -count=1 -timeout=8m`; `go test -race -p 1 ./domain/modelidentity ./adapter/modelcatalog ./domain/modelcatalog ./cmd/arbitron -run 'Identity|Version|ModelRefreshRootAdvances|UsabilityHTTPExactModelRoot|NormalizeCapturedReleaseDate' -count=1 -timeout=8m`; `git diff --check`. No schema/query/UI change requires generation or dashboard build. All P3T1 steps are checked; final full-release verification remains P4T3.
