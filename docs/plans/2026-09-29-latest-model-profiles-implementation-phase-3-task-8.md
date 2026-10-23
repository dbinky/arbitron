# Latest Model Profiles — Phase 3 Task 8: Editable identity pattern policy and production source Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Track checkboxes and observe behavioral red, green and refactor for every remaining change.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP.

**Goal:** Implement the shipped, admin-editable pattern table chosen in decisions D2, and feed it into identity/eligibility with revision-safe updates.

**Architecture:** Domain contracts in modelidentity; SQLite policy persistence; authenticated MCP tools; real initDomain wiring. This task defines policy ports before P3T1 despite its task number. Root integration occurs in P3T7 after all consumers exist.

**Spec/design:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`, design Phase 3, and `docs/designs/2026-09-29-latest-model-profiles-decisions.md` D2.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** P2T1 for target vocabulary only. No dependency on mapper, inventory, refresh or root task implementations; the mutation-gate port below avoids a construction cycle.

**Files:** Create `domain/modelidentity/policy.go`, `policy_service.go`, tests, `adapter/sqlite/model_identity_policy_repo.go`, tests, existing `adapter/sqlite/migrations/025_model_identity_policy.{up,down}.sql` (preserve applied SQL), `adapter/mcp/tools_model_identity_policy.go`, tests and shipped policy seed fixture. Modify `adapter/mcp/deps.go`, registration and `cmd/arbitron/main.go` in cooperation with P3T7; extend `cmd/arbitron/model_catalog_wiring_test.go` and source-evidence/operator documentation.

## Interfaces (owned here unless explicitly consumed)

Produce `PatternRule{Providers []string; Tier,Pattern string; VersionGroups []int}`; `IdentityPolicy{Revision int64; Rules []PatternRule}`; `PolicySource.Snapshot(context.Context) (IdentityPolicy,error)`; `PolicyStore` extends PolicySource with `Replace(context.Context,int64,[]PatternRule) (IdentityPolicy,error)` (expected revision, full replacement). Revision starts at 1, increments atomically and is read-only to clients. Define modelidentity `MutationGate.WithMutation(context.Context,func(context.Context) error) error` as a narrow interface, satisfied by P3T3 ExecutionGate. `PolicyService.Get(context.Context) (IdentityPolicy,error)` and `Update(context.Context,int64,[]PatternRule) (IdentityPolicy,error)` require admin. `ReadInternal(context.Context) (IdentityPolicy,error)` is trusted; `PolicyService.Snapshot(context.Context) (IdentityPolicy,error)` delegates to ReadInternal and implements PolicySource without public registration. `SetMutationGate(MutationGate)` is injected by P3T7 before any write; nil gate rejects updates, not successful no-ops. Get/Update return deep copies and typed conflict/validation errors.

Register MCP `model_identity_policy_get` and `model_identity_policy_update` with `expected_revision` and complete `rules` input; serialize providers/tier/pattern/version_groups and read-only revision. Service authorization remains authoritative. No UI or unauthenticated policy write is required.

## Data producers and composition-root wiring

Migration 025/seed fixture supplies every D2 initial row exactly: Anthropic opus/sonnet/haiku/fable; OpenAI sol/astra/luna/terra; DeepSeek flash/pro on deepseek/ollama-cloud/openrouter; GLM/glm-flash on zhipuai/ollama-cloud; Kimi/kimi-code on moonshotai/ollama-cloud. The shipped fixture is derived directly from the decision table, not an external classifier. Seed only when absent; never overwrite edits at restart. Admin MCP submissions -> PolicyService -> conditional SQLite replacement -> persisted revision. P3T7 constructs repo/service, injects shared gate, projects MCP deps and passes PolicyService as PolicySource into IdentityMapper; the policy-independent version resolver is NewVersionResolver(); P3T3 includes policy revision in inventory/commit fingerprints. P3T1 builds version/tier entries from this source, not a disconnected hardcoded table.

## Validation and ordering

Compile anchored full matches with a supported linear-time regex engine. Translate decision-table `\d` to the Go regexp equivalent `[0-9]` if necessary and wrap full-match anchors; tests establish identical accepted/excluded IDs. VersionGroups identify numeric captures (major/minor or dotted capture), parsed into a numeric tuple by P3T1; reject invalid capture indexes, nonnumeric capture grammar, empty provider/tier, invalid patterns, excessive policy size or identical provider/pattern rules assigning different targets. P3T1 also rejects any actual catalog ID matching conflicting rules as ambiguous/ineligible at map time; it never arbitrarily selects the first rule. Update validation does not depend on a future catalog runtime or attempt to prove arbitrary regex intersection. Normalize provider/tier only; preserve model spelling. The table controls membership/order, not proof of execution. Invalid edits leave the previous policy/revision and profiles unchanged. Policy edits reclassify on next refresh but do not move profiles by themselves, rewrite pins, downgrade versions or require already-running pinned calls to re-pass tier membership. Unknown tier remains visible and can be announced if usable.

## Strict-TDD steps

- [x] Happy: write `TestIdentityPolicyMCPRoundTrip` through real registered MCP -> SQLite -> reopen/internal source; assert exact submitted non-empty pattern and revision, then consume it in P3T7's new-model execution sink.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.
- [x] Success: `TestIdentityPolicySeedsD2` verifies every initial row and worked numeric/exclusion example; edits survive restart and do not reseed.
- [x] Failure: absent/non-admin identity, malformed regex/capture/target, contradictory duplicate rule and missing gate persist nothing; stale expected_revision returns conflict.
- [x] Error: read/write/migration faults propagate and retain previous policy; empty/nil source cannot authorize arbitrary identity/follow membership.
- [x] Edge: concurrent replacements, deep-copy mutation, dotted versus hyphen version, exact full-match exclusions, known routing-prefix stripping only, equal-version date tie-break, and policy change between evidence selection/CAS.
- [x] Input guard: actual root test creates a previously unmatched model, edits its policy through MCP, refreshes, and asserts the durable following profile plus final new-run model changes. Removing PolicySource injection or reading only a static initial fixture must fail. A policy-only round trip is not the release wiring sink.
- [x] Run focused tests before implementation, observe behavioral red after API stubs; implement the contracts minimally, rerun green, refactor and rerun. P3T7 owns final root-to-execution integration once downstream prerequisites exist.

**Focused gate:** `go test ./domain/modelidentity ./adapter/sqlite ./adapter/mcp -run IdentityPolicy -count=1`; then P3T7 root integration `go test ./cmd/arbitron -run 'IdentityPolicy|ModelProfiles' -count=1`. Inspect each exit status separately. Run `git diff --check`; commit only task-owned implementation after verification.

## Provenance / release gate

No missing human policy decision: D2 provides the initial table and ordering. This task produces its persisted/editable source; P3T1 consumes it, P3T3 coordinates revision changes, and P3T7 wires and tests those production paths. Confirmed usability still comes from P3T4, never policy membership alone.

## Existing implementation boundary

Policy seed/service/repository, migration 025 and registered MCP wiring already exist in this checkout. Preserve applied 025 and their tests; extend only missing consumer/revision/root assertions. No checkboxes above assert fresh verification of that implementation. P3T7 owns the new policy -> fresh catalog -> actual advancement/execution test once refresh exists. SpecificVersion uses the existing policy-independent NewVersionResolver(); only IdentityMapper reads PolicySource. The shared global constraints and happy/success/failure/error/edge obligations in the Phase 3 design apply here too.

## Independent submitted-policy size validation slice (2026-10-16)

Selection: earlier tasks retain their recorded proof/refresh/seed prerequisites. P3T8's policy-size validation is independent and already has real root/MCP/service/SQLite producers. Work is limited to enforcing the existing provider-list bound before normalization; final policy-to-refresh/new-run acceptance remains blocked above.

- [x] Write `TestIdentityPolicyProviderLimitBeforeNormalization` and `TestIdentityPolicyMCPRejectsOversizedProviderListRoot` before implementation. Cover the exact limit, over-limit duplicate providers, normalization, preserved caller input and authoritative rules/revision after rejection and reopen.
- [x] Run both specified gates red. ValidateRules, CompileRules and PolicyService accept 33 duplicate providers despite the existing 32-provider bound. Actual registered MCP accepts the oversized edit, persists revision 3 instead of preserving the accepted revision 2, and retains the wrong pattern on reopen. The exact-limit positive control already passes.
- [x] Validate raw provider-list sizes in the shared compiler before normalization and make all public validation/compilation/service entry points pass the original submission. Preserve valid normalization, model spelling, authorization, shared mutation gate and CAS behavior; add no limit, option, migration or runtime.
- [x] Run both specified focused gates, full affected packages and race verification after the ordered self-review.
- [x] Review spec compliance first, anti-green-theater second and code quality third; rerun green and commit only this verified slice plus task evidence. Full-task acceptance remains unchecked.

Ordered review and verification evidence:

1. Spec compliance first: enforce only the existing MaxProvidersPerRule bound on submitted rules before duplicate removal can hide excessive input. No new limit, option, migration, target grammar, discovery producer or refresh integration. The existing initDomain service/MCP/repository composition is unchanged and receives actual submitted rule lists.
2. Anti-green-theater second: the root test requires an exact-limit, nonempty submitted pattern to persist with normalized providers/tier and revision 2; absent input/service/gate/registration cannot satisfy that control. An oversized replacement with a different pattern must error and leave the entire authoritative policy unchanged, including after SQLite reopen/migrations. The initial red proved both durable sinks failed, not just the handler result. Strengthen the error assertion to distinguish size rejection from an unrelated conflict/fault and compare caller input against an independent copy rather than an alias.
3. Code quality third: read neighboring compiler/service/root tests, centralize raw-size checks in the existing compiler, preserve error types and valid normalization, and use existing migrated-root helpers. Format Go; no SQL/query/schema/UI changes require generation.

Post-review green (all exit 0): both specified focused gates; `go test ./domain/modelidentity ./adapter/mcp ./cmd/arbitron -count=1 -timeout=8m`; `go test ./adapter/sqlite -run 'IdentityPolicy|PolicySeed' -count=1 -timeout=8m`; `go test -race -p 1 ./domain/modelidentity ./adapter/sqlite ./adapter/mcp ./cmd/arbitron -run 'IdentityPolicy|InitialRules|ValidateRules|PolicyService|PolicySeed' -count=1 -timeout=8m`; `git diff --check`. No final policy-to-refresh/advancement/new-model execution acceptance is claimed; its existing upstream blockers remain open.

## Independent numeric-capture validation slice (2026-10-16)

- [x] Write `TestIdentityPolicyRejectsMalformedNumericCaptureGrammar`, `TestIdentityPolicyAcceptsNumericCaptureGrammar` and `TestIdentityPolicyMCPRejectsMalformedNumericGrammarRoot`. Cover empty captures, zero-minimum repetition, leading/trailing/repeated dots, mixed digit/dot classes, alternation, valid dotted versions and optional numeric captures. The actual registered MCP/root service must persist a nonempty submitted valid rule and preserve its exact durable rules/revision on invalid edits and database reopen.
- [x] Run both specified gates before implementation; observe behavioral red. All twelve malformed capture grammars are accepted by ValidateRules/CompileRules. Each of seven malformed MCP submissions also persists a replacement and increments the authoritative SQLite revision; reopen retains an invalid edit. Read the current expected revision before each attempt so one bad write cannot hide later failures behind stale-revision conflicts.
- [x] Implement only the required nonnumeric-capture-grammar rejection in the existing domain policy compiler. Restrict the alphabet as before, then traverse the regexp program paired with numeric-tuple grammar states, with a visited-state bound for repetition. Preserve optional absent groups, valid numeric/dotted captures, shipped D2 rules, normalization, authorization, gate and CAS semantics. No new option, migration, mapper, refresh source or wiring substitute.
- [x] Run the task gates and full affected identity/MCP/root tests green; also run race-enabled policy/seed/service/repository/root coverage. No interface, query, schema or UI change requires regeneration. The task regex omits several existing seed/service tests, so full identity tests and explicit race regex include them.
- [x] Complete ordered self-review and commit this verified slice only: spec compliance first (exact numeric grammar validation, no expanded feature); anti-green-theater second (real MCP -> authoritative SQLite/internal-source/reopen sinks and valid positive control); code quality third (reuse Go regexp/syntax and existing compiler/root helpers, bounded state traversal, gofmt). Temporarily bypass the new grammar check: every invalid root submission again changes its durable row/revision and the reopen sink fails. Restore the guard before final verification.

### Verification evidence

Selection skips P1T4/P2T2/P2T3/P3T1–P3T5's recorded unclosed evidence/runtime acceptance, and records P3T6/P3T7's missing probe/refresh producers under their first failing steps. P3T8's independent policy-validation requirement is already wired and fed: initDomain constructs the actual PolicyService/repository and shared gate, MCP dependencies register its tools, and submitted rules reach the existing compiler before SQLite replacement. This is not the blocked policy-to-refresh-to-selected-execution release sink.

Post-review gates (all exit 0):

- `go test ./domain/modelidentity ./adapter/sqlite ./adapter/mcp -run IdentityPolicy -count=1`
- `go test ./cmd/arbitron -run 'IdentityPolicy|ModelProfiles' -count=1`
- `go test ./domain/modelidentity ./adapter/mcp ./cmd/arbitron -count=1 -timeout=8m`
- `go test -race -p 1 ./domain/modelidentity ./adapter/sqlite ./adapter/mcp ./cmd/arbitron -run 'IdentityPolicy|InitialRules|ValidateRules|PolicyService' -count=1 -timeout=8m`
- `git diff --check`

The original full-task boxes remain unchecked for their broader acceptance; no final discovery, advancement, scheduled refresh or new-model execution claim is made.

## Current policy release sink

The source/service/MCP producer is present. P3T7 owns the shared policy edit → fresh source → persisted metadata/proof → advancement → new-run execution test; finish this task's local happy/success/failure/error/edge policy checks without a reverse dependency on P3T7. Update the existing root test to show a membership-only edit neither invalidates path confirmation nor invents a new canonical discovery event. Admin regexes may classify ordinary supported families, but cannot enable D8 following paths or certify a new immutable executable grammar; reject such following/probe claims through P2T2/P3T4 even after policy edits.

## Completion evidence (2026-10-18)

Selection: skip P3T4's recorded external Codex tool-free contract blocker and P3T7's recorded full-suite fixture blocker without re-failing those tasks. This pass completes P3T8 only; their unchecked acceptance remains unchanged.

- Happy/success: strengthen the registered MCP round trip to close/reopen SQLite and rerun migrations, comparing the exact submitted policy/revision through the internal source. Remove the root restart test's direct repository overwrite, so only the real MCP edit can satisfy its reopened sink. `TestIdentityPolicySeedsD2`, the independent D2 table/capture assertions and worked-example tests freshly verify every seeded row, numeric ordering, exclusions and no reseeding over edits.
- Failure/error: expanded MCP cases reject malformed/missing captures, empty providers/targets and contradictory duplicate rules, asserting the complete previous rules/revision rather than revision alone. Fresh service/repository tests cover authorization, absent gate, conflicts, deep copies, read/write faults and nil/empty sources. Add a real SQLite migration-recording fault, require the error and unchanged policy/unapplied migration, then retry successfully without reseeding.
- Behavioral red/green: `TestIdentityPolicyMCPRevisionBeyondInt32` fails before implementation: a valid server-owned revision 2147483648 cannot be edited through MCP. The existing parser incorrectly caps the specified int64 contract at MaxInt32. Change only that upper bound, excluding the float64 2^63 boundary before conversion. Rerun green; stale, fractional, nonpositive, string and overflowing revisions retain the exact saved policy, including after reopen. No new public option, migration or model rule is added. Other expanded assertions exercise behavior already implemented; no missing implementation is invented for them.
- Edge/input guard: `TestIdentityPolicyChangeRejectsInFlightSelectionRoot` edits membership through registered MCP during real prospective probe I/O. The stale create persists no profile; a fresh create and two-node run use the edited policy's older model. `TestModelProfilesPolicyEditFeedsFreshSelectionRoot` now starts with an MCP-submitted narrow policy, discovers a previously unmatched model, executes the old model while it remains unmatched, then submits the broader policy through MCP. Editing alone changes no profile. Actual manual refresh persists the new tier/order/revision and following model; the next two-node run executes the new model. Original canonical event payloads and exact unchanged path confirmations remain intact, with no extra probes. `TestIdentityPolicyCannotEnableD8FollowingRoot` proves even matching edited rules cannot certify opaque immutable spelling, following profiles or usability on Azure/Bedrock. Dotted/hyphen tuples, exact full matches, date tie-breaks and known-prefix handling are freshly covered.

Ordered self-review:

1. Spec compliance FIRST: the missing production behavior is only the int64 revision guard; all other work completes the specified local acceptance against existing real policy/service/store/mapper/coordinator/gate wiring. No new runtime, flag, static fallback policy, schema change or downstream release waiver.
2. Anti-green-theater SECOND: strengthen the final sink with a run after discovery but before the membership edit, plus exact executed old/old/new model sequences. Temporarily ignore the real source entirely: profile persistence fails. Then preserve its real revision but force shipped static rules: the durable profile, intermediate run pin and final actual HTTP request sequence all fail. Restore both mutations before verification. Restrict the shared HTTP leaf to independently configured fixture model IDs rather than accepting arbitrary request echoes. Missing source/policy/MCP/refresh/execution inputs cannot satisfy the positive sinks.
3. Code quality THIRD: reuse existing registered tools, policy/compiler/repository, migrated root and I/O-only ModelDependencies seams. Preserve applied migrations, remove the redundant catalog read and direct-write shortcut, keep concurrent fixture work context-bounded, and gofmt all touched Go files. No SQL/query/UI change requires generation.

Fresh post-review verification:

- PASS: `go test ./domain/modelidentity ./adapter/sqlite ./adapter/mcp -run IdentityPolicy -count=1`.
- PASS: `go test ./cmd/arbitron -run 'IdentityPolicy|ModelProfiles' -count=1`.
- PASS: full ordinary domain/modelidentity and adapter/sqlite tests (`go test ./domain/modelidentity ./adapter/sqlite ./adapter/mcp -count=1 -timeout=4m` reports those two packages green).
- PASS: `go test -race -p 1 ./domain/modelidentity ./adapter/sqlite ./adapter/mcp ./cmd/arbitron -run 'IdentityPolicy|ModelProfiles|PolicyService|ValidateRules' -count=1 -timeout=4m`; full `go test -race ./domain/modelidentity -count=1 -timeout=2m`.
- PASS: `git diff --check`.
- Out-of-task concerns, not represented as green: full adapter/mcp still has eleven existing profile fixture failures lacking mandatory version-resolver/selection inputs. A detached worktree at starting HEAD 55e798b freshly reproduces the identical failing test set. An expanded race regex also included the existing `TestIdentityRoutingPrefixRoot` fixture failure, independently reproduced at that HEAD; the task-owned race gate above passes with no race diagnostics. P4T3 still owns the full release gate. No unchecked upstream acceptance prevents completing this independent policy task.
