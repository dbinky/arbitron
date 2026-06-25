# Phase 1 — Task 4: enrichedSummaryDTO + HTTP mapper + ListEnriched service method

**Goal:** Add the `enrichedSummaryDTO` JSON projection (additive to `summaryDTO`) and the `toEnrichedSummaryDTO` mapper, plus a `ListEnriched` method on the `InstanceService` interface and `Engine` that runs the authorized list then the enrichment step, returning `[]EnrichedInstanceSummary`. The HTTP `List` handler is switched to emit the enriched shape (with the existing `dismissed` decoration preserved).

**Architecture:** The DTO extends `summaryDTO` (embedded) so the lean fields flatten into the same JSON object the dashboard already consumes. `Engine.ListEnriched` composes `ListInstances` (the unchanged authz boundary) → `InstanceEnricher.Enrich`; it is the single enriched read path both HTTP and MCP adopt. The handler keeps the existing dismissal decoration by mapping over the enriched rows. The `authMode` ("you" vs display name) is rendered here from the request identity's mode, sourced from the existing `domain/identity.Mode` available via the config the handler already receives.

**Tech Stack:** Go, `encoding/json`, `net/http`. Tests with `testing` + `testify` + `httptest`, following `instance_handler_test.go` mock patterns.

---

### Task 4: enrichedSummaryDTO + ListEnriched

**Files:**
- Modify: `adapter/http/instance_handler.go:20-35` (extend `InstanceService`), `:161-224` (add `enrichedSummaryDTO` + mapper + `toEnrichedSummaryResponse`), `:297-372` (switch `List` to enriched)
- Modify: `domain/engine/engine.go:464-481` (add `ListEnriched` after `ListInstances`)
- Create: `domain/engine/enricher_list_test.go` (service-level `ListEnriched` test)
- Modify: `adapter/http/instance_handler_test.go` (add enriched-list tests + extend `mockInstanceService`)

- [x] Step 1: Write the failing service-level test for `Engine.ListEnriched`

