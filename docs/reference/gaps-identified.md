# Gaps Identified — workflow-management

Concrete gaps between the current code and the spec/designs/plans. As the loop refines:
add newly found gaps under `## Open Issues`, move resolved ones to `## Fixed Previously`,
and log out-of-scope ideas under `## Won't Fix (Beyond Current Scope)`.

At seeding the implementation matches the plans/designs and the full Go suite is green,
so there are **no open implementation gaps**. The items below are verification/hardening
checks to actively confirm each pass (a green suite is necessary but not sufficient —
assertions can be shallow):

## Open Issues

_None recorded yet. During each review pass, add a concrete gap here and write a failing
test before fixing it. Candidate things to actively verify (promote to a real issue only
if found wanting):_
- The cross-store rename/delete/transfer rollback genuinely leaves no half-state on a
  mid-operation failure (folder moved but registry not, or vice versa).
- The delete running-instance guard uses the exact non-terminal state set and cannot be
  bypassed by a race.
- Every one of the seven ops emits exactly one audit event with correct actor/target/detail.
- Non-admin callers are rejected on every lifecycle MCP tool (fail-closed), and local mode
  still works.
- Legacy null-`workflow_id` instances/schedules still operate, and a renamed workflow's
  schedule still fires.

## Fixed Previously

_None yet._

## Won't Fix (Beyond Current Scope)

_Per the spec's Non-Goals — do NOT implement in the loop:_
- A management dashboard/UI (MCP-only this iteration).
- Updating external, name-based callers (e.g. a GitHub Action) on rename.
- Per-owner or per-department management delegation (admin-only).
- Bulk automatic migration of pre-existing ambiguous names.
- Changing workflow authoring/validation or instance execution semantics.
