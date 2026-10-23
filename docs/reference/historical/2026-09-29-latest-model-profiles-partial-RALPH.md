# RALPH — Latest Model Profiles Review Loop

You are in an automated refinement loop and the user is unavailable for input, so
do the work without asking for any user input.

## Goal

Review and refine what branch `dev-harness-and-model-management-2026-09-29-5e86d796`
(`22a953d..HEAD`) actually ships toward the Latest Model Profiles spec. For each
shipped behaviour, prove it is **wired-and-fed** in production and that it fails
safely. Keep every unbuilt or unfed part visible as a recorded, release-blocking
escalation, never as a tolerated no-op.

What the branch ships (live code):

- Persisted model settings: the catalog URL defaults to models.dev and the Teams announcement webhook defaults to empty. They can be edited through HTTP, MCP and the dashboard, with only the webhook redacted on public read (P1T1–P1T2).
- Following-profile selection mode, target and revision through YAML seed, SQLite and the cache; existing MCP edits preserve selection, but MCP cannot configure it (P2T1, W6).
- The raw guarded catalog fetch transport (part of P3T2), not yet wired.
- Connection config revisions and the installation model-state key bootstrapped at boot (part of P3T3).

What it does not ship: run pinning, CAS advancement, identity and usability
evidence, the catalog store, the refresh loop and announcements. These are
blocked on seven unresolved human decisions and recorded in
`docs/reference/gaps-identified.md` `## Won't Fix` (W1–W8). **Do NOT build them.**
They are new functionality owned by blocked tasks.

## Documents

- Spec: [`docs/specs/2026-09-29-latest-model-profiles-spec.md`](docs/specs/2026-09-29-latest-model-profiles-spec.md)
- Designs:
  - [phase 1 — settings and run pinning](docs/designs/2026-09-29-latest-model-profiles-design-phase-1.md)
  - [phase 2 — following profiles](docs/designs/2026-09-29-latest-model-profiles-design-phase-2.md)
  - [phase 3 — catalog refresh; authoritative task graph and dependency provenance](docs/designs/2026-09-29-latest-model-profiles-design-phase-3.md)
  - [phase 4 — announcements](docs/designs/2026-09-29-latest-model-profiles-design-phase-4.md)
- Plans (each carries its BLOCKED note):
  - [P1T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md), [P1T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-2.md), [P1T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-3.md), [P1T4](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-4.md)
  - [P2T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-1.md), [P2T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-2.md), [P2T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-3.md)
  - [P3T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-1.md), [P3T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-2.md), [P3T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-3.md), [P3T4](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-4.md), [P3T5](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-5.md), [P3T6](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-6.md), [P3T7](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-7.md)
  - [P4T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-1.md), [P4T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-2.md), [P4T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-3.md)
- Tracking:
  - [`docs/reference/focus-areas.md`](docs/reference/focus-areas.md): single-system plan, 4 areas.
  - [`docs/reference/paired-reviews.md`](docs/reference/paired-reviews.md): seams, 0 rows, with every considered combination under `## No seam`.
  - [`docs/reference/gaps-identified.md`](docs/reference/gaps-identified.md): open gaps and escalations.
- The previous feature's tracking files are archived under `docs/reference/historical/2026-07-27-workflow-spawn-node-*`.

## Test commands

Use exactly these toolchains, narrowed to the package or test name you touched:

| Suite | Command | Branch-point baseline |
|---|---|---|
| go | `go test ./...` | 5182 pass, 1 fail: `adapter/config > TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (sample workflow references aliases absent from the default profiles; unrelated to this branch) |
| install-bats | `shellcheck install.sh && bats tests/install` | 246 pass, 0 fail |
| release-bats | `bats tests/release` | 24 pass, 2 fail: `tests/release/dist_naming.bats > SHA256SUMS has 6 well-formed basename-only lines`, `tests/release/dist_naming.bats > two dist runs of the same commit yield byte-identical SHA256SUMS` |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | did not build at baseline: `dashboard/node_modules` was not installed in this worktree |

At seeding time (HEAD `2fd7552`), `go test ./...` shows only the baseline
failure. For dashboard changes, run `make ui` from the repository root so that
`adapter/dashboard/dist` matches the source.

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this
branch's job and must NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens; every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test. Where a hop ends at a consumer that a blocked task owns, the area passes only if that hop is recorded as a release-blocking escalation under `## Won't Fix` naming the blocked producer, and no production code feeds a nil, empty or hardcoded stand-in.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

