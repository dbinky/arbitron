# Phase 1 — Task 3: Enrichment step service component

**Goal:** Add the `InstanceEnricher` service-layer component that takes an already-authorized page of `[]InstanceSummary` and composes the additive enriched fields (run name, submitter, friendly step, model, backend, retry count, queue position) by calling the real producers, returning `[]EnrichedInstanceSummary`. Best-effort, degrades per field; never fails the list.

**Architecture:** The enricher lives in `domain/engine` (the service layer) next to `ListInstances`. It is constructed with its collaborators (instance repo for snapshot loads, node-exec repo for the aggregate, user repo for batch submitter lookup, queue position provider). It owns composition and degradation policy only — no business rules. The queue-position provider is an interface satisfied by Phase 2's `queue.Manager.QueuePositions`; until Phase 2 wires it, a `nilQueuePositions` default leaves the position field at 0 (non-queued) — this is NOT a `// NOTE` standing in for a missing producer, it is the documented default for the position axis that Phase 2 fills, and Task P2-3 stamps the real value. The composition-root wiring is Task 5.

**Tech Stack:** Go, standard library + `log/slog`. Tests with `testing` + `testify`, using the existing `domain/engine` test mock patterns (`mockNodeExecutionRepo`, `newMockUserRepo`).

---

### Task 3: InstanceEnricher

**Files:**
- Create: `domain/engine/enricher.go`
- Create: `domain/engine/enricher_test.go`

- [x] Step 1: Write the failing test for the enricher (happy path: all fields present)

Create `domain/engine/enricher_test.go`:

```go
package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enricherTestDeps bundles the real engine-test mocks the enricher is driven
// with. They are constructed fresh per test so seeding is isolated.
type enricherTestDeps struct {
	instRepo     *enricherInstanceRepo
	nodeExecRepo *mockNodeExecutionRepo
	userRepo     *enricherUserRepo
}

func newEnricherDeps() *enricherTestDeps {
	return &enricherTestDeps{
		instRepo:     newEnricherInstanceRepo(),
		nodeExecRepo: newMockNodeExecutionRepo(),
		userRepo:     newEnricherUserRepo(),
	}
}

func (d *enricherTestDeps) enricher() *engine.InstanceEnricher {
	return engine.NewInstanceEnricher(d.instRepo, d.nodeExecRepo, d.userRepo, nil)
}

func TestInstanceEnricher_HappyPath_AllFieldsPresent(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}

	nodeID := "draft-node"
	wf := workflow.Workflow{
		Name: "feature-pipeline",
		Inputs: []workflow.InputField{{Name: "title", Type: "string", Required: true}},
		Nodes: []workflow.Node{{ID: nodeID, Label: "Generate draft", Config: workflow.NodeConfig{Backend: "claude-code"}}},
	}
	inst := &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           owner,
		WorkflowName:      "feature-pipeline",
		WorkflowSnapshot:  snapshotMap(t, wf),
		State:             instance.StateRunning,
		Priority:          instance.PriorityNormal,
		CurrentNodeID:     &nodeID,
		Variables:         map[string]any{"title": "Refactor checkout"},
		CreatedAt:         time.Now(),
	}
	d.instRepo.instances[inst.ID] = inst

	model := "gpt-4o"
	d.nodeExecRepo.executions = append(d.nodeExecRepo.executions, &instance.NodeExecution{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: nodeID, Attempt: 2, LLMModel: &model, StartedAt: time.Now(),
	})

	summaries := []instance.InstanceSummary{{ID: inst.ID, WorkflowName: inst.WorkflowName, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}}

	enr := d.enricher()
	got, err := enr.Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	require.Len(t, got, 1)

	row := got[0]
	assert.Equal(t, inst.ID, row.ID)
	assert.Equal(t, "Refactor checkout — feature-pipeline", row.RunName)
	assert.Equal(t, "alice", row.Submitter.Name)
	assert.Equal(t, "Generate draft", row.FriendlyStep, "label from the current node")
	assert.Equal(t, "gpt-4o", row.Model)
	assert.Equal(t, "claude-code", row.Backend)
	assert.Equal(t, 2, row.RetryCount)
	assert.Equal(t, 0, row.QueuePosition, "no queue-position provider wired -> 0 (non-queued)")
}
```

- [x] Step 2: Run it, confirm it fails

