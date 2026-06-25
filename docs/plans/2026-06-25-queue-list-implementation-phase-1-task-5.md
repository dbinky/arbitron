# Phase 1 — Task 5: Composition-root wiring of the enricher + provenance probe

**Goal:** Wire the `InstanceEnricher` into the live server path: construct it at the composition root (`cmd/arbitron/main.go`) with the real `instanceRepo`, `nodeExecRepo`, and `userRepo`, set it on the engine, and confirm via a router-level integration test that the enriched fields (run name at minimum) reach the `/api/v1/instances` JSON response — failing if the enricher is absent or its production input is nil.

**Architecture:** The composition root is `cmd/arbitron/main.go` (`buildServer`/`runServe`), which already constructs the engine, repos, and `NewRouterFromDeps`. The enricher is constructed there and set on the engine via `SetInstanceEnricher`; the engine's `ListEnriched` (satisfied by the concrete `*engine.Engine`) is already the `Service` injected into `RouterDeps`. The provenance probe is a router-level test that drives `NewRouterFromDeps` with a real engine + a seeded in-memory repo and asserts the `runName` field reaches the wire — failing if the enricher is unwired (the run name would be the short-id-only fallback, not the title-derived name).

**Tech Stack:** Go, `net/http`/`httptest`. Tests with `testing` + `testify`, following `server_test.go`'s `NewRouterFromDeps` injection pattern.

---

### Task 5: Composition-root wiring + provenance probe

**Files:**
- Modify: `cmd/arbitron/main.go:757-767` (construct enricher, set on engine)
- Create: `adapter/http/instance_list_enriched_e2e_test.go` (router-level provenance probe)

- [ ] Step 1: Write the failing router-level provenance probe