`gaps-identified.md` must never record an inert or no-op production path as
acceptable. An unfed path is a gap to fix or an explicit escalation.

## Budget

| Item | Count |
|---|---|
| Single-system areas | 4 |
| Paired seams | 0 |
| Implied minimum passes (2 × 4 + 0) | 8 |
| Budget (500 × 0.35) | 175 |

2 × 4 + 0 = 8 ≤ 500 × 0.35 = 175. No budget deferrals. The plan is small
because the branch ships four live behaviours; the rest of the spec is unbuilt (W7) and blocked on human decisions (W8).

## Merge decisions (for the plan critic)

- **Area 3 merges three candidates:** "Teams webhook setting", "catalog URL setting" and "guarded catalog fetcher".
  - The fetcher has no production entry point: `NewFetcher` is called only by tests. Fold it under the catalog-setting behaviour it serves (merge-test clause 3), with the missing `Service.ReadInternal` → fetch path escalated in W1/W2.
  - The live catalog-setting sink is SQLite and its public response; outbound fetching is pending, not demonstrated wiring.
  - Both settings keys are reviewed by reading the same service, repo, handlers and dashboard card.
  - The `httpDeps` refactor and the dashboard error-parsing change are folded in as checks. They are hops on this path with no sink of their own.
- **Area 2 merges four candidates:** "YAML following seed", "profile_update selection patch", "profile revision" and "profile cache cloning".
  - All four write or read the same `profiles` columns through `ProfileRepo` and `rowToProfile`.
  - The revision has no independent entry point; it is written on the same create/update path and read through the same row mapper.
  - The advancement ports and journal table (`domain/provider/advance.go`, migration 021) have no entry or sink, so they are folded in as a check (W5).
- **Area 1 and Area 4 stay separate** although both come from commit 40026e8 and share migration 023.
  - They are read through disjoint files: `adapter/crypto` and `cmd/arbitron/model_state.go` versus `adapter/sqlite/connection_repo.go` and `queries/connections.sql`.
  - Each has its own entry (boot vs MCP `connection_update`) and its own sink (the key file and flag vs `connections.config_revision`).
  - Neither needs the other to be judged wired.
- **No layer areas.** Migrations, sqlc output, generated models, DTOs and docs are checks of the behaviour they serve.
- **No paired rows.** Every live cross-task hand-off of the new fields lies inside one behaviour. No cross-area candidate satisfies all three seam criteria; revision composition needs an unbuilt consumer. See `paired-reviews.md` `## No seam`.

## Plan critique

- areas 4 → 4, seams 0 → 0, implied passes 8 → 8, budget 175
- Merge test: No changes needed to area boundaries; no merges or splits. Area 2 shares the profile write/read path (clauses 1–2); Area 3's settings share service/repo/boundaries (clause 2), and its unfed fetcher is folded infrastructure without a production entry (clause 3). Areas 1 and 4 have separate entries and sinks despite shared migration/root files.
- Seam test: No changes needed; no struck or added seams. Rechecked all six area pairs and eight internal hand-offs. The resolver ignores revisions; connection deletion's profile-read failure is loud; the settings-to-fetch hand-off does not exist. Corrected No seam evidence accordingly.
- Block test, Area 1: qualified length-only key validation; require separate assessment of same-length corruption and tied boot/concurrency checks to their symbols.
- Block test, Area 2: corrected the MCP entry claim, repo-only concurrency evidence and cache/binding distinction; removed unsupported CAS recreation-safety reasoning and recorded it under W5.
- Block test, Area 3: separated live persistence sinks from the pending fetch consumer; corrected catalog/webhook redaction and HTTP/MCP authorization claims, with explicit handler/guard/fetch symbols.
- Block test, Area 4: distinguished direct-service tests from MCP dispatch and added concrete mixed-edit, rejection and referenced-delete checks. All four blocks retain every required field and at least three concrete checks per lens; no fields were missing.
- Size/budget test: No changes needed to counts or sequential numbering; added missing focus-header arithmetic and restated 2 × 4 + 0 = 8 ≤ 175 in both required files. No deferred seams.
- Risk-order test: No changes needed; high boot availability, profile integrity and settings authorization/secret protection precede medium revisions whose execution consumer is unbuilt.

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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` / `shellcheck install.sh && bats tests/install` / `bats tests/release` / `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` / `shellcheck install.sh && bats tests/install` / `bats tests/release` / `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./...` / `shellcheck install.sh && bats tests/install` / `bats tests/release` / `cd dashboard && npx tsc --noEmit && npx vitest run`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
