# Latest Model Profiles — Phase 4 Task 3: Announcement root wiring and full release verification Implementation Plan

> **UNBLOCKED (2026-10-21):** the only remaining blocker, P3T4's non-Codex (OpenCode) isolation contract, is resolved by D12 and implemented in `8aa67e6f`. The final-release BLOCKED note below is void. See "Final release verification (2026-10-21)" at the end.

> **UNBLOCKED (2026-10-19):** D11 makes the branch red suite this feature's own work, owned by P4T4. Do not wait on it here; finish this task's named gates. The release-verification box closes when P4T4 is green.

> **Current release order:** complete local receiver gates after P3T4's D10 probe work and P3T7's local runtime gates; P4T4 then repairs/verifies the full suite; this task finally runs all release gates. Historical Codex tool-free and feature-baseline BLOCKED notes below are void under D10/D11. They are execution history, not missing human decisions or permission to skip checks.

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Announcement root wiring and full release verification.

**Architecture:** Domain ports own contracts; adapters produce real inputs; the shared `cmd/arbitron/main.go:initDomain` composition root connects them. Execution order follows the dependency graph, not numeric phase order.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP/HTTP; React/Vitest for dashboard boundaries.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; `docs/designs/2026-09-29-latest-model-profiles-design-phase-4.md`. Read the Phase 3 execution/coverage map and this task's upstream Interfaces before coding.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** P1T2 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-2.md`), P1T4, P2T3, P3T7, P4T1–P4T2; full release requires all escalations resolved.

**Files:** Modify `cmd/arbitron/main.go`; create `cmd/arbitron/model_announcement_wiring_test.go`, extend `model_profiles_e2e_test.go`; update source-evidence and feature operator documentation under `docs/designs/latest-model-profiles-source-evidence.md`.

## Interfaces (owned here unless explicitly consumed)

Preserve and verify P3T7's actual `newModelRefresh` construction of AnnouncementService using SettingsService.ReadInternal, ModelCatalogRepo event/claim/retry methods, TeamsModelSender and clock; no receiver protocol or attempt Crypto dependency remains after D7. Replace a DeferredAnnouncer only in an explicitly staged deployment, not by adding a second service at the current root. Verify startup Drain and four-hour retry tied to existing RefreshLoop; Announce runs on every cycle completion/failure, not only new-model deltas. Keep one shared loop/drain lock and join lifecycle; no second independent discovery scheduler.

## Data producers and composition-root wiring

