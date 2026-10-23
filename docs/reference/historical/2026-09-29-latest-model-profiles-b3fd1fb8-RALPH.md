# RALPH — Latest Model Profiles Review Loop

You are in an automated refinement loop and the user is unavailable for input, so
do the work without asking for any user input.

## Goal

Deliver and review the Latest Model Profiles spec on branch
`dev-latest-model-profiles-2026-10-01-b3fd1fb8`. For each promised behaviour, prove it
is **wired-and-fed** in production and that it fails safely:

- a four-hour catalog refresh;
- exact-model usability on Claude Code, Codex, pi, OpenCode and connections;
- pinned and following profiles;
- atomic advancement;
- immutable run pins;
- at-least-once Teams announcements.

**State at seeding (HEAD `ce7f8d0`):** this branch adds the owner's decisions D1–D7
and the aligned plans. The code holds only the earlier slices:

- settings (P1T1–P1T2);
- profile selection fields (P2T1);
- the raw guarded fetcher (part of P3T2);
- connection revisions and the model-state key (part of P3T3).

D1–D7 removed every blocker. The remaining tasks are **open build gaps B1–B15** in
`docs/reference/gaps-identified.md`. They are planned functionality, not invented
functionality, and they are not Won't Fix. Implement them by following each plan
task exactly; the decisions record wins where they conflict.

### Build order inside a pass

Areas are reviewed in **risk order**, but code must be built in **task-graph
order**: P3T8 → P3T1 → P2T2 → P3T3 → P2T3 → P3T2 → P3T4 → P1T3 → P1T4 → P3T5 → P3T6 →
P3T7 → P4T1 → P4T2 → P4T3. That is B1 → B15, and it is acyclic per the design phase 3
task graph.

When an area's path is unbuilt, the area fails the wiring lens. Its "most important
issue" is then the **earliest open B-gap on that area's path**, for example B1 for
Area 1, because B8 depends on B2, which depends on B1. Build that task, or a coherent
slice of it, with strict TDD. Record which area it advances, then stop. Never stub a
producer with nil, empty or hardcoded data to make a later area look wired.

## Documents

- Spec: [`docs/specs/2026-09-29-latest-model-profiles-spec.md`](docs/specs/2026-09-29-latest-model-profiles-spec.md)
- **Decisions (authoritative over plan text):** [`docs/designs/2026-09-29-latest-model-profiles-decisions.md`](docs/designs/2026-09-29-latest-model-profiles-decisions.md) (D1–D7)
- Designs. The mission named `2026-10-01-*` filenames; the files in this worktree are dated `2026-09-29`:
  - [phase 1 — settings and run pinning](docs/designs/2026-09-29-latest-model-profiles-design-phase-1.md)
  - [phase 2 — following profiles](docs/designs/2026-09-29-latest-model-profiles-design-phase-2.md)
  - [phase 3 — catalog refresh; authoritative task graph, migration ownership, shared contracts](docs/designs/2026-09-29-latest-model-profiles-design-phase-3.md)
  - [phase 4 — announcements](docs/designs/2026-09-29-latest-model-profiles-design-phase-4.md)
- Plans:
  - [P1T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md), [P1T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-2.md), [P1T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-3.md), [P1T4](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-4.md)
  - [P2T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-1.md), [P2T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-2.md), [P2T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-3.md)
  - [P3T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-1.md), [P3T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-2.md), [P3T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-3.md), [P3T4](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-4.md), [P3T5](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-5.md), [P3T6](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-6.md), [P3T7](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-7.md), [P3T8](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-8.md)
  - [P4T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-1.md), [P4T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-2.md), [P4T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-3.md)
- Tracking:
  - [`docs/reference/focus-areas.md`](docs/reference/focus-areas.md): single-system plan, 9 behaviours, each with entry, sink, files, folded checks, two lenses and seed evidence.
  - [`docs/reference/paired-reviews.md`](docs/reference/paired-reviews.md): 16 seams, plus 20 combinations recorded under `## No seam`.
  - [`docs/reference/gaps-identified.md`](docs/reference/gaps-identified.md): build gaps B1–B15, carried defects, and Won't Fix.
