# Workflow Management — Phase 2, Task 2: Repoint trigger / cron / publish onto workflow_id

**Goal:** Make the live paths (instance trigger, schedule firing, publication) carry and
use the stable `workflow_id`, so a later rename/edit cannot break a running instance,
a schedule, or a publication.

**Architecture:** The trigger path resolves name→id and stamps `WorkflowID` on the
instance (keeping `WorkflowName` as a display label + `WorkflowSnapshot` for execution).
The cron evaluator stores+fires by `WorkflowID`, resolving id→current name at fire time.
Publish links the publication to `workflow_id`.

**Tech Stack:** Go, hexagonal, SQLite, uuid, testify. Depends on Phase 2 Task 1.

---

## Files
- Modify: `domain/instance/instance.go` (add `WorkflowID uuid.UUID`)
- Modify: `domain/engine/engine.go` (`TriggerWithDepartment`: resolve+stamp id; rerun by id)
- Modify: `domain/schedule/schedule.go` + schedule store (carry `WorkflowID`)
- Modify: schedule firing path (cron evaluator): resolve id→name→definition at fire time
- Modify: `domain/workflow/{service,publishing}.go` (publication carries workflow_id)
- Test:   `domain/engine/*_test.go`, `domain/schedule/*_test.go`, `domain/workflow/*_test.go`

## Steps

- [ ] **Step 1 — Failing test: trigger stamps WorkflowID.** In the engine trigger test
  (fake registry resolving "alpha"→idA), trigger "alpha" and assert the created instance
  has `WorkflowID == idA` (and still `WorkflowName == "alpha"` + a snapshot). FAIL first.

- [ ] **Step 2 — Add `WorkflowID` to the instance type** and the insert/select queries
  (the column exists from Task 1). Resolve name→id in `TriggerWithDepartment` and set it.
  PASS Step 1. Rerun path (`engine.go` rerun) resolves by `WorkflowID` (fall back to
  `WorkflowName` label if id is null on a legacy instance).

- [ ] **Step 3 — Failing test: schedule survives a rename.** Create a schedule for
  "alpha" (stores idA); simulate a rename by changing the registry name idA→"beta";
  fire the schedule and assert it loads the definition now named "beta" and triggers.
  FAIL first.

- [ ] **Step 4 — Schedule by id.** Add `WorkflowID` to `Schedule` + store; the create
  path resolves name→id. The cron evaluator loads `schedule.WorkflowID` → `ResolveName`
  → load definition → trigger. PASS Step 3. Keep `WorkflowName` as a display label.

- [ ] **Step 5 — Failing test + impl: publication carries id.** Publish "alpha"; assert
  the `published_workflows` row's `workflow_id == idA`. Update `Publish`/`WriteAndRecord`
  to set it. PASS.

- [ ] **Step 6 — Edge/failure tests:** firing a schedule whose `workflow_id` no longer
  resolves (workflow deleted) logs a warning and skips without crashing the cron loop;
  a legacy instance with NULL `workflow_id` still reruns via its `WorkflowName` label.

- [ ] **Step 7 — Gate + commit.** `go test ./...` green. `git add -A && git commit -m
  "feat(workflow): trigger/schedule/publish reference workflows by stable id"`
