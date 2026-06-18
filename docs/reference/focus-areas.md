# Focus Areas Review Tracking — workflow-management

Systematic review of the **workflow-management** implementation (already built and
green) for correctness, robustness, and alignment with the spec/designs/plans. Each
**single focus area** requires **2 full review passes**; each **paired focus area**
requires **1 full review pass**. A pass is checked only when the checklist passes for
that area. A red `make test` preempts everything until green.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Stable identity registry (`domain/workflow/registry.go`, `adapter/sqlite/workflow_identity_repo.go`, migration 016 — id allocation, name↔id, `UNIQUE(name)`) | [x] | [x] | done |
| 2 | Service identity integration + backfill (`service.go` register/uniqueness/resolve, `backfill.go`, `main.go` wiring) | [x] | [x] | done |
| 3 | Reference columns + backfill (migration 017, `ref_backfill*` — instances/schedules/publications `workflow_id`) | [x] | [x] | done |
| 4 | Trigger / cron / publish by id (`engine.go` resolver+stamp, schedule evaluator/service, publish link) | [x] | [x] | done |
| 5 | Admin gate + Rename (`requireAdmin`, `rename.go` — folder move + registry + rollback) | [x] | [ ] | |
| 6 | Guarded Delete + Unpublish (`delete.go`, `CountActiveByWorkflowID` guard, teardown order, history preserved) | [ ] | [ ] | |
| 7 | Duplicate / Edit-shared / Archive / Transfer (`admin_ops.go`, `adminSharedWrite`, `ArchiveChecker`) | [ ] | [ ] | |
| 8 | Audit emits + `ListByTarget` + MCP tool surface (`tools_workflow.go`, audit repo, action constants) | [ ] | [ ] | |
| 9 | Global name-uniqueness invariant (create/rename/duplicate/publish all reject collisions consistently) | [ ] | [ ] | |
| 10 | Non-transactional FS+SQLite cross-store integrity & rollback (rename/delete/transfer ordering, no half-state) | [ ] | [ ] | |
| 11 | Admin authorization fail-closed (every lifecycle op gated; MCP surfaces forbidden correctly; local-mode admin) | [ ] | [ ] | |
| 12 | Archived-state enforcement (trigger refuses archived; archived still listable/editable; idempotent toggle) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 13 | Rename + References (a renamed workflow's instances/schedules/publications still resolve via id) | [ ] | |
| 14 | Delete-guard + Instance state vocabulary (the non-terminal set matches `HasActiveRun`; no false guard) | [ ] | |
| 15 | Publish/Unpublish + shared folder & publication record (consistency; private copy survives unpublish) | [ ] | |
| 16 | MCP handlers + service sentinel mapping (all ops map ErrForbidden/AlreadyExists/HasRunningInstances/Archived/NotFound) | [ ] | |
| 17 | Backfill + live trigger (backfilled `workflow_id` equals what the live trigger path stamps) | [ ] | |

---

## Review Guidance

### Single Area Reviews
- **Pass 1 (Correctness & Coverage):** every public method has coverage across
  happy/success/failure/error/edge; behavior matches the design; error handling is
  thorough (no silent failures); port boundaries are clean.
- **Pass 2 (Robustness & Extensibility):** aligns to the spirit of the spec; assertions
  test real risks (not happy-path ceremony); edge cases handled (empty inputs,
  concurrency, the cross-store failure/rollback path, legacy null-id rows).

### Paired Area Reviews
Verify the two components integrate: data flows cleanly across the boundary; the stable
id is the contract (not the name); failure in one is handled by the other; integration
tests cover the seam, not just unit happy paths.
