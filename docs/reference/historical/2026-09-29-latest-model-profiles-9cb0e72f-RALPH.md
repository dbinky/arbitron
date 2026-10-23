# RALPH — Latest Model Profiles Review Loop

You are in an automated refinement loop and the user is unavailable for input, so
do the work without asking for any user input.

## Goal

Deliver and review the Latest Model Profiles spec on branch
`dev-latest-model-profiles-2026-10-16-9cb0e72f`. For every behaviour the branch
promises, prove it is **wired-and-fed** in production and that it fails safely:

- run pins: a post-migration run keeps the model versions it started with, NULL-pin legacy runs resolve live (D1);
- exact-model execution evidence on Claude Code, Codex, pi, OpenCode and direct connections (D3–D6);
- pinned and following profiles, created and edited by admins or by YAML seed;
- an admin-editable D2 identity policy that classifies the catalog;
- a single-flight startup / manual / four-hour refresh that discovers usable models and atomically advances eligible following profiles;
- editable catalog URL and Teams webhook settings, with the webhook empty by default and redacted on public reads;
- at-least-once Teams announcements of newly usable models (D7).

### State at seeding (HEAD `7ec204d`, 142 commits over `main` `315ee04c`)

Built and reachable from `cmd/arbitron/main.go`:

- settings (P1T1–P1T2);
- run pins and pinned resolution on every consumer (P1T3, most of P1T4);
- profile selection fields, admin validation and validated SQL writes (P2T1–P2T3 service portions);
- the identity policy and its MCP tools (P3T8, P3T1);
- the guarded fetcher and normalizer (P3T2);
- the execution-path inventory, revisions and gate (P3T3);
- per-lane exact-model execution verifiers (P3T4 execution slice);
- the catalog store and migration 022 (P3T5);
- the synchronous Teams model sender (P4T1), plus the announcement service and claim repository (P4T2).

**Not built.** These are the open build gaps in
`docs/reference/gaps-identified.md`:

- the tool-disabled **discovery probe router** (P3T4 remainder);
- `CandidateService` / `DiscoveryPipeline` / `RefreshService` / `RefreshCoordinator` / `RefreshLoop` (P3T6);
- the root `ModelDependencies` build/start split, candidate injection before `SeedProfiles`, and the catalog MCP tools (P3T7);
- production construction of the announcement service and sender (P4T3);
- Azure and Bedrock exact-execution producers (P1T4 remainder);
- `selection_mode` / `follow_target` / `revision` on the MCP profile tools (P2T2 remainder). `ProfileService.SetModelSelection` is never called in production, so every following create fails today with `ErrSelectionPortsMissing`.

Production Go has no caller of `ModelCatalogRepo.CommitCycle`, so today no refresh
happens, no profile advances and no announcement is sent.

These are **planned** functionality with plan tasks, not invented functionality,
and they are not Won't Fix. Implement them by following each plan task exactly.
The decisions record wins where a plan conflicts with it. **Never** close one by
constructing an inert collaborator, a typed `CommitCycle` fixture, a direct
`Drain` call or a second scheduler. The earlier loop recorded "BLOCKED" notes in
the plan files for 16 passes instead of building the missing producer. A pass
whose most important issue is an unbuilt producer **builds it**, or a coherent
TDD slice of it.

### Build order inside a pass

Areas are reviewed in **risk order**. Code is built in **producer order**, along
the critical path:

1. B1 (P3T4 discovery probe router)
2. B2 (P3T6 candidates, refresh service and loop)
3. B3 (P3T7 root assembly and catalog tools)
4. B4 (P4T3 announcement root wiring)

B5 (P1T4 Azure/Bedrock) and B6 (P2T2 MCP profile selection fields and errors)
are independent of that path. B6 can land before B3; B3 then injects the real
candidate source behind it.

When an area's path is unbuilt, the area fails the wiring lens. Its most
important issue is then the **earliest open B-gap on its path**. Build it, record
which area it advances, then stop.

## Documents