Real DB event payload -> service claim -> internal raw setting -> guarded synchronous sender -> external receiver -> 2xx/transport classification -> durable announced or retry state. Use actual stack assembly and transport leaf substitutions; both serve/MCP processes use same source/wiring. Configuration editing must affect destination at next drain without mutating frozen payload/history. Durable events from staged Phase 3 deployment survive service replacement.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestModelAnnouncementWiringToReceiverSink` actual stack starts with webhook configured and X-only source, persists old run, source later exposes usable Y, CAS advances aliases, new-run invocation uses Y and receiver stores exactly one card with original changes; active invocation remains X.
- Success: no-followers first discovery reaches external card with no profiles changed; empty default has zero sends; staged pending events drain after restart.
- Failure: non-admin settings edit cannot redirect delivery; suppressed discoveries do not replay after enable; wrong/missing exact-model evidence cannot announce.
- Error: force definitive send failure, crash/response loss/marker failure, restart with failed catalog fetch; a non-deduplicating receiver sees eventual delivery with original payload and durable announced state; accepted-response loss/marker failure may yield a duplicate, while a definitive failure before acceptance yields one delivered card.
- Edge: concurrent manual refresh/tick/drain, webhook clear/reconfigure, later path and model reappearance, interactive continuation/retry/rerun/child pins, cache publication failure repaired before new-run success.
- Input guard: removal of internal raw setting, DB events, sender or Announcer injection makes receiver-sink test fail. Assert actual receiver card count/content plus executed model; no callback-only integration assertion.
- Release: the seven provenance gates are resolved (decisions D1–D7); the completion check verifies the implementation honors each recorded rule, using real evidence shapes, not fakes standing in for authoritative evidence.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, 1, receiverCardCountForNewModel)
assert.Equal(t, originalChanges, receivedChanges)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

Resolve recorded external source/rollout decisions first and implement their production producers; update owning tasks/design provenance with verified file:symbol/evidence. Do not satisfy release gate by disabling consumers or documenting unknown as expected completion. Run full Go gate and dashboard type/tests/build; embed dashboard with `make ui`. Record that no harness was installed/upgraded and no real Teams URL was added. Commit only related implementation/artifacts after checks.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

> BLOCKED (2026-10-18, release verification): fresh `go test ./... -count=1 -timeout=5m` exits 1 in adapter/config, adapter/mcp, adapter/provider, cmd/arbitron and domain/provider. Remaining failures include missing mandatory pin/selection/gate inputs in profile-service fixtures, sample profiles absent from the default fixture, stale identity-prefix/admin-edit proofs and harness fixtures that reject source-wide/dated discovery or assert probe order/counts. The full run also observed one D8 azure-foundry batch retry failure; the explicit post-review D8 gate passes. Task-owned announcement failures are repaired and their focused/race gates pass. This is the previously recorded P3T7 baseline, not a newly missing runtime producer; no full-suite waiver is claimed and do not spin on the same blocker. P3T4's recorded Codex tool/MCP/plugin restriction source contract also remains unresolved, so this pass cannot certify full feature release.

**Focused gate:** `go test ./cmd/arbitron -run "TestModelAnnouncement|TestNextTickPublicationRecoveryRoot|TestModelProfilesRunnerCodexModesRoot|TestAutomaticCapabilitiesRoot|TestModelProfilesCloseJoinsManualAndTickerRoot|TestCatalogOpaquePathsNeverPersistConfirmationRoot|TestD8PinnedExecutionRoot|TestIdentityPolicyCannotEnableD8FollowingRoot" -count=1`. Release gates, after P4T4, are `go test ./... -count=1`; dashboard `npx tsc --noEmit` and `npx vitest run`; then repository-root `make ui`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Provenance / release gate (resolved 2026-10-01)

Provenance gates resolved 2026-10-01 (D1, D2, D3–D6, D7 in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`): implement and test the chosen rules exactly as recorded there; do not substitute synthetic evidence for the real probe/catalog/delivery behavior they define.

## Delivery acceptance tests

Drive actual four-hour clock ticks and registered settings tools through the production stack. Use a non-deduplicating HTTP receiver; an ordinary 2xx marks announced and a later tick sends nothing. For accepted-then-lost response, advance to NextAttemptAt, reopen the DB, fail catalog discovery, and assert the same frozen card arrives again and the event becomes announced. An ambiguous-delivery test asserting exactly one external card would contradict D7. Verify current destination edits redirect only later attempts, pending events pause while empty, suppressed events never replay, and a large multi-part event preserves every original profile change. The final release includes P3T8 admin policy producer and every real exact-model parser/session reader, not merely synthetic green reports.

Add receiver/reopen tests for the explicit completion Clock.Now feed, multipart intermediate claim state, clearing destination between parts, oversized 2xx response confirmation, and proof-path secret exclusion. Require P3T7's split build/start path to install AnnouncementService before the first seed/bootstrap discovery can drain events or any loop/recovery starts. The sender consumes the current raw internal setting per part; Complete/CompletePart persist retry times from the shared clock. Remove each input in turn and require the receiver plus durable-state assertion to fail.

## Current final release acceptance

The concrete sender/service/repository are already constructed by `cmd/arbitron/model_refresh.go:newModelRefresh`; there is no absent producer to replace with fixtures. Consume P3T7's ModelDependencies and common build/start/clock/transport seams, P3T6's full candidate discovery/cold bootstrap/recovery, P2T3's validator and P1T3/P1T4's D8 persisted exception (ModelPinRoutes → PinSet.OpaqueRoutes → ResolvedBinding.OpaquePinRoute). Extend existing wiring tests instead of duplicating root assembly. Settings remain initially empty; no real webhook is supplied.

