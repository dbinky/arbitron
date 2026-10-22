# RALPH — Workflow Auto-Arrangement (branch `dev-workflow-auto-arrangement-2026-10-18-9be59664`)

## Goal

Refine the work this branch adds on top of `345f751` (merge of main into
`dev-workflow-visual-readability-2026-10-16-853cf863`, the shallow branch point) until
every behaviour it promises is **wired-and-fed** in production and fails explicitly
instead of silently. The branch has 27 commits and touches 145 files. Most of them add
real-root acceptance for the §6.18 current-source contracts (crash settlement, active
rename, seed resume, retry lock sets, stdio transport, renderer and fonts). The rest
deliver the first P3T15.1 slices of parallel-group arrangement: captured ownership,
measured header text, ownership forwarded to render and review, stale-input rejection,
and revision-bound `View.Groups`.

P3T15.2–15.6 (reviewed group geometry, the v2 format, group drawing, group raster
review and live descendant tiles) are **not implemented** on this branch. That is an
explicit escalation in `docs/reference/gaps-identified.md` under `Won't Fix`. The loop
verifies that the interim group path fails explicitly. It does not build the missing
feature.

## Documents

- Spec: [docs/specs/2026-10-16-workflow-auto-arrangement-spec.md](docs/specs/2026-10-16-workflow-auto-arrangement-spec.md)
- Designs: [phase 1](docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md), [phase 2](docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-2.md), [phase 3](docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md). Read each one's "Current-source and parallel-group alignment (2026-10-18)" section first.
- Cross-task contracts: [docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md](docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md). §6.18 and §6.19 are binding for this branch.
- Plans: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-{1,2,3}-task-*.md` (P1T1–P1T9, P2T1–P2T9, P3T1–P3T15). Only P3T15 has open boxes.
- Single-system plan: [docs/reference/focus-areas.md](docs/reference/focus-areas.md)
- Paired seam plan: [docs/reference/paired-reviews.md](docs/reference/paired-reviews.md)
- Gap ledger: [docs/reference/gaps-identified.md](docs/reference/gaps-identified.md)
- Previous loop's seam table for the pre-branch feature (all approved, historical only): [docs/reference/historical/2026-10-16-workflow-auto-arrangement-paired-reviews.md](docs/reference/historical/2026-10-16-workflow-auto-arrangement-paired-reviews.md)

## Test commands

```
go test ./...
cd dashboard && npx tsc --noEmit && npx vitest run
```

Real-browser tests **skip** under plain `go test ./...`. Area 6 and seam rows
5 and 7 depend on them. When a pass touches those paths, run the named test with the
browser required, narrowed by name:

```
ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron/ -run '^TestName$' -count=1
ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ -run '^TestName$' -count=1
```

A skipped browser test is not evidence. Rebuild embedded assets (`make ui`) before a
browser test if any `dashboard/src` file changed.

### Branch-point baseline (failures that existed BEFORE this feature)

| Suite | Command | Passed | Failed | Failing tests |
|---|---|---|---|---|
| go | `go test ./...` | 9085 | 1 | `cmd/arbitron.TestLayoutE2E_NilCopiedIDStillRequests` |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | 1369 | 0 | none |

Seed note: commit `cfe19ac` rewrote that baseline test's expectation ("the copy gets an
independent identity"). It passes on HEAD (`go test ./cmd/arbitron -run
'TestLayoutE2E_NilCopiedIDStillRequests$'`: ok). Area 7 checks that the rewrite matches
production intent and does not weaken the test.

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this branch's job
and must NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens. Every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

`gaps-identified.md` never records an inert or no-op production path as acceptable. An
unfed path is a gap to fix or an explicit escalation, never a tolerated no-op.

## Budget arithmetic

| Item | Count |
|---|---|
| Single-system areas | 7 |
| Paired seams | 7 |
| Implied minimum passes (2 × 7 + 7) | **21** |
| Budget (500 × 0.35) | **175** |

21 ≤ 175. No seam was deferred for budget.

## Plan critique

- areas 9 → 7, seams 7 → 7, implied passes 25 → 21, budget 175.
- Merge seeded Areas 4 + 7 → Area 4: same HTTP/MCP/query files; the invalid-group read/retry exception needs the authorisation and generation path to be judged wired (merge-test clauses 1 and 2).
- Merge seeded Areas 6 + 9 → Area 6: one Coordinator/browser/measurement/render/review path; both read the same renderer and surface files, and group sizing depends on real font measurement (clauses 1 and 2). Seeded Area 8 becomes Area 7.
- Strike former seam 5: render-job encoding/decoding is an internal hop already in the group worker's path; retained as Area 6 Review 1 check 11 and under No seam.
- Add seam 6, Area 3 ↔ Area 4: retry and seed resume share the immutable row acknowledgement; missing-row retry uses `q.now()` while exact resume compares `Presentation.SavedAt`. Trace both row-present and row-missing recovery without weakening equality.
- Complete Area 7 (seeded Area 8): correct Promise and both lenses to require child-contract validation BEFORE fixed mutation locks, with final authority/settlement under lock; verified in P2T3, `publishRevision`, `precheckCopiedGroups` and `seedRevision`.
- Complete Area 3: add the explicit pending/no-claim check for a peer relocation; the old No seam startup-order rationale was not valid across processes.
- Complete Areas 4 and 6: consolidate all fields/lenses from their merged blocks and retain the ordinary/group browser controls; Area 4 explicitly checks that invalid-group tolerance cannot bypass authorisation.
- Complete Area 5: anchor the identity-error, EOF and SQLite-contention failure checks to their production/harness symbols.
- Block test: No changes needed to the remaining complete blocks; resolve named Go test checks to their source files and preserve at least three concrete checks per lens. Area 1 keeps the first-raw-read mutation proof separate from the unwired-reader policy in G2.
- Seam test: No changes needed to the other six retained seams or reverse-row handling. Every remaining pair is accounted for; seed/retry is no longer incorrectly labelled unrelated.
- Size/budget test: recomputed 2 × 7 + 7 = 21 ≤ 500 × 0.35 = 175; No changes needed for budget deferral.
- Risk order: promote historical-retry seam 4 to high (authority/history corruption) and browser/stdout seam 5 from low to high (whole MCP stream corruption); both precede medium ownership/display checks. Single-system risk order: No changes needed.
- Scope check: retain W1 as an explicit feature-completeness escalation; no production or test changes, approval changes, or role-block edits.

## What every review checks

- For every new exported field, collaborator or config the branch added, `grep -rn <Symbol>` the whole repo. Is it **constructed or populated outside `*_test.go`**? If it is only set in tests, the review fails.
- For every new component: is it assembled into the real composition root (`cmd/arbitron/main.go:initDomain`, `cmd/arbitron/layout_wiring.go:newLayoutStack`/`attachQuery`) so the live path reaches it? Name the wiring site, or fail the review.
- Does any production call site pass `nil`, empty or hardcoded values where a real producer should feed it?
- Do any tests pass only because a fake ignores a load-bearing argument, or because an "integration" test asserts an intermediate hop instead of the sink?
- Does the area have coverage across all five scenario categories (happy, success, failure, error, edge)? Would at least one test **fail** if its production input were nil or empty?

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
