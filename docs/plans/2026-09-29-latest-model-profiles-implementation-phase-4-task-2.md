# Latest Model Profiles — Phase 4 Task 2: Durable claims, announcement retries and recovery Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Durable claims, announcement retries and recovery.

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

**Depends on:** P3T5 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-5.md`), P3T6 Announcer and P4T1 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-1.md`).

**Files:** Create `domain/modelcatalog/announcements.go`, tests, `adapter/sqlite/model_announcement_repo.go`, tests; extend unshipped migration 022 event schema, or reserved migration 026 with down pair if 022 has shipped; do not reuse existing 024 or P3T8-owned 025.

## Interfaces (owned here unless explicitly consumed)

Produce `AnnouncementStore.ClassifyAwaiting(context.Context,bool) error`, `ClaimPending(context.Context,time.Time,time.Duration) (Event,error)`, `Complete(context.Context,string,string,SendOutcome,time.Time) error`, `RecoverClaims(context.Context,time.Time) error`; state and claim ID are checked atomically. Implement `AnnouncementService.Announce(context.Context,RefreshReport) error` and `Drain(context.Context) error`. Constructor requires store, settings internal reader, sender and clock. P3T5 Event adds `Attempts int`, `NextAttemptAt time.Time`, `LastCode string`; P4T2 supplies transitions, timestamps and atomic retry updates, extending unshipped migration 022 (if already shipped add reserved 026, since 024 is connection_revision_floor and 025 is P3T8 policy).

`ClaimPending` selects due pending or due delivery_unknown, atomically increments Attempts and sets ClaimID/in_flight/LeaseUntil. `Complete` verifies EventID/ClaimID: confirmed -> announced; definitive failure -> pending; ambiguous -> delivery_unknown. Both retryable states get persisted backoff; start at four hours, double per failed attempt up to 24 hours, checked at later startup/refresh drains. `RecoverClaims` changes expired in_flight to delivery_unknown with retry eligibility, preserving original payload and attempt count. Marker failure after successful send leaves the claim to expire and retry; a duplicate is accepted under D7. No receiver reconciliation, original destination recovery, encrypted DeliveryAttempt or Crypto injection is consumed. The existing P3T3 secret remains for execution fingerprints. Current raw destination is read for every attempted send.

## Data producers and composition-root wiring

SQLite pending/delivery_unknown frozen events, not report.NewlyUsable, feed Drain. Settings.ReadInternal supplies the current destination; an empty value pauses retryable events and read errors preserve them. Clock/UUID producers supply claim IDs, 60s leases, attempt times and backoff; sends are bounded to 15s with one sequential drain per process. Atomic DB claims coordinate multiple processes. Synchronous sender status/transport results feed Complete; P4T3 constructs store/service and attaches startup/scheduled/manual drain including discovery failure. No component reads a redacted public URL or treats a settings fault as disabled.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestAnnouncementDurableOriginalPayload` claims first configured observation, sends exactly original changes, marks announced and repeated drain sends nothing.
- Success: restart retries definitive failure even next catalog fetch fails; no-follower event works; awaiting-setting classification retries retained observation.
- Failure: empty setting pauses pending, discovery-disabled backlog stays suppressed; incorrect claim ID cannot complete another worker's event.
- Error: settings/store faults, crash before/after send, accepted-response loss, marker failure and expired lease preserve retryable delivery_unknown; next eligible drain sends again and can create an accepted duplicate.
- Edge: concurrent drains, clear/reconfigure destination, later path/reappearance, restart between CAS/event/report and delivery; same canonical identity never creates second event.
- Input guard: sender fake checks raw URL/EventID/frozen payload against durable row; removing payload/destination producer must fail. Recovery test uses a receiver that accepts duplicate EventIDs; asserts eventual external delivery and announced sink, and exactly two identical original cards after accepted-response loss plus retry. Ordinary success still sends once.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, "announced", reopenedEvent.State)
assert.Equal(t, 1, receiverCardCount)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

Empty/read-failed current setting pauses pending events, never suppresses/announces them. Empty at first discovery stays suppressed forever. Awaiting_setting classified on first successful read, preserving payload/time. Claim due pending/delivery_unknown -> in_flight, persist claim/lease/attempt before outbound call. Any 2xx+marker -> announced; non-2xx/definite failure -> pending; timeout/reset/ambiguous -> delivery_unknown. Both failures retry with persisted backoff at later refreshes (D7). Expired in_flight -> delivery_unknown; marker failure remains in_flight until lease recovery. Retry with the current configured webhook and unchanged payload, including after a destination edit. Never mark before send or discard ambiguity to avoid duplicates. Drain failures are separate from refresh success.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

**Focused gate:** `go test ./domain/modelcatalog ./adapter/sqlite -run "Announcement|Claims" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Independent durable store/service portion (2026-10-16)

