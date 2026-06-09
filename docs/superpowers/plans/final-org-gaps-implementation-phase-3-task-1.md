# Final Org Gaps — Phase 3, Task 1: Run Origin via Context + Engine Stamping

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a self-owned run-origin type + context helpers in `domain/instance`, add the two origin fields to `Instance`/`InstanceSummary`, have the engine read origin from context when building an instance, and stamp origin at the two schedule trigger sites (cron `fireOne` + the Phase-1 `Runner.Fire` seam) — all without changing the `Trigger` signature and without the engine importing `domain/schedule`.

**Architecture:** Origin flows engine-inbound through `context.Context` exactly as identity does. `domain/instance` owns `TriggerSource`, `Origin`, and the `WithOrigin`/`OriginFromContext` helpers (the package that owns the field owns the carrier, keeping the domain edge one-directional). The engine reads `instance.OriginFromContext(ctx)` and defaults to `manual` when absent. The two schedule trigger sites set the origin on the context before calling `TriggerWithDepartment`.

**Tech Stack:** Go, `github.com/google/uuid`, standard `context`, `testing`.

---

## CROSS-PHASE DEPENDENCY (read before starting)

This task touches `domain/schedule/runner.go` (`Runner.Fire`), which is delivered by **Phase 1 (Schedule "run now")**. Task ordering:

- **The `fireOne` change (cron origin) is independent** — `domain/schedule/evaluator.go` already exists. Do that part regardless.
- **The `Runner.Fire` change (run_now origin) REQUIRES Phase 1's `schedule.Runner` to exist.** If `domain/schedule/runner.go` does not yet exist when this task runs, complete every task EXCEPT Task 5 (the `Runner.Fire` steps, Steps 23–27), leave a `// TODO(phase-1-seam)` note in this plan's tracking, and the run_now origin is wired by whoever lands last. Phase 1 and Phase 3 must not both claim to have wired it — the run_now stamping is the explicit Phase-1 seam this task fills.
- This task is **independent of Phase 2** (SSO group mapping).

Before starting, run: `ls domain/schedule/runner.go` — if present, do all tasks; if absent, do Tasks 1–4 and Task 6 only and mark Task 5 (Steps 23–27) blocked-on-phase-1.

---

## Context (existing code you must match)

- `domain/instance/instance.go` — defines `Instance` (line ~87) and `InstanceSummary` (line ~114). `Visibility` is a self-owned type redeclared locally rather than imported, the SAME one-directional pattern `Origin` must follow.
- `domain/engine/engine.go` — `TriggerWithDepartment` builds the `instance.Instance` literal at line ~341. `identity.FromContext(ctx)` is read at line ~271. Add origin reading right before the literal.
- `domain/schedule/evaluator.go` — `fireOne` (line ~175) builds `fireCtx := identity.WithIdentity(ctx, id)` (line ~188) then calls `e.trigger.TriggerWithDepartment(fireCtx, ...)`.
- `domain/schedule/runner.go` — Phase-1 `Runner.Fire`. Mirror `fireOne`'s context-building; set origin to `run_now`.

The engine MUST NOT import `domain/schedule`. Origin is carried as `context` values defined in `domain/instance`, which both the engine and schedule already import.

---

### Task 1: Origin type + context helpers in `domain/instance`

**Files:**
- Create: `domain/instance/origin.go`
- Create (test): `domain/instance/origin_test.go`

- [ ] **Step 1: Write the failing tests for the origin type + context round-trip**

Create `domain/instance/origin_test.go`:

