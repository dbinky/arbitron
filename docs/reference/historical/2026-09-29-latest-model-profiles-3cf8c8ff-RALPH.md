# RALPH — Latest Model Profiles Review Loop

You are in an automated refinement loop and the user is unavailable for input, so
do the work without asking for any user input.

## Goal

Review and harden the Latest Model Profiles feature on branch
`dev-latest-model-profiles-2026-10-18-3cf8c8ff`: the 20 commits from `0ce2359`
(branch point) to `978362e`. For every behaviour the branch promises, prove that it
is **wired-and-fed** in production and that it fails safely.

The branch's promises, from the spec, the D1–D9 decision record and the commit log:

- **Run pins (D1).** A post-migration run keeps the exact models pinned at trigger. NULL-pin legacy runs resolve live.
- **D8 opaque routes.** Native Azure/Bedrock profiles are pinned-only. A pinned run executes its configured deployment or model ID through a route exception persisted at trigger (migration 031). Following and usability claims on those routes are refused.
- **Refresh.** A startup, manual or four-hour refresh discovers every catalog model on every compatible configured path. It atomically advances eligible following profiles after capability validation, and recovers failed publication on the next cycle.
- **Exact-model evidence (D3–D6).** On Claude Code, Codex, pi, OpenCode and direct connections, a probe or a pinned run is accepted only on the harness's own evidence. Each probe runs on the same launch path as real runs.
- **Interactive follow-up turns** stay on the pinned model.
- **Profiles.** Admins create and edit pinned or following profiles through MCP. YAML seed creates them on first boot, initialized from durable prospective discovery, before serving.
- **Classification.** The editable D2 identity policy and the guarded catalog fetch classify models, with canonical identity kept stable across scoped routing prefixes.
- **Announcements (D7).** Teams announcements of newly usable models are at-least-once. The webhook is empty by default and redacted on public reads.

## Inputs

- Spec: [`docs/specs/2026-09-29-latest-model-profiles-spec.md`](docs/specs/2026-09-29-latest-model-profiles-spec.md)
- Decisions (they win over any plan text): [`docs/designs/2026-09-29-latest-model-profiles-decisions.md`](docs/designs/2026-09-29-latest-model-profiles-decisions.md)
- Designs:
  - [phase 1](docs/designs/2026-09-29-latest-model-profiles-design-phase-1.md)
  - [phase 2](docs/designs/2026-09-29-latest-model-profiles-design-phase-2.md)
  - [phase 3](docs/designs/2026-09-29-latest-model-profiles-design-phase-3.md) (its "Current closure map" is authoritative)
  - [phase 4](docs/designs/2026-09-29-latest-model-profiles-design-phase-4.md)
  - [source evidence](docs/designs/latest-model-profiles-source-evidence.md)
- Plans: `docs/plans/2026-09-29-latest-model-profiles-implementation-phase-{1..4}-task-*.md`:
  - P1T1–P1T4
  - P2T1–P2T3
  - P3T1–P3T8
  - P4T1–P4T3
- Tracking:
  - [`docs/reference/focus-areas.md`](docs/reference/focus-areas.md): single-system plan, 10 behaviours.
  - [`docs/reference/paired-reviews.md`](docs/reference/paired-reviews.md): 13 seams.
  - [`docs/reference/gaps-identified.md`](docs/reference/gaps-identified.md): the open-gap ledger.
- The previous run's tracking files for this feature are archived under `docs/reference/historical/2026-09-29-latest-model-profiles-9cb0e72f-*.md`. They are history, not a checklist.

## Test commands

```
go test ./...
cd dashboard && npx tsc --noEmit && npx vitest run
```

