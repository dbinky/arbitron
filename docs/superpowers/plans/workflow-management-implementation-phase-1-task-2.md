# Workflow Management — Phase 1, Task 2: Service integration + backfill

**Goal:** Wire the registry into `WorkflowService` so create/publish register an id and
enforce global uniqueness, name↔id resolution is available internally, and legacy
id-less workflows are backfilled — with all existing name-addressed behavior preserved.

**Architecture:** `WorkflowService` gains a `WorkflowRegistry` dependency (wired at the
composition root next to the audit wiring). Create/publish allocate+register an id;
load ensures an id (lazy backfill); a startup backfill registers any unregistered
workflow. Uniqueness violations surface the existing `ErrAlreadyExists`.

**Tech Stack:** Go, hexagonal, SQLite, uuid, testify. Depends on Phase 1 Task 1.

---

## Files
- Modify: `domain/workflow/service.go` (registry dep; register on create/publish; resolve helpers; uniqueness check)
- Modify: `domain/workflow/workflow.go` or new `domain/workflow/backfill.go` (lazy id-assign on load)
- Create: `domain/workflow/backfill.go` (`BackfillRegistry(ctx)` startup scan)
- Modify: `cmd/arbitron/main.go` (construct identity repo + inject into `WorkflowService`)
- Test:   `domain/workflow/service_registry_test.go`, `domain/workflow/backfill_test.go`

## Steps

- [ ] **Step 1 — Failing test: create registers id + unique name.** Using the existing
  `WorkflowService` test harness (fake/real repo + a real or fake registry), assert:
  creating a workflow returns/persists a non-zero `ID`; a row exists in the registry;
  `service.resolveID(name)` returns it. Run `go test ./domain/workflow/ -run Registry`
  → FAIL.

- [ ] **Step 2 — Inject the registry.** Add the `WorkflowRegistry` field to the service
  struct + constructor param (follow the existing `SetAudit` wiring style — either a
  constructor arg or a `SetRegistry`). Update `cmd/arbitron/main.go` to build the
  sqlite identity repo and pass it in. Build only: `go build ./...`.

- [ ] **Step 3 — Register on create/publish.** In `Create` (and `Publish`'s record
  step), allocate `uuid.New()` if the workflow has no id, write it into the persisted
  `workflow.json`, and call `registry.Register`. On `ErrAlreadyExists`, abort BEFORE
  writing the folder (uniqueness check first). Make Step 1 pass.

- [ ] **Step 4 — Failing test: duplicate name rejected, no folder written.** Create
  "alpha", then create "alpha" again → `ErrAlreadyExists`; assert the second create did
  not create/modify a folder. Implement the pre-write check. PASS.

- [ ] **Step 5 — resolve helpers.** Add unexported `resolveID(ctx,name)` /
  `resolveName(ctx,id)` on the service delegating to the registry; used by later phases.
  Unit-test both directions.

- [ ] **Step 6 — Lazy backfill on load.** In the load path, if `workflow.json` has no
  `id`, allocate one, persist it, and `Register` it (ignore `ErrAlreadyExists` if a row
  already exists for that name — adopt it via `ResolveID`). Test: a fixture workflow
  written without an id gets a stable id on first load and the same id on reload.

- [ ] **Step 7 — Startup backfill.** `BackfillRegistry(ctx)` lists both scopes via the
  repo, and for each unregistered workflow assigns+persists an id and registers it;
  idempotent. Call it once at startup in `main.go` after wiring. Test idempotency
  (running twice yields no duplicates/errors).

- [ ] **Step 8 — Edge: case/whitespace.** Confirm uniqueness uses the same normalized
  name as `safeWorkflowName` (so "Alpha" vs "alpha" behavior matches existing rules).
  Add a test pinning the chosen behavior.

- [ ] **Step 9 — Full gate + commit.** `go test ./...` green. `git add -A && git commit
  -m "feat(workflow): register stable ids, enforce global name uniqueness, backfill legacy"`