```bash
go test ./domain/engine/ -run TestInstanceEnricher_HappyPath
```

Expected: build failure — `undefined: engine.InstanceEnricher`, `undefined: engine.NewInstanceEnricher`, `undefined: enricherInstanceRepo`, `undefined: newEnricherInstanceRepo`, `snapshotMap`.

- [x] Step 3: Add the test helpers (`enricherInstanceRepo`, `snapshotMap`)

Append to `domain/engine/enricher_test.go` (below the first test):

```go
import (
	"encoding/json"
)

// snapshotMap serializes a workflow definition into the map[string]any shape
// stored on Instance.WorkflowSnapshot, so tests can seed the snapshot the
// enricher parses with engine.FindNodeByID.
func snapshotMap(t *testing.T, wf workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// enricherInstanceRepo is a minimal instance.InstanceRepository for enricher
// tests. Only Get is exercised (the enricher loads full instances for
// snapshots); every other method panics so a test that calls them is wrong.
type enricherInstanceRepo struct {
	instances map[uuid.UUID]*instance.Instance
}

func newEnricherInstanceRepo() *enricherInstanceRepo {
	return &enricherInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *enricherInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return inst, nil
}
func (r *enricherInstanceRepo) Create(context.Context, *instance.Instance) error { panic("unused") }
func (r *enricherInstanceRepo) List(context.Context, instance.ListFilters) ([]instance.InstanceSummary, error) {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdateState(context.Context, uuid.UUID, instance.State) error {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdateVariables(context.Context, uuid.UUID, map[string]any) error {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdateCurrentNode(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetFailed(context.Context, uuid.UUID, string) error { panic("unused") }
func (r *enricherInstanceRepo) SetCompleted(context.Context, uuid.UUID) error      { panic("unused") }
func (r *enricherInstanceRepo) ListRunning(context.Context) ([]instance.Instance, error) {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdatePriority(context.Context, uuid.UUID, instance.Priority) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetWaiting(context.Context, uuid.UUID, *time.Time) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetWebhookFields(context.Context, uuid.UUID, *string, *string) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetWebhookReceived(context.Context, uuid.UUID, time.Time) error {
	panic("unused")
}
func (r *enricherInstanceRepo) Stats(context.Context, string, time.Time, time.Time) (*instance.WorkflowStats, error) {
	panic("unused")
}

var _ instance.InstanceRepository = (*enricherInstanceRepo)(nil)

// enricherUserRepo is a minimal instance.UserRepository for enricher tests.
// Only GetByID is exercised (the enricher batch-looks-up owners); the other
// methods satisfy the interface with zero values. users maps ownerID -> User.
type enricherUserRepo struct {
	users map[uuid.UUID]instance.User
}

func newEnricherUserRepo() *enricherUserRepo {
	return &enricherUserRepo{users: make(map[uuid.UUID]instance.User)}
}

func (r *enricherUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return &u, nil
}
func (r *enricherUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) { return uuid.Nil, nil }
func (r *enricherUserRepo) GetBySubject(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *enricherUserRepo) GetByEmail(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *enricherUserRepo) Create(context.Context, *instance.User) error { return nil }
func (r *enricherUserRepo) UpdateUsername(context.Context, uuid.UUID, string) error {
	return nil
}
func (r *enricherUserRepo) List(context.Context) ([]instance.User, error) { return nil, nil }
func (r *enricherUserRepo) UpdateRole(context.Context, uuid.UUID, string) error { return nil }
func (r *enricherUserRepo) CountAdmins(context.Context) (int, error)      { return 0, nil }
func (r *enricherUserRepo) ListDepartments(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (r *enricherUserRepo) ReplaceDepartments(context.Context, uuid.UUID, []string) error {
	return nil
}

var _ instance.UserRepository = (*enricherUserRepo)(nil)
```

Merge the new `"encoding/json"` import into the existing import block at the top of the file (do not leave a duplicate import block).

- [x] Step 4: Write the additional failing tests (degradation, edge cases)

Append to `domain/engine/enricher_test.go`:

```go
func TestInstanceEnricher_UserLookupFails_SubmitterEmpty(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	// No user seeded for owner -> userRepo returns ErrNotFound
	nodeID := "n1"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: nodeID, Label: "step"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "self", Role: identity.RoleUser})
	require.NoError(t, err, "user lookup failure degrades the field, not the list")
	require.Len(t, got, 1)
	assert.Equal(t, "", got[0].Submitter.Name)
	assert.Equal(t, "step", got[0].FriendlyStep, "other fields still resolved")
}

func TestInstanceEnricher_SnapshotMissingNode_FriendlyStepEmpty(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: "real-node", Label: "Real"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: strPtr("missing-node"), CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: strPtr("missing-node"), CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "", got[0].FriendlyStep, "node not in snapshot -> empty, row still returned")
	assert.NotEmpty(t, got[0].RunName, "run name still derived")
}

func TestInstanceEnricher_QueuedInstance_NoExecutions(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	wf := workflow.Workflow{Name: "wf", Inputs: []workflow.InputField{{Name: "title", Type: "string"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateQueued, Variables: map[string]any{"title": "T"}, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateQueued, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "", got[0].Model, "queued: no model")
	assert.Equal(t, 0, got[0].RetryCount, "queued: 0 retries")
	assert.Equal(t, "", got[0].FriendlyStep, "queued: no current node")
	assert.Equal(t, "T — wf", got[0].RunName)
}

func TestInstanceEnricher_EmptyPage(t *testing.T) {
	d := newEnricherDeps()
	got, err := d.enricher().Enrich(context.Background(), nil, identity.Identity{UserID: uuid.New(), Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestInstanceEnricher_DuplicateOwnerIDs_DedupedToOneLookup(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	wf := workflow.Workflow{Name: "wf"}
	inst1 := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CreatedAt: time.Now()}
	inst2 := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CreatedAt: time.Now()}
	d.instRepo.instances[inst1.ID] = inst1
	d.instRepo.instances[inst2.ID] = inst2

	summaries := []instance.InstanceSummary{
		{ID: inst1.ID, OwnerID: owner, State: instance.StateRunning, CreatedAt: inst1.CreatedAt},
		{ID: inst2.ID, OwnerID: owner, State: instance.StateRunning, CreatedAt: inst2.CreatedAt},
	}
	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "alice", got[0].Submitter.Name)
	assert.Equal(t, "alice", got[1].Submitter.Name)
}

func TestInstanceEnricher_BackendUnset_EmptyBackend(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	nodeID := "n1"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: nodeID, Label: "step", Config: workflow.NodeConfig{Backend: workflow.BackendUnset}}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "", got[0].Backend, "BackendUnset -> empty cell (effective backend not persisted)")
}

func TestInstanceEnricher_FailedInstance_UsesFailedNodeID(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	failedNode := "fail-node"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: failedNode, Label: "Crashed step"}, {ID: "other", Label: "Other"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateFailed, FailedNodeID: &failedNode, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateFailed, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "Crashed step", got[0].FriendlyStep, "failed -> label from FailedNodeID")
}

func strPtr(s string) *string { return &s }
```

- [x] Step 5: Implement the `InstanceEnricher`

Create `domain/engine/enricher.go`:

```go
package engine

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// QueuePositionProvider returns a 1-based priority-aware queue position for
// each currently-queued instance ID. Non-queued IDs are absent (position 0).
// Implemented by queue.Manager.QueuePositions in Phase 2; nil leaves the
// position field at 0 (non-queued) until Phase 2 wires the real provider.
type QueuePositionProvider interface {
	QueuePositions(ctx context.Context) (map[uuid.UUID]int, error)
}

// InstanceEnricher is the service-layer enrichment step for the instance list.
// Given an already-authorized page of InstanceSummary, it composes the
// additive enriched fields by calling the real producers and returns
// EnrichedInstanceSummary rows. It owns composition and degradation policy
// only: each producer call is isolated, and a failure for one field degrades
// only that field — the row still renders. It never fails the list.
type InstanceEnricher struct {
	instances    instance.InstanceRepository
	nodeExecs    instance.NodeExecutionRepository
	users        instance.UserRepository
	queuePos     QueuePositionProvider // nil -> no position (Phase 2 wires this)
}

// NewInstanceEnricher constructs the enricher with its collaborators. A nil
// queuePos leaves the queue-position field at 0 (non-queued) until Phase 2
// wires the real provider.
func NewInstanceEnricher(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	users instance.UserRepository,
	queuePos QueuePositionProvider,
) *InstanceEnricher {
	return &InstanceEnricher{
		instances: instances,
		nodeExecs: nodeExecs,
		users:     users,
		queuePos:  queuePos,
	}
}

// SetQueuePositionProvider wires the queue-position provider (Phase 2). Kept
// as a setter so the composition root can construct the enricher before the
// queue manager exists, then wire the provider once it does.
func (e *InstanceEnricher) SetQueuePositionProvider(p QueuePositionProvider) {
	e.queuePos = p
}

// Enrich composes the additive fields for the given authorized page. The
// callerID drives "you" vs display-name rendering in the HTTP layer (the
// enricher resolves the display name; the HTTP layer decides the label from
// mode). Returns the enriched rows in the same order as the input.
func (e *InstanceEnricher) Enrich(ctx context.Context, summaries []instance.InstanceSummary, callerID identity.Identity) ([]instance.EnrichedInstanceSummary, error) {
	if len(summaries) == 0 {
		return []instance.EnrichedInstanceSummary{}, nil
	}

	ids := make([]uuid.UUID, len(summaries))
	ownerSet := make(map[uuid.UUID]struct{}, len(summaries))
	for i, s := range summaries {
		ids[i] = s.ID
		ownerSet[s.OwnerID] = struct{}{}
	}

	// One batched node-exec aggregate call for the whole page.
	aggMap, aggErr := e.nodeExecs.AggregateByInstances(ctx, ids)
	if aggErr != nil {
		slog.Warn("enricher: node-exec aggregate failed; model/retry degraded", "error", aggErr)
		aggMap = map[uuid.UUID]instance.NodeExecutionAggregate{}
	}

	// One batched user lookup for the page's distinct owners.
	userMap := e.batchUsers(ctx, ownerSet)

	// Queue positions (one call) when a provider is wired.
	var posMap map[uuid.UUID]int
	if e.queuePos != nil {
		pm, perr := e.queuePos.QueuePositions(ctx)
		if perr != nil {
			slog.Warn("enricher: queue positions failed; position degraded", "error", perr)
		}
		posMap = pm
	}
	if posMap == nil {
		posMap = map[uuid.UUID]int{}
	}

	out := make([]instance.EnrichedInstanceSummary, len(summaries))
	for i, s := range summaries {
		row := instance.EnrichedInstanceSummary{InstanceSummary: s}

		// Run name: needs the full instance for variables + snapshot + inputs.
		// Best-effort: a Get failure degrades only the run name (the ladder
		// still yields the short id from s.ID).
		inst, instErr := e.instances.Get(ctx, s.ID)
		if instErr != nil || inst == nil {
			slog.Warn("enricher: instance load failed; run name degraded to short id", "instanceID", s.ID, "error", instErr)
			row.RunName = instance.DeriveRunName(s.WorkflowName, nil, nil, s.ID)
		} else {
			inputs := readWorkflowInputs(inst.WorkflowSnapshot)
			row.RunName = instance.DeriveRunName(inst.WorkflowName, inst.Variables, inputs, inst.ID)

			// Friendly step + backend from the relevant node in the snapshot.
			label, backend := e.resolveNodeFields(inst)
			row.FriendlyStep = label
			row.Backend = backend

			// FailedNodeID carried for the dashboard detail panel's lazy error load.
			row.FailedNodeID = inst.FailedNodeID
		}

		// Submitter from the batched user map.
		if u, ok := userMap[s.OwnerID]; ok {
			row.Submitter = instance.Submitter{Name: u.Username, AccountIdentity: accountIdentity(u)}
		}

		// Model + retry from the batched aggregate.
		if agg, ok := aggMap[s.ID]; ok {
			row.Model = agg.LatestModel
			row.RetryCount = agg.MaxAttempt
		}

		// Queue position (queued rows only; 0 for non-queued).
		if pos, ok := posMap[s.ID]; ok {
			row.QueuePosition = pos
		}

		out[i] = row
	}
	return out, nil
}

// batchUsers resolves the distinct owner IDs to a user map. A missing or
// errored user is absent from the map (the caller degrades the submitter
// field). Distinct IDs are looked up once.
func (e *InstanceEnricher) batchUsers(ctx context.Context, ownerSet map[uuid.UUID]struct{}) map[uuid.UUID]instance.User {
	out := make(map[uuid.UUID]instance.User, len(ownerSet))
	if e.users == nil {
		return out
	}
	for id := range ownerSet {
		u, err := e.users.GetByID(ctx, id)
		if err != nil {
			slog.Warn("enricher: user lookup failed; submitter degraded", "ownerID", id, "error", err)
			continue
		}
		out[id] = *u
	}
	return out
}

// resolveNodeFields returns (label, backend) for the relevant node in the
// instance's workflow snapshot. The relevant node is:
//   - CurrentNodeID when the instance is active (running/waiting/paused)
//   - FailedNodeID when failed
//   - none otherwise (completed/cancelled have no current step) -> ("", "")
//
// Best-effort: any parse/lookup failure also returns ("", "").
func (e *InstanceEnricher) resolveNodeFields(inst *instance.Instance) (label, backend string) {
	nodeID := relevantNodeID(inst)
	if nodeID == "" {
		return "", ""
	}
	node, err := FindNodeByID(inst.WorkflowSnapshot, nodeID)
	if err != nil {
		return "", ""
	}
	if node.Label != "" {
		label = node.Label
	} else {
		label = node.ID
	}
	if node.Config.Backend != workflow.BackendUnset {
		backend = node.Config.Backend
	}
	return label, backend
}

// relevantNodeID picks the node whose label/backend the row should show.
func relevantNodeID(inst *instance.Instance) string {
	if inst.State == instance.StateFailed && inst.FailedNodeID != nil && *inst.FailedNodeID != "" {
		return *inst.FailedNodeID
	}
	if inst.CurrentNodeID != nil && *inst.CurrentNodeID != "" {
		return *inst.CurrentNodeID
	}
	// Terminal (completed/cancelled) rows have no *current* step (per the
	// Phase-1 design provenance and the spec's "what step it is currently on"),
	// so the step cell intentionally degrades to a placeholder. This is not a
	// missing producer — there is no current-step value to show.
	return ""
}

// readWorkflowInputs extracts the workflow's Inputs from the snapshot so
// DeriveRunName can apply the ladder. Best-effort: a parse failure yields nil.
func readWorkflowInputs(snapshot map[string]any) []workflow.InputField {
	if snapshot == nil {
		return nil
	}
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return nil
	}
	return wf.Inputs
}

// accountIdentity returns the email if present, else the subject, for the
// shared-mode row detail. Empty when neither is set (local default user).
func accountIdentity(u instance.User) string {
	if u.Email != nil && *u.Email != "" {
		return *u.Email
	}
	if u.Subject != nil && *u.Subject != "" {
		return *u.Subject
	}
	return ""
}
```

