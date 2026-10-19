# RALPH — Parallel Workflow Gathering Review Loop

You are in an automated refinement loop and the user is unavailable for input, so
do the work without asking for any user input.

## Goal

The parallel-workflow-gathering feature (`parallel_group` nodes: parallel branches
and multi-child workflow groups, `all_succeed` / `first_success` completion rules,
ordered gathered results, optional file gathering with interactive conflict
resolution, MCP authoring, and aggregate run visibility) was implemented across 4
design phases and 47 plan tasks. Each task was verified in isolation. What has
**not** been verified is whether the pieces are wired together and fed by real
producers, and whether the assembled behaviours fail safely.

This loop reviews the assembled feature one **behaviour** at a time (entry point to
sink, across every layer), then reviews each **seam** where two independently
built behaviours share state, a contract, or an ordering. It fixes what it finds.
It does not rebuild from scratch.

**Branch:** `dev-task-parallelism-2026-10-16-d35eab7c`
**Branch point:** `315ee04c7e2a9acc3ee64d2905c8a80156bdf893`

## Source documents

- **Spec:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md`
- **Designs:**
  - `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` — group definition, validation & MCP authoring
  - `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` — execution core (results only)
  - `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` — working-area VCS & file gathering
  - `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` — run visibility
- **Plans:** `docs/plans/2026-10-16-parallel-workflow-gathering-implementation-phase-{1..4}-task-*.md`
- **Plan dependency closure:** `docs/plans/2026-10-16-parallel-workflow-gathering-implementation-alignment.md`
  (design IDs P1-A … P4-C → producer tasks and composition/sink tests)
- **Single-system review plan:** `docs/reference/focus-areas.md`
- **Paired integration review plan:** `docs/reference/paired-reviews.md`
- **Open gaps:** `docs/reference/gaps-identified.md`
- **Previous loop (spawn node, archived):** `docs/reference/historical/2026-07-27-workflow-spawn-node-*.md`

## Test commands

```
go test ./...
cd dashboard && npx tsc --noEmit && npx vitest run
```

Narrow these to the package or name filter you touched during a pass (for example
`go test ./domain/engine/ -run TestGroupCoordinator` or
`cd dashboard && npx vitest run src/graph/groupLayout`). The verify-tests script
re-runs every touched suite against the baseline after each commit.

## Branch-point test baseline

The repo was **already red** at the branch point. These failures are NOT this
feature's job and MUST NOT block the loop:

| Suite | Command | Build | Passed | Failed | Failing tests |
|---|---|---|---|---|---|
| go | `go test ./...` | ok | 5247 | 1 | `github.com/arbitron/arbitron/adapter/config.TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | ok | 522 | 0 | — |

Compare every run against this table. Fix **NEW** failures only.

## Budget

The loop runs under a cap of **500** passes. Measured overhead (gap closing,
regression fixes, re-reviews) means only about 35% of passes advance a review
box, so the plan must satisfy `2 × singles + paired ≤ 500 × 0.35`.

| Item | Count |
|---|---|
| Single-system areas (`docs/reference/focus-areas.md`) | 13 |
| Paired seams (`docs/reference/paired-reviews.md`) | 23 |
| Implied minimum passes (`2 × 13 + 23`) | 49 |
| Budget (`500 × 0.35`) | 175 |
| Headroom | 126 |

Work is taken **highest risk first** in both tables, so a run stopped by the cap
has still covered the most consequential behaviours and seams.

## Review order

1. **Phase 1 — single-system reviews.** Each area in `docs/reference/focus-areas.md`
   needs two approvals: **Review 1** on the wiring lens, then **Review 2** on the
   failure lens. Each area's `### Area N` block lists its files, folded-in checks,
   and the numbered checks for each lens.
2. **Phase 2 — paired reviews.** Starts only once every single-system box is ticked.
   Each row of `docs/reference/paired-reviews.md` is one seam, reviewed in both
   directions in one pass, and needs one approval. The row's TRACE says what to
   follow and its BREAK says what must be shown impossible or caught.

## What every review actually checks

The tables say *what* to review; these criteria decide whether a review passes.
The recurring failure mode is code that is present and green in isolation but
**never wired-and-fed** in production.

- For every new exported field / collaborator / config the branch added:
  `grep -rn <Symbol>` the whole repo — is it **constructed or populated outside
  `*_test.go`**? If it is only ever set in tests, the review fails.