Create `adapter/http/instance_list_enriched_e2e_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_InstanceList_EnricherProvenanceProbe drives the REAL composition
// path: a concrete *engine.Engine with the enricher wired, seeded with an
// instance whose submitted title is "ProbeTitle". The runName reaching the
// wire MUST be "ProbeTitle — probe-wf" — NOT the short-id fallback. If the
// enricher is unwired (or its instance-repo input is nil), the runName
// degrades to "probe-wf <short-id>" and this test FAILS, proving the wiring
// is load-bearing.
func TestRouter_InstanceList_EnricherProvenanceProbe(t *testing.T) {
	owner := uuid.New()
	nodeID := "n1"
	now := time.Now()
	wf := workflow.Workflow{Name: "probe-wf", Inputs: []workflow.InputField{{Name: "title", Type: "string", Required: true}}, Nodes: []workflow.Node{{ID: nodeID, Label: "Step"}}}
	inst := &instance.Instance{
		ID:            uuid.New(),
		OwnerID:       owner,
		WorkflowName:  "probe-wf",
		WorkflowSnapshot: snapshotMapForRouter(t, wf),
		State:         instance.StateRunning,
		Priority:      instance.PriorityNormal,
		CurrentNodeID: &nodeID,
		Variables:     map[string]any{"title": "ProbeTitle"},
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	instRepo := newMockInstanceRepo()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	userRepo := &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}

	// Real engine with the enricher wired — the composition-root path.
	eng := engine.NewEngine(
		newScopeAwareRepoForRouter(), instRepo, nodeExecRepo, snapRepo,
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, eventbusNewNoOp(),
		newMockQueueManagerForRouter(), &mockQueueRepoForRouter{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(instRepo, nodeExecRepo, userRepo, nil))

	// Inject an admin identity so the list is unconstrained (the instance is
	// visible regardless of owner). The auth middleware is nil here; we pass
	// the identity via context on the request directly.
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: nodeExecRepo,
		Snapshots: snapRepo,
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "ProbeTitle — probe-wf", resp[0]["runName"], "enricher must derive the run name from the submitted title, not the short-id fallback")
	assert.Equal(t, "Step", resp[0]["friendlyStep"], "enricher must resolve the current node label from the snapshot")
	assert.Equal(t, "alice", resp[0]["submitterName"], "enricher must resolve the owner display name")
}

// TestRouter_InstanceList_EnricherAbsent_DegradesToShortID confirms that
// WITHOUT the enricher wired, the runName is the short-id fallback (proving
// the probe above is load-bearing — it fails when the wiring is inert).
func TestRouter_InstanceList_EnricherAbsent_DegradesToShortID(t *testing.T) {
	owner := uuid.New()
	wf := workflow.Workflow{Name: "probe-wf", Inputs: []workflow.InputField{{Name: "title", Type: "string"}}, Nodes: []workflow.Node{{ID: "n1", Label: "Step"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "probe-wf", WorkflowSnapshot: snapshotMapForRouter(t, wf), State: instance.StateRunning, Variables: map[string]any{"title": "ProbeTitle"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	instRepo := newMockInstanceRepo()
	instRepo.instances[inst.ID] = inst

	eng := engine.NewEngine(
		newScopeAwareRepoForRouter(), instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(),
		&probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}},
		map[workflow.NodeType]engine.NodeExecutor{}, eventbusNewNoOp(),
		newMockQueueManagerForRouter(), &mockQueueRepoForRouter{},
	)
	// No SetInstanceEnricher — the enricher is unwired (the degraded path).

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: newMockNodeExecRepo(),
		Snapshots: newMockSnapshotRepo(),
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.NotEqual(t, "ProbeTitle — probe-wf", resp[0]["runName"], "unwired enricher must NOT derive the title-based name")
	assert.Contains(t, resp[0]["runName"], "probe-wf", "degraded run name still includes the workflow name")
}

// --- helpers ---

func snapshotMapForRouter(t *testing.T, wf workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// probeUserRepo is a minimal UserRepository for the router probe (only GetByID
// is exercised by the enricher's batch lookup).
type probeUserRepo struct {
	users map[uuid.UUID]instance.User
}

func (r *probeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return &u, nil
}
func (r *probeUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) { return uuid.Nil, nil }
func (r *probeUserRepo) GetBySubject(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *probeUserRepo) GetByEmail(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *probeUserRepo) Create(context.Context, *instance.User) error { return nil }
func (r *probeUserRepo) UpdateUsername(context.Context, uuid.UUID, string) error { return nil }
func (r *probeUserRepo) List(context.Context) ([]instance.User, error)            { return nil, nil }
func (r *probeUserRepo) UpdateRole(context.Context, uuid.UUID, string) error      { return nil }
func (r *probeUserRepo) CountAdmins(context.Context) (int, error)                 { return 0, nil }
func (r *probeUserRepo) ListDepartments(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (r *probeUserRepo) ReplaceDepartments(context.Context, uuid.UUID, []string) error { return nil }

var _ instance.UserRepository = (*probeUserRepo)(nil)

// newScopeAwareRepoForRouter mirrors the engine-test newScopeAwareRepo (a
// no-op workflow scope resolver). It is redeclared here because the router
// test package (http_test) is distinct from the engine test package.
func newScopeAwareRepoForRouter() *scopeAwareRepoForRouter { return &scopeAwareRepoForRouter{} }

type scopeAwareRepoForRouter struct{}

func (r *scopeAwareRepoForRouter) LoadVisible(_ context.Context, _ uuid.UUID, name string) (*workflow.Workflow, workflow.Scope, error) {
	return &workflow.Workflow{Name: name}, workflow.Scope{Kind: workflow.ScopePrivate}, nil
}

// eventbusNewNoOp returns the no-op event bus. The router test package already
// imports eventbus via server_test.go's newRouterTestDeps; reuse it.
func eventbusNewNoOp() engine.EventBus { return eventbusNoOpForRouter{} }

type eventbusNoOpForRouter struct{}

func (eventbusNoOpForRouter) Publish(uuid.UUID, engine.Event)             {}
func (eventbusNoOpForRouter) Subscribe(uuid.UUID) (<-chan engine.Event, func()) { return nil, func() {} }
func (eventbusNoOpForRouter) SubscribeAll() (<-chan engine.Event, func()) { return nil, func() {} }

// newMockQueueManagerForRouter mirrors mockQueueManagerForRouter in
// server_test.go but is redeclared here to avoid coupling to that file's
// private type. It only needs Enqueue + NotifyCapacityFreed.
type mockQueueManagerForRouter struct{}

func (mockQueueManagerForRouter) Enqueue(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}
func (mockQueueManagerForRouter) NotifyCapacityFreed(context.Context) {}

type mockQueueRepoForRouter struct{}

func (mockQueueRepoForRouter) CountRunning(context.Context) (int, error) { return 0, nil }
func (mockQueueRepoForRouter) CountRunningByOwner(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}
func (mockQueueRepoForRouter) CountQueued(context.Context) (int, error) { return 0, nil }
func (mockQueueRepoForRouter) CountWaiting(context.Context) (int, error) { return 0, nil }
func (mockQueueRepoForRouter) NextEligible(context.Context, int, int) (*instance.Instance, error) {
	return nil, nil
}
func (mockQueueRepoForRouter) SetQueued(context.Context, uuid.UUID, time.Time) error { return nil }
func (mockQueueRepoForRouter) ListWaitingWithTimer(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (mockQueueRepoForRouter) ListWaitingWithWebhook(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (mockQueueRepoForRouter) AggregateByInstances(context.Context, []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	return map[uuid.UUID]instance.NodeExecutionAggregate{}, nil
}

var _ instance.QueueRepository = (*mockQueueRepoForRouter)(nil)
```