Create `domain/engine/enricher_list_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
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

// enricherListRepo is a recordingListRepo variant that also stores full
// instances so the enricher's per-row Get (for snapshot/variables) resolves.
type enricherListRepo struct {
	summaries  []instance.InstanceSummary
	instances  map[uuid.UUID]*instance.Instance
	gotFilters instance.ListFilters
}

func (r *enricherListRepo) List(_ context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	r.gotFilters = filters
	return r.summaries, nil
}
func (r *enricherListRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return inst, nil
}
func (r *enricherListRepo) Create(context.Context, *instance.Instance) error { panic("unused") }
func (r *enricherListRepo) UpdateState(context.Context, uuid.UUID, instance.State) error {
	panic("unused")
}
func (r *enricherListRepo) UpdateVariables(context.Context, uuid.UUID, map[string]any) error {
	panic("unused")
}
func (r *enricherListRepo) UpdateCurrentNode(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (r *enricherListRepo) SetFailed(context.Context, uuid.UUID, string) error { panic("unused") }
func (r *enricherListRepo) SetCompleted(context.Context, uuid.UUID) error      { panic("unused") }
func (r *enricherListRepo) ListRunning(context.Context) ([]instance.Instance, error) {
	panic("unused")
}
func (r *enricherListRepo) UpdatePriority(context.Context, uuid.UUID, instance.Priority) error {
	panic("unused")
}
func (r *enricherListRepo) SetWaiting(context.Context, uuid.UUID, *time.Time) error { panic("unused") }
func (r *enricherListRepo) SetWebhookFields(context.Context, uuid.UUID, *string, *string) error {
	panic("unused")
}
func (r *enricherListRepo) SetWebhookReceived(context.Context, uuid.UUID, time.Time) error {
	panic("unused")
}
func (r *enricherListRepo) Stats(context.Context, string, time.Time, time.Time) (*instance.WorkflowStats, error) {
	panic("unused")
}

var _ instance.InstanceRepository = (*enricherListRepo)(nil)

func TestEngine_ListEnriched_AppliesAuthzThenEnriches(t *testing.T) {
	owner := uuid.New()
	nodeID := "n1"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: nodeID, Label: "Step"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: &nodeID, Variables: map[string]any{"title": "T"}, CreatedAt: time.Now()}
	repo := &enricherListRepo{
		summaries: []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, WorkflowName: "wf", State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}},
		instances: map[uuid.UUID]*instance.Instance{inst.ID: inst},
	}
	nodeExecRepo := newMockNodeExecutionRepo()
	userRepo := newEnricherUserRepo()
	userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}

	eng := engine.NewEngine(
		newScopeAwareRepo(), repo, nodeExecRepo, newMockSnapshotRepo(),
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(repo, nodeExecRepo, userRepo, nil))

	got, err := eng.ListEnriched(userCtx(owner), instance.ListFilters{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "T — wf", got[0].RunName)
	assert.Equal(t, "alice", got[0].Submitter.Name)
	assert.Equal(t, "Step", got[0].FriendlyStep)
	require.NotNil(t, repo.gotFilters.OwnerID)
	assert.Equal(t, owner, *repo.gotFilters.OwnerID, "authz filter applied before enrichment")
}

func TestEngine_ListEnriched_NoIdentity_NotFound(t *testing.T) {
	repo := &enricherListRepo{}
	eng := engine.NewEngine(
		newScopeAwareRepo(), repo, newMockNodeExecutionRepo(), newMockSnapshotRepo(),
		newMockUserRepo(), map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(repo, newMockNodeExecutionRepo(), newEnricherUserRepo(), nil))

	_, err := eng.ListEnriched(context.Background(), instance.ListFilters{})
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestEngine_ListEnriched_RepoError_Propagates(t *testing.T) {
	// recordingListRepo returns its error from List; the enrichment step is
	// never reached.
	sentinel := errors.New("boom")
	repo := &recordingListRepo{err: sentinel}
	eng := engine.NewEngine(
		newScopeAwareRepo(), repo, newMockNodeExecutionRepo(), newMockSnapshotRepo(),
		newMockUserRepo(), map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(repo, newMockNodeExecutionRepo(), newEnricherUserRepo(), nil))

	_, err := eng.ListEnriched(userCtx(uuid.New()), instance.ListFilters{})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}
```

- [x] Step 2: Run it, confirm it fails

```bash
go test ./domain/engine/ -run TestEngine_ListEnriched
```

Expected: build failure — `undefined: engine.Engine.ListEnriched`, `undefined: engine.Engine.SetInstanceEnricher`.

- [x] Step 3: Add `SetInstanceEnricher` + `ListEnriched` to the Engine

In `domain/engine/engine.go`, add a field to the `Engine` struct (after `resolver` near line 41):

```go
	enricher *InstanceEnricher // optional; when set, ListEnriched uses it
```

Add the setter after `SetWorkflowResolver` (after line 159):

```go
// SetInstanceEnricher wires the list-enrichment step. When set, ListEnriched
// runs the authorized ListInstances then the enricher to compose the
// additive fields. When unset, ListEnriched returns the un-enriched
// summaries wrapped in EnrichedInstanceSummary (run name only).
func (e *Engine) SetInstanceEnricher(en *InstanceEnricher) {
	e.enricher = en
}
```

Add the method after `ListInstances` (after line 481):

