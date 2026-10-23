# Latest Model Profiles — Phase 4 Task 1: Synchronous model card sender and delivery evidence contract Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Synchronous model card sender and delivery evidence contract.

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

**Depends on:** P1T1 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md`), P3T2 guarded client and P3T5 payload types.

**Files:** Create `domain/modelcatalog/announcement_sender.go`, `adapter/notification/model_announcement_sender.go`, tests. Reuse `adapter/notification/teams_payload.go` structures and shared guarded client, without using asynchronous TeamsNotifier.Notify.

## Interfaces (owned here unless explicitly consumed)

Produce `SendOutcome{State,Code string}` states `confirmed`, `definite_failure`, `ambiguous`; `ModelAnnouncementSender.Send(context.Context,string,AnnouncementPayload) (SendOutcome,error)` and `SendPart(context.Context,string,AnnouncementPart) (SendOutcome,error)`. URL is the raw internal setting; stable EventID is retained for local correlation, with no receiver idempotency claim. `NewTeamsModelSender(*http.Client)` requires a bounded guarded client; any 2xx is confirmed (D7). No Reconcile/receipt/receiver-contract collaborator is consumed or required.

## Data producers and composition-root wiring

P3T5 durable immutable event payload supplies provider/tier/first time/paths/original profile changes; P1T1 internal raw webhook supplies destination; actual synchronous HTTP response supplies delivery outcome; 2xx confirms, non-2xx or transport fault remains retryable. P4T3 constructs sender/service with production guard. Dedicated card names all original alias from/to changes, or explicitly no profiles changed; unknown tier is identified as unknown.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestModelCardOriginalChanges` HTTP receiver checks one card with stable EventID, canonical provider/model and all original changes.
- Success: no-followers payload explicitly says no profiles changed; escapes hostile names; sender waits for classified response.
- Failure: definitive rejection maps definite_failure; empty destination never sends; no default supplied webhook.
- Error: SSRF/redirect/body cap, timeout after receiver acceptance, response loss or uncertain outcome maps ambiguous; raw URL absent from errors/logs.
- Edge: multiple paths share card; unknown tier visible; response cap and destination change; duplicate EventID is not suppressed by a fake receiver; accepted-then-lost response may produce a duplicate on a later retry.
- Input guard: receiver verifies non-empty exact secret destination and actual serialized immutable payload; fake sender inspects EventID/changes, never ignores them. Tests drive the real HTTP serialization/status classifier; no invented receiver deduplication protocol is used.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, payload.EventID, receivedEventID)
assert.Equal(t, "ambiguous", acceptedThenLostResponse.State)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

Synchronous POST: timeout 15s, payload/response cap 28KiB, no automatic redirect/retry of POST; guard outbound address. Classify transport outcome conservatively: timeout/write uncertainty or uncertain response is ambiguous, not definite failure. Do not return raw url.Error/response bodies that leak secret. Definitive failures retry later; any received 2xx is confirmed even with an unread/oversized response body; failures and ambiguity retry through P4T2 until delivered. No header/event ID makes Teams idempotent; Reconcile is removed by D7. Tests cover 200/201/202/204, non-2xx, timeout/connection reset and accepted-then-lost response without a deduplicating receiver.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

**Focused gate:** `go test ./adapter/notification -run "ModelAnnouncement|ModelCard" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes. The verified sender prerequisite is committed separately from the blocked root acceptance above.

## Provenance / release gate (resolved 2026-10-01)

Provenance gates resolved 2026-10-01 (D7 in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`): implement and test the chosen rules exactly as recorded there; do not substitute synthetic evidence for the real probe/catalog/delivery behavior they define.

## Payload bounds and complete change coverage

Every committed alias/from/to change must reach the receiver; never silently truncate a changes array to fit a card. Produce deterministic `AnnouncementPart{EventID string; Index,Count int; Payload AnnouncementPayload}` via `PartitionAnnouncement(AnnouncementPayload, maxBytes int) ([]AnnouncementPart,error)` if serialization exceeds 28KiB. The default single part is unchanged; multiple numbered cards retain canonical identity/first time and partition all original changes. P4T2 durably tracks confirmed part indexes in the event and retries only unconfirmed parts; ambiguous part sends may duplicate. If a single item cannot fit, return a classified payload error and preserve pending state with an actionable diagnostic. Tests use enough committed changes to cross the boundary and assert their exact union at the receiver, without dropped/duplicated changes on the ordinary successful path. Sender's per-POST cap remains 28KiB; HTTP response reading is bounded independently.