Note: the `scopeAwareRepoForRouter`, `eventbusNoOpForRouter`, `mockQueueManagerForRouter`, and `mockQueueRepoForRouter` types above are redeclared in this file because the `http_test` package is distinct from `engine_test` and `server_test`'s private types. If any of these names already exist in the `http_test` package (check with `rg -n "type mockQueueManagerForRouter|type scopeAwareRepo" adapter/http/`), rename the ones in this file to avoid a collision (e.g. prefix with `enricherProbe`). The `eventbusNewNoOp` helper should instead reuse the real `eventbus.NewNoOp()` — import `github.com/arbitron/arbitron/adapter/eventbus` and replace the `eventbusNoOpForRouter` shim with `eventbus.NewNoOp()`. Adjust the import block accordingly and remove the shim type.

- [ ] Step 2: Run it, confirm it fails

```bash
go test ./adapter/http/ -run TestRouter_InstanceList_Enricher
```

Expected: the provenance probe FAILS — `runName` is the short-id fallback because the enricher is not yet wired at the composition root (the test constructs it explicitly, but `Engine.ListEnriched` is not yet the path the handler calls — the handler still calls `ListInstances` until Task 4's handler switch is in place). If Task 4 is already in place, the probe passes once the enricher is constructed in the test; the absent-enricher test confirms the probe is load-bearing. Fix any compile errors from the redeclared helper types first.

- [ ] Step 3: Wire the enricher at the composition root

In `cmd/arbitron/main.go`, after the engine is constructed (after line 767, after the `NewEngine` call and before `eng.SetWorkspaceManager`), add:

```go
	// 11b. Construct the instance-list enrichment step and wire it to the
	// engine so ListEnriched composes the additive queue-list fields (run
	// name, submitter, friendly step, model, backend, retry count). The
	// queue-position provider is wired in Phase 2 (queue.Manager.QueuePositions).
	instanceEnricher := engine.NewInstanceEnricher(instanceRepo, nodeExecRepo, userRepo, nil)
	eng.SetInstanceEnricher(instanceEnricher)
```

- [ ] Step 4: Confirm the build passes and the provenance probe passes

```bash
go build ./... && go test ./adapter/http/ -run TestRouter_InstanceList_Enricher
```

Expected: build succeeds; the provenance probe passes (`runName` is `"ProbeTitle — probe-wf"`); the absent-enricher test passes (`runName` is the degraded fallback).

- [ ] Step 5: Confirm the full test gate passes

```bash
go test ./...
```

Expected: all package tests pass (existing router tests that decode the list into `[]instance.InstanceSummary` still work — the enriched JSON is a superset; existing handler tests use `mockInstanceService` which now has a `ListEnriched` fallback wrapping `ListInstances`).

- [ ] Step 6: Commit

```bash
git add cmd/arbitron/main.go adapter/http/instance_list_enriched_e2e_test.go
git commit -m "feat(server): wire InstanceEnricher at the composition root + provenance probe"
```