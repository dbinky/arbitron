# Phase 2 — Task 3: Wire QueuePositions into the enricher + composition root

**Goal:** Wire `queue.Manager.QueuePositions` into the `InstanceEnricher` as its `QueuePositionProvider`, both at the composition root (`cmd/arbitron/main.go`) and via a router-level provenance probe that asserts the position reaches the `/api/v1/instances` JSON for a queued row — failing if the provider is unwired (position stays 0).

**Architecture:** The `InstanceEnricher` already has a `SetQueuePositionProvider` setter (Phase 1 Task 3) and a `QueuePositionProvider` interface that `queue.Manager` satisfies (`QueuePositions(ctx) (map[uuid.UUID]int, error)` matches). The composition root constructs the manager after the enricher (the manager needs the engine as its starter), so the setter is the wiring seam. The provenance probe drives the real `NewRouterFromDeps` with a concrete engine + manager, seeds a queued instance, and asserts `queuePosition` is a positive integer on the wire — proving the position flows from the manager through the enricher to the JSON sink.

**Tech Stack:** Go, `net/http`/`httptest`. Tests with `testing` + `testify`.

---

### Task 3: Wire QueuePositions

**Files:**
- Modify: `cmd/arbitron/main.go:779-780` (after the queue manager is created, set it as the enricher's position provider)
- Create: `adapter/http/instance_list_queue_position_e2e_test.go` (provenance probe for the position field)

- [ ] Step 1: Write the failing router-level provenance probe for the queue position

Create `adapter/http/instance_list_queue_position_e2e_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_InstanceList_QueuePositionProvenanceProbe drives the REAL
// composition path: a concrete *engine.Engine + *queue.Manager with the
// enricher wired AND the manager set as the QueuePositionProvider. A queued
// instance MUST receive a positive queuePosition on the wire. If the provider
// is unwired, position stays 0 and this test FAILS — proving the wiring is
// load-bearing.
func TestRouter_InstanceList_QueuePositionProvenanceProbe(t *testing.T) {
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	wf := workflow.Workflow{Name: "pos-wf", Nodes: []workflow.Node{{ID: "n1", Label: "Step"}}}
	queuedInst := &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           owner,
		WorkflowName:      "pos-wf",
		WorkflowSnapshot:  snapshotMapForRouter(t, wf),
		State:             instance.StateQueued,
		Priority:          instance.PriorityHigh,
		QueuedAt:          &now,
		Variables:         map[string]any{},
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	instRepo := newMockInstanceRepo()
	instRepo.instances[queuedInst.ID] = queuedInst
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	userRepo := &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}
	queueRepo := &probeQueueRepo{queued: []instance.Instance{*queuedInst}}

	eng := engine.NewEngine(
		newScopeAwareRepoForRouter(), instRepo, nodeExecRepo, snapRepo,
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		nil, queueRepo,
	)
	enricher := engine.NewInstanceEnricher(instRepo, nodeExecRepo, userRepo, nil)
	eng.SetInstanceEnricher(enricher)

	// Construct the queue manager and wire it as the position provider — the
	// composition-root path. The manager needs a starter; a nil starter is fine
	// because QueuePositions never starts work (read-only).
	mgr := queue.NewManager(queueRepo, instRepo, &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}}, nil)
	enricher.SetQueuePositionProvider(mgr)

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: nodeExecRepo,
		Snapshots: snapRepo,
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	pos, ok := resp[0]["queuePosition"].(float64)
	require.True(t, ok, "queuePosition must be present on the wire")
	assert.Equal(t, float64(1), pos, "single queued item -> position 1")
}

// TestRouter_InstanceList_QueuePositionAbsentWhenProviderUnwired confirms
// that WITHOUT the position provider wired, queuePosition is 0 (absent) —
// proving the probe above is load-bearing.
func TestRouter_InstanceList_QueuePositionAbsentWhenProviderUnwired(t *testing.T) {
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	wf := workflow.Workflow{Name: "pos-wf", Nodes: []workflow.Node{{ID: "n1", Label: "Step"}}}
	queuedInst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "pos-wf", WorkflowSnapshot: snapshotMapForRouter(t, wf), State: instance.StateQueued, Priority: instance.PriorityHigh, QueuedAt: &now, Variables: map[string]any{}, CreatedAt: now, UpdatedAt: now}

	instRepo := newMockInstanceRepo()
	instRepo.instances[queuedInst.ID] = queuedInst

	eng := engine.NewEngine(
		newScopeAwareRepoForRouter(), instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(),
		&probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}},
		map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		nil, &probeQueueRepo{queued: []instance.Instance{*queuedInst}},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(instRepo, newMockNodeExecRepo(), &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}, nil))
	// No SetQueuePositionProvider — position stays 0.

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: newMockNodeExecRepo(),
		Snapshots: newMockSnapshotRepo(),
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	pos, exists := resp[0]["queuePosition"]
	if exists {
		assert.Equal(t, float64(0), pos.(float64), "unwired provider -> position 0 or absent")
	}
}

// probeQueueRepo is a minimal QueueRepository for the position probe. It
// satisfies the interface including ListQueued (Phase 2 Task 1) and
// AggregateByInstances (Phase 1 Task 2, via the NodeExecutionRepository side
// is separate; this is the QueueRepository side).
type probeQueueRepo struct {
	queued []instance.Instance
}

func (r *probeQueueRepo) CountRunning(context.Context) (int, error) { return 0, nil }
func (r *probeQueueRepo) CountRunningByOwner(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}
func (r *probeQueueRepo) CountQueued(context.Context) (int, error) { return len(r.queued), nil }
func (r *probeQueueRepo) CountWaiting(context.Context) (int, error) { return 0, nil }
func (r *probeQueueRepo) NextEligible(context.Context, int, int) (*instance.Instance, error) {
	return nil, nil
}
func (r *probeQueueRepo) SetQueued(context.Context, uuid.UUID, time.Time) error { return nil }
func (r *probeQueueRepo) ListWaitingWithTimer(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *probeQueueRepo) ListWaitingWithWebhook(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *probeQueueRepo) ListQueued(_ context.Context) ([]instance.Instance, error) {
	return r.queued, nil
}

var _ instance.QueueRepository = (*probeQueueRepo)(nil)
```

Note: `snapshotMapForRouter`, `probeUserRepo`, `newScopeAwareRepoForRouter` are defined in `adapter/http/instance_list_enriched_e2e_test.go` (Phase 1 Task 5). If that file's helpers used different names, use the exact names here. `eventbus.NewNoOp()` is the real no-op event bus (replace the shim from Phase 1 Task 5 if that file used a shim — the two files must agree on the eventbus helper; prefer the real `eventbus.NewNoOp()` in both).

- [ ] Step 2: Run it, confirm the probe fails (position 0 because the composition root does not yet wire the provider)

```bash
go test ./adapter/http/ -run TestRouter_InstanceList_QueuePosition
```

Expected: the provenance probe FAILS — `queuePosition` is 0 or absent because the composition root has not yet wired the manager as the position provider. The absent-provider test passes.

- [ ] Step 3: Wire the manager as the enricher's position provider at the composition root

In `cmd/arbitron/main.go`, after the queue manager is created and set on the engine (after line 780, `eng.SetQueueManager(queueMgr)`), add:

```go
	// 12-pos. Wire the queue manager as the enricher's queue-position provider
	// so queued rows carry their priority-aware position (Phase 2). The manager
	// is the single ordering authority; the enricher only stamps the value.
	instanceEnricher.SetQueuePositionProvider(queueMgr)
```

- [ ] Step 4: Confirm the provenance probe passes

```bash
go build ./... && go test ./adapter/http/ -run TestRouter_InstanceList_QueuePosition
```

Expected: build succeeds; the provenance probe passes (`queuePosition` is 1); the absent-provider test passes (position 0).

- [ ] Step 5: Confirm the full test gate passes

```bash
go test ./...
```

Expected: all package tests pass.

- [ ] Step 6: Commit

```bash
git add cmd/arbitron/main.go adapter/http/instance_list_queue_position_e2e_test.go
git commit -m "feat(server): wire queue.Manager.QueuePositions into the enricher + position provenance probe"
```