- [x] Write and run red `TestModelAnnouncementAllDiscoveredCandidatesRoot`: drive first boot, real four-hour ticks and registered manual refresh through actual stack, guarded transport leaf and non-deduplicating receiver. On a path with a follower, an older native model, another tier and an unknown-tier exact success each produce their own canonical event/card with correct original changes (or explicitly none); missing/wrong execution evidence produces none. No-profile default harness also reaches durable event/receiver. Source membership and unknown metadata survive reopen; D8 pinned requests work but neither following nor usability is confirmed on those paths.
- [x] Add ordinary/success/failure/error/edge root tests for first-boot seeded execution, next-cycle publication recovery, retry despite discovery failure, raw-setting edits, multipart clock/backoff/part union, shutdown and secret redaction. Assert receiver content/count, durable event state/parts and executed old X/new Y models together. Retain D7 ambiguity duplicates; do not demand exactly one after accepted-response loss.
- [x] Remove each real producer/field injection in controlled mutations (restored afterward): full-source enumeration, native ID, persistent candidate proof, capability validator, shared coordinator/ticker, journal recovery, raw destination, event store and final Announcer. Require the corresponding final sink to fail, not merely a mocked call assertion.
- [x] Implement any remaining root feed/lifecycle corrections in owning tasks; rerun named tests green, then `go test ./... -count=1`, dashboard TypeScript/Vitest and `make ui` from repo root. Repair task-induced missing-pin/gate fixtures through real valid inputs, retaining genuine legacy tests; no known failing fixture exception marks the full release gate passed. Record only freshly run verification. No unchecked downstream acceptance blocks the upstream producer tasks per D9.
  - [x] Full-Go prerequisite only, verified by P4T4 on 2026-10-20 at code/test revision `b7193a3d9cbf28953c55fee7b86bb93e094f274d`: `go test ./... -count=1 -timeout=10m`, full affected-package `go test -race ./adapter/config ./cmd/arbitron -count=1 -timeout=15m`, `go vet ./...`, empty `gofmt -l .`, and `git diff --check` all exit 0. P4T4 records origin/main provenance and repairs. This does not check the composite release obligation; rerun this task's dashboard/build and remaining source-contract/release acceptance separately.

## Independent receiver/recovery acceptance slice (2026-10-18)

Selection: P3T4's Codex restriction contract and P3T7's full-suite fixture blocker were already documented. Skip re-failing those tasks; P4T3's existing announcement/root producers permit independent receiver acceptance. Leave the composite release steps above unchecked.

- [x] Extend the existing build/Start, runner, publication and D8 fixtures instead of constructing a parallel graph. Add `TestModelAnnouncementAllDiscoveredCandidatesRoot`, `TestModelAnnouncementRetryReopenDespiteDiscoveryFailureRoot` and `TestModelAnnouncementMultipartPauseReopenRoot` through actual migrated SQLite, registered settings/manual-refresh tools, shared clock/ticker and a non-deduplicating HTTP server. Assert receiver bytes/count, durable state/parts and old-X/new-Y execution together.
- [x] Run before changing production behavior: the new acceptance cases already pass against the existing runtime; no absent feature is fabricated to manufacture red. The full Go gate does reproduce the old announcement fixtures' behavioral reds (two discoveries delivered/stored where they expected only Y). Correct those expectations to retain full-source discovery, rather than disabling the old model or a consumer. Runtime implementation remains unchanged.
- [x] Cover first boot, older/other-tier/unknown-tier native IDs, wrong/missing proof refusal, source/unknown metadata reopen, two real ticks and manual refresh, no-profile default harness, D8 non-delivery, publication recovery despite fetch failure, current raw destination, suppressed/read-fault history, completion-time backoff, rejected send, accepted-response loss, marker failure, oversized 2xx, multipart pause/remaining-part union and shutdown. Definitive failure delivers once; ambiguous outcomes may deliver the identical card twice.
- [x] Perform ordered spec-compliance, anti-green-theater and quality review; restore every deliberate mutation and rerun named focused/race tests. This is acceptance of already-wired runtime behavior, not a claim that the original new tests naturally failed against HEAD.
- [x] Run full Go and dashboard gates; record the exact full-Go blocker above without checking the composite release gate. TypeScript, Vitest and `make ui` pass; generated assets have no diff. No schema/query generation is needed. Update operator/source-evidence documentation; no real URL, paid provider call or harness installation/update.
- [x] Commit only this verified test/documentation slice with a focused `feat:` message; leave broader task and release acceptance unchecked.

