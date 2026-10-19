# RALPH — Workflow Spawn Node Review Loop

You are in an automated refinement loop and the user is unavailable for input, so
do the work without asking for any user input.

## Goal

The `spawn_workflow` feature (5 design phases, 27 plan tasks) has just been
implemented task by task. Each task was verified in isolation. What has **not**
been verified is whether the pieces are wired together and behave correctly as a
whole.

This loop reviews the assembled feature: every component the change set
introduced, and every seam where two of them meet. It fixes what it finds. It
does not rebuild from scratch.

**Branch:** `dev-workflow-spawn-nodes-2026-07-27-c0c76804`
**Branch point:** `64768283d2c32b64023801103c7aab2f5ea538d3`

## Source documents

- **Spec:** `docs/specs/2026-07-27-workflow-spawn-node-spec.md`
- **Designs:**
  - `docs/designs/2026-07-27-workflow-spawn-node-design-phase-1.md` — contract & linkage foundation
  - `docs/designs/2026-07-27-workflow-spawn-node-design-phase-2.md` — child input preparation
  - `docs/designs/2026-07-27-workflow-spawn-node-design-phase-3.md` — spawn executor (fire-and-forget) and wiring
  - `docs/designs/2026-07-27-workflow-spawn-node-design-phase-4.md` — wait-for-child
  - `docs/designs/2026-07-27-workflow-spawn-node-design-phase-5.md` — recursive cancellation and run hierarchy
- **Plans:** `docs/plans/2026-07-27-workflow-spawn-node-implementation-phase-{1..5}-task-*.md`
- **Review plan:** `docs/reference/focus-areas.md`
- **Open gaps:** `docs/reference/gaps-identified.md`

## Test commands

```
go test ./...
cd dashboard && npx tsc --noEmit
cd dashboard && npx vitest run
```

## Branch-point test baseline

The repo was **already red** at the branch point. These failures are NOT this
feature's job and MUST NOT block the loop:

| Suite | Command | Build | Passed | Failed | Failing tests |
|---|---|---|---|---|---|
| go | `go test ./...` | ok | 4694 | 1 | `github.com/arbitron/arbitron/adapter/config.TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` |
| dashboard-typecheck | `cd dashboard && npx tsc --noEmit` | ok | 0 | 0 | — |
| dashboard-vitest | `cd dashboard && npx vitest run` | ok | 436 | 0 | — |

Compare every run against this table. Fix **NEW** failures only.

## What every review actually checks

The tables in `docs/reference/focus-areas.md` say *what* to review. These
criteria decide whether a review **passes**. The recurring failure mode is code
that is present and green in isolation but **never wired-and-fed** in production.

- For **every** new exported field / collaborator / config the feature added:
  `grep -rn <Symbol>` the whole repo — is it **constructed or populated outside
  `*_test.go`**? If it is only ever set in tests, the review fails.
- For **every** new component: is it assembled into the real composition root
  (`cmd/arbitron/main.go` `initDomain` — the `executors` map, `eng.SetSpawnLinks`,
  `instanceEnricher.SetSpawnLinks`, `queueMgr.SetChildTerminalHook`, the startup
  sweep order) so the live path reaches it? Name the wiring site or fail it.
- Does any production call site pass `nil`/empty/hardcoded where a real producer
  should feed it? (a `spawnLinks` repo never set, a zero-value `ChildVisibility`,
  an empty `ProfileAlias`, a `parent` selector never sent from the client.) List each.
- Do any tests pass only because a **fake ignores a load-bearing argument** (a
  fake `SpawnWaitRepository` that ignores `nodeID` or `mode`; a fake
  `ChildCountsByParents` that ignores `ChildVisibility`; a fake spawner that
  ignores `SpawnRequest.Link`), or because an "integration" test asserts an
  intermediate hop instead of the sink?
- Does the area have coverage across all five scenario categories (happy,
  success, failure, error, edge), and would **at least one test fail if its
  production input were nil/empty**? Green over-mocked tests do not count.

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this
feature's job and MUST NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**; every paired review has **1**.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every plan task is genuinely implemented — **wired-and-fed, not merely present**:
  constructed at the real composition root AND fed real data from a producer that
  exists in the code (NOT `nil`/empty/hardcoded, NOT only set in tests).
- The phase designs are satisfied in spirit.
- **No NEW test failures** beyond the recorded branch-point baseline — i.e. the
  current set of failing tests is a subset of the baseline.
- **No new build/compile break.** A suite that built at the baseline must still build.

An inert or no-op production path is never "acceptable". If
`docs/reference/gaps-identified.md` would record one (e.g. "the enricher's
child-count call is skipped when no identity is on the context — fine"), that is
a **gap to fix or an explicit escalation**, not a tolerated no-op.

## Constraints

- **Do NOT use sub-agents.** Review and fix by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One review area per iteration. Fix the most important issue, then stop.**
- **Do NOT invent new functionality.** Log perceived gaps in `docs/reference/gaps-identified.md`. Clearly out-of-scope items go under `Won't Fix`.
- **Approvals are independent.** Never record two approvals for a single-system area in one pass — the second approval exists to catch what the first missed.

## Iteration Structure

1. **Read tracking** — Read `docs/reference/focus-areas.md`. Pick the next review with an unchecked status (single-system reviews first, lowest number).
2. **Read the code** — Read every file the review covers.
3. **Cross-reference** — Apply the wired-and-fed criteria in RALPH.md. Record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue per iteration, fixed well across all affected files.
5. **Write/verify tests** — happy path, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run tests** — Run `go test ./...`, `cd dashboard && npx tsc --noEmit`, `cd dashboard && npx vitest run`. Compare against the recorded baseline; fix NEW failures only.
7. **Assess the review**, then proceed to Wrap Up regardless of the outcome. **Do not go back to step 4.**

Most review areas need multiple passes. That is normal and expected — do not try
to finish everything in one pass, and never record an approval to end the loop sooner.

## Wrap Up

**Step A** — Did the review area meet every criterion for approval?

**Step B** — If it did: tick exactly ONE status box for it in `docs/reference/focus-areas.md`. If it did not: leave the boxes alone.

**Step C** — Commit and push. Your work is valuable regardless of review status.

**Step D** — Check both files:
1. Does every single-system review have BOTH approvals, and every paired review its one approval?
2. Are there ANY unchecked `[ ]` items in `docs/reference/gaps-identified.md`?

Then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**