```go
package instance

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestOriginFromContext_AbsentReturnsFalse(t *testing.T) {
	_, ok := OriginFromContext(context.Background())
	if ok {
		t.Fatalf("expected ok=false when no origin set")
	}
}

func TestWithOrigin_RoundTripsScheduleOrigin(t *testing.T) {
	id := uuid.New()
	ctx := WithOrigin(context.Background(), Origin{Source: OriginSchedule, ScheduleID: &id})
	got, ok := OriginFromContext(ctx)
	if !ok {
		t.Fatalf("expected ok=true after WithOrigin")
	}
	if got.Source != OriginSchedule {
		t.Fatalf("source = %q, want %q", got.Source, OriginSchedule)
	}
	if got.ScheduleID == nil || *got.ScheduleID != id {
		t.Fatalf("scheduleID = %v, want %v", got.ScheduleID, id)
	}
}

func TestWithOrigin_RunNowAndManualSources(t *testing.T) {
	ctx := WithOrigin(context.Background(), Origin{Source: OriginRunNow})
	got, ok := OriginFromContext(ctx)
	if !ok || got.Source != OriginRunNow {
		t.Fatalf("run_now: ok=%v source=%q", ok, got.Source)
	}
	if got.ScheduleID != nil {
		t.Fatalf("run_now with no schedule id should be nil, got %v", got.ScheduleID)
	}
}

func TestTriggerSourceConstants(t *testing.T) {
	if OriginManual != "manual" || OriginSchedule != "schedule" || OriginRunNow != "run_now" {
		t.Fatalf("unexpected source string values: %q %q %q", OriginManual, OriginSchedule, OriginRunNow)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./domain/instance/ -run TestOrigin -v`
Expected: FAIL — `undefined: OriginFromContext`, `undefined: WithOrigin`, `undefined: Origin`, etc.

- [ ] **Step 3: Implement `domain/instance/origin.go`**

Create `domain/instance/origin.go`:

```go
package instance

import (
	"context"

	"github.com/google/uuid"
)

// TriggerSource records how a run was started. It is a self-owned type in
// domain/instance (the package that owns the field) so the engine can read run
// origin from context WITHOUT importing domain/schedule — the same
// one-directional domain edge instance.Visibility / instance.Claims preserve.
type TriggerSource string

const (
	// OriginManual is the default: a REST/MCP trigger that carries no origin.
	OriginManual TriggerSource = "manual"
	// OriginSchedule is a cron-tick fire from the schedule evaluator.
	OriginSchedule TriggerSource = "schedule"
	// OriginRunNow is an on-demand "run now" fire of a schedule (Phase 1).
	OriginRunNow TriggerSource = "run_now"
)

// Origin is the run's provenance, threaded into the trigger context by the two
// schedule trigger sites (cron + run-now). ScheduleID is nil for a manual run.
type Origin struct {
	Source     TriggerSource
	ScheduleID *uuid.UUID
}

// originKey is the unexported context key; using a private type prevents
// collisions with any other package's context values.
type originKey struct{}

// WithOrigin returns a copy of ctx carrying o so the engine can stamp the run's
// provenance without a Trigger signature change.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFromContext returns the Origin set by WithOrigin, or ok=false when none
// is present (a plain manual trigger). Callers default to OriginManual on !ok.
func OriginFromContext(ctx context.Context) (Origin, bool) {
	o, ok := ctx.Value(originKey{}).(Origin)
	return o, ok
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./domain/instance/ -run TestOrigin -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add domain/instance/origin.go domain/instance/origin_test.go
git commit -m "feat(instance): add Origin type and context helpers for run provenance"
```

---

### Task 2: Add origin fields to `Instance` and `InstanceSummary`

**Files:**
- Modify: `domain/instance/instance.go` (`Instance` struct ~line 87, `InstanceSummary` struct ~line 114)
- Modify (test): `domain/instance/origin_test.go` (add struct-field tests)

- [ ] **Step 6: Write the failing test for the new struct fields**

Append to `domain/instance/origin_test.go`:

```go
func TestInstanceCarriesOriginFields(t *testing.T) {
	id := uuid.New()
	inst := Instance{TriggerSource: OriginSchedule, ScheduleID: &id}
	if inst.TriggerSource != OriginSchedule {
		t.Fatalf("Instance.TriggerSource not stored")
	}
	if inst.ScheduleID == nil || *inst.ScheduleID != id {
		t.Fatalf("Instance.ScheduleID not stored")
	}
}

func TestInstanceSummaryCarriesOriginFields(t *testing.T) {
	id := uuid.New()
	s := InstanceSummary{TriggerSource: OriginRunNow, ScheduleID: &id}
	if s.TriggerSource != OriginRunNow {
		t.Fatalf("InstanceSummary.TriggerSource not stored")
	}
	if s.ScheduleID == nil || *s.ScheduleID != id {
		t.Fatalf("InstanceSummary.ScheduleID not stored")
	}
}
```

- [ ] **Step 7: Run to verify it fails**

Run: `go test ./domain/instance/ -run TestInstance -v`
Expected: FAIL — `unknown field TriggerSource in struct literal` / `ScheduleID`.

- [ ] **Step 8: Add the fields to `Instance`**

In `domain/instance/instance.go`, in the `Instance` struct, add after the `Visibility Visibility` field (line ~97):

```go
	// TriggerSource records how the run was started (manual / schedule /
	// run_now). It defaults to OriginManual; the engine stamps it from
	// OriginFromContext at trigger time (Phase 3). ScheduleID is the originating
	// schedule for schedule/run_now runs, nil otherwise. There is intentionally
	// NO foreign key on schedule_id: a schedule may be deleted while its
	// historical runs remain.
	TriggerSource     TriggerSource
	ScheduleID        *uuid.UUID
```

- [ ] **Step 9: Add the fields to `InstanceSummary`**

In `domain/instance/instance.go`, in the `InstanceSummary` struct, add after the `Visibility` field (line ~120):

```go
	TriggerSource TriggerSource `json:"triggerSource"`
	ScheduleID    *uuid.UUID    `json:"scheduleId,omitempty"`
```

- [ ] **Step 10: Run to verify it passes + the package still builds**

