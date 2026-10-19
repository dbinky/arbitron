# Parallel Workflow Gathering — Phase 2 Task 16: Atomic production terminal receipts

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make the first-success winner follow the order successful task transitions
commit, including transitions in the same timestamp second, callback loss, reverse
callback delivery, and a coordinator racing task completion.

**Prerequisites:** P2-T1, P2-T2, P2-T3, P2-T9. Execute this task BEFORE P2-T10, P2-T13 and
P2-T14. P2-T9 stays pure and consumes the same `GroupTask.TerminalSeq` contract.

**Architecture:** SQLite terminal-state triggers populate the existing group roster in
the SAME transaction as the production child state update. A single-row durable clock
allocates the sequence. Callback intake never defines production completion order.
No new engine port, optional collaborator, or second terminal ledger is introduced.

## Interfaces and ownership

- `domain/instance/group_run.go:GroupTask` and `GroupRunRepository` (P2-T1) are unchanged.
  `TerminalSeq`, `TerminalAt`, `LiveStatus`, `Result`, and `Failure` keep their exact types.
- This task extends P2-T2's `019_parallel_groups.up.sql` AFTER both group tables exist:
  `group_terminal_clock(singleton INTEGER PRIMARY KEY CHECK(singleton=1), value INTEGER
  NOT NULL)` with one row `(1,0)`, and terminal INSERT/UPDATE triggers on `instances`.
  The down migration drops these triggers before dropping their tables and the clock.
  The migration number is owned by P2-T2; do not create a competing migration 020.
- Trigger names: `group_task_terminal_insert`, `group_task_terminal_update`,
  `group_task_terminal_immutable`. Trigger UPDATE/INSERT gates require a complete
  group link and terminal state; the UPDATE trigger runs only on the first transition
  from a nonterminal state. Repeated same-state writes do not change the receipt.
- `GroupRunRepo.RecordTaskTerminal` keeps its signature and idempotent return contract.
  Refactor its transaction to allocate from this SAME clock, rather than `MAX+1`, only
  when no receipt exists. If a production child row exists, return its already-persisted
  receipt; a terminal child lacking one is a storage/wiring error, never a callback-time
  allocation. Direct adapter-unit fixtures without a child may still seed a receipt via
  this method. Clock gaps are harmless; sequence order is authoritative.

## Production data provenance and wiring

`InstanceRepo.SetCompleted`, `SetFailed`, and terminal `UpdateState` calls are the real
writers (`adapter/sqlite/instance_repo.go`). They are used by `Engine.run`,
`failInstance`, cancellation, crash recovery, and queue webhook expiration. Their SQL
updates activate the trigger inside their own transaction, including raw queue SQL using
the same table. The trigger matches `group_tasks.child_instance_id = NEW.id` AND
`group_run_id = NEW.group_run_id` AND `task_key = NEW.group_task_key` before writing.
A complete group child with no matching roster aborts the state write: it cannot publish
a successful terminal with no receipt. The launcher already persists the roster first.

Each receipt captures:

- `live_status = NEW.state`, `terminal_at = COALESCE(NEW.completed_at, NEW.updated_at)`;
  allocate `terminal_seq` by incrementing the clock, in the same transaction.
- `result = json_remove(NEW.variables, '$.workspace')`. Compare the adapter test to
  `variable.ReservedInputNames`; extend the removal when that set grows.
- For failed/cancelled states, a JSON failure object with `state`, `failedNodeId`,
  `error`, `attempt`, using `NEW.failed_node_id` and the latest failed row in
  `node_executions` for that child (matching the failed node when present, ordering by
  attempt descending then started_at and id). Missing trace gives an explicit generic
  state/reason and attempt zero, not an absent receipt. Completion has no failure object.
- Preserve an existing receipt on any replay; do not modify the frozen decision/output.

The immutable trigger rejects a different terminal/nonterminal state after an already
terminal GROUP task. A cancelled task cannot subsequently become a success because a
late executor calls `SetCompleted`. Existing non-group terminal behavior is unchanged.