```go
// ListEnriched returns the instances visible to the caller with the additive
// enriched fields composed (run name, submitter, friendly step, model,
// backend, retry count, queue position). Authorization is identical to
// ListInstances (the same service-set filters); enrichment runs only on the
// authorized page. A request with no identity returns ErrNotFound, exactly
// like ListInstances. When no enricher is wired, rows carry the run name only
// (derived from the lean summary) so the endpoint still works.
func (e *Engine) ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
	lc, ok := listConstraint(ctx)
	if !ok {
		return nil, instance.ErrNotFound
	}
	if lc.Unconstrained {
		filters.OwnerID = nil
		filters.Departments = nil
		filters.IncludePublic = false
	} else {
		filters.OwnerID = lc.OwnerID
		filters.Departments = lc.Departments
		filters.IncludePublic = lc.IncludePublic
	}
	summaries, err := e.instances.List(ctx, filters)
	if err != nil {
		return nil, err
	}
	if e.enricher == nil {
		out := make([]instance.EnrichedInstanceSummary, len(summaries))
		for i, s := range summaries {
			out[i] = instance.EnrichedInstanceSummary{
				InstanceSummary: s,
				RunName:         instance.DeriveRunName(s.WorkflowName, nil, nil, s.ID),
			}
		}
		return out, nil
	}
	callerID, _ := identity.FromContext(ctx)
	return e.enricher.Enrich(ctx, summaries, callerID)
}
```

Add `"github.com/arbitron/arbitron/domain/identity"` to the import block if not already present (it is already imported in `engine.go`).

- [x] Step 4: Run the service test, confirm it passes

```bash
go test ./domain/engine/ -run TestEngine_ListEnriched
```

Expected:

```
ok  	github.com/arbitron/arbitron/domain/engine	0.00s
```

- [x] Step 5: Write the failing HTTP test for the enriched `List` response

Append to `adapter/http/instance_handler_test.go`:

```go
func TestInstanceHandler_List_EnrichedFields(t *testing.T) {
	h, instRepo, _, _, svc := newInstanceHandler()
	owner := uuid.New()
	nodeID := "draft-node"
	now := time.Now()
	inst := &instance.Instance{
		ID:            uuid.New(),
		OwnerID:       owner,
		WorkflowName:  "feature-pipeline",
		State:         instance.StateRunning,
		Priority:      instance.PriorityNormal,
		CurrentNodeID: &nodeID,
		Variables:     map[string]any{"title": "Refactor"},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	inst.WorkflowSnapshot = map[string]any{
		"name":  "feature-pipeline",
		"nodes": []any{map[string]any{"id": nodeID, "label": "Generate draft", "config": map[string]any{"backend": "claude-code"}}},
	}
	instRepo.instances[inst.ID] = inst

	svc.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: inst.ID, WorkflowName: "feature-pipeline", State: instance.StateRunning, Priority: instance.PriorityNormal, CurrentNodeID: &nodeID, CreatedAt: now, UpdatedAt: now},
			RunName:         "Refactor — feature-pipeline",
			Submitter:       instance.Submitter{Name: "alice", AccountIdentity: "alice@example.com"},
			FriendlyStep:    "Generate draft",
			Model:           "gpt-4o",
			Backend:         "claude-code",
			RetryCount:      2,
			QueuePosition:   0,
		}}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	row := resp[0]
	assert.Equal(t, "Refactor — feature-pipeline", row["runName"])
	assert.Equal(t, "Generate draft", row["friendlyStep"])
	assert.Equal(t, "gpt-4o", row["model"])
	assert.Equal(t, "claude-code", row["backend"])
	assert.Equal(t, float64(2), row["retryCount"])
	assert.Equal(t, "alice", row["submitterName"])
	assert.Equal(t, "alice@example.com", row["submitterIdentity"])
	assert.Equal(t, false, row["dismissed"], "dismissed decoration preserved")
}

func TestInstanceHandler_List_EnrichedProvenanceProbe(t *testing.T) {
	// Provenance probe: when the service returns NO enriched rows (empty),
	// the handler still returns 200 + []. When the service is unwired
	// (listEnrichedFn nil), the handler falls back to the plain service list
	// so the endpoint never breaks.
	h, _, _, _, svc := newInstanceHandler()
	svc.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return nil, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp)
}
```

- [x] Step 6: Run it, confirm it fails

```bash
go test ./adapter/http/ -run TestInstanceHandler_List_Enriched
```

