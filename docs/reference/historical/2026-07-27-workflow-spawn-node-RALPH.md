# RALPH — Workflow Auto-Arrangement Review Loop

You are in an automated refinement loop and the user is unavailable, so do the work
without asking for user input.

## Goal

Make the workflow auto-arrangement branch true end to end. Every authoring save is
stamped with a server revision, saved immediately and arranged asynchronously into
continuous downward flow. Every candidate is rendered by the real shared renderer and
visually reviewed. Refinement is bounded by `layout.maxattempts` (default 5). Results
are published only to the revision they reviewed, and workflow and instance views show
the same revision-scoped layout with truthful status and a Retry action.

The branch is present and largely green in isolation. This loop exists to prove that
each behaviour is **wired and fed** in production, from its entry point to its sink, and
that the seams between independently built behaviours hold.

## Inputs

- Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`
- Designs: `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md`, `-phase-2.md`, `-phase-3.md`
- Implementation contracts: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md`
- Plans: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-{1,2,3}-task-*.md` (P1T1–P1T9, P2T1–P2T9, P3T1–P3T14). Their `> BLOCKED:` notes are the source of most seeded gaps.
- Single-system plan: `docs/reference/focus-areas.md` (12 behaviours, each with a wiring lens and a failure lens)
- Paired plan: `docs/reference/paired-reviews.md` (19 seams, plus a `## No seam` audit)
- Gap ledger: `docs/reference/gaps-identified.md`
- Composition root: `cmd/arbitron/main.go:initDomain` and `cmd/arbitron/layout_wiring.go:newLayoutStack`

## Test commands

```
go test ./...
cd dashboard && npx tsc --noEmit && npx vitest run
```

Narrow these to the package or test name you touched (for example
`go test ./domain/layout -run TestRun_ -count=1`, or `cd dashboard && npx vitest run src/hooks`).
Real-browser tests need Chrome or Chromium. `make test-render` sets
`ARBITRON_REQUIRE_BROWSER=1`, so a missing browser fails instead of skipping.

### Branch-point baseline (failures that existed before this feature)

