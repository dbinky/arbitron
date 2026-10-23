# Latest Model Profiles — Design Phase 4: Announcements

> **Current alignment (2026-10-19):** D1–D11 and the current closure map in Phase 3 govern implementation. Historical completion/blocker records are not a present inventory. Existing runtime producers are extended in place; upstream producer completion never waits for downstream acceptance. D8 permits native Azure/Bedrock configured-ID pinned execution while rejecting following/usability claims.

Spec: `docs/specs/2026-09-29-latest-model-profiles-spec.md`
Depends on: Phase 1 internal raw setting; Phase 3 durable model events,
advancement journal, RefreshReport and Announcer. Decisions D7 is authoritative.

## Goal

When Teams is configured, each newly confirmed usable canonical model produces
at least one announcement identifying its original committed profile changes,
or explicitly no profiles changed. Discovery includes paths with no followers.
The empty default sends nothing. Failures preserve original events, working
profiles and run pins; a rare duplicate after ambiguous delivery is accepted.

## Architecture and shared contracts

| Unit | Responsibility / owner |
|---|---|
| AnnouncementService | Implements Announcer, wakes after every refresh result, and drains durable events at startup and each four-hour tick including discovery failure (P4T2/P4T3). |
| ModelAnnouncementSender / Teams adapter | Synchronous bounded guarded POST; any 2xx confirms, failures or uncertain transport outcomes retry later (P4T1). No Reconcile or receiver-idempotency collaborator. |
| Durable event store | Unique canonical identity, immutable original payload, eligibility, claims, retry timing and part acknowledgements (P3T5/P4T2). |
| PartitionAnnouncement | Deterministic numbered card parts if all original changes exceed per-POST bounds; no silent change truncation (P4T1/P4T2). |

Existing `TeamsNotifier.Notify` dispatches asynchronously and cannot confirm
external delivery; use the dedicated sender with shared Adaptive Card structures
and guarded transport. Construct it at actual initDomain before recovery/loops.
The current `newModelRefresh` root already constructs the concrete durable service.
P3T7 preserves that injection before Start; P4T3 verifies it through the same
serve/MCP lifecycle. DeferredAnnouncer is limited to explicitly staged tests and
cannot satisfy the release receiver sink.

## Discovery and delivery contract

- First confirmed usability plus event and original committed advancements are
  recorded atomically. Raw setting configured -> pending; empty -> suppressed;
  read fault -> awaiting_setting. First configured refresh announces too.
- Freeze canonical ID/provider/tier (unknown allowed), first-confirmed time,
  paths and actual alias/from/to changes. Retry never reconstructs changes from
  today's profile or a later report with no advancements.
- Deduplicate discoveries by canonical identity. Additional paths, temporary
  disappearance, restart or webhook changes create no new event.
- Empty at discovery suppresses forever; later enable does not replay it.
  Clearing a webhook pauses existing pending events. Read faults never clear,
  suppress or mark delivered. awaiting_setting classifies on first successful
  read while preserving observation/payload.
- Atomically claim a due pending/delivery_unknown event with ClaimID, a 60s
  lease and incremented attempt count. One process drains sequentially; DB
  claims coordinate processes. Send with the current raw internal destination.
- Any 2xx + saved marker -> announced; non-2xx/definite failure -> pending;
  timeout/reset/ambiguous result -> delivery_unknown. Retry both failure states
  with persisted backoff (four hours, doubling to 24 hours) at later drains.
- Crash/marker-write failure after acceptance leaves in_flight; expiry recovers
  delivery_unknown and retries. Never mark before send. D7 explicitly accepts
  possible duplicate delivery; no receiver receipt lookup is required.
- For multipart payloads, persist partition plan and confirmed indexes. Retry
  only unconfirmed parts and mark the event announced once all parts confirm.
  Ambiguous parts may duplicate; every original profile change must reach Teams.

## Data flow

```text
first usable model + actual CAS changes -> durable immutable event
  -> startup / refresh completion / four-hour drain (even fetch failure)
  -> raw setting -> claim due event -> synchronous bounded Teams POST
       2xx + marker -> announced
       failure     -> pending -> persisted backoff -> retry
       ambiguous   -> delivery_unknown -> persisted backoff -> retry
       crash       -> expired claim -> delivery_unknown -> retry
empty at discovery -> suppressed; read fault -> awaiting_setting
```

## Data provenance and wiring