Expected: build failure — `undefined: svc.listEnrichedFn`, and the JSON fields `runName`/`friendlyStep`/etc. are absent from the response (the handler still emits the lean `summaryResponse`).

- [x] Step 7: Extend `InstanceService` and `mockInstanceService` with `ListEnriched`

In `adapter/http/instance_handler.go`, add to the `InstanceService` interface (after `ListInstances`):

```go
	// ListEnriched returns the authorized page with additive enriched fields.
	// When the enricher is unwired, rows carry the run name only. Implementations
	// apply the same authz as ListInstances.
	ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
```

In `adapter/http/instance_handler_test.go`, add to `mockInstanceService`:

```go
	listEnrichedFn func(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
```

and the method:

```go
func (m *mockInstanceService) ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
	if m.listEnrichedFn != nil {
		return m.listEnrichedFn(ctx, filters)
	}
	// Fallback: wrap the plain list so tests that don't seed listEnrichedFn
	// still get a response (matching the engine's unwired-enricher behavior).
	summaries, err := m.ListInstances(ctx, filters)
	if err != nil {
		return nil, err
	}
	out := make([]instance.EnrichedInstanceSummary, len(summaries))
	for i, s := range summaries {
		out[i] = instance.EnrichedInstanceSummary{InstanceSummary: s}
	}
	return out, nil
}
```

- [x] Step 8: Add the `enrichedSummaryDTO` + mapper and switch the `List` handler

First, update the SQLite `ListInstances` query + mapper to populate the new `OwnerID` field on `InstanceSummary` (added in Phase 1 Task 1). In `adapter/sqlite/queries/instances.sql`, add `owner_id` to the `ListInstances` SELECT column list (line 19):

```sql
SELECT id, owner_id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at, department, visibility, trigger_source, schedule_id
FROM instances
```

