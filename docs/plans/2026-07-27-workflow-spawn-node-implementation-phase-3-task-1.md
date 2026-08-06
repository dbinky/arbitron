# Phase 3 — Task 1: context-carried spawn link and creation-time stamping

**Goal:** Give the spawn link a context carrier and make `Engine.TriggerWithDepartment` stamp it onto the `Instance` literal it constructs, so a child row is created with its link already set.

**Architecture:** New file `domain/instance/spawn_context.go` provides `WithSpawnLink` / `SpawnLinkFromContext`, the exact mechanism `instance.Origin` already uses for creation-time provenance (`WithOrigin` / `OriginFromContext`, read at the trigger path and stamped beside `TriggerSource`/`ScheduleID`). A **sibling** context value is used rather than extending `Origin`: `Origin` answers "how did this run start" and is stamped onto `trigger_source`/`schedule_id`; overloading it would force every `Origin` consumer — including both schedule trigger sites — to reason about spawning. The stamp goes in the same `Instance` literal, so the link is written by the creating INSERT and never as a follow-up.

**Tech Stack:** Go 1.25.5, standard library `testing`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 1: spawn-link context carrier + trigger-path stamp

**Files:**
- Create: `domain/instance/spawn_context.go`
- Modify: `domain/engine/engine.go:411-421` (origin stamp block) and `:432-450` (the `Instance` literal)
- Test:   `domain/instance/spawn_context_test.go`
- Test:   `domain/engine/engine_spawn_stamp_test.go`

---

- [x] **Step 1: Write the failing test for the context carrier**

Create `domain/instance/spawn_context_test.go`:

```go
package instance

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSpawnLinkContext_RoundTrip(t *testing.T) {
	want := SpawnLink{
		ParentInstanceID: uuid.New(),
		ParentNodeID:     "spawn-1",
		Mode:             SpawnModeWaitForChild,
	}
	ctx := WithSpawnLink(context.Background(), want)

	got, ok := SpawnLinkFromContext(ctx)
	if !ok {
		t.Fatal("SpawnLinkFromContext must report ok=true after WithSpawnLink")
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestSpawnLinkContext_AbsentReportsNotOk(t *testing.T) {
	if _, ok := SpawnLinkFromContext(context.Background()); ok {
		t.Fatal("a plain context carries no spawn link")
	}
}

// The Origin carrier and the SpawnLink carrier are INDEPENDENT: setting one
// must not clear or shadow the other, because a spawned child still records how
// its parent's run was started.
func TestSpawnLinkContext_IndependentOfOrigin(t *testing.T) {
	sid := uuid.New()
	ctx := WithOrigin(context.Background(), Origin{Source: OriginSchedule, ScheduleID: &sid})
	ctx = WithSpawnLink(ctx, SpawnLink{
		ParentInstanceID: uuid.New(), ParentNodeID: "n", Mode: SpawnModeFireAndForget,
	})

	if o, ok := OriginFromContext(ctx); !ok || o.Source != OriginSchedule {
		t.Fatalf("the origin must survive alongside a spawn link, got %+v ok=%v", o, ok)
	}
	if l, ok := SpawnLinkFromContext(ctx); !ok || l.Mode != SpawnModeFireAndForget {
		t.Fatalf("the spawn link must survive alongside an origin, got %+v ok=%v", l, ok)
	}
}

// A derived context overrides the inherited link, which is what makes a
// grandchild carry ITS parent's link rather than its grandparent's.
func TestSpawnLinkContext_InnerOverridesOuter(t *testing.T) {
	outer := SpawnLink{ParentInstanceID: uuid.New(), ParentNodeID: "a", Mode: SpawnModeFireAndForget}
	inner := SpawnLink{ParentInstanceID: uuid.New(), ParentNodeID: "b", Mode: SpawnModeWaitForChild}

	ctx := WithSpawnLink(WithSpawnLink(context.Background(), outer), inner)
	got, _ := SpawnLinkFromContext(ctx)
	if got != inner {
		t.Fatalf("the innermost link must win: got %+v, want %+v", got, inner)
	}
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/instance/ -run TestSpawnLinkContext
```

Expected output:

```
# github.com/arbitron/arbitron/domain/instance [github.com/arbitron/arbitron/domain/instance.test]
domain/instance/spawn_context_test.go:16:9: undefined: WithSpawnLink
domain/instance/spawn_context_test.go:18:12: undefined: SpawnLinkFromContext
FAIL	github.com/arbitron/arbitron/domain/instance [build failed]
```

- [x] **Step 3: Implement the carrier**

Create `domain/instance/spawn_context.go`:

```go
package instance

import "context"

// spawnLinkKey is the unexported context key; using a private type prevents
// collisions with any other package's context values.
type spawnLinkKey struct{}

// WithSpawnLink returns a copy of ctx carrying l, so the engine can stamp a
// child's parent link at instance CONSTRUCTION without a Trigger signature
// change.
//
// This is a SIBLING of WithOrigin, not an extension of it. Origin answers "how
// did this run start" and is stamped onto trigger_source/schedule_id; the spawn
// link answers "which node of which run created me". Folding the link into
// Origin would force both schedule trigger sites — and every other Origin
// consumer — to reason about spawning.
//
// The link MUST be carried this way rather than written after the trigger
// returns: Engine.TriggerWithDepartment creates AND enqueues, so control
// returns to the caller only after the child may already be running. A child
// that reached a terminal state before a follow-up link write landed would
// leave its waiting parent with nothing to match, and the parent would hang.
func WithSpawnLink(ctx context.Context, l SpawnLink) context.Context {
	return context.WithValue(ctx, spawnLinkKey{}, l)
}

// SpawnLinkFromContext returns the link set by WithSpawnLink, or ok=false when
// none is present (a root run: manual, scheduled, or webhook-triggered).
func SpawnLinkFromContext(ctx context.Context) (SpawnLink, bool) {
	l, ok := ctx.Value(spawnLinkKey{}).(SpawnLink)
	return l, ok
}
```

- [x] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/instance/
go test ./domain/instance/ -run TestSpawnLinkContext -v
```

Expected output:

```
--- PASS: TestSpawnLinkContext_RoundTrip (0.00s)
--- PASS: TestSpawnLinkContext_AbsentReportsNotOk (0.00s)
--- PASS: TestSpawnLinkContext_IndependentOfOrigin (0.00s)
--- PASS: TestSpawnLinkContext_InnerOverridesOuter (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/instance	0.2s
```

- [x] **Step 5: Write the failing test for the trigger-path stamp**

Create `domain/engine/engine_spawn_stamp_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stampFixture registers a trivial one-node workflow so Trigger succeeds, and
// returns the fixtures.
func stampFixture(t *testing.T) *testFixtures {
	t.Helper()
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "child",
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{
					{Variable: "x", Operation: "set", Value: 1},
				}}},
		},
	})
	return f
}

// Happy path: a trigger whose context carries a spawn link creates the instance
// with the link ALREADY SET on the row observed by Create.
func TestTrigger_StampsSpawnLinkAtConstruction(t *testing.T) {
	f := stampFixture(t)
	parentID := uuid.New()

	ctx := instance.WithSpawnLink(context.Background(), instance.SpawnLink{
		ParentInstanceID: parentID,
		ParentNodeID:     "spawn-1",
		Mode:             instance.SpawnModeWaitForChild,
	})

	childID, _, err := f.engine.Trigger(ctx, "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	require.NotNil(t, got.ParentInstanceID,
		"the link must be present on the instance handed to Create, not written afterwards")
	assert.Equal(t, parentID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeWaitForChild, got.SpawnMode)
}