- Spec: [`docs/specs/2026-09-29-latest-model-profiles-spec.md`](docs/specs/2026-09-29-latest-model-profiles-spec.md)
- **Decisions (authoritative over plan text):** [`docs/designs/2026-09-29-latest-model-profiles-decisions.md`](docs/designs/2026-09-29-latest-model-profiles-decisions.md) (D1–D7)
- Designs:
  - [phase 1 — settings and run pinning](docs/designs/2026-09-29-latest-model-profiles-design-phase-1.md)
  - [phase 2 — following profiles](docs/designs/2026-09-29-latest-model-profiles-design-phase-2.md)
  - [phase 3 — catalog refresh; authoritative task graph, migration ownership, shared contracts](docs/designs/2026-09-29-latest-model-profiles-design-phase-3.md)
  - [phase 4 — announcements](docs/designs/2026-09-29-latest-model-profiles-design-phase-4.md)
  - source evidence: [`docs/designs/latest-model-profiles-source-evidence.md`](docs/designs/latest-model-profiles-source-evidence.md)
- Plans:
  - [P1T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md), [P1T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-2.md), [P1T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-3.md), [P1T4](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-4.md)
  - [P2T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-1.md), [P2T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-2.md), [P2T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-3.md)
  - [P3T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-1.md), [P3T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-2.md), [P3T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-3.md), [P3T4](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-4.md), [P3T5](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-5.md), [P3T6](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-6.md), [P3T7](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-7.md), [P3T8](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-3-task-8.md)
  - [P4T1](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-1.md), [P4T2](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-2.md), [P4T3](docs/plans/2026-09-29-latest-model-profiles-implementation-phase-4-task-3.md)
- Tracking:
  - [`docs/reference/focus-areas.md`](docs/reference/focus-areas.md): single-system plan with 8 behaviours. Each block names the entry, sink, files, folded checks, both lenses and seed evidence.
  - [`docs/reference/paired-reviews.md`](docs/reference/paired-reviews.md): 14 seams, plus 14 combinations recorded under `## No seam`.
  - [`docs/reference/gaps-identified.md`](docs/reference/gaps-identified.md): build gaps, carried defects and Won't Fix.
- The earlier loops' files are archived under `docs/reference/historical/2026-09-29-latest-model-profiles-*`. Do not read them during a pass.

## Test commands

Use exactly these toolchains, narrowed to the package or test name you touched:

| Suite | Command | Branch-point baseline |
|---|---|---|
| go | `go test ./...` | 5456 pass, 1 fail: `adapter/config::TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (pre-existing) |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | 526 pass, 0 fail |
| install-bats | `shellcheck install.sh && bats tests/install` | 246 pass, 0 fail |
| release-bats | `bats tests/release` | 24 pass, 2 fail: `tests/release/dist_naming.bats::SHA256SUMS has 6 well-formed basename-only lines`, `tests/release/dist_naming.bats::two dist runs of the same commit yield byte-identical SHA256SUMS` (pre-existing) |

The three baseline failures are not this branch's job and must not block the
loop. Fix only **new** failures.

For dashboard changes, run `make ui` from the repository root afterwards, so the
embedded `adapter/dashboard/dist` matches the source.

Whole-project `sqlc generate` trips over the hand-maintained `sqlc.slice` query
in `adapter/sqlite/queries/instances.sql`. Regenerate in scope and hand-check the
generated conversions.

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this
branch's job and must NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens; every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

`gaps-identified.md` must never record an inert or no-op production path as
acceptable. An unfed production path is a gap to fix or an explicit escalation,
never a tolerated no-op.

## Budget

| Item | Count |
|---|---|
| Single-system areas | 8 |
| Paired seams | 14 |
| Implied minimum passes (2 × 8 + 14) | 30 |
| Budget (500 × 0.35) | 175 |

2 × 8 + 14 = 30 ≤ 175. No seam is deferred for budget.

The headroom covers two kinds of work. The first is the build gaps B1–B6: they
are the "fix the most important issue" step of the failing wiring reviews, not
extra areas. The second is gap closing and re-review.

## Plan critique

- areas 9 → 8, seams 15 → 14, implied passes 33 → 30, budget 175
- Merge: old Areas 2 + 7 → Area 2; merge-test clauses 1 and 3. Fetch/normalize/map are fed only by refresh and terminate at its `CommitCycle`; policy classification cannot be accepted without its selector. Retain the MCP policy edit and revision/CAS checks within that path. Old Areas 8/9 become 7/8.
- Struck former seam 10 (old 2 ↔ 7): internal classification-to-selection hop; moved policy races, excluded snapshots and unmatched targets into Area 2 lenses.
- Struck former seam 15 (old 7 ↔ 9) as a separate row: duplicates 2 ↔ 8 after merging; canonical-ID uniqueness retained alongside frozen changes in new row 13.
- Added seam 3 (3 ↔ 6): admin tools/connection edits feed resumed turns through the live cache, unlike model-only advancement; verify revoked/cleared tools, current route and old model pin together.
- Block test: no missing blocks or empty fields; completed the concrete symbol anchors listed below.
- Area 1: anchored trigger faults, missing run context, invalid routes and coherent capture to production symbols.
- Area 2: combined ingestion/policy files, both lenses and seed evidence; folded the internal seam into selector/commit checks.
- Area 3: anchored turn persistence, cancellation and session-end checks; linked admin edit coverage.
- Area 4: anchored launch/stream evidence and fingerprint checks; kept Codex account filtering in Area 2.
- Area 5: anchored each lane's evidence rejection and factory path.
- Area 6: anchored MCP schema, selection injection, seed and revision-return checks.
- Area 7: anchored settings auth/redaction/fault checks, corrected `Service.Public` to `Get`, and distinguished unreachable announcement `ReadInternal` callers from an absent caller.
- Area 8: anchored startup drain, event classification, claims, retry and sender checks.
- Merge test otherwise: No changes needed; retained batch/interactive entries, distinct harness lanes, admin profile writes, settings edits and external announcement delivery.
- Seam test otherwise: No changes needed; no reverse pairs. Added task/commit provenance to retained rows and rewrote `No seam` for all 28 current combinations.
- Size/budget test: No changes needed beyond recomputation; 30 ≤ 175, none deferred.
- Risk order: promoted former seam 13 (new 11, settings ↔ refresh) to high and ahead of notification correctness: SSRF/query-token leakage outranks card content. Former row 12 (new 12) also precedes cards because accepted invalid profiles prevent workflow starts. Singles: No changes needed beyond the merge.
- Evidence corrections: Area 2 builds/injects candidates before seed and starts the loop after recovery; it uses historical current-model identity when absent from the latest slice; disappearance alone neither proves eligibility nor requires a blanket freeze (phase 3 design). Area 2 ↔ 3 no longer treats a live connection as pinned; `PinningResolver.Resolve` pins only the model.

## What every review actually checks

The tables say *what* to review; these criteria decide whether a review passes.
The recurring failure is code that is present and green in isolation but
**never wired-and-fed** in production:

- For every new exported field / collaborator / config the branch added: `grep -rn <Symbol>` the whole repo. Is it **constructed or populated outside `*_test.go`**? If it is only ever set in tests, the review fails.
- For every new component: is it assembled into the real composition root (`cmd/arbitron/main.go:initDomainWithModelStateSecret`) so the live path reaches it? Name the wiring site, or fail the review.
- Does any production call site pass `nil`/empty/hardcoded where a real producer should feed it?
- Do any tests pass only because a fake ignores a load-bearing argument? Does an "integration" test assert an intermediate hop (for example "Reload was called" or "CommitCycle returned nil") instead of the sink: the executed model in argv or the request body, the durable row, the receiver card?
- Does the area have coverage across all five scenario categories (happy, success, failure, error, edge)? Would at least one test **fail** if its production input were nil or empty?

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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./... · cd dashboard && npx tsc --noEmit && npx vitest run · shellcheck install.sh && bats tests/install · bats tests/release`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./... · cd dashboard && npx tsc --noEmit && npx vitest run · shellcheck install.sh && bats tests/install · bats tests/release`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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
6. **Run targeted tests** — the tests covering what you changed, using `go test ./... · cd dashboard && npx tsc --noEmit && npx vitest run · shellcheck install.sh && bats tests/install · bats tests/release`'s toolchains narrowed to the package or name filter. Do not re-run whole suites: the verify-tests script re-runs every touched suite against the recorded baseline after you commit. Fix NEW failures only.
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

