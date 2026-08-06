# Ralph Refinement Charter — Queue List Visibility

You are in an automated refinement loop and the user is unavailable for input, so
do the work without asking for any user input.

## Goal

Harden the **Queue List Visibility** feature to production quality. The queue list
shows all workflow instances in a richer, sortable, density-aware table so users
can instantly disambiguate simultaneous runs (especially multiple feature-pipeline
runs) without opening each instance. Each row carries a generated run name plus
separate sortable fields: workflow, submitter, submitted local time, status,
friendly current step, elapsed, priority-aware queue position, model/backend, and
retry count. A density toggle (balanced / dense / expanded) is remembered per user,
and last-error detail is lazy-loaded into an inline row-detail panel.

The implementation already exists and is green (see "Arrival state" below). This
loop **reviews, verifies provenance, deepens coverage, and closes gaps** — it does
not rebuild from scratch.

## Source documents

- Spec: `docs/specs/2026-06-25-queue-list-spec.md`
- Designs:
  - `docs/designs/2026-06-25-queue-list-design-phase-1.md` — backend enrichment + run name
  - `docs/designs/2026-06-25-queue-list-design-phase-2.md` — default ordering + priority-aware queue position
  - `docs/designs/2026-06-25-queue-list-design-phase-3.md` — dashboard rich table, density, row details
- Plans: `docs/plans/2026-06-25-queue-list-implementation-phase-{1,2,3}-task-*.md`
- Working trackers: `focus-areas.md`, `gaps-identified.md` (repo root)

## Test / build commands (use these EXACTLY — do not invent or guess)

```
go test ./...
cd dashboard && npx vitest run
cd dashboard && npx tsc --noEmit
```

`make ui` rebuilds the embedded dashboard bundle (`adapter/dashboard/dist`) that the
Go binary serves via `//go:embed dist/*` (`adapter/dashboard/embed.go`). Any change
under `dashboard/src` is NOT live until this is rerun — a green vitest run does not
prove the served binary changed.

## Branch-point test baseline (failures that existed BEFORE this feature)

These pre-existing failures are NOT this feature's job and MUST NOT block the loop.
"Done" gates on the absence of *new* failures, not on a fully-green suite.

| Suite | Command | Build | Passed | Failed (baseline) |
| --- | --- | --- | --- | --- |
| go | `go test ./...` | ok | 3434 | 1 — `adapter/config.TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` |
| dashboard-vitest | `cd dashboard && npx vitest run` | ok | 370 | 0 |
| dashboard-tsc | `cd dashboard && npx tsc --noEmit` | ok | 0 | 0 |

## Arrival state (verified at seed time)

- `go test ./...`: 1 failure — exactly the baseline `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`. **No new Go failures.**
- `cd dashboard && npx vitest run`: 428 passed / 0 failed (up from 370 — ~58 new tests, all green).
- `cd dashboard && npx tsc --noEmit`: clean.
- Provenance (grep-verified, not inferred): the enricher is constructed at the real
  composition root `cmd/arbitron/main.go:782` with the real `instanceRepo`,
  `nodeExecRepo`, `userRepo`; the queue-position provider is wired to the real
  `queueMgr` at `main.go:792`; `Engine.ListEnriched` calls it (`domain/engine/engine.go:528`)
  and is consumed by both HTTP (`adapter/http/instance_handler.go:384`) and MCP
  (`adapter/mcp/tools_instance.go:115`); the embedded `dist` bundle contains the live
  feature markers (`balanced`, `density`, `expanded`, `queuePosition`, `runName`).

This is a **review-and-harden** loop, not a build-from-scratch loop. The remaining
work is depth: closing the gaps in `gaps-identified.md`, the provenance/coverage
checks in `focus-areas.md`, and design-spirit polish.

## Constraints

- **Read before you write.** Read the relevant code before modifying or judging it.
- **One focus area per iteration.** Fix the single most important issue, well.
- **Strict TDD.** Write/extend the failing test FIRST; never weaken an assertion to
  make a test pass. Cover all five categories (happy / success / failure / error /
  edge) or document why one does not apply. This repo uses Go table tests + testify
  and Vitest + @testing-library/react.
