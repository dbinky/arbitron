# RALPH — Workflow Auto-Arrangement, reviewed parallel groups (branch `dev-workflow-auto-arrangement-2026-10-19-99d6a783`)

## Goal

Refine the work this branch adds on top of `4c785c9` (the tip of
`dev-workflow-auto-arrangement-2026-10-18-9be59664`, the shallow branch point) until
every behaviour it promises is **wired-and-fed** in production and fails explicitly
instead of silently.

The branch is commits `26f8d31..9a02802`, which implement the reviewed parallel-group
arrangement that the previous loop escalated as missing:

| Task | Commit | What it adds |
|---|---|---|
| P3T16 | `05a0e1a` | recursive group placement, routing and preflight; v2 format and contract |
| P3T17 | `abf3a90` | one group drawing path, actual measurement, capture and runtime probes |
| P3T18 | `4deec6c` | group reviewer, raster evidence and authoritative stored results |
| P3T19 | `b55cc5d` | live group inputs, independent child layouts and route mounts |
| P3T20 | `70162e9` | real composition-root and release proof |

It touches 217 files, about 4,000 of them production lines. The one open plan step is
P3T16's Unicode task-key sizing box, which is blocked on a validation-policy decision
(`docs/reference/gaps-identified.md`, Won't Fix W1). The loop does not build missing
features. It verifies and hardens the four behaviours below.

## Documents

- Spec: [docs/specs/2026-10-16-workflow-auto-arrangement-spec.md](docs/specs/2026-10-16-workflow-auto-arrangement-spec.md)
- Designs: [phase 1](docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md), [phase 2](docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-2.md), [phase 3](docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md). Read each one's parallel-group alignment section first.
- Cross-task contracts: [docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md](docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md). §§6.19–6.20 are binding for this branch.
- Plans: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-{1,2,3}-task-*.md`. This branch executes [P3T16](docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-16.md), [P3T17](docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-17.md), [P3T18](docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-18.md), [P3T19](docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-19.md) and [P3T20](docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-3-task-20.md). Their execution records name the RED/GREEN evidence logs.
- Single-system plan: [docs/reference/focus-areas.md](docs/reference/focus-areas.md)
- Paired seam plan: [docs/reference/paired-reviews.md](docs/reference/paired-reviews.md)
- Gap ledger: [docs/reference/gaps-identified.md](docs/reference/gaps-identified.md)
- Previous loop (pre-branch scope, all historical): `docs/reference/historical/2026-10-18-workflow-auto-arrangement-{RALPH,focus-areas,paired-reviews,gaps-identified}.md`

## Test commands

```
go test ./... -count=1
cd dashboard && npx tsc --noEmit && npx vitest run
```

Real-browser tests **skip** under plain `go test ./...`. Areas 2, 3 and 4 (root
measurement) and paired rows 1–4 depend on them. When a pass touches those paths,
run the named test with the browser required, narrowed by name:

```
ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ -run '^TestName$' -count=1
ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron/ -run '^TestName$' -count=1
```

A skipped browser test is not evidence. If any `dashboard/src` file changed, rebuild the
embedded assets (`make ui`) before a browser test and commit the rebuilt
`adapter/dashboard/dist`. The full browser gate is `make test-render`, which is
unfiltered over both packages and has a 90-minute timeout. Do not run it per pass.

### Branch-point baseline (failures that existed BEFORE this feature)

| Suite | Command | Passed | Failed | Failing tests |
|---|---|---|---|---|
| go | `go test ./... -count=1` | 9524 | 0 | none |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | 1403 | 0 | none |

Both suites built and passed at the branch point. Any failure is NEW unless shown to be
an environment flake. The P3T19 and P3T20 records report route-test timeouts under heavy
host load: rerun the failing file alone before treating such a timeout as a
regression, and never raise a timeout or weaken an assertion to make it pass.

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
| Single-system areas | 4 |
| Paired seams | 5 |
| Implied minimum passes (2 × 4 + 5) | **13** |
| Budget (500 × 0.35) | **175** |

13 ≤ 175. No seam was deferred for budget. `paired-reviews.md` records merged,
duplicate, covered and unrelated combinations under `## No seam`.

## Plan critique

- areas 6 → 4, seams 6 → 5, implied passes 18 → 13, budget 175
- Merge seed areas 2 + 5 → Area 2: merge-test clauses 1–3; `RenderSurface.runRenderJob` shares static collection, probe comparisons and restoration, and `ReadabilityReviewer.Review` has one accepted verdict.
- Merge seed areas 3 + 6 → Area 3: merge-test clauses 1–2; `GraphView` joins polled geometry and live maps into the same tiles, and `ArrangedGraph` supplies their selection/follow scene. Areas 1 and 4 keep their numbers.
- Strike seed seam 6 as a separate row: consolidate route drawing/parity into row 3, now the same Area 2 ↔ Area 3 pair. No check is dropped.
- Strike seed seam 5 as a separate row: consolidate child RetryAck/generation ordering into row 4, now the same Area 1 ↔ Area 3 pair. No check is dropped.
- Add seam 5, Area 4 ↔ Area 1: P3T16 mixed-crossing scores cross persisted retry generations into pre-branch `storedBetter`; the cited score-only and render-error tests do not prove that comparison.
- Block test: No changes needed for missing fields or minimum check counts; all six seed blocks were populated. Merged blocks retain both sets of concrete checks.
- Area 2 block corrected: `HasRenderedEvidence` lives in `coordinator.go`; `animationPhases` lives in `RenderSurface.tsx`. `job.ts:groupsForJob` rejects missing ownership before capture. G1 records one demonstrated failure, not unproved failure on every attempt.
- Area 4 block corrected: `cloneGroupGraph` lives in `group_geometry.go`; the one-attempt scoring test does not establish retained-best selection. Paired row 5 names the missing cross-generation check.
- Seam test: No changes needed for reverse rows. Replaced false “no shared state” exclusions with explicit coverage by retained seams; empty winner is omitted by the live producer and is not an unsupported-value BREAK.
- Size/budget test: 2 × 4 + 5 = 13 ≤ 500 × 0.35 = 175; no deferrals.
- Risk-order test: No changes needed; revision/evidence/redaction consequences remain ahead of geometry quality. Merged route/probe checks inherit their containing area's higher risk; the added best-score seam is med.

## What every review checks

- For every new exported field, collaborator or config the branch added, `grep -rn <Symbol>` the whole repo. Is it **constructed or populated outside `*_test.go`**? If it is only set in tests, the review fails.
- For every new component: is it assembled into the real composition root so the live path reaches it? Name the wiring site, or fail the review. The roots are `cmd/arbitron/layout_wiring.go` (`NewBrowserRenderer` :64, `NewReadabilityReviewer` :76, `NewCoordinator` :141, `NewWorker` :145) for the server, and `dashboard/src/routes/WorkflowReview.tsx:ReviewPage` and `dashboard/src/routes/ExecutionView.tsx:GraphView` for the client.
- Does any production call site pass `nil`, empty or hardcoded values where a real producer should feed it?
- Do any tests pass only because a fake ignores a load-bearing argument, or because an "integration" test asserts an intermediate hop instead of the sink? `domain/layout/layouttest` doubles must honour and copy every group input, and must never be installed at the root.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./... -count=1` and `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./... -count=1` and `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./... -count=1` and `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