| Suite | Command | Baseline |
|---|---|---|
| go | `go test ./...` | builds; 5247 passed, 1 failed: `github.com/arbitron/arbitron/adapter/config.TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (Won't Fix W1) |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | did not build: `dashboard/node_modules` was not installed (`ERR_MODULE_NOT_FOUND`), so there is no baseline (Won't Fix W3). Run `npm ci` in `dashboard/` before relying on it. |

Pre-existing failures are not this branch's job and must not block the loop. Fix new failures only.

## Budget

| Item | Count |
|---|---|
| Single-system areas | 12 (2 passes each) |
| Paired seams | 19 (1 pass each) |
| Implied minimum passes | 2 × 12 + 19 = **43** |
| Budget | 500 × 0.35 = **175** |

43 ≤ 175, so the plan fits. Passes are also spent closing the 13 seeded gaps and
re-reviewing. Rows are ordered highest risk first, so a run that stops at its cap has
still covered what matters most.

## Plan critique

areas 13 → 12, seams 24 → 19, implied passes 50 → 43, budget 175

- Merge: seeded areas 7 (cache) and 8 (worker) → Area 7; merge-test clauses 1 and 2. `worker.go:processRevision` calls `publish` after `Complete`; `reconcileCache` repeats that same path and `layout_repo.go` supplies the same fences (P2T8 §8.2/§8.4).
- Merge test: No changes needed elsewhere. Save operations, settled reads, query reads, retries, measurement mode, render/review mode and live views retain distinct entry-to-sink sub-paths; these are operation/page-mode splits, not standalone helper, DTO or wiring areas.
- Struck seeded seam 12 (retry/cache): duplicates row 6 after merging the worker/cache area; both completion and cache-generation assumptions retained there.
- Struck seeded seam 13 (settlement/cache): duplicates row 2 after merging the worker/cache area; settlement-before-publication retained there.
- Struck seeded seam 19 (read/retry eligibility): both policies belong to P2T5 and share `passProblem`; criterion 2 fails; state matrix moved to Area 6 Review 2 check 3.
- Struck seeded seam 21 (measurement/sizing): already inside Area 11's measured-text-to-boxes path; maxWidth/padding/clipping check completed in its wiring lens.
- Struck seeded seam 23 (geometry/dashboard validation): dashboard and render review use the same `resolveDisplayGeometry`/`validateGeometry`; covered by row 19 and Area 12's failure lens, without a second direct consumer seam.
- Struck seeded seam 24 (coordinator/refinement): same-package direct call already inside Area 10's refinement path; changed-options/digest/early-exit check completed in its failure lens.
- Added row 18, Area 4 ↔ Area 6: P2 relocation changes the committed journal envelope consumed by P3 historical retry; a green historical read can conceal a requeued row with the wrong destination. SHARED, TRACE and BREAK are in the row.
- Block test: completed Area 7 after the merge, retaining lifecycle, budget, evidence, lease, publication, cache fallback and reserved-file checks in seven checks per lens.
- Block test: completed Area 6's retry eligibility state matrix from `Reader.View`, `Requester.Retry` and `QueryService.retry`.
- Block test: completed Area 10's refinement/digest/early-exit check from `Refine` and `Coordinator.Run`.
- Block test: completed Area 11's measurement-to-box check and corrected measurement failures to `StageRendering` from `Coordinator.Run`.
- Block test: completed Area 12's shared-validator check from `runRenderJob` and `ArrangedGraph`.
- Block test: No changes needed to other blocks; all required fields and at least three concrete checks per lens were present.
- Seam test: No changes needed for reverse rows (none). The No seam audit covers every remaining pair; removed rows remain explained there. Cache-outage BREAKs now distinguish wrong geometry from falsely reviewed status.
- Size/budget test: 2 × 12 + 19 = 43 ≤ 500 × 0.35 = 175; both headers updated; no deferrals needed.
- Risk order: promoted copied-cache revision contamination (row 12) to high, ahead of availability/display seams; folded settlement/cache corruption into high-risk row 2. No changes needed to single-system order; the merged worker retains the earlier cache risk position.

## Definition of done

"Done" means **all** of:

- Every single-system review has **2 approvals**, one per lens (Review 1 is the wiring lens, Review 2 the failure lens). Every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired and fed**, verified by grep and a composition-root trace, not by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No new test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

The repo may already be red on arrival; that is not a blocker. An inert or no-op
production path is never "acceptable" in the gap ledger. It is a gap to fix, or an
explicit escalation.

## What every review actually checks

The recurring failure is code that is present and green in isolation but **never wired
and fed** in production. Every review applies these checks in addition to the
area's own lens checks:

- For every new exported field, collaborator or config the area touches, `grep -rn <Symbol>` the whole repo. Is it constructed or populated outside `*_test.go`? If it is only ever set in tests, the review fails. (Seed example: `Store.Supersede` has no production caller, gap G12.)
- For every new component: is it assembled into the real composition root (`initDomain` / `newLayoutStack` / `routerDeps` / `mcpDeps`) so the live path reaches it? Name the wiring site, or fail the review.
- Does any production call site pass `nil`, empty or hardcoded values where a real producer should feed it? (For example: `Layouts` left nil, a fake renderer, a constant budget.)
- Do any tests pass only because a fake ignores a load-bearing argument, or because an "integration" test asserts an intermediate hop instead of the sink (the SQLite row, the layout.json bytes, the HTTP/MCP response, the rendered DOM)?
- Does the area have coverage across all five scenario categories (happy, success, failure, error, edge), and would at least one test **fail** if its production input were nil or empty?

Do not relax reviewer thresholds, deadlines or assertions to turn a red test green.
Do not skip real-browser tests and count the skip as acceptance. Do not bypass
authorisation or settlement.

---

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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` and `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` and `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` and `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