P2-T3's transactional `Get` reads the whole ordered roster consistently. Thus when a
coordinator can see terminal b, every earlier committed terminal a is already in the
same roster snapshot. P2-T10 must decide from this roster, not sort wall-clock times
from separate child reads. Timestamp seconds are for display only.

Composition root: existing `cmd/arbitron/main.go:initDomain` calls `RunMigrations(db)`
before creating the repositories. P2-T14's end-to-end terminal-order test drives this
site and the downstream persisted winner. Removing the trigger must make it fail; a
coordinator fallback must not conceal missing production capture.

## Strict TDD implementation steps

- [x] Step 1: Write `adapter/sqlite/group_terminal_test.go` before changing the migration. Use real
   SQLite, a persisted run/roster and queued group children, the existing InstanceRepo
   terminal methods, and GroupRunRepo reads. Never call `RecordTaskTerminal` in the main
   producer tests. Run them red against the callback-only design.
- [x] Step 2: Cover happy/success: commit b then a with identical `completed_at`, assert sequence
   b < a and exact result bytes minus reserved inputs; reverse notifications and assert
   `DecideGroup` picks b. Test failed and cancelled receipts and available variables.
- [x] Step 3: Cover failure/error: delete one matching roster before the write (the state transition
   rolls back), remove the clock row, inject an aborting receipt trigger, and verify no
   terminal state is committed without a receipt. A nil/empty result where a known child
   output was persisted must fail the test. Test latest failure-node/attempt selection.
- [x] Step 4: Cover edge/races: interleave a coordinator roster read with two commits (barriers,
   no sleeps); earliest success remains winner. Cancel before late `SetCompleted` and
   verify cancelled receipt/state survives. Replay terminal writes, restart/reconcile
   without callbacks, and mix manually seeded and trigger-produced receipts across runs.
   Delete the highest old receipt and ensure the durable clock never moves backwards.
Red verification (before implementation): `go test ./adapter/sqlite/ -run '^TestGroupTerminal_' -count=1` fails on missing receipts/clock/triggers, successful writes with no roster, callback fabrication, and cancelled-to-completed writes. Tests drive the actual terminal methods/table writes and transactional roster sink, not callback-generated receipts.

- [x] Step 5: Implement the clock/triggers and refactor `RecordTaskTerminal` to share the allocator.
   Keep SQL source/query definitions consistent; no schema rebuild or adapter dependency
   is introduced. Run focused tests green, then `go test ./adapter/sqlite/ ./domain/engine/`.
- [x] Step 6: Require P2-T10 to update its fixtures when that consumer task runs: the `finish` helper records the receipt at the simulated state
   transition; write failed traces BEFORE that transition. Cancellation fakes feed the
   same atomic boundary. Keep a distinct missing-receipt fake that errors rather than
   inventing order. These fakes must not use callback invocation order as terminal order.
- [x] Step 7: Run P2-T14 through the real `initDomain` and downstream variable sink with both tasks
   deliberately sharing one timestamp; remove each trigger temporarily to observe red,
   restore it, run green and refactor. Run focused race tests and `git diff --check`.

> RESOLVED (completion pass after P2-T14/P2-T15): P2-T14 has now supplied the real root and downstream tests; all Step 7 verifications pass as recorded below. Historical blocker: P2-T14 has not created `cmd/arbitron/group_e2e_test.go`, `bootGroupExecStack`, or the production coordinator/launcher/executor wiring in `initDomain`. Its remaining P2-T17 prerequisite is also absent. This pass implements only P2-T16's independent storage producer, not P2-T14/T17. The existing real Engine + SQLite persisted-parent-output integration now passes, but is not a substitute for this explicitly required composition-root same-timestamp winner proof. Leave this step unchecked until P2-T14 supplies that seam; do not fabricate production receipts or claim mode enablement.