**Branch-point baseline (failures that existed BEFORE this branch, not this branch's job):**

| Suite | Command | Build | Passed | Failed | Pre-existing failing tests |
|---|---|---|---|---|---|
| go | `go test ./...` | ok | 7267 | 1 | `github.com/arbitron/arbitron/adapter/config::TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` |
| dashboard | `cd dashboard && npx tsc --noEmit && npx vitest run` | ok | 526 | 0 | none |

A pre-existing failure never blocks the loop. A NEW failure, or anything that built at
baseline and no longer builds, is this branch's job.

## Budget

| Item | Value |
|---|---|
| Single-system areas | 10 |
| Paired seams | 13 |
| Implied minimum passes = 2 × 10 + 13 | **33** |
| Budget = 500 × 0.35 | **175** |

33 ≤ 175. No seam is deferred for budget. The headroom is for closing the open gaps
in `docs/reference/gaps-identified.md`, fixing regressions and re-reviewing.

## Plan critique

- areas 10 → 10, seams 17 → 13, implied passes 37 → 33, budget 175
- Merge test: No changes needed. Retained route, lane and entry splits; clarified probe ownership and folded real-run parity into Areas 4–5. No standalone layer or small-change area remains.
- Struck seeded row 3 (Areas 1 ↔ 4): internal batch pin-to-Claude/Codex hop; Area 1 wiring 6 and failure 7 own it.
- Struck seeded row 4 (Areas 1 ↔ 5): internal batch pin-to-pi/OpenCode/HTTP hop; Area 1 wiring 7 and failure 8 own it.
- Struck seeded row 7 (Areas 6 ↔ 4): internal continuation request-to-harness hop; Area 6 wiring 5 and failure 6 own it.
- Struck seeded row 8 (Areas 6 ↔ 5): HTTP verification is the continuation sink; Area 6 wiring 6 and failure 7 own it.
- Struck seeded row 10 (Areas 2 ↔ 7): shared `ValidatePinRoutes`, explicit refusal rather than a silent break; underlying connection edits are not profile edits. Area 7 wiring 5 and Area 2 failures 1–3 retain the checks.
- Struck seeded row 14 (Areas 1 ↔ 7): production `explicitVersion` preserves spelling, so the proposed differing-model break is unsupported; trigger refusal is explicit. Area 7 failure 6 and G11 now state the actual invariant.
- Added row 12 (Areas 7 ↔ 10): prospective admin discovery commits durable events before profile persistence; later drains must deliver frozen payloads even without a new delta.
- Added row 13 (Areas 9 ↔ 10): prospective seed discovery can commit events before startup refresh; interruption/reopen must preserve delivery and suppression.
- Block test: No changes needed to missing fields or minimum lens counts; all ten blocks were complete. Added the struck-row checks to Areas 1, 6 and 7; corrected Area 7's stale-policy assertion against `CandidateService.read`/`remapView`.
- Reverse-row test: No changes needed; no reverse pairs. All 45 combinations remain accounted for: 13 seams and 32 No seam entries.
- Size/budget test: No changes needed to area count or deferrals; recomputed 2 × 10 + 13 = 33 ≤ 500 × 0.35 = 175.
- Risk order: No changes needed to surviving relative order; high-consequence execution/state rows precede medium notification rows. Added notification seams at the end.

## Work order

Areas and seams are taken in table order, which is highest risk first. A pass whose
most important issue is an open gap in `gaps-identified.md` closes that gap, or a
coherent TDD slice of it. Never close a gap by constructing an inert collaborator,
by writing a fixture that bypasses the production producer, by calling a sink directly
instead of through its entry point, or by recording a "BLOCKED" note in a plan file.
Where a gap truly needs an owner decision (it is marked `[ESCALATION]`), keep it open
with the options stated; never mark it acceptable.

## What every review actually checks

The tables say *what* to review. These criteria decide whether a review passes. The
recurring failure is code that is present and green in isolation but **never
wired-and-fed** in production.

- **New symbols.** For every new exported field, collaborator or config the branch added, `grep -rn <Symbol>` the whole repo. Is it **constructed or populated outside `*_test.go`**? If it is only ever set in tests, the review fails.
- **Composition root.** For every new component: is it assembled into the real composition root (`cmd/arbitron/main.go:initDomainWithModelDependencies`, `cmd/arbitron/model_refresh.go:newModelRefresh`, `cmd/arbitron/model_dependencies.go:domainStack.Start`) so the live path reaches it? Name the wiring site, or fail the review.
- **Real inputs.** Does any production call site pass `nil`, empty or hardcoded values where a real producer should feed it?
- **Honest tests.** Do any tests pass only because a fake ignores a load-bearing argument? Or because an "integration" test asserts an intermediate hop instead of the sink? Or because they drive a test-only entry (for example `modelRefresh.start()`) instead of the production one?
- **Coverage.** Does the area have coverage across all five scenario categories (happy, success, failure, error, edge)? Would at least one test **fail** if its production input were nil or empty?

## Definition of done

The repo may already be red on arrival. Pre-existing failures are NOT this branch's
job and must NOT block the loop. "Done" means ALL of:

- Every single-system review has **2 approvals**, one per lens. Every paired review has **1**, covering both directions.
- No unchecked items remain in `docs/reference/gaps-identified.md`.
- Every area is **wired-and-fed**, verified by grep rather than by a green isolated test.
- Every paired row's **BREAK** has been shown impossible or caught.
- **No NEW test failures** beyond the recorded baseline, and nothing that built at baseline is broken.

An inert or no-op production path is never "acceptable". An unfed production path is a
gap to fix or an explicit `[ESCALATION]`, never a tolerated no-op.

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