// A plain trigger (no spawn link on the context) creates a ROOT run.
func TestTrigger_NoSpawnLinkCreatesRoot(t *testing.T) {
	f := stampFixture(t)

	childID, _, err := f.engine.Trigger(context.Background(), "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.True(t, got.IsRoot())
	assert.Equal(t, instance.SpawnMode(""), got.SpawnMode)
}

// A PARTIAL link is never stamped: a row with a parent id but no node id could
// not be matched by the wake and would strand its parent forever.
func TestTrigger_PartialSpawnLinkIsNotStamped(t *testing.T) {
	f := stampFixture(t)

	ctx := instance.WithSpawnLink(context.Background(), instance.SpawnLink{
		ParentInstanceID: uuid.New(), // no node id, no mode
	})
	childID, _, err := f.engine.Trigger(ctx, "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.True(t, got.IsRoot(), "an incomplete link must not be persisted at all")
}

// The spawn link and the run origin are stamped independently.
func TestTrigger_SpawnLinkAndOriginCoexist(t *testing.T) {
	f := stampFixture(t)
	sid := uuid.New()
	parentID := uuid.New()

	ctx := instance.WithOrigin(context.Background(), instance.Origin{
		Source: instance.OriginSchedule, ScheduleID: &sid,
	})
	ctx = instance.WithSpawnLink(ctx, instance.SpawnLink{
		ParentInstanceID: parentID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
	})

	childID, _, err := f.engine.Trigger(ctx, "child", map[string]any{}, "normal")
	require.NoError(t, err)

	got := f.instanceRepo.get(childID)
	require.NotNil(t, got)
	assert.Equal(t, instance.OriginSchedule, got.TriggerSource)
	require.NotNil(t, got.ScheduleID)
	assert.Equal(t, sid, *got.ScheduleID)
	require.NotNil(t, got.ParentInstanceID)
	assert.Equal(t, parentID, *got.ParentInstanceID)
}
```

- [x] **Step 6: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestTrigger_
```

Expected output:

```
--- FAIL: TestTrigger_StampsSpawnLinkAtConstruction (0.00s)
    engine_spawn_stamp_test.go:...: the link must be present on the instance handed to Create, not written afterwards
FAIL	github.com/arbitron/arbitron/domain/engine	0.4s
```

- [x] **Step 7: Implement the stamp**

In `domain/engine/engine.go`, immediately after the origin block (which ends
with the `scheduleID = o.ScheduleID` assignment, ~line 421), add:

```go
	// Stamp the parent/child spawn link from the trigger context. It rides the
	// SAME context-carried creation-time mechanism as Origin above, and is
	// stamped into the SAME Instance literal below — never as a follow-up write.
	// This trigger path creates AND enqueues, so a link written after enqueue
	// could lose a race with a fast child reaching a terminal state, leaving its
	// waiting parent with nothing to match. An INCOMPLETE link is ignored
	// entirely rather than partially persisted.
	var parentInstanceID *uuid.UUID
	var parentNodeID *string
	var spawnMode instance.SpawnMode
	if l, ok := instance.SpawnLinkFromContext(ctx); ok && l.Valid() {
		p := l.ParentInstanceID
		n := l.ParentNodeID
		parentInstanceID = &p
		parentNodeID = &n
		spawnMode = l.Mode
	}
```

Then add the three fields to the `inst := &instance.Instance{...}` literal,
after `ScheduleID: scheduleID,`:

```go
		TriggerSource:     triggerSource,
		ScheduleID:        scheduleID,
		ParentInstanceID:  parentInstanceID,
		ParentNodeID:      parentNodeID,
		SpawnMode:         spawnMode,
```

- [x] **Step 8: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/engine.go
go test ./domain/engine/ -run TestTrigger_ -v
```

Expected output:

```
--- PASS: TestTrigger_StampsSpawnLinkAtConstruction (0.00s)
--- PASS: TestTrigger_NoSpawnLinkCreatesRoot (0.00s)
--- PASS: TestTrigger_PartialSpawnLinkIsNotStamped (0.00s)
--- PASS: TestTrigger_SpawnLinkAndOriginCoexist (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.5s
```

- [x] **Step 9: Confirm nothing regressed**

```bash
go test ./domain/... ./adapter/... ./cmd/...
```

Expected: `ok` for every package.

> NOTE: `adapter/config` `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`
> fails (UNKNOWN_PROFILE on the sample feature-pipeline nodes). Verified
> PRE-EXISTING by stashing this task's changes — unrelated to the spawn link.

- [x] **Step 10: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/instance/spawn_context.go domain/instance/spawn_context_test.go domain/engine/engine.go domain/engine/engine_spawn_stamp_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): stamp the spawn link onto the child instance at construction"
```
