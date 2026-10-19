# Parallel Workflow Gathering — Implementation alignment and dependency closure

Spec: [product requirements](../specs/2026-10-16-parallel-workflow-gathering-spec.md).
This index is a binding cross-task contract and execution order. It records planned
producers; it does not assert that these new capabilities are already implemented.

## Execution order

Task numbers are identities, not an instruction to ignore prerequisites:

- Phase 1: T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → T9.
- Phase 2: T1 → T2 → T3 → T4 → T5 → T6 → T9 → **T16** → **T17** → T7 → T8 →
  T10 → T11 → T12 → T13 → T14 → T15. T8 calls T9's decision helpers; T16
  creates atomic terminal receipts; T17 completes linked-worktree isolation.
- Phase 3: T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → T9 → T10 → T11.
  T6 declares the bridge port; T8 implements it; T11 wires both after T10.
- Phase 4: T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → T9 → T10.

Execute every task's red → minimal implementation → green → refactor cycle. The added
revision sections and new tasks replace the old behaviors they identify, not optional
extra credit after enabling a mode. Migration 019 belongs to P2-T2, extended by P2-T16;
migration 020 belongs to P3-T4, with the notification rebuild owned by P3-T8. Check the
repository's next free numbers when implementing; update every reference together if moved.

## Design dependency provenance

| ID | Producer tasks | Composition / sink tests | Required production input and outcome |
|---|---|---|---|
| P1-A | [P1-T1](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-1.md), [P1-T2](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-2.md), [P1-T6](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-6.md), [P1-T7](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-7.md) | [P1-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-8.md) | Real MCP create/inspect/update, full manifest nesting, shared ownership and typed config |
| P1-B | [P1-T3](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-3.md), [P1-T4](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-4.md), [P1-T5](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-5.md) | [P1-T7](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-7.md), [P1-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-8.md) | Caller-scoped child contract/profile/file inputs; missing checker rejects before persistence |
| P1-C | [P1-T9](2026-10-16-parallel-workflow-gathering-implementation-phase-1-task-9.md) | [P2-T14](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-14.md), [P3-T11](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-11.md) | Real trigger refuses unsupported mode, then runs enabled modes at downstream sinks |
| P2-A | [P2-T1](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-1.md), [P2-T2](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-2.md), [P2-T3](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-3.md), [P2-T4](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-4.md), [P2-T9](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-9.md), [P2-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-10.md), [P2-T11](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-11.md), [P2-T13](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-13.md), [P2-T16](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-16.md) | [P2-T14](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-14.md), [P2-T15](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-15.md) | Atomic terminal receipts, frozen ordered output, durable actions, exactly one downstream continuation |
| P2-B | [P2-T5](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-5.md), [P2-T6](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-6.md), [P2-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-8.md), [P2-T17](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-17.md) | [P2-T14](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-14.md), [P3-T11](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-11.md) | Deep copies, task-local recursive bindings, actual fork bytes and Git metadata isolation |
| P2-C | [P2-T1](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-1.md), [P2-T2](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-2.md), [P2-T7](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-7.md), [P2-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-8.md) | [P2-T14](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-14.md), [P2-T15](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-15.md) | Reserved child IDs and complete persisted group/branch links before enqueue |
| P2-D | [P2-T12](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-12.md) | [P2-T14](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-14.md), [P2-T15](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-15.md) | Retained-ancestry cancellation through completed intermediates and terminal parents |
| P3-A | [P2-T17](2026-10-16-parallel-workflow-gathering-implementation-phase-2-task-17.md), [P3-T2](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-2.md), [P3-T3](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-3.md) | [P3-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-10.md), [P3-T11](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-11.md) | Full-area baseline/fork/immutable refs, actual staging/conflict paths, parent bytes and rollback |
| P3-B | [P3-T4](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-4.md), [P3-T5](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-5.md), [P3-T6](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-6.md), [P3-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-10.md) | [P3-T11](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-11.md) | Preterminal finalized refs, durable reports, retained/pruned resources and recovery |
| P3-C | [P3-T7](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-7.md), [P3-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-8.md), [P3-T9](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-9.md), [P3-T6](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-6.md), [P3-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-10.md) | [P3-T11](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-11.md), [P4-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-8.md), [P4-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-10.md) | Embedded/scoped prompt, real MERGE_HEAD refs, automatic verification, human session/abandon/notification sinks |
| P3-D | [P3-T1](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-1.md), [P3-T2](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-2.md) | [P3-T11](2026-10-16-parallel-workflow-gathering-implementation-phase-3-task-11.md) | Real cancellable bounded Git execution; host-tool and process failures |
| P4-A | [P4-T1](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-1.md), [P4-T2](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-2.md), [P4-T3](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-3.md) | [P4-T3](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-3.md), [P4-T5](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-5.md), [P4-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-8.md), [P4-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-10.md) | Authorized recursive graph-state, active-ancestor list and queue/live sinks |
| P4-B | [P4-T4](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-4.md), [P4-T5](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-5.md) | [P4-T5](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-5.md), [P4-T6](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-6.md) | Persisted ancestry, per-event authorization, source identities and honest subtree lifetime |
| P4-C | [P4-T6](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-6.md), [P4-T7](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-7.md), [P4-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-8.md), [P4-T9](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-9.md), [P4-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-10.md) | [P4-T8](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-8.md), [P4-T9](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-9.md), [P4-T10](2026-10-16-parallel-workflow-gathering-implementation-phase-4-task-10.md) | HTTP-fed DTO/hooks/layout, both run views, pagination, poll/reconnect and embedded bundle |