- **Wired-and-fed, not merely present.** A component is done only when it is
  constructed at the real composition root AND fed real data by a producer that
  exists in the code — never `nil`/empty/hardcoded, never set only in `*_test.go`.
- **Don't record an unfed/no-op production path as "acceptable"** in
  `gaps-identified.md`. An unfed production path is a GAP to fix or an explicit
  escalation — never a tolerated no-op.
- **Spec non-goals stand:** no new queue actions (pause/cancel/rerun/reprioritize/
  bulk); no change to execution/scheduling/priority/concurrency; no last-error
  content in default columns; the richer list applies to ALL workflows, not just
  feature-pipeline.
- **Do NOT run a blanket `sqlc generate`** — it corrupts the hand-curated
  `adapter/sqlite/generated/` tree. Mirror the existing hand-written repos.
- After any `dashboard/src` change, run `make ui` so the embedded bundle reflects it.

## Iteration structure

1. Read `focus-areas.md`; pick the lowest-numbered incomplete item.
2. Read that area's code deeply — contracts, error/degradation paths, the
   composition-root wiring, and the producer that actually feeds it.
3. Cross-reference against the spec/designs/plans; log any new gap in
   `gaps-identified.md` under `## Open Issues`.
4. Fix the single most important issue (test first); move it to `## Fixed`.
5. Run all three test commands. A new failure (vs. baseline) preempts everything.
6. Assess the checklist honestly, then wrap up.

## Definition of done — checklist (be brutally honest)

- [ ] **No new test failures vs the recorded baseline** (pre-existing failures
      excluded), and nothing that built at baseline is now broken. Concretely: the
      Go failure set is a subset of `{TestSampleFeaturePipeline_ValidatesWithDefaultProfiles}`;
      vitest and tsc stay green.
- [ ] **Every new component is wired-and-fed**: constructed at the real composition
      root and fed a real producer — verified by grep that the symbol is
      constructed/populated outside `*_test.go`, NOT by a green isolated test alone.
      (Enricher, `DeriveRunName`, `NodeExecutionRepository.AggregateByInstances`,
      `queue.Manager.QueuePositions`, `useDensityPreference`, `queueColumns`,
      `QueueTableRow`, `RowDetailPanel`, `DensityToggle`.)
- [ ] **Every plan task is genuinely implemented** — wired-and-fed, not merely
      present — across Phase 1–3 tasks.
- [ ] The feature's behaviors have **meaningful coverage across all five scenario
      categories** (happy / success / failure / error / edge), and **at least one
      test per component would fail if its production input were nil/empty** (a green
      over-mocked test does NOT count). The queue-position e2e already models this
      with a load-bearing negative control
      (`adapter/http/instance_list_queue_position_e2e_test.go`); replicate that
      discipline for run name, submitter, model/retry, friendly step/backend, and the
      density preference.
- [ ] The Phase 1–3 **designs are satisfied in spirit**, including degrade-per-field
      best-effort enrichment and the cap-aware simulated queue rank.
- [ ] No `## Open Issues` remain in `gaps-identified.md`, and no entry there records
      an unfed/no-op production path as merely "acceptable."
- [ ] The embedded dashboard bundle (`adapter/dashboard/dist`) reflects the current
      `dashboard/src` (rerun `make ui` after any UI change).

Do NOT add a checklist box requiring the entire suite to be green — the baseline is
already red by one pre-existing failure.

## Wrap up each iteration

- **Step A — Pass?** All boxes honestly checked for the area you worked. No partial credit.
- **Step B — Update tracking:** mark the area complete in `focus-areas.md`; move
  resolved gaps to `## Fixed` in `gaps-identified.md`. Update trackers only at wrap-up.
- **Step C — Commit:** `git add -A && git commit -m "refine(queue-list): <what changed>"`.
- **Step D — Promise tag.** If ALL focus areas are complete AND there are no
  `## Open Issues` AND the done checklist holds, output `<promise>FINIT</promise>`;
  otherwise output `<promise>CLOSER</promise>`. Output exactly one tag, then stop.