- The previous partial-delivery loop's files are archived as `docs/reference/historical/2026-09-29-latest-model-profiles-partial-*.md`.

## Test commands

Use exactly these toolchains, narrowed to the package or test name you touched:

| Suite | Command | Branch-point baseline |
|---|---|---|
| go | `go test ./...` | 5323 pass, 1 fail: `adapter/config > TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (pre-existing; sample aliases missing from the default profiles; not this branch's job) |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | did not build at baseline: `dashboard/node_modules` not installed |

For dashboard changes, run `npm ci` in `dashboard/` first, then `make ui` from the
repository root so `adapter/dashboard/dist` matches the source.

Whole-project `sqlc generate` hits the hand-maintained `sqlc.slice` query at
`adapter/sqlite/queries/instances.sql:86`. Regenerate in scope and hand-check the
conversions, as earlier tasks did.

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this
branch's job and must NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens; every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

`gaps-identified.md` must never record an inert or no-op production path as
acceptable. An unfed path is a gap to fix or an explicit escalation, never a
tolerated no-op. The staged `DeferredAnnouncer` counts as an unfed path until B15
replaces it.

## Budget

| Item | Count |
|---|---|
| Single-system areas | 9 |
| Paired seams | 16 |
| Implied minimum passes (2 × 9 + 16) | 34 |
| Budget (500 × 0.35) | 175 |

2 × 9 + 16 = 34 ≤ 500 × 0.35 = 175. No seams are deferred. The headroom of 141
passes remains available for B1–B15 build work as the "fix the most important issue"
step of failing wiring reviews, not as additional areas.

## Review boundaries after critique

- Area 3 owns the entire refresh path: startup/manual/timer entry, inventory and key-backed revisions, gate, selection, atomic CAS/journal/report, publication/recovery and catalog projection. The gate has no independent stale-advancement sink; the scheduler calls the same `RefreshService.RefreshInternal` reviewed for advancement.
- Areas 1 and 2 remain distinct execution entry points (node/child-input versus interactive continuation). Areas 5 and 6 remain distinct harness lanes. Shared implementation does not erase these allowed sub-path splits.
- Area 7 retains policy, mapper, normalizer and fetcher as one catalog-ingestion behaviour; Area 8 retains admin/YAML profile validation and persistence; Area 9 retains discovery-event creation through durable Teams delivery.
- Area 4 is the settings behaviour (seed Area 11), with its own authorized admin entries and persisted sink. Catalog-source and announcement consumers each have a paired hand-off.
- Migrations, generated queries, DTOs, source evidence, sample comments, clock seam, key bootstrap and connection revisions remain named checks inside the behaviours they serve.

## Plan critique

areas 11 → 9, seams 12 → 16, implied passes 34 → 34, budget 175

- Merge: seed Areas 3 + 4 → Area 3; clauses 1 and 3: inventory/gate tokens only prove stale-advancement rejection when `commitAdvanceTx`/`CommitCycle` consult them. Retained every revision/key check.
- Merge: seed Areas 3 + 9 → Area 3; clauses 1 and 2: `RefreshService.RefreshInternal` selection, commit, publication and recovery are the same path/files reached by the scheduler and manual refresh. Retained lifecycle, limits and public-catalog checks.
- Strike seed seam 3: gate ↔ advancement is internal to merged Area 3; retained the real connection-rotation/second-process race in its failure lens.
- Strike seed seam 6 as a separate row: inventory ↔ probes duplicates the merged Area 3 ↔ Area 5 evidence boundary; combined with seed seam 5 in row 7, preserving both revision and executable-spelling checks.
- Strike seed seam 10 as a separate row: loop ↔ admin edit duplicates Area 3 ↔ Area 8 after the merge; combined with seed seam 4 in row 6, preserving cold-seed and coordinator ordering.
- Strike seed seam 11 as a separate row: loop ↔ announcements duplicates Area 3 ↔ Area 9 after the merge; combined with seed seam 9 in row 14, preserving failed-fetch/startup drains.
- Add row 3, Area 1 ↔ Area 5: P3T4 proof readers versus P1T4 normal-run streaming/rollout consumers can accept the wrong executed model.
- Add row 4, Area 1 ↔ Area 6: P3T4 provider/session proof versus P1T4 pi/OpenCode execution can accept another provider or model.
- Add row 5, Area 2 ↔ Area 5: a P3T4 fresh-turn reader can validate the wrong resumed turn in P1T4.
- Add row 8, Area 3 ↔ Area 6: independent pi/OpenCode/HTTP native-spelling and configuration evidence must reach selection and execution intact.
- Add row 10, Area 4 ↔ Area 7: a stale but valid catalog URL can keep fetching successfully after an accepted settings edit; the seed's loud-failure rationale was false.
- Add row 12, Area 5 ↔ Area 7: P3T1/P3T2 unmatched NativeID retention must feed P3T4 Claude/Codex discovery without an eligibility filter.
- Add row 13, Area 6 ↔ Area 7: P3T1/P3T2 unknown-tier/slash-bearing identity must feed P3T4 pi/OpenCode/HTTP discovery without a listing or tier prerequisite.
- Add row 15, Area 7 ↔ Area 9: P3T1 canonical identity versus P3T5 lifetime event uniqueness can duplicate or suppress discoveries after a policy edit or aggregator change.
- Complete Area 1 block: anchored lookup, corruption, trigger, retry and output-acceptance checks to concrete files/symbols.
- Complete Area 2 block: anchored resumed-turn proof, missing-context, legacy and unsupported-backend failure checks.
- Complete Area 3 block (seed 3/4/9): retained and anchored transaction/publication, inventory/gate and lifecycle lenses, including the struck internal seam.
- Complete Area 4 block (seed 11): anchored auth, redaction, concurrent settings patches and dashboard writes; corrected root projection references.
- Complete Area 5 block: anchored account-cache, rollout, fallback, limits and confirmation-reuse checks.
- Complete Area 6 block: anchored export/provider mismatch, optional-listing, secret projection and opaque-model checks.
- Complete Area 7 block: anchored normalizer, policy, ordering and version-recognition failures; corrected the existing guarded-fetcher call chain.
- Complete Area 8 block: anchored selection, nil ports, seed retry, sparse-write conflicts and route preservation.
- Complete Area 9 block (seed 10): anchored classification, claims, recovery, multipart acknowledgement and URL redaction.
- Risk order: settings moves from seed Area 11/med to Area 4/high because unauthorized writes redirect the catalog/webhook and public reads can leak a webhook secret; it must precede notification-delivery risk. Both tables are high before med.
- Merge test: No changes needed for the remaining entry-point/harness sub-path splits and independent admin/ingestion/delivery behaviours.
- Block test: No changes needed to the eight required field names or entry/sink coverage; concrete lens anchors were incomplete as listed above.
- Seam test: No changes needed for reverse rows (none existed); struck rows are recorded under No seam, and all 36 area pairs are accounted for.
- Size/budget test: No changes needed to the 175-pass limit; 34 minimum passes fit and no budget deferrals are needed.
- Preservation: approvals remain empty; role blocks and definition of done are unchanged. Only the four tracking files changed.

## What every review actually checks

The tables say *what* to review; these criteria decide whether a review passes.
The recurring failure is code that is present and green in isolation but **never
wired-and-fed** in production:

- For every new exported field / collaborator / config the branch added:
  `grep -rn <Symbol>` the whole repo — is it **constructed or populated outside
  `*_test.go`**? If it is only ever set in tests, the review fails.
- For every new component: is it assembled into the real composition root so the
  live path reaches it? Name the wiring site, or fail the review.
- Does any production call site pass `nil`/empty/hardcoded where a real producer
  should feed it?
- Do any tests pass only because a fake ignores a load-bearing argument, or
  because an "integration" test asserts an intermediate hop instead of the sink?
- Does the area have coverage across all five scenario categories (happy, success,
  failure, error, edge), and would at least one test **fail** if its production
  input were nil/empty?

Each area's `### Area N` block in `docs/reference/focus-areas.md` lists its entry,
sink, full file list, folded-in checks, the wiring-lens and failure-lens checks,
and the seed evidence already found.

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