Ordered self-review:
1. Spec first: receiver/root acceptance only, extending `newModelRefresh`/`initDomainWithModelDependencies` consumers and existing lifecycle helpers. No runtime feature, option, scheduler, identity mapping or source-evidence claim is added. Retain D7 duplicates and D8 refusal. Production behavior already implements the exercised contracts, so no production patch is necessary.
2. Anti-green-theater second: real connection credentials, source membership/native IDs, stored candidate proofs, capability validator, shared clock/coordinator/ticker, journal, raw settings and event repository feed the real sender. Independently remove full-source enumeration, native spelling, candidate-proof persistence, selector validator, ticker work, manual coordinator ownership, per-cycle journal recovery, raw SendPart destination, event INSERT, final Announcer injection and shared announcement clock. Each run exits 1 at receiver/execution/durable/lifecycle assertions (not mocked callback counts); all changes are restored. Source and native mutations break receiver discoveries; ticker mutation explicitly misses the final Y card; coordinator removal leaves manual work alive after Close; validator/journal/proof mutations break model selection/publication/run sinks; event/raw-destination/Announcer mutations eliminate cards; clock mutation breaks persisted deadline and eventual delivery. These are controlled behavioral-red checks, not pre-implementation red claims.
3. Quality third: reuse neighboring root/runner helpers; introduce only per-model evidence overrides at the HTTP leaf, a synchronized test clock and real HTTP receiver. Snapshot actual startup report counts instead of assuming seeding makes one report. The multipart race gate initially outlasted the two-second small-fixture report wait: wait on its actual receiver sink with a bounded 30-second allowance for hundreds of real alias validations, then require durable completion. Remove fatal assertions from background transport callbacks, assert unknown-entry presence and every part's current destination, format Go and verify no mutation remains.

Fresh verification (after review):
- PASS: `go test ./cmd/arbitron -run 'TestModelAnnouncement|TestNextTickPublicationRecoveryRoot|TestModelProfilesRunnerCodexModesRoot|TestAutomaticCapabilitiesRoot|TestModelProfilesCloseJoinsManualAndTickerRoot|TestCatalogOpaquePathsNeverPersistConfirmationRoot|TestD8PinnedExecutionRoot|TestIdentityPolicyCannotEnableD8FollowingRoot' -count=1 -timeout=3m`.
- PASS: task-owned `go test -race -p 1 ./cmd/arbitron ./adapter/notification ./adapter/sqlite ./domain/modelcatalog -run 'TestModelAnnouncement|TestNextTickPublicationRecoveryRoot|TestModelProfilesRunnerCodexModesRoot|TestAutomaticCapabilitiesRoot|TestModelProfilesCloseJoinsManualAndTickerRoot' -count=1 -timeout=5m` (no data races).
- PASS: dashboard `npx tsc --noEmit && npx vitest run`; repository-root `make ui`.
- FAIL: full `go test ./... -count=1 -timeout=5m`, for the exact release blocker above. Announcement tests in that full run pass; no complete release is asserted.
- PASS: `git diff --check`. No production mutation, generated asset or unrelated change is staged.

## Current provider-provenance receiver control (2026-10-19)

- [x] Extend the existing full-source receiver root test with two catalog offerings for one canonical identity, different provider/tier/spelling, and only the later offering actually confirmed. P3T6 evidence.Provider → P3T5 migration-032 reopen/confirmingEntry → immutable payload → real Teams serializer must name that confirmed offering with its actual original changes (or none). A historical unconfirmed aggregator entry must not supply the card's tier/provider. Wrong identity/provider/config produces no new card; later paths, policy edits, model disappearance and restart do not create a second lifetime event or rewrite the first payload. Clear/load-bearing-input controls must fail at receiver bytes and durable state, not only at an intermediate event.
- [x] Rerun D10 Codex batch/interactive discovery through the root-built restricted launcher before final release, retaining actual account-cache/rollout evidence and new-run/receiver sinks. A runner that ignores network/MCP/plugin restrictions, a missing proof producer or a DeferredAnnouncer cannot pass release. Finish the full Go/dashboard/build gates after P4T4 and record fresh results; new acceptance boxes remain unchecked until verified.
  - [x] Independently confirm each restricted Codex mode at durable proof, serialized receiver and reopened new-run execution sinks; reject wrong/missing rollout, missing account cache and rejected restriction configuration. Verify sink failures when restrictions, rollout verification, raw destination or final announcer are removed, then restore every mutation.
  - [x] Run fresh focused, race, full Go, dashboard TypeScript/Vitest and repository-root `make ui` gates after P4T4. Verify the committed production revision plus this acceptance test independently of inherited P3T6 edits. These gates do not certify the still-unfinished non-Codex source contract.

> VOID (2026-10-21, see UNBLOCKED above) — BLOCKED (final release; updated 2026-10-20): P3T4's recorded non-Codex inherited tool/MCP/plugin isolation acceptance remains unfinished. The Codex receiver/new-run acceptance and fresh full Go/dashboard/build gates now pass as recorded below; only the unresolved source-contract acceptance prevents this composite final-release step from closing. Do not re-fail the recorded blocker or infer full release from these green gates.