## Bounded serialization and secret-free card paths

Once response headers establish a 2xx, body read/close faults cannot revoke D7 confirmation; boundedly drain or close the body, never parse it as a receipt. If no status is received after a possible write, classify ambiguous. Add 202-with-oversized-body and 204/body-read-error success tests alongside timeout-before-headers ambiguity. This resolves the earlier generic response-cap error case without changing D7.

Serialize only safe path identifiers/config revisions from payload.Paths; never emit ProofRef/raw account paths/credential closures, configured catalog URLs, raw destinations or proof files. Test with secret-bearing ProofRef fixture values and assert their absence from receiver card and logs. Include all original profile changes and all advertised paths within the partitioner's full serialized 28KiB accounting (JSON escaping/envelopes/index/count included). For large path lists as well as changes, partition safe paths or return an explicit retained-payload error for an oversized indivisible item; never silently drop them. EventID + numbered parts identifies one durable discovery, not multiple new events.

## Independent synchronous sender prerequisite (2026-10-16)

- [x] Add behavioral tests for original changes/EventID/time/provider/model, no followers/unknown tier, hostile names, synchronous response handling, 200/201/202/204, rejection/redirect, guarded destinations, timeout/reset/accepted-response loss, secret-free errors and independent response-read bounds. Exercise non-deduplicating real HTTP receivers and exact multipart union (350 paths and 350 changes), including escaping/envelope/numbering in each POST's 28KiB cap. Reject missing payload identity, malformed parts, and oversized indivisible paths/changes with static actionable diagnostics.
- [x] Run the focused gate red for missing APIs, then behavioral red with API stubs. Run the formerly blocking receiver/wait/timeout cases separately with bounded test waits; observe absent receiver calls and missing ambiguous outcomes. During review, additionally observe red for the indivisible-item diagnostic and required non-nil guarded client before implementing those fixes.
- [x] Implement domain SendOutcome/AnnouncementPart/ModelAnnouncementSender and shared byte bound; implement dedicated synchronous TeamsModelSender plus safe rendering/PartitionAnnouncement in adapter/notification. Require the caller's guarded client, clone its configuration, cap timeout at 15s, disable redirects and POST replay, preserve any received 2xx despite body faults, and never return transport/response secrets. No asynchronous TeamsNotifier, receipt protocol, deduplication, retry loop, setting default, database migration or speculative root service is added.
- [x] Strengthen producer-to-sink coverage with TestModelAnnouncementPersistedOriginalPayloadToReceiver: real migrated SQLite CommitCycle performs actual alias CAS/journal/event creation (zero or two followers), current aliases change afterward, DB reopens, and the original durable payload plus actual Settings.ReadInternal destination reach a real HTTP receiver. Changing the destination changes only routing, not card history. The network substitution rejects any missing/redacted/stale destination. This boundary test does not claim the blocked root/drain acceptance.
- [x] Complete ordered self-review: spec compliance first (including removing an unrequested nil-client fallback); anti-green-theater second (temporarily omit production path serialization and observe ordinary, multipart and persisted receiver-sink failures, then restore it); code quality third (reuse neighboring card structures/outbound guard, keep safe rendering/byte-accurate partitioning together, avoid adapter imports in domain, and fix response-loss test shared state with a channel/atomic counter). Format and rerun the gates after all fixes.

### Verification and downstream handoff

`notification.PartitionAnnouncement` lives beside the actual Teams serializer; P4T2's SQLite repository calls it before persisting the first-claim plan, while its domain orchestration consumes the domain sender port/part types. This keeps Teams envelope/escaping knowledge out of domain and guarantees the partitioner's exact per-POST accounting. All part indexes are one-based. P4T3 must supply `outbound.NewGuardedClient` at the real composition root; constructor injection in these tests is not that release wiring.