Regenerate the sqlc code OR hand-edit `adapter/sqlite/generated/instances.sql.go` to add `OwnerID` to the `ListInstancesRow` struct and the Scan call. The generated tree is hand-curated (per `ref_backfill_repo.go`'s note about not blanket-regenerating); add the field to the `ListInstancesRow` struct and the `Scan` in `ListInstances`:

```go
type ListInstancesRow struct {
	ID            string
	OwnerID       string
	WorkflowName  string
	State         string
	Priority      string
	CurrentNodeID sql.NullString
	CreatedAt     string
	UpdatedAt     string
	CompletedAt   sql.NullString
	QueuedAt      sql.NullString
	Department    sql.NullString
	Visibility    string
	TriggerSource string
	ScheduleID    sql.NullString
}
```

and in the `ListInstances` method's `rows.Scan`, add `&i.OwnerID` as the second field (after `&i.ID`).

Then in `adapter/sqlite/instance_repo.go`, in the `List` method's summary construction (around line 188), add the `OwnerID` field:

```go
		ownerID, err := parseUUID(row.OwnerID)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		summaries[i] = instance.InstanceSummary{
			ID:            id,
			OwnerID:       ownerID,
			WorkflowName:  row.WorkflowName,
			...
		}
```

Also add `OwnerID` to the `summaryDTO` and `toSummaryDTO` in `adapter/http/instance_handler.go` so the owner id is exposed on the wire (the dashboard's enriched row does not need it directly, but the lean DTO should carry it for parity with the domain type). In `summaryDTO` (line 166), add after `ID`:

```go
type summaryDTO struct {
	ID            string  `json:"id"`
	OwnerID       string  `json:"ownerId"`
	WorkflowName  string  `json:"workflowName"`
	...
}
```

and in `toSummaryDTO` (line 182), add:

```go
		OwnerID:       s.OwnerID.String(),
```

Then, in `adapter/http/instance_handler.go`, add after the `summaryResponse` block (after line 224):

```go
// enrichedSummaryDTO is the JSON projection of instance.EnrichedInstanceSummary.
// It embeds summaryDTO so the lean fields flatten into the same JSON object;
// the enriched fields are additive. submitterName/submitterIdentity carry the
// resolved owner (the HTTP layer renders "you" vs the name from authMode in
// the dashboard, not here — the dashboard reads authMode from /me).
type enrichedSummaryDTO struct {
	summaryDTO
	RunName           string  `json:"runName"`
	FriendlyStep      string  `json:"friendlyStep,omitempty"`
	Model             string  `json:"model,omitempty"`
	Backend           string  `json:"backend,omitempty"`
	RetryCount        int     `json:"retryCount"`
	QueuePosition     int     `json:"queuePosition,omitempty"`
	SubmitterName     string  `json:"submitterName,omitempty"`
	SubmitterIdentity string  `json:"submitterIdentity,omitempty"`
	FailedNodeID      *string `json:"failedNodeId,omitempty"`
}

func toEnrichedSummaryDTO(e instance.EnrichedInstanceSummary) enrichedSummaryDTO {
	return enrichedSummaryDTO{
		summaryDTO:        toSummaryDTO(e.InstanceSummary),
		RunName:           e.RunName,
		FriendlyStep:      e.FriendlyStep,
		Model:             e.Model,
		Backend:           e.Backend,
		RetryCount:        e.RetryCount,
		QueuePosition:     e.QueuePosition,
		SubmitterName:     e.Submitter.Name,
		SubmitterIdentity: e.Submitter.AccountIdentity,
		FailedNodeID:      e.FailedNodeID,
	}
}

// enrichedResponse is the enriched list-item JSON: the enriched DTO plus the
// per-user "dismissed" flag, mirroring summaryResponse.
type enrichedResponse struct {
	enrichedSummaryDTO
	Dismissed bool `json:"dismissed"`
}

func toEnrichedResponse(e instance.EnrichedInstanceSummary, dismissed bool) enrichedResponse {
	return enrichedResponse{
		enrichedSummaryDTO: toEnrichedSummaryDTO(e),
		Dismissed:          dismissed,
	}
}
```

Now switch the `List` handler to use the enriched path. In `InstanceHandler.List`, replace the block that builds the response (the `summaries, err := h.service.ListInstances(...)` call through the `out` loop) with:

```go
	enriched, err := h.service.ListEnriched(r.Context(), filters)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteJSON(w, http.StatusOK, []instance.EnrichedInstanceSummary{})
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list instances")
		return
	}

	includeDismissed := false
	if v := r.URL.Query().Get("includeDismissed"); v != "" {
		if parsed, perr := strconv.ParseBool(v); perr == nil {
			includeDismissed = parsed
		}
	}

	user, uerr := h.currentUser.CurrentUser(r)
	if uerr != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	dismissedIDs, err := h.dismissals.DismissedIDs(r.Context(), user)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load dismissals")
		return
	}

	out := make([]enrichedResponse, 0, len(enriched))
	for _, e := range enriched {
		isDismissed := dismissedIDs[e.ID]
		if isDismissed && !includeDismissed {
			continue
		}
		out = append(out, toEnrichedResponse(e, isDismissed))
	}
	WriteJSON(w, http.StatusOK, out)
```

- [x] Step 9: Run the HTTP tests, confirm they pass

```bash
go test ./adapter/http/ -run TestInstanceHandler_List
```

Expected:

```
ok  	github.com/arbitron/arbitron/adapter/http	0.00s
```

- [x] Step 10: Confirm the whole module builds and all tests pass

```bash
go build ./... && go test ./...
```

Expected: no build errors; all package tests pass (existing handler tests that decode into `[]instance.InstanceSummary` still work because the enriched JSON is a superset — the extra fields are ignored by the lean struct).

- [ ] Step 11: Commit

```bash
git add adapter/http/instance_handler.go adapter/http/instance_handler_test.go domain/engine/engine.go domain/engine/enricher_list_test.go
git commit -m "feat(http): emit enriched instance list (run name, submitter, step, model, backend, retry, position)"
```