| Input | Existing source / planned producer |
|---|---|
| Raw current destination | P1T1 settings.Service.ReadInternal; P1T2 admin HTTP/MCP/dashboard supplies persisted value. Public redaction is never sender input. |
| Canonical provider/tier/version and path confirmation | P3T1/P3T8 identity policy + P3T2 parsed catalog + P3T4 exact-model probes, persisted by P3T5. |
| Original profile changes | P2T3 commitAdvanceTx actual results in P3T5 atomic cycle/journal; no read of current profile substitutes historical evidence. |
| Event eligibility, frozen payload and recovery | P3T5 migration 022; P4T2 claim/retry/part methods extend that schema or reserved 026 if shipped. |
| Cards and outbound guard | Existing adapter/notification/teams_payload.go structures; P3T2 outbound guarded client; P4T1 dedicated serialized model sender. |
| Attempt/claim time and ID | Standard time.Now/UUID producer and injected clock; P4T2 persists attempts, NextAttemptAt, ClaimID/LeaseUntil. |
| Delivery outcome | P4T1 actual synchronous HTTP status/transport result; D7 defines 2xx and ambiguity/retry semantics. |

P4T3 constructs repo/service/sender with actual DB/settings/transport/clock at
initDomain and injects Announcer before any loop/recovery starts. Calls at every
cycle completion/failure and startup drain retry queues even with no new usable
delta. One shared cancellation/join lifecycle closes the loop before DB shutdown.
No optional connCrypto, encrypted original URL, receiver Reconcile or fictitious
idempotency contract is an input after D7. Existing model-state key remains for
P3T3 revision fingerprints.

## Error handling

Delivery and classification faults preserve events and are separate from refresh
status. Do not log raw webhook URLs, response bodies or raw url.Error values;
use static classified codes/redacted diagnostics. Delivery does not undo model
advancement or change run pins. Invalid/oversized single items retain pending
state with an explicit payload diagnostic; ordinary large changes lists are
partitioned rather than silently omitted. Suppressed events remain suppressed.

## Testing — strict TDD

Write each failing behavioral test, observe red, implement to green and refactor.

- Happy: one ordinary newly usable model with several original changes reaches
  the real serialized receiver card and announced state; later drain sends zero.
- Success: no-followers card, first configured refresh, canonical multi-path
  deduplication and multipart union containing all original changes.
- Failure: empty discovery suppresses; clearing pauses pending; non-admin cannot
  redirect; rejection leaves original payload pending for next eligible drain.
- Error: SSRF/timeout/reset, setting/store faults, accepted-response loss,
  marker failure, crash/restart/expired lease and concurrent claims. A receiver
  without deduplication eventually sees original payload; accepted loss may
  produce a second card, as D7 permits.
- Edge: discovery failure at retry, no new delta, destination clear/reconfigure,
  awaiting_setting recovery, suppressed backlog, later paths/reappearance,
  part-confirmation restart and raw URL redaction.

Use migrated SQLite, actual composition-root assembly and I/O-leaf substitution
only. Fakes inspect raw destination, EventID, part numbering and exact frozen
changes. Integration assertions include receiver content/count and durable
announced state plus new/active-run executed model sinks. Remove each production
producer in turn and require the corresponding sink test to fail.

## Implementation alignment

- P4T1: [synchronous sender](../plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-1.md).
- P4T2: [claims and retries](../plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-2.md).
- P4T3: [root wiring and release](../plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-3.md).

No unresolved delivery-policy dependency remains: D7 chose at-least-once.
The Phase 3 execution/coverage map is authoritative for producer ordering.

P4T2 Complete/CompletePart take explicit time from the injected Clock.Now so backoff is reproducible and persisted; the repository never invents a second wall clock. The first claim calls P4T1 PartitionAnnouncement and persists its full plan before I/O. A confirmed nonfinal part retains in_flight ownership; only the final part marks announced. Raw destination is re-read before each part; empty/fault pauses further sends. P4T1 treats received 2xx as confirmed even if body reading fails/exceeds its cap, and projects safe path fields instead of posting internal ProofRef/account paths. P4T3 verifies these producer-to-receiver/durable-state contracts. P3T7 splits build/start so final announcer injection precedes all startup work; no announcement scheduler is duplicated.

## Current root delivery completion

newModelRefresh already constructs ModelAnnouncementRepo, TeamsModelSender and AnnouncementService. P4T3 extends that exact site; P3T7 separates build/start and passes one Now/NewTicker clock to all consumers, P3T6 makes Announcer mandatory and drains on failed discovery/publication recovery. Full-source older/another-tier/unknown-tier candidates on follower-consumed paths must reach a receiver, not just newest eligible replacements. Native D8 pinned execution is permitted but contributes no confirmed usability/announcement. The final sink includes external card bytes/count, durable delivery state/parts/backoff and old/new executed model IDs. Dropped inputs or no-op announcers cannot satisfy release.