- [x] Write migrated-SQLite claim tests for original payload, atomic concurrent connections, wrong/stale ownership, fixed-time persisted backoff/reopen, expiry/renewal, multipart acknowledgement/retry, awaiting-setting/suppression, invalid payload deferral and database marker faults. Add an applied-022 upgrade test for reserved migration 026.
- [x] Run the exact focused gate before implementation. Observe missing-API compilation failures, then behavioral red with API stubs: pending events cannot be claimed, awaiting events never classify, attempts/plans are absent and no receiver cards arrive. Do not count fixture or compilation errors as behavioral red.
- [x] Implement the owned AnnouncementStore/AnnouncementService contracts and SQLite transitions. Persist the full 28KiB partition plan before sending; claim atomically, renew per part, protect all completion writes by owner/lease, retain confirmed indexes and frozen payloads, and persist explicit-clock 4h/8h/16h/24h backoff. Announce drains durable events rather than report.NewlyUsable.
- [x] Write/run TestAnnouncementDurableOriginalPayload and service boundary tests through real migrated CommitCycle/profile advancement/settings/reopen/Teams serializer/HTTP receiver. Cover no followers, later profile edits, definite failure, accepted-response loss, failed marker/crash recovery, clearing/reconfiguring destination between parts, current raw destination, concurrent drains, slow multipart renewal across independent connections and renewal faults.
- [x] Observe service behavioral red before its implementation; then pass the exact focused gate and related catalog/publication/migration, notification/root and race gates. During review, observe a separate red for an invalid payload EventID repeatedly remaining immediately claimable; defer the actual stored row by its SQL ID and rerun green.
- [x] Complete ordered spec-compliance, anti-green-theater and code-quality reviews; rerun the task gate after fixes. Commit only this verified portion and its evidence; full root/lifecycle acceptance stays unchecked above.

### Execution evidence

- Selection: earlier unfinished tasks still record missing exact-model mappings or discovery/refresh/root prerequisites. P4T1's sender/partitioner and P3T5's committed-event producer now exist, so P4T2's owned persistence/service portion is independently implementable. Do not retry or certify the earlier blockers.
- Red: `go test ./domain/modelcatalog ./adapter/sqlite -run 'Announcement|Claims' -count=1` first identifies missing fields/ports, then fails behaviorally with stubs. Repository tests observe no claims/classification/plans; service tests observe no receiver cards or errors/recovery. The actual migrated event, settings and profile producers are used; no report delta is substituted for the durable queue.
- Spec review first: implement only P4T2 ownership, preserve applied 022 and add 026 with the repository's forward-only column-addition down convention. Keep the task's exact multipart/time/claim contracts, current destination and D7 duplicates. Full production lifecycle construction/feed is blocked above, not accepted as a no-op. The service exposes the required Announce/Drain methods using narrow structural clock/internal-reader inputs; it does not implement P3T6's missing refresh loop or invent its runtime.
- Anti-green-theater second: assert actual receiver bytes, exact destination URI, EventID, numbered-part count/index and complete original change union, plus durable state/attempts/confirmed indexes after reopen. Real profile changes after discovery cannot reconstruct the card. Accepted-response loss and marker failure produce exactly two identical original cards and eventual announced state. A deliberately older, valid stored partition plan differs from today's partitioner; recomputing it on retry fails the persisted-plan sink. Temporarily removing raw destination input or per-part renewal also reproduces red; all mutations are restored.
- Quality third: read neighboring repository/migration/sender conventions; use short SQLite write-first transactions for cross-connection claims, one sequential cancellable drain, existing UUID/partition/serializer helpers, explicit fixed-width UTC timestamps and named backoff bounds. Secret-bearing errors are not exposed or stored in LastCode. Fix the historical migration-gap test to omit 026 until its missing 022 dependency is applied. Invalid event identity now defers the actual stored row rather than hot-looping on a payload ID. No sqlc schema/query inputs or UI assets changed.
- Green gates: exact focused gate; full domain/modelcatalog, adapter/notification and cmd/arbitron suites; `go test ./adapter/sqlite -run 'Catalog|Cycle|Publication|Profile|Advance|ValidatedSelection|Migration' -count=1`; `go test -race -p 1 ./domain/modelcatalog ./adapter/sqlite ./adapter/notification -run 'Announcement|Claims|Catalog|Cycle|Publication' -count=1 -timeout=8m`; `git diff --check`.
- Broader concern: full adapter/sqlite retains the same 18 instance/queue/visibility/origin fixtures creating new rows without version-1 model pins. Each fails with `new instances require a version 1 snapshot; legacy version 0 is read-only`. Announcement/catalog/profile/migration gates pass; do not weaken pin persistence or change unrelated fixtures in this task.