Verification and self-review:
- Red before implementation: all new producer tests failed on missing atomic receipts/clock/triggers, callback fabrication, or unguarded cancelled-to-completed writes.
- Green: `go test ./adapter/sqlite/ ./domain/engine/ -count=1`; `go test -race ./adapter/sqlite/ -run 'TestGroupTerminal|TestGroupRunRepo' -count=1`; `go test -race ./domain/engine/ -run 'TestParallelGroupExecutor|TestNewParallelGroupExecutor|TestGroupCoordinator|TestGroupDispatch' -count=1`.
- Step 6 fixtures already meet the revised contract: `coordFixture.finish` calls `captureReceipt` at the simulated transition, failure traces precede that transition, cancellation captures before callback, and `TestGroupCoordinator_MissingAtomicReceiptBlocksDecisionAndActions` refuses missing intake. All these tests pass unchanged. Updated SQLite link fixtures to persist rosters before terminal children, rather than weakening production guards.
- Self-review in order: (1) spec: no new ports/options/migration number; triggers and fixture seeding share one durable clock; immutable group terminals and frozen output preserved. (2) anti-green-theater: real instance terminal methods/raw queue-shaped state SQL feed transactional roster reads with exact nonempty payload assertions and downstream winner selection; dropped-trigger tests demand storage errors, not synthesized callback order; the real Engine sink integration is green. Root e2e remains explicitly blocked above. (3) quality: checked neighboring repository/SQL patterns, bounded test barriers with cleanup releases, formatted/vetted changes; `git diff --check` clean.
- `go test ./... -count=1` passes all but the existing `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` unknown-profile failure. Reproduced the same focused failure from unchanged HEAD `6ff2d3b` in a temporary `git archive` checkout.
- `go vet ./adapter/sqlite/ ./domain/engine/` and `gofmt -l` clean. Commit partial storage progress with the blocker recorded; this is not P2-T14 production enablement.

Step 7 completion evidence (verification-only pass; no production changes needed):
- `TestGroupE2E_FirstSuccessUsesCommitOrderWithinOneTimestamp` boots the actual `initDomain`, launches real gated tasks, commits b then a with the same timestamp, reverses terminal notifications, and asserts persisted downstream `observedWinner == "b"`, nonempty winner results and exactly one success continuation. P2-T14/P2-T15 have supplied the previously missing wiring and proof.
- Temporarily embedded a throwaway migration dropping one trigger at a time. Removing `group_task_terminal_update` made the real-root same-timestamp test fail on the missing receipt; removing `group_task_terminal_immutable` made the real-root late-success test fail because cancellation could be overwritten; removing `group_task_terminal_insert` made `TestGroupTerminal_TerminalInsertWithoutRosterRollsBack` fail because an unreceipted terminal INSERT succeeded. Normal engine children are created queued and finish via UPDATE, so the INSERT-specific mutation uses its actual repository producer, not a fabricated engine INSERT path. Removed every mutation file and reran green.
- Green after restoration: `go test -race ./cmd/arbitron/ -run '^TestGroupE2E_(FirstSuccessUsesCommitOrderWithinOneTimestamp|CancelledTaskCannotPublishLateSuccess|MissingTerminalTriggerCannotFabricateSuccess|TerminalStorageFailureCannotPublishSuccess)$' -count=1`; `go test -race ./adapter/sqlite/ -run 'TestGroupTerminal|TestGroupRunRepo' -count=1`; `go test -race ./domain/engine/ -run 'TestParallelGroupExecutor|TestNewParallelGroupExecutor|TestGroupCoordinator|TestGroupDispatch' -count=1`.
- Also green: `go test ./adapter/sqlite/ ./domain/engine/ -count=1`, `go vet ./adapter/sqlite/ ./domain/engine/`, and `git diff --check`.
- Self-review in order: (1) spec compliance: only the remaining verification step was completed; no extra schema, ports or features. (2) anti-green-theater: reviewed real migrations/repositories/launcher/coordinator/executor wiring before freeze and persisted downstream assertions; removal probes prove the load-bearing capture/immutability boundaries, and missing-trigger tests prohibit callback fabrication. (3) quality: reviewed adjacent repository and e2e patterns; no code cleanup was needed. Reran focused race gates after review before committing.

**Files:** modify `adapter/sqlite/migrations/019_parallel_groups.{up,down}.sql`,
`adapter/sqlite/group_run_repo.go`; create
`adapter/sqlite/group_terminal_test.go`; extend `cmd/arbitron/group_e2e_test.go` in P2-T14.