## Confirmed-offering receiver acceptance (2026-10-20)

Selection: skip P3T4's recorded non-Codex isolation blocker and P3T7's P4T4-owned full-suite checkbox without re-failing either. Complete this task's independent provider-provenance receiver step only. Preserve and exclude the inherited uncommitted P3T6 plan, selector and test files.

- Extend the existing `TestModelAnnouncementAllDiscoveredCandidatesRoot`, not a parallel composition graph. The configured guarded source first retains an unconfirmed `ollama-cloud`/pro/`offering-v4.1` offering; a later OpenRouter/flash offering shares its canonical identity and is actually proved. A real persisted policy rule classifies the alternate native spelling. Actual SQLite/event/sender/HTTP receiver bytes must name OpenRouter/flash and the original worker X→Y change, never the historical unconfirmed offering.
- Before successful confirmation, wrong identity, wrong provider-qualified spelling and missing proof produce no event/card. Rotate the actual connection credential during an otherwise exact authenticated response: no card, event or profile advancement is accepted. A fresh four-hour tick confirms/commits/publishes Y. The old in-flight two-node run still executes X/X; a new run executes Y/Y.
- Add another real connection/path, edit the classification policy, remove/reintroduce the model, close/reopen SQLite and Start again. Both source offerings, unknown metadata and selector-stamped provider provenance survive reopen; additional path confirmation is durable. The same frozen event ID/time/provider/tier/paths/changes and exact single received Y card survive every transition, and a post-restart run executes Y. Check parsed card facts, safe path projection and exclusion of credentials, destination secrets and internal proof references.
- Run the expanded test before changing runtime behavior. Correct a fixture-only mistake first: `catalogFrozenPayloads` requires suppressed events, so it cannot inspect this configured receiver's announced events. The corrected test passes against the already-implemented runtime; no missing production behavior or pre-implementation behavioral red is claimed. Do not invent a runtime change solely to manufacture red.

Ordered self-review, completed before the final gates:

1. **Spec compliance first:** only this existing receiver acceptance test and task evidence change. No runtime API/option, mapping rule shipped to production, second assembly, schema/query, unrelated fixture repair or release waiver. The temporary test policy is supplied through the real persisted admin service. Remaining final-release work stays unchecked.
2. **Anti-green-theater second:** independently disable confirmed-offering selection, clear selector Provider, corrupt the serialized tier, omit original committed changes and remove the raw destination. Each exits 1: receiver provider/tier bytes are wrong, durable/reopened proof provenance is empty, the card tier is wrong, receiver worker changes are missing, or first-boot cards are absent. Strengthen change assertions to inspect receiver facts before a fatal payload-length assertion, and assert exact receiver path facts plus proof-reference exclusion. Restore all production files byte-for-byte, including inherited selector edits. Temporarily restore the committed selector from HEAD `6009238` and run the expanded receiver test five times green; restore the inherited selector afterward. This test does not depend on unstaged P3T6 work.
3. **Code quality third:** reuse the existing lifecycle HTTP leaves, common build/Start/Close, real settings/policy/connection services, actual MCP refresh and non-deduplicating receiver. Keep only HTTP substitutions, synchronized proof-I/O rotation, bounded waits, actual report-count barriers, parsed card facts and exact immutable payload comparisons. Release the hook mutex before fatal assertions, format Go and verify no mutation remains. No sqlc/UI generation, paid provider call, harness installation/update or real Teams destination was used.

Fresh post-review gates (all exit 0):