- For every new component: is it assembled into the real composition root
  (`cmd/arbitron/main.go:initDomain`, router in `adapter/http/server.go`, dashboard
  route/component that mounts it) so the live path reaches it? Name the wiring site,
  or fail the review.
- Does any production call site pass `nil`/empty/hardcoded where a real producer
  should feed it?
- Do any tests pass only because a fake ignores a load-bearing argument, or because
  an "integration" test asserts an intermediate hop instead of the sink?
- Does the area have coverage across all five scenario categories (happy, success,
  failure, error, edge), and would at least one test **fail** if its production
  input were nil/empty?

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this branch's
job and must NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens; every paired review
  has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at
  baseline is broken.

An unfed production path is a gap to fix or an explicit escalation, never a
tolerated no-op. `docs/reference/gaps-identified.md` must not record an inert path
as "acceptable".

## Plan critique

areas 15 → 13, seams 27 → 23, implied passes 57 → 49, budget 175

Seed numbers below identify the reviewed input; current numbers identify the edited tables.

- Merge seed Areas 4 + 8 → Area 4: starting copies are internal preparation in `group_launcher.go:Launch`, with no independent entry; judging roster launch requires tracing the same preparation and child sink (merge clauses 1–3).
- Merge seed Areas 9 + 13 → Area 8: `resolveNodeContexts` consumes panel tasks to attribute branch nodes; both share `GraphState` traversal, authorization and render files, with no panel endpoint (merge clauses 1–3).
- Strike seed seam 10 → No seam Area 1 ↔ Area 10: mode dispatch and invalid group links are checks of the existing spawn path alone; moved into its failure lens.
- Strike seed seam 13 → No seam Area 6 ↔ Area 10: timestamp and terminal-parent behavior belong to the same mode-agnostic cancellation implementation; moved into its failure lens.
- Strike seed seam 14 → No seam Area 4 ↔ Area 11: shared AnalyzeGroups rejects bad ownership explicitly; gate bypass and late resolver errors are single-area checks, not silent combination failures.
- Strike seed seam 25 → No seam Area 5 ↔ Area 7: watcher/sweep calls feed the pruner already owned by resource retention; completed that area's entry and wiring lens.
- Strike seed seam 27 → No seam Area 4 ↔ Area 10: Enqueue's post-Create reload failure occurs with either caller alone; moved caller checks into the launch failure lens.
- Add seam 7 (Area 3 ↔ Area 7): live session completion and recovery write the same attempt/report; recovery also calls completion under the gather gate, exposing lost updates and reentrant-lock risk.
- Area 1 block: supplied production file:symbol anchors for terminal receipts, decision and action checks.
- Area 2 block: supplied production anchors for eligibility, apply/rollback and error routing.
- Area 3 block: supplied production anchors for bridge wiring and verified session completion.
- Area 4 block: combined launch/isolation paths and checks; anchored lenses and folded in both Enqueue callers.
- Area 5 block: completed watcher/sweep entry and files, anchored finalization checks, retained prune timing checks.
- Area 6 block: anchored cancellation checks and folded in ordinary-spawn timestamp/terminal-parent cases.
- Area 7 block: anchored recovery checks and added the recovery/completion lock-ownership check.
- Area 8 block: combined node and panel entry-to-render coverage, with production anchors and retained redaction checks.
- Area 9 block: anchored event routing/lifetime checks and added parent-scoped conflict-payload authorization.
- Area 10 block: anchored legacy spawn checks and folded in exclusive terminal dispatch.
- Area 11 block: anchored authoring gate, contract lookup and deletion checks.
- Area 12 block: anchored ancestor-query, hierarchy and queue deduplication checks.
- Area 13 block: anchored static layout, task-card link and fallback checks.
- Seam audit: corrected task provenance (launcher P2-T8, coordinator P2-T10, queue P4-T10, panel rendering P4-T8); extended current seam 13 to test hidden-task data in real conflict frames. No changes needed for reverse rows; none existed.
- Risk order: promoted seed seams 11, 18, 21 and 26 to high and placed them before medium seams: wrong-parent file writes and unauthorized path/token disclosure outrank stale-display checks. The merged live-view area retains high risk. No changes needed to the remaining single-area order.
- Merge audit: No changes needed to the remaining boundaries; task terminal, parent re-entry, session commands, recovery, static requests and live requests are distinct entry sub-paths.
- Block structure: all eight fields existed; completed symbol-level lenses as listed above. No changes needed for missing blocks or empty fields.
- Budget: 49 ≤ 175, headroom 126; no deferrals. No changes needed to the cap or overhead factor.
- Evidence corrections: P3-T10 explicitly requires files-mode Retry to replay failure and retains unapplied workspaces for debugging; narrowed those seeded gaps accordingly. Open gaps use OPEN labels, leaving approval boxes only in the two tables. Read all OPEN records under `## Open`; the preserved roles' legacy checkbox-grep hint does not select these records.
- Preservation: no production/test edits or approval changes; role blocks and definition of done remain verbatim.