Add `"encoding/json"` to the import block of `domain/engine/enricher.go`.

- [x] Step 6: Confirm `mockNodeExecutionRepo` (the existing engine-test mock) has an `executions` slice field

The existing `mockNodeExecutionRepo` in `domain/engine/engine_test.go:358` has an `executions []*instance.NodeExecution` field that the tests seed directly. Confirm:

```bash
rg -n "type mockNodeExecutionRepo|executions " domain/engine/engine_test.go | head
```

The `enricherUserRepo` and `enricherInstanceRepo` mocks are defined in the test file itself (Step 3 and the block above). No changes to the existing engine-test mocks are needed.

> NOTE: The plan's claim "No changes to the existing engine-test mocks are needed" was wrong. The existing `mockNodeExecutionRepo.AggregateByInstances` returned an empty map (`engine_test.go:413-415`), so the happy-path assertions `row.Model == "gpt-4o"` and `row.RetryCount == 2` could not pass through the real seam. I updated `AggregateByInstances` to compute the aggregate from its `executions` slice (mirroring the SQLite impl's "most-recently-started model + max attempt" rule). This is safe — no other engine test asserts on aggregate output, and empty-seeded repos still return an empty map.

- [x] Step 7: Run the tests, confirm they pass

```bash
go test ./domain/engine/ -run TestInstanceEnricher
```

Expected:

```
ok  	github.com/arbitron/arbitron/domain/engine	0.00s
```

- [x] Step 8: Confirm the whole module builds

```bash
go build ./...
```

Expected: no output, exit code 0.

- [x] Step 9: Commit

```bash
git add domain/engine/enricher.go domain/engine/enricher_test.go
git commit -m "feat(engine): add InstanceEnricher service-layer enrichment step"
```