- Specified focused gate: `go test ./cmd/arbitron -run 'TestModelAnnouncement|TestNextTickPublicationRecoveryRoot|TestModelProfilesRunnerCodexModesRoot|TestAutomaticCapabilitiesRoot|TestModelProfilesCloseJoinsManualAndTickerRoot|TestCatalogOpaquePathsNeverPersistConfirmationRoot|TestD8PinnedExecutionRoot|TestIdentityPolicyCannotEnableD8FollowingRoot' -count=1 -timeout=4m`.
- Expanded receiver test: `go test ./cmd/arbitron -run '^TestModelAnnouncementAllDiscoveredCandidatesRoot$' -count=20 -timeout=3m`.
- `go test -race -p 1 ./cmd/arbitron ./adapter/notification ./adapter/sqlite ./domain/modelcatalog -run 'TestModelAnnouncement|TestNextTickPublicationRecoveryRoot|TestModelProfilesRunnerCodexModesRoot|TestAutomaticCapabilitiesRoot|TestModelProfilesCloseJoinsManualAndTickerRoot' -count=1 -timeout=5m`. SQLite/domain match no tests in this regex, so also run their full ordinary packages below; no data-race diagnostics.
- `go test ./adapter/notification ./adapter/sqlite ./domain/modelcatalog -count=1 -timeout=4m`.
- `go test ./cmd/arbitron -run '^TestUsabilityCodexD10RestrictionsRoot$' -count=1 -timeout=2m` and `go test ./adapter/codex -run '^TestCodexProbeD10Restrictions$' -count=1 -timeout=2m`, separately inspected.
- `go vet ./cmd/arbitron ./adapter/notification ./adapter/sqlite ./domain/modelcatalog`; gofmt; `git diff --check`.

The full Go/dashboard/build release gates are not rerun or claimed by that provider-provenance pass: the existing P4T4/P3T4 release prerequisites above remained unchecked. That pass committed only `cmd/arbitron/model_announcement_acceptance_test.go` and this task file; the fresh Codex/gate pass below supersedes its verification inventory, not the outstanding non-Codex source contract.

## Restricted Codex receiver/new-run acceptance (2026-10-20)

Selection: skip the unchanged P3T4 non-Codex isolation blocker, without repeating its failure, and complete P4T3's independent D10 and fresh-verification substeps. Preserve and exclude the inherited P3T6 plan, `domain/modelcatalog/candidates.go` and its two untracked test files.

`cmd/arbitron/model_announcement_codex_d10_test.go:TestModelAnnouncementCodexD10DiscoveryRoot` extends the existing build/Start/Close, MCP, ticker and actual non-deduplicating receiver fixtures. Each of batch and interactive modes must independently produce the first usable proof/card; the other mode always returns a fallback. The process leaf validates resolved binary, subscription auth/home, private cwd, exact model/exec/JSON arguments, identity-only prompt, deadline/reader ceiling, strict config, read-only sandbox, no network, disabled plugins/apps/search and per-server MCP overrides fed by enumeration of an actual hostile inherited config file. Account cache and correlated rollout are real files consumed by production readers. No stdout/request echo substitutes for rollout proof.

Five cases per mode cover exact, wrong executed model, missing turn, absent account cache and rejected restrictions. Failed startup checks persist no event/card. Restoring valid I/O then driving a real four-hour tick or registered manual refresh reaches the receiver with the original no-profile-change payload. Assert exact card model/provider/EventID/path facts, announced state, proof provenance, secret exclusion, reopen equality, unchanged-proof reuse and a fresh run's pinned model plus batch output or interactive assistant transcript.

Run the new assertions before any production change. Fixture-only failures exposed an incorrect assumption that RunSpec's ordinary response budget itself is clamped (the existing reader correctly consumes `procenv.ResponseLimit(ctx,configured)`), missing interactive orchestration/prompt scaffolding, checking an interactive WAIT's unset final Output instead of its durable transcript, and assuming a nonexistent other-mode path would retry. Correct these fixtures; the new acceptance then passes against the existing runtime. No absent production behavior or pre-implementation behavioral red is fabricated, and no runtime implementation is changed.

Ordered self-review:

1. **Spec compliance first:** only the independent D10 receiver/new-run acceptance, current verification evidence and source-evidence documentation change. No feature, option, scheduler, mapping, schema/query, unrelated suite repair or release waiver. Existing following X/Y and active-run stability fixtures remain in the focused gate; this added case specifically proves no-advancement discovery independently for each mode.
2. **Anti-green-theater second:** independently remove network, plugin and enumerated-MCP overrides, batch sandbox and interactive sandbox; each positive control fails at the missing receiver card. Bypass the shared rollout exact-model comparison: both wrong-model cases wrongly deliver cards and create durable events. Remove only the actual raw SendPart destination or replace the injected announcer with DeferredAnnouncer: both exact controls fail at the receiver. Restore every production file byte-for-byte; inherited selector edits remain unchanged. Assert durable reopened proof membership and exact receiver path facts, not only callbacks or an intermediate evidence DTO.
3. **Code quality third:** reuse neighboring lifecycle/receiver/runner helpers and existing production parsers. Keep leaf errors secret-free, synchronize process outcome/counters, bound waits, check actual `exec` and complete `mcp list --json` command inputs, and format Go. Reopen before starting the new execution so that shutdown/reopen is not coupled to a terminal engine-event race. Rerun focused and new race cases after those final test-strengthening edits.