Post-review gates:

- `go test ./adapter/notification -run 'ModelAnnouncement|ModelCard' -count=1` — green.
- `go test ./domain/modelcatalog ./adapter/notification ./adapter/outbound -count=1` — green.
- `go test -race ./adapter/notification ./adapter/outbound ./domain/modelcatalog -count=1` — green.
- `go test ./adapter/sqlite -run 'Catalog|Cycle|Publication|Settings' -count=1` — green.
- `go test ./cmd/arbitron -run 'ModelProfiles|Settings|ModelCatalogPublication' -count=1 -timeout=5m` — green (existing root regressions, not missing announcement wiring).
- `git diff --check` — green. No schema/query/UI changes require generation or embedding.

The broader SQLite gate still fails existing instance visibility/origin/workflow-ID/queue fixtures that create legacy version-0 snapshots (`new instances require a version 1 snapshot; legacy version 0 is read-only`). This pass changes no SQLite/instance code and does not widen scope to repair those unrelated tests. No harness was installed/upgraded and no real Teams URL was supplied. Full task completion remains blocked on the explicitly owned root/service producers above.

## Current sender integration ownership

TeamsModelSender/PartitionAnnouncement and the durable service/repository now exist, and `cmd/arbitron/model_refresh.go:newModelRefresh` already constructs them. Preserve the synchronous serialized HTTP boundary and its exact-size/2xx/secret-free tests. P4T3 owns missing startup/tick/manual receiver plus announced-state/executed-model acceptance; it extends existing `model_announcement_wiring_test.go`, rather than blocking implementation of this task or creating a second sender. Unknown-tier discoveries must render the native/canonical model with an explicitly unknown tier and no fabricated profile changes.

## Sender task closure (2026-10-18)

The remaining test-writing checkbox was stale: the named sender/card tests, migrated-SQLite original-payload receiver test, and actual startup receiver test already exist. Their original red/green sequence is recorded above; this pass adds no speculative behavior, test duplicates or manufactured implementation red. Current root construction supplies the guarded client, durable repository and raw internal setting before startup. The historical missing-root blocker above no longer applies to this task. Full startup/tick/manual multipart and executed-model release acceptance remains owned by P4T3, not waived here.

Ordered self-review:
1. Spec compliance: checked D7, the Phase 3 closure map, sender/partitioner contracts and every Happy/Success/Failure/Error/Edge bullet against existing tests. The tests cover original changes, no followers/unknown tier, escaping, exact destinations, bounds, status classification, redirects/SSRF, synchronous completion, secret-free diagnostics and accepted-loss duplicates. No additional production feature is required.
2. Anti-green-theater: temporarily removed card path serialization; the ordinary, multipart (350 expected paths versus zero received) and migrated-SQLite receiver tests failed at the serialized sink. Restored it. Separately removed the raw destination passed to the sender; `TestModelProfilesAnnouncementInstalledBeforeStartupRoot` failed with zero received cards and durable `pending` rather than `announced`. Restored it. These are coverage controls, not claims of newly absent production behavior.
3. Code quality: compared the existing serializer with neighboring Teams payload structures and checked bounded client cloning, safe evidence projection, shared constants and domain ports. No code changes were needed; all temporary mutations are restored. No schema/query/UI changes require generation.

Fresh verification after restoration (all exit 0):
- `go test ./adapter/notification -run 'ModelAnnouncement|ModelCard' -count=1`
- `go test ./domain/modelcatalog ./adapter/notification ./adapter/outbound -count=1`
- `go test ./adapter/sqlite -run 'Catalog|Cycle|Publication|Settings' -count=1`
- `go test ./cmd/arbitron -run '^(TestModelProfilesAnnouncementInstalledBeforeStartupRoot|TestModelProfilesCloseJoinsManualAndTickerRoot)$' -count=1 -timeout=3m`
- `go test -race ./adapter/notification ./adapter/outbound ./domain/modelcatalog -count=1`
- `git diff --check`

Selection skipped P3T4's recorded unresolved Codex tool-free contract and P3T7's recorded full-suite fixture verification blocker without re-failing either. Neither prevents this independent sender closure. No full-suite or final-release success is claimed.
