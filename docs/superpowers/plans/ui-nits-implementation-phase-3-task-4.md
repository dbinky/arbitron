# UI Nits Phase 3 — Task 4: Engine retry hook (resurface on new outcome) + wiring

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. STRICT TDD: failing test → run (expect FAIL) → minimal impl → run (expect PASS) → commit. **Depends on Task 1 (the `DismissalRepository` port + adapter).** Independent of Tasks 2/3 except for shared wiring in `main.go`.

**Goal:** Retrying a failed instance clears its dismissal for everyone (so it resurfaces in Active, then in its fresh terminal category), and re-running yields a brand-new un-dismissed instance. Wire a `DismissalRepository` into the engine and `cmd/arbitron/main.go`.

---

### Task 4: `Retry` calls `ClearForInstance`; engine wiring; engine tests

**Context for the implementer (read before starting):**

- The engine is `domain/engine/engine.go`. `Engine` struct (lines ~21-39) holds repos as fields. The constructor `NewEngine(...)` (lines ~42-65) takes 9 positional args. **Many call sites pass exactly 9 args** (e.g. `domain/engine/engine_test.go:628`, `:1751`; `executor_timer_test.go` ×4; `executor_webhook_test.go`; `loop_mechanics_test.go`; `cmd/arbitron/main.go:353`). Adding a 10th positional arg would force edits to all of them.
- The engine already uses **optional setter wiring** for late/optional deps: `SetScheduler`, `SetWebhookRegistry`, `SetNotifications`, `SetExternalNotifier`, `SetWorkspaceManager`, `SetQueueManager`. **Follow this pattern**: add a `dismissals instance.DismissalRepository` field + `SetDismissals(repo)` setter, and guard the call with a nil check (like `e.notifications == nil` at line ~662). This keeps every existing `NewEngine` call compiling unchanged.
- `Retry` is at lines ~991-1035. It loads the instance, rejects non-failed (`instance.ErrNotFailed`), applies variable edits, sets current node to the failed node, transitions to `StateRunning`, registers a cancel context, publishes `instance.retrying`, and launches `go e.run(...)`. The dismissal clear should happen **after** the state is set to running (so the item is genuinely active again) and before/around the `instance.retrying` publish. Because it must clear for ALL users, call `ClearForInstance(ctx, instanceID)`.
- `Rerun` is at lines ~1040-1060. It validates the source is terminal, then calls `e.Trigger(...)` which `CreateInstance`s a **new** UUID. A new instance has no `instance_dismissals` rows, so it is inherently un-dismissed — **no code change needed, only a clarifying comment**.
- Engine test fixtures: `domain/engine/engine_test.go` has `newTestFixtures()` (returns an `f` with `engine`, `instanceRepo`, `eventBus`, `userRepo`, etc.), `mockUserRepo` (line ~400), `mockQueueRepo`/`mockQueueManager`, `singleNodeWorkflow()`, `waitForInstance(...)`. A failing-then-passing executor pattern is in `TestEngine_RetryPreservesUneditedNodeOutputs` (line ~1798) and `TestEngine_RetryFailed` (line ~1274). Use a `sync.Once`-gated executor that fails the first run and succeeds the retry.
- You will need an in-memory dismissal mock for engine tests. The engine package tests are in `package engine_test`; define a small `mockDismissalRepo` there (a `map[uuid.UUID]map[uuid.UUID]bool` keyed by instance then user, or simpler: a `map[uuid.UUID]int` counting `ClearForInstance` calls plus a per-instance dismissed set). Keep it minimal — the engine only calls `ClearForInstance` from `Retry`.

**Files:**
- `domain/engine/engine.go` (modify: add `dismissals` field, `SetDismissals` setter, `ClearForInstance` call in `Retry`, comment in `Rerun`)
- `domain/engine/engine_dismissal_test.go` (new — retry-clears + rerun-fresh tests)
- `cmd/arbitron/main.go` (modify: construct `NewDismissalRepo(db)`, `eng.SetDismissals(...)`, and pass it into `RouterDeps` + `Users`)

---

#### Step 4.1 — Failing test: retry of a dismissed failed instance clears its dismissal

- [ ] Create `domain/engine/engine_dismissal_test.go` (package `engine_test`). First the mock, then the test. The mock tracks dismissals and records `ClearForInstance`:

```go
package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dismissalRecorder is a minimal instance.DismissalRepository for engine tests.
type dismissalRecorder struct {
	mu        sync.Mutex
	dismissed map[uuid.UUID]map[uuid.UUID]bool // instanceID -> userID -> true
	cleared   []uuid.UUID
}

func newDismissalRecorder() *dismissalRecorder {
	return &dismissalRecorder{dismissed: map[uuid.UUID]map[uuid.UUID]bool{}}
}
func (d *dismissalRecorder) Dismiss(_ context.Context, u, i uuid.UUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dismissed[i] == nil {
		d.dismissed[i] = map[uuid.UUID]bool{}
	}
	d.dismissed[i][u] = true
	return nil
}
func (d *dismissalRecorder) Restore(_ context.Context, u, i uuid.UUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dismissed[i] != nil {
		delete(d.dismissed[i], u)
	}
	return nil
}
func (d *dismissalRecorder) DismissedIDs(_ context.Context, u uuid.UUID) (map[uuid.UUID]bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[uuid.UUID]bool{}
	for i, users := range d.dismissed {
		if users[u] {
			out[i] = true
		}
	}
	return out, nil
}
func (d *dismissalRecorder) ClearForInstance(_ context.Context, i uuid.UUID) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleared = append(d.cleared, i)
	delete(d.dismissed, i)
	return nil
}
func (d *dismissalRecorder) clearedCount(i uuid.UUID) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.cleared {
		if c == i {
			n++
		}
	}
	return n
}

var _ instance.DismissalRepository = (*dismissalRecorder)(nil)
```

```go
// Integration: retrying a dismissed FAILED instance clears its dismissal so it
// resurfaces. Uses a workflow whose node fails once then succeeds on retry.
func TestEngine_RetryClearsDismissal(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow() // adjust name to the fixture helper actually present
	f.workflowRepo.add(wf)

	dis := newDismissalRecorder()
	f.engine.SetDismissals(dis)

	// Make the single node fail the first time, succeed on retry.
	var once sync.Once
	f.executor.onNode( /* the node id in singleNodeWorkflow */ "A",
		func(_ context.Context, _ workflowNodeType, _ varsType) (*nodeResultType, error) {
			var err error
			once.Do(func() { err = errFirstRun })
			if err != nil {
				return nil, err
			}
			return &nodeResultType{}, nil
		})

	id, _, err := f.engine.Trigger(wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// User dismisses the failed instance.
	user := uuid.New()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))

	// Retry it.
	require.NoError(t, f.engine.Retry(id, nil))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.GreaterOrEqual(t, dis.clearedCount(id), 1, "Retry must ClearForInstance the dismissal")
	ids, _ := dis.DismissedIDs(context.Background(), user)
	assert.False(t, ids[id], "after retry the instance is no longer dismissed for the user")
}
```

> **Implementer note:** the exact types (`workflowNodeType`, `varsType`, `nodeResultType`, `errFirstRun`, the executor-registration signature, and the fixture node id) must be copied from the real signatures used by `TestEngine_RetryPreservesUneditedNodeOutputs` / `TestEngine_RetryFailed` in `engine_test.go` (which use `func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error)` and `f.executor.onNode(...)`). Replace the placeholders above with those concrete types and imports before running. The placeholders are intentionally flagged so they are not left as-is.

- [ ] Run: `go test ./domain/engine/ -run TestEngine_RetryClearsDismissal`
- [ ] Expected: **FAIL (build)** — `f.engine.SetDismissals` undefined.

#### Step 4.2 — Minimal impl: `dismissals` field + setter + clear-on-retry

- [ ] In `engine.go`, add the field to `Engine`:

```go
	dismissals       instance.DismissalRepository
```

- [ ] Add the setter near `SetNotifications`:

```go
// SetDismissals wires the per-user dismissal repository. When set, Retry clears
// an instance's dismissals (for all users) so a retried item resurfaces. When
// unset, retry behaves exactly as before.
func (e *Engine) SetDismissals(repo instance.DismissalRepository) {
	e.dismissals = repo
}
```

- [ ] In `Retry`, after `UpdateState(ctx, instanceID, instance.StateRunning)` succeeds and before publishing `instance.retrying`, clear dismissals:

```go
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	// Resurface on a new outcome: clear this instance's dismissals for every
	// user so the now-running item reappears in Active and, when it reaches a
	// fresh terminal state, in Completed/Failed. A new result is new
	// information the user should see. Best-effort: a clear failure must not
	// block the retry, but is surfaced as an error.
	if e.dismissals != nil {
		if err := e.dismissals.ClearForInstance(ctx, instanceID); err != nil {
			return fmt.Errorf("failed to clear dismissals on retry: %w", err)
		}
	}
```

> Decide error policy: returning the error aborts the retry before launching the goroutine, which is the safest (the instance is already `running` in the DB, but the run has not started — matching how earlier `Retry` steps return on error). Keep it as a hard error for determinism; the test asserts a successful retry path so the clear must succeed.