Fresh results, all exit 0 after final corrections:

- Task focused gate: `go test ./cmd/arbitron -run 'TestModelAnnouncement|TestNextTickPublicationRecoveryRoot|TestModelProfilesRunnerCodexModesRoot|TestAutomaticCapabilitiesRoot|TestModelProfilesCloseJoinsManualAndTickerRoot|TestCatalogOpaquePathsNeverPersistConfirmationRoot|TestD8PinnedExecutionRoot|TestIdentityPolicyCannotEnableD8FollowingRoot' -count=1 -timeout=4m`.
- New acceptance repeated 20 times green; final `go test -race -p 1 ./cmd/arbitron -run '^TestModelAnnouncementCodexD10DiscoveryRoot$' -count=1 -timeout=3m` passes without race diagnostics.
- `go test -race -p 1 ./cmd/arbitron ./adapter/notification ./adapter/sqlite ./domain/modelcatalog -run 'TestModelAnnouncement|TestNextTickPublicationRecoveryRoot|TestModelProfilesRunnerCodexModesRoot|TestAutomaticCapabilitiesRoot|TestModelProfilesCloseJoinsManualAndTickerRoot' -count=1 -timeout=6m`. SQLite/domain match no tests under that regex, so separately run `go test ./adapter/notification ./adapter/sqlite ./domain/modelcatalog -count=1 -timeout=4m` green.
- `go test ./adapter/codex -run '^TestCodexProbeD10Restrictions$' -count=1` and `go test ./cmd/arbitron -run '^TestUsabilityCodexD10RestrictionsRoot$' -count=1 -timeout=2m` each pass.
- Full `go test ./... -count=1 -timeout=10m` passes in the working tree. Repeat after final test strengthening in an isolated detached worktree at `acefac8e` plus only this new acceptance file: all packages pass, including cmd/arbitron (133.430s). Thus no inherited P3T6 change/test is required for this slice or its full-Go gate. Remove the verification worktree afterward.
- `go vet ./cmd/arbitron ./adapter/codex ./adapter/notification ./adapter/sqlite ./domain/modelcatalog`, gofmt and `git diff --check` pass.
- Dashboard `npx tsc --noEmit` and `npx vitest run` pass (50 files, 526 tests); repository-root `make ui` passes with no generated diff. Install locked dashboard dependencies with `npm ci` first; rerun dashboard tests sequentially after make's own npm ci (parallel deletion of node_modules is not a code failure). Existing npm audit reports 9 dependency vulnerabilities and Vite warns about a >500kB chunk; no lockfile or dependency repair is part of this task.

These are fixture I/O acceptance and build results, not a new upstream CLI contract or paid model smoke test. No harness installation/update, real Teams URL, schema/query regeneration or runtime change. Commit only the new Codex test, source-evidence documentation and this task evidence. Full-task/release boxes stay unchecked solely for the recorded P3T4 non-Codex isolation acceptance.

## Final release verification (2026-10-21)

The composite release boxes were open solely for P3T4's OpenCode isolation. D12 closes it, and `TestUsabilityOpenCodeD12IsolationRoot` drives the OpenCode route through the real refresh, ticker, factory and receiver to a durable proof, a card and an ordinary pinned run. They are now checked.

The branch was merged with origin/main (#76 parallel_group, #79, #82, #83 Windows removal) and its migrations were renumbered to 023–037. The merge needed three production corrections, each with tests:
- branch-kind group tasks inherit the parent's pins (D1);
- resumed conversations use the live binding's tool scope;
- workflow seeding and registry reconciliation also run in Start, after config profiles are seeded.

Fresh results at `f02d34af`, all exit 0:
- focused gate (plus `TestUsabilityOpenCodeD12IsolationRoot` and `TestUsabilityConfirmedExactModel`);
- full `go test -count=1 ./...` (48 packages) and `go vet ./...`, with `gofmt -l` clean;
- dashboard `npx tsc --noEmit` and `npx vitest run` (67 files, 839 tests);
- `adapter/dashboard/dist` matches a fresh `npm run build` of the source.

No real Teams URL, paid model call or harness install/update was used.