## Loop protocol

## Role: single-review-1

*The WIRING lens: does the path exist and is it fed? Refined and assessed by two
different model families. The first independent look at an area.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row and its `### Area N` block in `docs/reference/focus-areas.md`, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`; do not read the closed history).
2. **Read the code** — every file in the area's **Files** list, entry to sink.
3. **Cross-reference** — work the area's **Review 1 — wiring lens** checks with real greps; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` / `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Does the area meet every wiring-lens check?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 1 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit and push. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: single-review-2

*The FAILURE lens: how does it fail? Review 1 already passed on the wiring lens.
This pass asks a different question, on a different model family — that
difference is the entire reason two approvals exist.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **One single-system area per iteration.** Fix the most important issue, then stop.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row and its `### Area N` block in `docs/reference/focus-areas.md`, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`; do not read the closed history).
2. **Read the code** — every file in the area's **Files** list, entry to sink.
3. **Cross-reference** — work the area's **Review 2 — failure lens** checks: nil and empty inputs, timeouts, partial failure and rollback, concurrency, ordering; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` / `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Does the area meet every failure-lens check?

**Step B** — Record a recommendation; tick NOTHING. The approver ticks Review 2 Status in `docs/reference/focus-areas.md`.

**Step C** — Commit and push. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**

## Role: paired-review

*Phase 2: runs only once every single-system box is ticked. One row is one seam,
reviewed in BOTH directions in the same pass. Refined and assessed by two
different model families.*

### Constraints
- **Do NOT use sub-agents.** Work by hand, one component at a time.
- **One seam per iteration, both directions; batch further rows only when they share the context you have already loaded.** Fix the most important issue, then stop.
- **Read before you write.** Read a file before modifying it; verify before declaring anything fine.
- **Do NOT invent new functionality.** Log gaps in `docs/reference/gaps-identified.md`; out-of-scope items go under `Won't Fix`.
- **The refiner never ticks a box; the approver never writes production code.** They run on different model families on purpose.

### Iteration Structure
1. **Read tracking** — `RALPH.md`, the assigned row in `docs/reference/paired-reviews.md`, the `### Area` blocks of both behaviours it joins, and the OPEN items in `docs/reference/gaps-identified.md` (grep for `[ ]`).
2. **Read the code** — both sides of the seam and the seam itself, following the row's TRACE.
3. **Cross-reference** — demonstrate that the row's BREAK cannot happen: a test that goes red, or a cited guard at `file:symbol`; check what each side assumes about the other; record findings in `docs/reference/gaps-identified.md`.
4. **Fix the most important issue** — ONE issue, fixed well across all affected files.
5. **Write/verify tests** — happy, success, failure, error, edge. Strict TDD when adding behaviour.
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` / `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
7. **Assess, then proceed to Wrap Up regardless of the outcome.** Do not go back to step 4.

### Wrap Up
**Step A** — Is the seam sound in both directions, with its BREAK demonstrated impossible or caught?

**Step B** — Record a recommendation per row; tick NOTHING. The approver ticks each row it can verify in `docs/reference/paired-reviews.md`, each cited separately.

**Step C** — Commit and push. Your work is valuable regardless of review status.

**Step D** — One line: `Progress: <what advanced>` — the fix you landed, the gap you closed, the component you wired, or explicitly what did not advance and why. The progress assessor has no tools and sees only your response.

**Step E** — Check the tracking files, then output exactly one tag:
- All reviews approved AND no open gaps: `<promise>FINIT</promise>`
- Otherwise: `<promise>CLOSER</promise>`

**Stop after the tag every time.**