- [ ] In `Rerun`, add a clarifying comment above the `Trigger` call:

```go
	// Rerun creates a brand-new instance id via Trigger; new instances have no
	// dismissal rows, so the rerun is inherently un-dismissed for every user.
	// The original instance's historical dismissal (if any) is intentionally
	// left intact. No dismissal bookkeeping is needed here.
	id, _, err := e.Trigger(inst.WorkflowName, inst.OriginalVariables, string(inst.Priority))
	return id, err
```

- [ ] Run: `go test ./domain/engine/ -run TestEngine_RetryClearsDismissal`
- [ ] Expected: **PASS** (after replacing the placeholder types with the real ones).
- [ ] Run: `go test ./domain/engine/...` → **PASS** (all existing engine tests still green; `NewEngine` arity unchanged, so no call site edits needed).
- [ ] Commit: `feat(dismissal): engine Retry clears dismissals on resurface [ui-nits p3 t4]`

#### Step 4.3 — Failing test: rerun yields a new un-dismissed instance

- [ ] Add:

```go
// Rerun produces a NEW instance id with no dismissal rows; the old instance's
// dismissal (if any) is untouched.
func TestEngine_RerunYieldsUndismissedNewInstance(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	dis := newDismissalRecorder()
	f.engine.SetDismissals(dis)

	id, _, err := f.engine.Trigger(wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	// Dismiss the original completed instance for a user.
	user := uuid.New()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))

	newID, err := f.engine.Rerun(id)
	require.NoError(t, err)
	assert.NotEqual(t, id, newID, "rerun must create a new instance id")

	ids, _ := dis.DismissedIDs(context.Background(), user)
	assert.True(t, ids[id], "original instance stays dismissed (historical)")
	assert.False(t, ids[newID], "new instance has no dismissal rows")
	assert.Equal(t, 0, dis.clearedCount(newID), "rerun does not clear dismissals")
}
```

- [ ] Run: `go test ./domain/engine/ -run TestEngine_RerunYieldsUndismissedNewInstance` → **PASS** (no impl change beyond the comment; the test pins the contract).
- [ ] Commit: `test(dismissal): rerun yields fresh un-dismissed instance [ui-nits p3 t4]`

#### Step 4.4 — Wire the dismissal repo in `cmd/arbitron/main.go`

- [ ] After the notification repo wiring (`main.go` ~line 401-403, `notifRepo := adaptsqlite.NewNotificationRepo(db); eng.SetNotifications(notifRepo)`), add:

```go
	// 12a-2. Create the per-user dismissal repo and wire it to the engine so
	// Retry resurfaces dismissed items on their new outcome.
	dismissalRepo := adaptsqlite.NewDismissalRepo(db)
	eng.SetDismissals(dismissalRepo)
```

- [ ] If `main.go` stores deps on a struct (it references `ds.instanceRepo`, `ds.eng`, etc. when building `RouterDeps`), add `dismissalRepo` to that struct so it is reachable at the `NewRouterFromDeps` call (~line 534). Grep `main.go` for where `notifRepo`/`instanceRepo` are stored on the deps struct and mirror it (e.g. `ds.dismissalRepo = dismissalRepo`). Also ensure the user repo (`userRepo`, already constructed and passed to `NewEngine` at line ~358) is reachable there.
- [ ] In the `NewRouterFromDeps(adapthttp.RouterDeps{ ... })` literal (~line 534-553), add:

```go
		Dismissals:          ds.dismissalRepo, // or dismissalRepo if not on the struct
		Users:               ds.userRepo,      // acting-user seam source (GetDefaultUser today)
```

- [ ] Run: `go build ./...` → **PASS**.
- [ ] Run full gate: `make test` → **PASS**.
- [ ] Manual smoke (optional, per reference_arbitron_serve memory): `make build && ./arbitron serve`, then `curl -X POST localhost:PORT/api/v1/instances/<failed-id>/dismiss` then retry and confirm it reappears. Document result in the task report.
- [ ] Commit: `feat(dismissal): wire DismissalRepository + Users into router/engine [ui-nits p3 t4]`

**Definition of done for Task 4:** engine has `SetDismissals`; `Retry` calls `ClearForInstance` (resurface) when wired; `Rerun` documented as inherently un-dismissed (new id); engine tests prove retry clears + rerun stays fresh; `main.go` constructs `NewDismissalRepo`, wires it to the engine and the router (`Dismissals` + `Users`); `make test` green; no existing `NewEngine` call site edited.