## Provenance / release gate (resolved 2026-10-01)

Provenance gates resolved 2026-10-01 (D7 in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`): implement and test the chosen rules exactly as recorded there; do not substitute synthetic evidence for the real probe/catalog/delivery behavior they define.

## Multipart producer/consumer contract

Consume P4T1's deterministic PartitionAnnouncement and AnnouncementPart. Extend Event with `Parts []AnnouncementPart`, `PartCount int` and `ConfirmedParts []int`, implemented here with matching SQLite JSON columns. The first claim computes/stores the full deterministic partition plan under the transaction before send; restart uses stored Parts, never repartitions already-acknowledged indexes under a changed implementation. `Send` accepts each part's Payload with card numbering passed through `SendPart(context.Context,string,AnnouncementPart)` on ModelAnnouncementSender (P4T1 owns this method); the single-part path delegates identically. Add `CompletePart(context.Context,string,string,int,SendOutcome,time.Time) error`. A confirmed part is immutable; the event becomes announced only when all parts are confirmed. Failure/ambiguity releases the claim into its retry state; retry begins at the first unconfirmed part. Read the current raw setting immediately before each part and retain the same frozen payload. Add restart/backoff/response-loss tests checking the receiver union contains every committed change and confirmed parts are not resent in normal recovery. P4T3's root test passes a large durable event through this path; no fake may ignore part index/count or payload.

Produce `AnnouncementStore.RenewClaim(context.Context,string,string,time.Time,time.Duration) error`: before each part, atomically verify owner and extend the 60s lease; stop without sending another part if ownership is lost or renewal fails. Each POST remains <=15s even for large multipart events. Completion/renewal cannot let an old worker mark a newer worker's claim. Tests slow part sends and run concurrent drains to verify ordinary ownership yields one union; crash/ambiguous-send duplicates remain the explicit D7 exception.

For multipart events, Complete with confirmed is allowed only after all stored part indexes are confirmed; otherwise reject without changing the event. CompletePart records one matching part index and sets announced only for the complete union. Test that a single successful first part cannot mark a multi-part event delivered.

## Retry-time and multipart construction producers

Complete/CompletePart receive `at` from the root-injected shared Clock.Now; compute NextAttemptAt from that value and the durably incremented Attempts, never an implicit repository time.Now. Zero/missing completion time is rejected. ClaimPending/RecoverClaims/RenewClaim already receive clock time explicitly. Add tests fixing time at T, proving retry deadlines T+4h, T+8h and capped T+24h survive reopen and do not depend on wall time.

Implement `NewModelAnnouncementRepo(*sql.DB)` here. Its first-claim transaction calls P4T1 PartitionAnnouncement with 28KiB and stores Parts/PartCount before claiming/sending; it does not require an undefined partition-service injection. A payload error leaves the immutable event pending with a static LastCode and bounded retry eligibility; it cannot hot-loop inside Drain. Clock-produced completion/backoff plus durable part plan are verified by P4T3's actual receiver/reopen test. Before each part reread the raw current destination and renew ownership; if empty/error, stop/pause safely rather than continue to an old cleared destination. A send already in flight may finish; retain its actual result.

For a multipart success, CompletePart keeps the claim in_flight while unconfirmed parts remain; the final confirmed part sets announced. A failure/ambiguous part releases the claim with persisted backoff, and already confirmed indexes stay fixed. Test this exact intermediate state and that wrong indexes/claims or a missing partition plan never advance delivery.

## Current durable delivery integration ownership

Keep existing AnnouncementService/ModelAnnouncementRepo/migration 026 and P4T1 partitioner, constructed in `newModelRefresh`. P3T6 supplies mandatory Announcer and failure-cycle invocation; P3T7 supplies shared lifecycle/clock; P4T3 drives real receiver and durable announced/part/backoff sinks through those sites. Do not wait for final discovery integration to finish local claim/retry tests. ClassifyAwaiting, ClaimPending, RenewClaim, Complete/CompletePart and RecoverClaims consume the same root Now clock; no second timer or dynamic original-payload reconstruction is introduced.

## Durable delivery task closure (2026-10-18)

The remaining top-level checkboxes were stale. The owned service, repository, migration 026 and named tests already exist; their original failing-test → behavioral-red → implementation → green sequence is recorded above. No new implementation, duplicate tests or manufactured implementation red is needed. The historical missing-root blocker is obsolete: `newModelRefresh` constructs the real repository/service/sender with the actual DB, internal settings and shared clock before startup. P4T3 retains full startup/tick/manual multipart receiver and executed-model release acceptance; this closure does not check that task or waive its gates.

Ordered self-review:
1. **Spec compliance:** checked D7, the current Phase 3 closure map and every local contract against the existing implementation/tests. `TestAnnouncementDurableOriginalPayload` covers original CAS changes, no followers, reopen and ordinary send-once behavior. `TestAnnouncementAmbiguousAndMarkerFailureRecovery` covers accepted-response loss/marker failure, lease recovery, current destination and two identical cards without receiver deduplication. Multipart pause/settings-fault/renewal tests cover frozen plans, confirmed-index retention, settings faults, destination edits and independent connections. Repository tests cover due-time boundaries, explicit-clock 4h/8h/16h/24h backoff/reopen, stale/wrong claims, atomic concurrency, corruption deferral/rollback and applied-022 upgrade. Catalog tests cover first-read classification, suppression, later paths/reappearance and canonical uniqueness. No delivery-policy change or extra option was added.
2. **Anti-green-theater:** temporarily replaced completion's injected time with `time.Now`; `TestAnnouncementClaimsBackoffReopenAndRecovery` failed at all five persisted retry-deadline assertions. Restored it. Temporarily removed original changes from the part passed to the sender; `TestAnnouncementDurableOriginalPayload/2` failed because the serialized HTTP receiver lost both original alias changes. Restored it. Temporarily replaced the real root Announcer injection with `DeferredAnnouncer`; `TestModelProfilesAnnouncementInstalledBeforeStartupRoot` failed with zero received cards and durable `pending` instead of `announced`. Restored it. These are coverage controls, not claims of newly missing behavior. Receiver bytes and durable states, not collaborator call counts, establish these sinks.
3. **Code quality:** reviewed neighboring catalog transactions, synchronous sender and migration conventions. Existing code uses short write-first SQLite transactions, owner/lease checks, named bounds, secret-free codes, explicit timestamps and the durable original partition plan. All temporary mutations are restored; there is no production/test/schema/UI diff, so no formatting, sqlc or asset regeneration is needed.

Fresh post-restoration verification (all exit 0):
- `go test ./domain/modelcatalog ./adapter/sqlite -run 'Announcement|Claims' -count=1`
- `go test ./domain/modelcatalog ./adapter/notification ./adapter/outbound -count=1`
- `go test ./adapter/sqlite -run 'Catalog|Cycle|Publication|Profile|Advance|ValidatedSelection|Migration' -count=1`
- `go test ./cmd/arbitron -run '^(TestModelProfiles.*|TestModelSettings.*|TestModelCatalogPublication.*)$' -count=1 -timeout=5m`
- `go test -race -p 1 ./domain/modelcatalog ./adapter/sqlite ./adapter/notification -run 'Announcement|Claims|Catalog|Cycle|Publication' -count=1 -timeout=8m`
- `go test -race ./cmd/arbitron -run '^(TestModelProfilesAnnouncementInstalledBeforeStartupRoot|TestModelProfilesCloseJoinsManualAndTickerRoot)$' -count=1 -timeout=3m`
- `git diff --check`

A broader exploratory root regex, `ModelProfiles|Settings|ModelCatalogPublication`, also selected the already-recorded stale `TestModelAnnouncementSettingsSeamThroughHTTP/read_fault_awaits_and_delivers_after_HTTP_save` fixture: it expects one card but full-source discovery correctly produces two. That command was red; no broader/full-release success is claimed. The task's exact focused gate and the explicit affected root/race gates above are green. Selection skipped P3T4's unresolved Codex tool-free source contract and P3T7's full-suite fixture blocker without retrying either; three plan tasks remain unchecked (P3T4, P3T7, P4T3).