## Shared contracts and anti-green requirements

- `workflow.GroupTask` is author config; `instance.GroupTask` is the persisted roster.
  P1-T1/T2 own parsing/ownership; execution/layout must consume that same analysis.
- The coordinator constructor takes `(groups, instances, waker, canceller, identities, bus, queueMgr)`;
  failure traces are read by the atomic terminal writer, not an unused coordinator dependency.
- P2-T16 produces `TerminalSeq`, terminal status, available results and failure details
  in the SAME transaction as the child terminal state. Callback-time sequence allocation
  and timestamp sorting are not production sources. P2-T10 returns on incomplete intake.
- P2-T17 owns `filesystem.CopyGroupGitMetadata` and `ErrGroupRepositoryLocked`;
  P2-T5 calls it for snapshot and fork, and P3-T2 reuses it. File mode's
  `git.ErrRepositoryLocked` aliases the shared sentinel. Metadata cannot point at the
  parent's writable refs/index. Results-only mode does not require a Git process.
- `GroupResolutionStarter` (P3-T6) has `StartResolution`, `CloseSession`, and
  `NotifyResolutionRequired`; `GroupSessionCloser` (P3-T8, consumed by T10) has the last
  two. `*GroupResolutionBridge` is the production implementation of both. The fakes
  record/check full refs, and production wiring injects this same bridge in P3-T11.
- P3-T2 captures ordinary file/directory permission bits in private manifests and restores
  them on fork; P3-T3 consumes those manifests for eligible mode deltas and exact rollback.
  Real-root tests assert modes as well as bytes. Git executable bits alone are insufficient.
- `WorkingArea.ConflictPaths` supplies the post-agent truth. A clean first attempt
  commits/applies and continues without human end; remaining conflicts notify once per
  attempt and stay open. Persisted session/notification state recovers missed effects.
  The staging prompt uses `MERGE_HEAD` and `MERGE_HEAD^`, which actually exist there.
- Final output `outcome` matches the route after gathering. `taskOutcome` and
  `GroupDecision.Outcome` preserve the immutable task decision. Late tasks update live
  inspection only. File failure cannot masquerade as final success.
- Absolute paths into the parent area are checked recursively in BOTH inherited and
  explicit composed values; authors bind replacements through task-local `workspace`.
  Nested maps/arrays do not exempt a writable shared path.
- No permanent allow/deny cache on subtree events; check each source through the service.
  No silent 1,000-context cutoff. A storage error that prevents proving inactivity keeps
  socket/poll resync alive with unknown state; hidden activity stays redacted.
- Composition tests assert downstream variables, parent file bytes, retained reports,
  actual WebSocket frames and rendered queue/run content. Intermediate calls and fabricated
  refs alone do not prove wiring. Unused fake operations return errors, not success.
- First-success/cancel/restart tests use bounded process gates, never timing-based skips.
  The only allowed skips describe unavailable host tools or unsupported platform features.

## Coverage and completion gate

The dependency table covers every design ID P1-A through P4-C, including repeated references
in later designs. Each row identifies a producer AND real composition/sink tests. Existing
leaf producers (scoped Service.Load, Instance/NodeExecution repositories, workflow scripts,
WorkspaceManager, owner IdentityResolver, ProfileResolver, FileReader, EventBus and queue)
are named in the consuming task's provenance section; none is replaced by a proposed port.

Each task must run its existing focused five-way coverage (happy, success, failure, error,
edge) plus the added revision tests before it is complete. Pure types/builders are exercised
through their first live consumer as well as unit tests. Storage tasks cover invalid input,
missing data and transaction failure; UI tasks cover source identity, unavailable data,
redaction and reconnect/drop recovery. New tasks T16/T17 enumerate all five categories.
The final production gate is `go test ./...`; Phase 4 also runs dashboard TypeScript/Vitest
checks and `make ui`. A documented pre-existing failure must be reproduced on the unchanged
base; do not silently exempt a new failure. This alignment review changes documentation only.

All identified dependency edges have producing and wiring tasks. There is no deferred
human design decision or accepted missing-input/no-op hole in this planned graph.