Run: `go test ./domain/instance/ -v`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add domain/instance/instance.go domain/instance/origin_test.go
git commit -m "feat(instance): carry TriggerSource and ScheduleID on Instance and summary"
```

---

### Task 3: Engine reads origin from context when building the instance

**Files:**
- Modify: `domain/engine/engine.go` (`TriggerWithDepartment`, the `inst := &instance.Instance{...}` literal at ~line 341)
- Test: `domain/engine/engine_origin_test.go` (create) — OR add to an existing engine test file if one exercises `TriggerWithDepartment` with a fake instance repo. Prefer a new focused file.

- [ ] **Step 12: Write the failing engine test**

Inspect an existing engine test (e.g. `domain/engine/*_test.go`) to reuse its fakes/helpers for constructing an `Engine` with an in-memory workflow + instance repo and triggering a workflow. Create `domain/engine/engine_origin_test.go` modeled on that setup. The test must assert three cases by capturing the `*instance.Instance` passed to the fake instance repo's `Create`:

```go
package engine

// NOTE: reuse the existing engine test harness in this package (the same fakes
// other *_test.go files build an Engine with). The three assertions below are
// the load-bearing part; adapt construction to the local harness.

// 1. Manual trigger (no origin on ctx) => inst.TriggerSource == instance.OriginManual, inst.ScheduleID == nil.
// 2. ctx = instance.WithOrigin(ctx, instance.Origin{Source: instance.OriginSchedule, ScheduleID: &sid})
//    => inst.TriggerSource == instance.OriginSchedule, *inst.ScheduleID == sid.
// 3. ctx = instance.WithOrigin(ctx, instance.Origin{Source: instance.OriginRunNow, ScheduleID: &sid})
//    => inst.TriggerSource == instance.OriginRunNow, *inst.ScheduleID == sid.
```

Write each case as a real `func Test...` using the local harness — capturing the created instance via the fake repo. Do NOT leave the comment block as the test body; it documents the three required assertions.

- [ ] **Step 13: Run to verify it fails**

Run: `go test ./domain/engine/ -run Origin -v`
Expected: FAIL — `TriggerSource` is empty (engine doesn't stamp it yet).

- [ ] **Step 14: Stamp origin in `TriggerWithDepartment`**

In `domain/engine/engine.go`, immediately BEFORE the `inst := &instance.Instance{` literal (currently at ~line 341, just after `origVars` is built), add:

```go
	// Stamp run provenance from the trigger context. Manual REST/MCP triggers
	// carry no origin and default to OriginManual; the two schedule trigger sites
	// (cron fireOne, run-now Runner.Fire) set it via instance.WithOrigin. The
	// engine reads it WITHOUT importing domain/schedule, the same one-directional
	// edge identity uses.
	triggerSource := instance.OriginManual
	var scheduleID *uuid.UUID
	if o, ok := instance.OriginFromContext(ctx); ok {
		triggerSource = o.Source
		scheduleID = o.ScheduleID
	}
```

Then, inside the `inst := &instance.Instance{...}` literal, add these two fields (e.g. after `Visibility: st.Visibility,`):

```go
		TriggerSource:     triggerSource,
		ScheduleID:        scheduleID,
```

- [ ] **Step 15: Run to verify it passes**

Run: `go test ./domain/engine/ -run Origin -v`
Expected: PASS (all three cases).

- [ ] **Step 16: Confirm the engine still does NOT import domain/schedule**

Run: `grep -n "domain/schedule" domain/engine/engine.go`
Expected: NO output (empty). If anything prints, you wired origin wrong — origin must flow via `domain/instance` context helpers only.

- [ ] **Step 17: Commit**

```bash
git add domain/engine/engine.go domain/engine/engine_origin_test.go
git commit -m "feat(engine): stamp run origin from context onto created instance"
```

---

### Task 4: Cron site (`fireOne`) sets schedule origin

**Files:**
- Modify: `domain/schedule/evaluator.go` (`fireOne`, line ~188 where `fireCtx` is built)
- Test: `domain/schedule/evaluator_test.go` (add a test capturing the ctx passed to the fake `Trigger`)

- [ ] **Step 18: Write the failing test asserting fireOne sets schedule origin on the trigger ctx**

Inspect `domain/schedule/evaluator_test.go` for its fake `Trigger` (the type implementing `TriggerWithDepartment`). Add a test that makes the fake capture the `ctx` it receives, drives one due schedule through `Tick` (or `fireOne` directly if reachable), and asserts:

```go
// After a due fire, the captured ctx carries an Origin:
//   o, ok := instance.OriginFromContext(capturedCtx)
//   ok == true
//   o.Source == instance.OriginSchedule
//   o.ScheduleID != nil && *o.ScheduleID == s.ID
// Import: "github.com/arbitron/arbitron/domain/instance"
```

Implement it as a real test using the existing evaluator test harness (fake repo returning one enabled, due schedule; fake identity resolver; fake active checker returning false; fake Trigger capturing ctx).

- [ ] **Step 19: Run to verify it fails**

Run: `go test ./domain/schedule/ -run FireOneOrigin -v` (name the test accordingly)
Expected: FAIL — origin absent from captured ctx.

- [ ] **Step 20: Set the origin in `fireOne`**

In `domain/schedule/evaluator.go`, add the import:

```go
	"github.com/arbitron/arbitron/domain/instance"
```

In `fireOne`, change the `fireCtx` construction (line ~188) from:

```go
	fireCtx := identity.WithIdentity(ctx, id)
```

to:

```go
	scheduleID := s.ID
	fireCtx := instance.WithOrigin(identity.WithIdentity(ctx, id), instance.Origin{
		Source:     instance.OriginSchedule,
		ScheduleID: &scheduleID,
	})
```

(Take the address of a local copy of `s.ID`, never of the loop variable, so each fire records its own schedule id.)

- [ ] **Step 21: Run to verify it passes + the whole schedule package is green**

Run: `go test ./domain/schedule/ -v`
Expected: PASS.

- [ ] **Step 22: Commit**

```bash
git add domain/schedule/evaluator.go domain/schedule/evaluator_test.go
git commit -m "feat(schedule): cron fireOne stamps schedule run origin"
```

---

### Task 5: Run-now site (`Runner.Fire`) sets run_now origin — PHASE-1 SEAM

> **BLOCKED unless `domain/schedule/runner.go` exists (Phase 1 delivered it).** Run `ls domain/schedule/runner.go` first. If absent, SKIP this task and record it as blocked-on-phase-1 in the execution tracker; do not stub a Runner here (Phase 1 owns the Runner's construction).

**Files:**
- Modify: `domain/schedule/runner.go` (`Runner.Fire`, the context-building line that mirrors `fireOne`)
- Test: `domain/schedule/runner_test.go` (add a captured-ctx origin assertion alongside Phase 1's existing Runner tests)

- [ ] **Step 23: Write the failing test asserting Runner.Fire sets run_now origin**

In `domain/schedule/runner_test.go`, using Phase 1's Runner test harness (a fake `Trigger` capturing its ctx), add a test that fires a schedule via `Runner.Fire` and asserts:

```go
// o, ok := instance.OriginFromContext(capturedCtx)
//   ok == true
//   o.Source == instance.OriginRunNow
//   o.ScheduleID != nil && *o.ScheduleID == s.ID
```

- [ ] **Step 24: Run to verify it fails**

Run: `go test ./domain/schedule/ -run RunnerFireOrigin -v`
Expected: FAIL — origin absent or `OriginManual`.

- [ ] **Step 25: Set the origin in `Runner.Fire`**

In `domain/schedule/runner.go`, find where `Fire` builds the trigger context (it wraps `identity.WithIdentity(ctx, id)` exactly like `fireOne`). Wrap it with `instance.WithOrigin` using `OriginRunNow`:

```go
	scheduleID := s.ID
	fireCtx := instance.WithOrigin(identity.WithIdentity(ctx, id), instance.Origin{
		Source:     instance.OriginRunNow,
		ScheduleID: &scheduleID,
	})
```

Add the `"github.com/arbitron/arbitron/domain/instance"` import if not already present.

- [ ] **Step 26: Run to verify it passes**

Run: `go test ./domain/schedule/ -v`
Expected: PASS.

- [ ] **Step 27: Commit**

```bash
git add domain/schedule/runner.go domain/schedule/runner_test.go
git commit -m "feat(schedule): run-now Runner.Fire stamps run_now origin (phase-1 seam)"
```

---

### Task 6: Full backend build + vet gate

- [ ] **Step 28: Build and vet the whole module**

Run: `go build ./... && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 29: Run the three touched packages together**

Run: `go test ./domain/instance/... ./domain/engine/... ./domain/schedule/...`
Expected: all PASS.

> NOTE: at this point origin lives only in memory; it is NOT yet persisted. Task 2 (`final-org-gaps-implementation-phase-3-task-2.md`) adds the migration + repo read/write so origin round-trips through SQLite and reaches `InstanceSummary`. Until Task 2 lands, the repo silently drops the two new fields — that is expected and is why Task 1's engine test captures the in-memory instance rather than asserting a DB round-trip.

---

## Self-review checklist (run after implementing)

- [ ] Engine does NOT import `domain/schedule` (`grep` returns empty).
- [ ] `Origin`, `TriggerSource`, `WithOrigin`, `OriginFromContext` all live in `domain/instance/origin.go`.
- [ ] Manual triggers default to `OriginManual`, `ScheduleID == nil`.
- [ ] `fireOne` and `Runner.Fire` take the address of a LOCAL copy of `s.ID`, not the loop/range variable.
- [ ] `InstanceSummary` JSON tags are exactly `triggerSource` and `scheduleId,omitempty`.
