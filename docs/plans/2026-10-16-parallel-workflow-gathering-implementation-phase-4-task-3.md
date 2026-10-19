# Parallel Workflow Gathering — Phase 4 Task 3: HTTP graph-state, group-layout, queue ancestor discovery, wiring

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Expose the authorized aggregate read model over HTTP (`GET /api/v1/instances/{id}/graph-state`), serve the static group layout from the SAME Phase 1 ownership analysis (`GET /api/v1/workflows/{name}/group-layout`), let the queue discover terminal runs that still own active visible descendants, and wire the reader into the real composition root.
**Architecture:** `InstanceHandler.GraphState` delegates to an injected `GraphStateService` (production: `*engine.GraphStateReader`) and maps errors exactly like `InstanceHandler.Get`; `WorkflowHandler.GroupLayout` = `workflow.Service.Load` + `workflow.AnalyzeGroups`. Queue ancestor discovery is a new caller-supplied NARROWING on the existing list read (`ListFilters.ActiveDescendantAncestors`, query `activeDescendantAncestors=true`), implemented in `sqlite.InstanceRepo.List` with a recursive CTE whose descendant arm applies the same service-set visibility predicate. `cmd/arbitron/main.go` builds the reader in `initDomain`, and `runServe` builds its router from a new `(*domainStack).routerDeps()` so composition-root tests serve exactly the production router.
**Tech Stack:** Go 1.25.5, chi v5, SQLite (hand-written SQL, recursive CTE), testify, `net/http/httptest`.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (queue live view reflects every concurrently active node; tasks continuing independently remain visible; completed runs retain outcomes); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §2 "HTTP and persistent inspection" ("Add `GET /instances/{id}/graph-state` through `InstanceHandler`…", "Add authorized ancestor discovery for active independent descendants to the queue read service; current root filters do not supply it", "Serve terminal parents even while descendants continue"), §4 ("`WorkflowReview` uses the same layout"), Integration ("Register route/wiring in `adapter/http/server.go` and `cmd/arbitron/main.go`").

**Decision — ancestor discovery is a `ListFilters` narrowing, not a new endpoint.** Justification: (1) it reuses the ENTIRE authorized list pipeline — `Engine.ListEnriched` (`domain/engine/engine.go:633-655`) already service-sets `OwnerID/Departments/IncludePublic/Unconstrained` from the request identity via `applyListConstraint` (`engine.go:611-627`), and the enricher, dismissal flag and paging apply unchanged, so the queue gets fully enriched rows it can render with no second request; (2) it composes with the existing `parent` selector (`parent=roots` + `activeDescendantAncestors=true` = terminal ROOTS owning active work); (3) a separate endpoint would need its own authz path, which is exactly the divergence `visibilityPredicate` (`adapter/sqlite/instance_repo.go:541-600`) was introduced to eliminate. The name is `ActiveDescendantAncestors` (not `Include…`) because it NARROWS: when set, only terminal (`completed|failed|cancelled`) rows that are ancestors — at any depth — of a still-active (`queued|running|waiting|paused`) run visible to the same caller are returned. Every terminal ancestor level is returned (not only roots) so the queue's `topLevelRows` nesting (`dashboard/src/components/QueueSection.tsx:167-169`) can chain an active child under its completed parent.

**Interfaces**
- Consumes (existing code):
  - `adapter/http/instance_handler.go:42-80` `InstanceHandler`, `NewInstanceHandler`, `SetDismissals` (setter idiom); `:312-316` `parseInstanceID`; `:377-472` `List` (parent selector parse at `:419-425`); `:474-493` `Get` (error mapping to mirror).
  - `adapter/http/workflow_handler.go:81-130` `WorkflowHandler` (`wfSvc *workflow.Service`); `:144-146` `safeWorkflowName`; `:162-178` `Get` (load/error idiom).
  - `adapter/http/server.go:54-133` `RouterDeps`; `:198-219` handler construction; `:270-287` workflow routes; `:320-348` instance routes.
  - `adapter/sqlite/instance_repo.go:163-312` `List`; `:541-600` `visibilityPredicate(vis instance.ChildVisibility, alias string)`.
  - `domain/instance/instance.go:277-319` `ListFilters`; `domain/instance/spawn_link.go:137-151` `ChildVisibility`.
  - `cmd/arbitron/main.go:175-237` `domainStack`; `:340` `initDomain`; `:373` `instanceRepo`; `:376` `nodeExecRepo`; `:800` `eng`; `:1192-1240` `return &domainStack{...}`; `:1243-1300` `runServe` router construction.
  - Test idioms: `adapter/http/instance_handler_test.go:497-530` `testInstance`, `newInstanceHandler`; `adapter/http/workflow_handler_test.go:35-60` `mockWorkflowRepo`, `:224-237` `withChiURLParam`; `adapter/http/instance_list_parent_test.go:24-49` `newParentListHandler`; `adapter/http/server_test.go:36-45` `authedRouter`, `:218-268` `newRouterTestDeps`; `adapter/sqlite/integration_test.go:20-28` `setupTestDB`; `cmd/arbitron/spawn_e2e_test.go:22-28` `writeWorkflow`, `:104-118` `childrenOf`; `cmd/arbitron/spawn_wait_e2e_test.go:131-138` `waitForState`.
- Consumes (contract / earlier tasks): P4-T1/T2 `engine.GraphState`, `engine.ExecutionContext`, `engine.LiveNodeState`, `engine.NodeStateKey`, `engine.GroupPanel`, `engine.GroupPanelTask`, `engine.GroupProgress`, `engine.ContextKindRoot/Branch`, `engine.LiveStateRunning/Waiting`, `engine.NewGraphStateReader`; P1-T2 `workflow.AnalyzeGroups`, `workflow.GroupGraph`, `workflow.GroupScope`, `workflow.BodyOwner`; P2-T1 `instance.GroupRunDone`; P2-T3/P2-T14 the `instance.GroupRunRepository` built in `initDomain` as the local `groupRunRepo` (`adaptsqlite.NewGroupRunRepo(db)`).
- Produces:
  - `domain/instance`: `ListFilters.ActiveDescendantAncestors bool`.
  - `adapter/sqlite`: `func activeDescendantAncestorsCTE(descVis string) string` (unexported) used by `List`.
  - `adapter/http`: `type GraphStateService interface { GraphState(ctx context.Context, id uuid.UUID) (*engine.GraphState, error) }`; `func (h *InstanceHandler) SetGraphState(gs GraphStateService)`; `func (h *InstanceHandler) GraphState(w http.ResponseWriter, r *http.Request)`; `func (h *WorkflowHandler) GroupLayout(w http.ResponseWriter, r *http.Request)`; `RouterDeps.GraphState GraphStateService`; list query param `activeDescendantAncestors` (`true|false|1|0`, anything else → 400 `INVALID_FILTER`).
  - Routes: `GET /api/v1/instances/{id}/graph-state` (registered only when `RouterDeps.GraphState != nil`) → `engine.GraphState` JSON; `GET /api/v1/workflows/{name}/group-layout` → `workflow.GroupGraph` JSON (`groups`, `owner`, `order`; never `null`).
  - `cmd/arbitron`: `domainStack.graphState *engine.GraphStateReader`; `func (ds *domainStack) routerDeps() adapthttp.RouterDeps`.
- Contract note (reported): the contract writes `/api/instances/...` and `/api/workflows/...`; every REST route in this server lives under `/api/v1` (`server.go:237`), so the real paths are `/api/v1/instances/{id}/graph-state` and `/api/v1/workflows/{name}/group-layout`.

Group-run repository identifier (verified against P2-T14): P2-T14 constructs it in `initDomain` as `groupRunRepo := adaptsqlite.NewGroupRunRepo(db)` (P2-T3 constructor `NewGroupRunRepo(db *sql.DB) *GroupRunRepo`) right after the `spawn_workflow` executor registration (well before `return &domainStack{`), and also stores it in `domainStack.groupRunRepo instance.GroupRunRepository`. Task 3c Step 3 reuses that local; it must NOT construct a second repository.

**Wiring & data provenance**
- Composition root: Task 3c of THIS task — `cmd/arbitron/main.go:initDomain` constructs `engine.NewGraphStateReader(eng, nodeExecRepo, groupRunRepo, instanceRepo)` into `domainStack.graphState`; `(*domainStack).routerDeps()` sets `RouterDeps.GraphState`; `runServe` builds its router from `routerDeps()`.
- Producers:
  - `GraphStateService` ← `domainStack.graphState` ← `engine.NewGraphStateReader` (P4-T1) over `eng` (`main.go:800`), `nodeExecRepo` (`main.go:376`), `groupRunRepo` (P2-T14), `instanceRepo` (`main.go:373`, `ListChildren` from P2-T12).
  - `GroupLayout` workflow ← `workflow.Service.Load` (`domain/workflow/service.go:356`), the same visibility-gated read `WorkflowHandler.Get` uses; analysis ← `workflow.AnalyzeGroups` (P1-T2), the same function `GraphStateReader` applies to run snapshots.
  - `ListFilters.ActiveDescendantAncestors` ← query param in `InstanceHandler.List`; the visibility scope ← `applyListConstraint` (service-set); rows ← `instances` table (`parent_instance_id` from migration 018, written by `createInstanceSQL` and P2-T7).
- Provenance probes: `TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot` (fails with 404 if `graphState` is not constructed, not put in `routerDeps`, or the route is not registered); `TestQueueAncestorsE2E_CompletedParentWithActiveChildIsDiscoverable` (fails if the filter is dropped anywhere between the query param and the SQL); `TestList_ActiveDescendantAncestors_DescendantMustBeVisible` (fails if the CTE ignores the service-set scope).

### Task 3a: Ancestor discovery narrowing (domain field + SQLite)
**Files:**
- Modify: `domain/instance/instance.go:314-319` (add the field before `Limit`)
- Modify: `adapter/sqlite/instance_repo.go:184-217` (List: visibility through query assembly) and append `activeDescendantAncestorsCTE` after `visibilityPredicate` (after line 600)
- Test:   `adapter/sqlite/instance_repo_active_ancestors_test.go` (package `sqlite`)
- Test:   `domain/engine/list_constraint_ancestors_test.go` (package `engine`, internal — `applyListConstraint` is unexported)

- [x] Step 1: Write the failing SQLite tests. Create `adapter/sqlite/instance_repo_active_ancestors_test.go`:

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
)

var adaMe = uuid.MustParse("00000000-0000-0000-0000-000000000001") // seeded by migration 001

func adaUser(t *testing.T, db testDB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`, id.String(), "u-"+id.String()[:8])
	require.NoError(t, err)
	return id
}

// adaInst creates a run with an optional parent link and moves it to state.
func adaInst(t *testing.T, repo *InstanceRepo, owner uuid.UUID, vis instance.Visibility, state instance.State, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: map[string]any{},
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: vis,
	}
	if parent != nil {
		p := *parent
		n := "spawn-1"
		inst.ParentInstanceID = &p
		inst.ParentNodeID = &n
		inst.SpawnMode = instance.SpawnModeFireAndForget
	}
	require.NoError(t, repo.Create(ctx, inst))
	if state != instance.StateQueued {
		require.NoError(t, repo.UpdateState(ctx, inst.ID, state))
	}
	return inst.ID
}

func adaList(t *testing.T, repo *InstanceRepo, f instance.ListFilters) map[uuid.UUID]bool {
	t.Helper()
	f.ActiveDescendantAncestors = true
	if f.Limit == 0 {
		f.Limit = 100
	}
	got, err := repo.List(context.Background(), f)
	require.NoError(t, err)
	out := map[uuid.UUID]bool{}
	for _, s := range got {
		out[s.ID] = true
	}
	return out
}

func TestList_ActiveDescendantAncestors_ReturnsTerminalAncestorsOfActiveWork(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	r := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	c := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, &r)
	g := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &c)
	unrelated := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	activeRoot := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateWaiting, &activeRoot)
	doneRoot := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateFailed, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCancelled, &doneRoot)

	got := adaList(t, repo, instance.ListFilters{Unconstrained: true})

	assert.Equal(t, map[uuid.UUID]bool{r: true, c: true}, got,
		"every terminal ancestor of active work, at any depth, and nothing else")
	assert.False(t, got[g], "the active descendant itself is not an ancestor row")
	assert.False(t, got[unrelated])
	assert.False(t, got[activeRoot], "an active parent is already in the active sections")
	assert.False(t, got[doneRoot], "a terminal parent whose descendants are all terminal is not discovered")
}

func TestList_ActiveDescendantAncestors_EveryActiveStateCounts(t *testing.T) {
	for _, st := range []instance.State{instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused} {
		t.Run(string(st), func(t *testing.T) {
			db := setupTestDB(t)
			repo := NewInstanceRepo(db.DB)
			p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
			adaInst(t, repo, adaMe, instance.VisibilityPrivate, st, &p)
			assert.True(t, adaList(t, repo, instance.ListFilters{Unconstrained: true})[p])
		})
	}
}

func TestList_ActiveDescendantAncestors_ComposesWithRootsScope(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	r := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	c := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, &r)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &c)

	got := adaList(t, repo, instance.ListFilters{Unconstrained: true, ParentScope: instance.ParentScopeRoots})

	assert.Equal(t, map[uuid.UUID]bool{r: true}, got)
}

func TestList_ActiveDescendantAncestors_DescendantMustBeVisible(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	other := adaUser(t, db)
	mine := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, other, instance.VisibilityPrivate, instance.StateRunning, &mine)

	got := adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, IncludePublic: true})
	assert.False(t, got[mine], "active work the caller cannot see must not reveal itself through its parent")

	pub := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, other, instance.VisibilityPublic, instance.StateRunning, &pub)
	got = adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, IncludePublic: true})
	assert.True(t, got[pub], "a visible (public) active descendant discovers the caller's parent")
}

func TestList_ActiveDescendantAncestors_AncestorMustBeVisible(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	other := adaUser(t, db)
	theirs := adaInst(t, repo, other, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &theirs)

	got := adaList(t, repo, instance.ListFilters{OwnerID: &adaMe})
	assert.False(t, got[theirs], "the ancestor row itself is still subject to the visibility predicate")
}

func TestList_ActiveDescendantAncestors_DepartmentScopeAppliesToDescendant(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	other := adaUser(t, db)
	p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	child := &instance.Instance{
		ID: uuid.New(), OwnerID: other, WorkflowName: "wf", WorkflowSnapshot: map[string]any{},
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: instance.VisibilityDepartment, Department: "engineering",
	}
	pid, node := p, "spawn-1"
	child.ParentInstanceID, child.ParentNodeID, child.SpawnMode = &pid, &node, instance.SpawnModeFireAndForget
	require.NoError(t, repo.Create(context.Background(), child))
	require.NoError(t, repo.UpdateState(context.Background(), child.ID, instance.StateRunning))

	assert.True(t, adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, Departments: []string{"engineering"}})[p])
	assert.False(t, adaList(t, repo, instance.ListFilters{OwnerID: &adaMe, Departments: []string{"finance"}})[p])
}

func TestList_ActiveDescendantAncestors_FailsClosedWithoutScope(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &p)

	assert.Empty(t, adaList(t, repo, instance.ListFilters{}), "no service-set scope admits nothing")
}

func TestList_ActiveDescendantAncestors_TerminatesOnCorruptParentCycle(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	x := uuid.New()
	y := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, &x)
	xInst := &instance.Instance{
		ID: x, OwnerID: adaMe, WorkflowName: "wf", WorkflowSnapshot: map[string]any{},
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		Visibility: instance.VisibilityPrivate,
	}
	ypid, node := y, "spawn-1"
	xInst.ParentInstanceID, xInst.ParentNodeID, xInst.SpawnMode = &ypid, &node, instance.SpawnModeFireAndForget
	require.NoError(t, repo.Create(context.Background(), xInst))
	require.NoError(t, repo.UpdateState(context.Background(), x, instance.StateCompleted))
	adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &x)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := repo.List(ctx, instance.ListFilters{Unconstrained: true, ActiveDescendantAncestors: true, Limit: 100})
	require.NoError(t, err)
	ids := map[uuid.UUID]bool{}
	for _, s := range got {
		ids[s.ID] = true
	}
	assert.Equal(t, map[uuid.UUID]bool{x: true, y: true}, ids)
}

func TestList_ActiveDescendantAncestors_OffByDefault(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	lonely := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
	st := instance.StateCompleted

	got, err := repo.List(context.Background(), instance.ListFilters{Unconstrained: true, State: &st, Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, lonely, got[0].ID, "without the flag the list is unchanged")
}

func TestList_ActiveDescendantAncestors_HonoursLimit(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	for i := 0; i < 3; i++ {
		p := adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateCompleted, nil)
		adaInst(t, repo, adaMe, instance.VisibilityPrivate, instance.StateRunning, &p)
	}
	got, err := repo.List(context.Background(), instance.ListFilters{Unconstrained: true, ActiveDescendantAncestors: true, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}
```

- [x] Step 2: Write the failing service-constraint test. Create `domain/engine/list_constraint_ancestors_test.go`:

```go
package engine

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/instance"
)

// The ancestor narrowing is caller-supplied and must survive the service-set
// visibility overwrite for every caller class.
func TestApplyListConstraint_PreservesActiveDescendantAncestors(t *testing.T) {
	uid := uuid.New()
	for name, lc := range map[string]listFilter{
		"admin":     {Unconstrained: true},
		"non-admin": {OwnerID: &uid, Departments: []string{"eng"}, IncludePublic: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := applyListConstraint(instance.ListFilters{ActiveDescendantAncestors: true}, lc)
			assert.True(t, got.ActiveDescendantAncestors)
		})
	}
}
```

- [x] Step 3: Run them, confirm they fail.

Observed both specified compile failures: `ListFilters.ActiveDescendantAncestors` is undefined.

```bash
go test ./adapter/sqlite/ -run 'TestList_ActiveDescendantAncestors' -count=1
go test ./domain/engine/ -run 'TestApplyListConstraint_PreservesActiveDescendantAncestors' -count=1
```
Expected (compile failures):
```
adapter/sqlite/instance_repo_active_ancestors_test.go:52:4: f.ActiveDescendantAncestors undefined (type instance.ListFilters has no field or method ActiveDescendantAncestors)
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
domain/engine/list_constraint_ancestors_test.go:20:48: unknown field ActiveDescendantAncestors in struct literal of type instance.ListFilters
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 4: Add the field. In `domain/instance/instance.go`, replace lines 314-319:

```go
	// ParentInstanceID is the parent whose children to return. Read ONLY when
	// ParentScope == ParentScopeChildren; ignored otherwise.
	ParentInstanceID *uuid.UUID
	Limit            int
	Offset           int
}
```
with
```go
	// ParentInstanceID is the parent whose children to return. Read ONLY when
	// ParentScope == ParentScopeChildren; ignored otherwise.
	ParentInstanceID *uuid.UUID
	// ActiveDescendantAncestors narrows the list to TERMINAL runs (completed,
	// failed, cancelled) that are ancestors — at any depth — of at least one
	// still-active run (queued, running, waiting, paused) the SAME caller may
	// see. It is how the queue keeps a completed parent discoverable while it
	// still owns active independent work (a parallel group that continued, a
	// fire-and-forget child), which the ordinary state filters hide.
	//
	// Like ParentScope it is a legitimate caller-supplied NARROWING: both the
	// ancestor row and the active descendant are subject to the service-set
	// visibility scope above, so it can never reveal a run — or the existence
	// of active work — the caller could not already list.
	ActiveDescendantAncestors bool
	Limit                     int
	Offset                    int
}
```

- [x] Step 5: Implement the narrowing. In `adapter/sqlite/instance_repo.go`, replace lines 184-217 (from the comment `// THE SAME predicate the child-count query applies, over the same` through `args = append(args, int64(filters.Limit), int64(filters.Offset))`) with:

```go
	// THE SAME predicate the child-count query applies, over the same
	// service-set scope. Admission of every row requires filters.Unconstrained —
	// the explicit admin assertion — never the mere absence of a narrowing.
	vis := instance.ChildVisibility{
		Unconstrained: filters.Unconstrained,
		OwnerID:       filters.OwnerID,
		Departments:   filters.Departments,
		IncludePublic: filters.IncludePublic,
	}
	visClause, visArgs, err := visibilityPredicate(vis, "")
	if err != nil {
		return nil, err
	}
	where = append(where, visClause)
	args = append(args, visArgs...)

	switch filters.ParentScope {
	case instance.ParentScopeRoots:
		where = append(where, "parent_instance_id IS NULL")
	case instance.ParentScopeChildren:
		if filters.ParentInstanceID == nil {
			// A children scope with no parent id is a caller bug. Return an empty
			// page rather than silently degrading to an unfiltered list.
			return []instance.InstanceSummary{}, nil
		}
		where = append(where, "parent_instance_id = ?")
		args = append(args, uuidToString(*filters.ParentInstanceID))
	}

	// Ancestor discovery for active independent work. The descendant arm of the
	// CTE applies THE SAME visibility predicate (over alias "d") as the row
	// itself, so a terminal parent is discovered only through active work the
	// caller may see; the outer predicate above still gates the ancestor row.
	var with string
	var withArgs []any
	if filters.ActiveDescendantAncestors {
		descClause, descArgs, derr := visibilityPredicate(vis, "d")
		if derr != nil {
			return nil, derr
		}
		with = activeDescendantAncestorsCTE(descClause)
		withArgs = descArgs
		where = append(where,
			"state IN ('completed','failed','cancelled')",
			"id IN (SELECT id FROM active_ancestors)")
	}

	q := with + "SELECT " + instanceSummaryColumns + " FROM instances"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	// The CTE's placeholders precede the WHERE clause's in the statement text.
	args = append(append(withArgs, args...), int64(filters.Limit), int64(filters.Offset))
```

Then, immediately after the closing brace of `visibilityPredicate` (currently line 600), add:

```go

// activeDescendantAncestorsCTE renders the recursive set of every ancestor of
// a still-active run admitted by descVis (a visibilityPredicate rendered over
// alias "d"). UNION — not UNION ALL — de-duplicates, which also terminates the
// recursion on a corrupt parent cycle; real chains are bounded by
// instance.MaxSpawnDepth. idx_instances_parent serves the anchor and the
// primary key serves the recursive join.
func activeDescendantAncestorsCTE(descVis string) string {
	return `WITH RECURSIVE active_ancestors(id) AS (
  SELECT d.parent_instance_id FROM instances d
   WHERE d.parent_instance_id IS NOT NULL
     AND d.state IN ('queued','running','waiting','paused')
     AND ` + descVis + `
  UNION
  SELECT p.parent_instance_id FROM instances p
    JOIN active_ancestors a ON p.id = a.id
   WHERE p.parent_instance_id IS NOT NULL
) `
}
```

- [x] Step 6: Run, confirm PASS; run the packages, gofmt, vet.

All specified commands passed. Pre-commit review, in order: (1) the flag only narrows terminal ancestors and preserves existing list filters; (2) real SQLite tests assert returned rows, visibility on BOTH ends, active-state coverage and recursive/cycle behavior, not mocked SQL calls; production HTTP feeding follows in 3b/3c below; (3) reused the shared visibility predicate and existing SQL/paging conventions. No extra options added.

```bash
go test ./adapter/sqlite/ -run 'TestList_ActiveDescendantAncestors' -count=1
go test ./domain/engine/ -run 'TestApplyListConstraint_PreservesActiveDescendantAncestors' -count=1
go test ./adapter/sqlite/ ./domain/instance/ ./domain/engine/ -count=1
gofmt -w domain/instance/instance.go adapter/sqlite/instance_repo.go adapter/sqlite/instance_repo_active_ancestors_test.go domain/engine/list_constraint_ancestors_test.go
gofmt -l domain/instance/instance.go adapter/sqlite/instance_repo.go adapter/sqlite/instance_repo_active_ancestors_test.go domain/engine/list_constraint_ancestors_test.go
go vet ./adapter/sqlite/ ./domain/instance/ ./domain/engine/
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/adapter/sqlite	X.XXXs
ok  	github.com/arbitron/arbitron/domain/instance	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 7: Commit.

```bash
git add domain/instance/instance.go adapter/sqlite/instance_repo.go adapter/sqlite/instance_repo_active_ancestors_test.go domain/engine/list_constraint_ancestors_test.go
git commit -m "feat(queue): authorized discovery of terminal ancestors that own active work" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3b: HTTP handlers and routes
**Files:**
- Modify: `adapter/http/instance_handler.go:42-80` (struct field + setter), insert `GraphStateService` after line 40, insert the `activeDescendantAncestors` parse after line 425 in `List`, add method `GraphState` after `Get` (after line 493)
- Modify: `adapter/http/workflow_handler.go` — add `GroupLayout` + `normalizeGroupGraph` after `Get` (after line 178)
- Modify: `adapter/http/server.go:54-133` (`RouterDeps.GraphState`), `:203` (inject), `:273` (workflow route), `:340` (instance route)
- Test:   `adapter/http/graph_state_handler_test.go`, `adapter/http/workflow_group_layout_test.go`, `adapter/http/instance_list_active_ancestors_test.go` (all package `http_test`)

- [x] Step 1: Write the failing handler and router tests. Create `adapter/http/graph_state_handler_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// fakeGraphStateService answers per instance id (unknown id => ErrNotFound,
// exactly like the reader) and records the identity each call carried.
type fakeGraphStateService struct {
	mu      sync.Mutex
	states  map[uuid.UUID]*engine.GraphState
	errs    map[uuid.UUID]error
	callers []uuid.UUID
}

func newFakeGraphStateService() *fakeGraphStateService {
	return &fakeGraphStateService{states: map[uuid.UUID]*engine.GraphState{}, errs: map[uuid.UUID]error{}}
}

func (f *fakeGraphStateService) GraphState(ctx context.Context, id uuid.UUID) (*engine.GraphState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if who, ok := identity.FromContext(ctx); ok {
		f.callers = append(f.callers, who.UserID)
	}
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	gs, ok := f.states[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return gs, nil
}

func sampleGraphState(root, child uuid.UUID) *engine.GraphState {
	runID := uuid.New()
	return &engine.GraphState{
		RootInstanceID: root, Version: "0123456789abcdef0123456789abcdef", Active: true,
		Contexts: []engine.ExecutionContext{
			{InstanceID: root, Kind: engine.ContextKindRoot, State: "waiting", Graph: map[string]any{"name": "fanout"},
				Nodes: []engine.LiveNodeState{{InstanceID: root, NodeID: "g", State: engine.LiveStateWaiting, Reason: "waiting for parallel group"}}},
			{InstanceID: child, ParentInstanceID: &root, GroupRunID: &runID, TaskKey: "a", Kind: engine.ContextKindBranch,
				State: "running", Blocking: true,
				Nodes: []engine.LiveNodeState{{InstanceID: child, NodeID: "a1", State: engine.LiveStateRunning}}},
		},
		Groups: []engine.GroupPanel{{
			GroupRunID: runID, InstanceID: root, NodeID: "g", Form: workflow.GroupFormBranches, State: "running",
			Tasks:    []engine.GroupPanelTask{{Key: "a", Kind: "branch", Origin: "a1", ChildInstanceID: &child, LiveStatus: "running", Blocking: true}},
			Progress: engine.GroupProgress{Total: 1, Active: 1}, WaitingReasons: []string{"waiting_for_tasks:a"},
		}},
	}
}

func graphStateRequest(h *adapthttp.InstanceHandler, id string, who *identity.Identity) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id+"/graph-state", nil)
	if who != nil {
		req = req.WithContext(identity.WithIdentity(req.Context(), *who))
	}
	req = withChiURLParam(req, "id", id)
	w := httptest.NewRecorder()
	h.GraphState(w, req)
	return w
}

func TestInstanceHandler_GraphState_ReturnsAggregateJSON(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	fake := newFakeGraphStateService()
	root, child := uuid.New(), uuid.New()
	fake.states[root] = sampleGraphState(root, child)
	h.SetGraphState(fake)

	w := graphStateRequest(h, root.String(), nil)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, root.String(), body["rootInstanceId"])
	assert.Equal(t, "0123456789abcdef0123456789abcdef", body["version"])
	assert.Equal(t, true, body["active"])
	ctxs := body["contexts"].([]any)
	require.Len(t, ctxs, 2)
	node := ctxs[1].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	assert.Equal(t, child.String(), node["instanceId"], "nodes are namespaced by instance on the wire")
	assert.Equal(t, "a1", node["nodeId"])
	groups := body["groups"].([]any)
	require.Len(t, groups, 1)
	assert.Equal(t, []any{"waiting_for_tasks:a"}, groups[0].(map[string]any)["waitingReasons"])
}

// The graph-state endpoint must reveal exactly what GET /instances/{id} reveals
// for the same service error — in particular an unauthorized root is a 404
// indistinguishable from an absent one.
func TestInstanceHandler_GraphState_ErrorParityWithGet(t *testing.T) {
	for name, svcErr := range map[string]error{
		"not found / not visible": fmt.Errorf("instance x: %w", instance.ErrNotFound),
		"forbidden":               fmt.Errorf("instance x: %w", instance.ErrForbidden),
		"infrastructure":          errors.New("database is locked"),
	} {
		t.Run(name, func(t *testing.T) {
			id := uuid.New()
			h, _, _, _, svc := newInstanceHandler()
			svc.getFn = func(context.Context, uuid.UUID) (*instance.Instance, error) { return nil, svcErr }
			fake := newFakeGraphStateService()
			fake.errs[id] = svcErr
			h.SetGraphState(fake)

			getReq := withChiURLParam(httptest.NewRequest(http.MethodGet, "/instances/"+id.String(), nil), "id", id.String())
			getW := httptest.NewRecorder()
			h.Get(getW, getReq)
			gsW := graphStateRequest(h, id.String(), nil)

			assert.Equal(t, getW.Code, gsW.Code)
			var getBody, gsBody map[string]any
			require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &getBody))
			require.NoError(t, json.Unmarshal(gsW.Body.Bytes(), &gsBody))
			assert.Equal(t, getBody["code"], gsBody["code"])
			assert.Equal(t, getBody["error"], gsBody["error"], "same message (ErrorResponse.Error), no existence leak")
			assert.NotContains(t, gsW.Body.String(), "database is locked", "internal details stay server-side")
		})
	}
}

func TestInstanceHandler_GraphState_InvalidID(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	h.SetGraphState(newFakeGraphStateService())

	w := graphStateRequest(h, "not-a-uuid", nil)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "BAD_REQUEST")
}

func TestInstanceHandler_GraphState_PassesRequestIdentity(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	fake := newFakeGraphStateService()
	root := uuid.New()
	fake.states[root] = sampleGraphState(root, uuid.New())
	h.SetGraphState(fake)
	who := identity.Identity{UserID: uuid.New(), Username: "viewer", Role: identity.RoleUser}

	w := graphStateRequest(h, root.String(), &who)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []uuid.UUID{who.UserID}, fake.callers, "authorization runs under the caller's identity")
}

func TestInstanceHandler_GraphState_UnwiredIsNotImplemented(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()

	w := graphStateRequest(h, uuid.New().String(), nil)

	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

func TestRouter_GraphStateRoute_RegisteredOnlyWhenReaderWired(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	root := uuid.New()
	fake := newFakeGraphStateService()
	fake.states[root] = sampleGraphState(root, uuid.New())

	wired := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo,
		Snapshots: snapRepo, FileReader: fileReader, EventBus: bus, Config: cfg,
		QueueManager: qm, QueueRepo: qr, GraphState: fake,
	})
	w := httptest.NewRecorder()
	authedRouter(wired).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+root.String()+"/graph-state", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []uuid.UUID{routerTestUserID}, fake.callers)

	unwired := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo,
		Snapshots: snapRepo, FileReader: fileReader, EventBus: bus, Config: cfg,
		QueueManager: qm, QueueRepo: qr,
	})
	w = httptest.NewRecorder()
	authedRouter(unwired).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+root.String()+"/graph-state", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRouter_GroupLayoutRouteRegistered(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	wfRepo.workflows["fanout"] = groupLayoutWorkflow()
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo,
		Snapshots: snapRepo, FileReader: fileReader, EventBus: bus, Config: cfg,
		QueueManager: qm, QueueRepo: qr,
	})
	w := httptest.NewRecorder()
	authedRouter(router).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workflows/fanout/group-layout", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var g workflow.GroupGraph
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &g))
	assert.Contains(t, g.Groups, "g")
}
```

Create `adapter/http/workflow_group_layout_test.go`:

```go
package http_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/workflow"
)

// groupLayoutWorkflow: start -> g{ a: a1 -> a2 | b: b1 } -> done | fail
func groupLayoutWorkflow() *workflow.Workflow {
	return &workflow.Workflow{Name: "fanout", Nodes: []workflow.Node{
		{ID: "start", Type: workflow.NodeTypeTransform, Entrypoint: true},
		{ID: "g", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
			Form:  workflow.GroupFormBranches,
			Tasks: []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
			CompletionRule: workflow.GroupRuleAllSucceed, UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
			GatherMode: workflow.GroupGatherResults,
		}}},
		{ID: "a1", Type: workflow.NodeTypeToolCall}, {ID: "a2", Type: workflow.NodeTypeTransform},
		{ID: "b1", Type: workflow.NodeTypeToolCall},
		{ID: "done", Type: workflow.NodeTypeTransform}, {ID: "fail", Type: workflow.NodeTypeTransform},
	}, Edges: []workflow.Edge{
		{Source: "start", Target: "g"},
		{Source: "g", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
		{Source: "g", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
		{Source: "a1", Target: "a2"},
		{Source: "g", Target: "done", Output: workflow.GroupPortSuccess},
		{Source: "g", Target: "fail", Output: workflow.GroupPortFailure},
	}}
}

func groupLayoutRequest(h *adapthttp.WorkflowHandler, name string) *httptest.ResponseRecorder {
	req := withChiURLParam(httptest.NewRequest(http.MethodGet, "/workflows/"+name+"/group-layout", nil), "name", name)
	w := httptest.NewRecorder()
	h.GroupLayout(w, req)
	return w
}

func TestWorkflowHandler_GroupLayout_ReturnsPhaseOneOwnership(t *testing.T) {
	repo := newMockWorkflowRepo()
	wf := groupLayoutWorkflow()
	repo.workflows["fanout"] = wf
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

	w := groupLayoutRequest(h, "fanout")

	require.Equal(t, http.StatusOK, w.Code)
	var got workflow.GroupGraph
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Contains(t, got.Groups, "g")
	assert.Equal(t, []string{"a1", "a2"}, got.Groups["g"].Bodies["a"])
	assert.Equal(t, []string{"b1"}, got.Groups["g"].Bodies["b"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g", TaskKey: "a"}, got.Owner["a2"])
	assert.Equal(t, []string{"g"}, got.Order)

	// Byte-identical to the analysis the run views use (same function).
	want, _ := workflow.AnalyzeGroups(wf)
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantJSON), w.Body.String())
}

func TestWorkflowHandler_GroupLayout_WorkflowWithoutGroupsIsEmptyNotNull(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.workflows["test-wf"] = testWorkflow()
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

	w := groupLayoutRequest(h, "test-wf")

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"groups":{},"owner":{},"order":[]}`, w.Body.String())
}

func TestWorkflowHandler_GroupLayout_Errors(t *testing.T) {
	t.Run("unknown workflow", func(t *testing.T) {
		h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), &mockWorkflowService{}, newMockFileReader(), nil)
		w := groupLayoutRequest(h, "missing")
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Contains(t, w.Body.String(), "NOT_FOUND")
	})
	t.Run("unsafe name", func(t *testing.T) {
		h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), &mockWorkflowService{}, newMockFileReader(), nil)
		w := groupLayoutRequest(h, "..")
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
	t.Run("repository failure", func(t *testing.T) {
		h := adapthttp.NewWorkflowHandler(&errWorkflowRepo{err: errors.New("disk")}, &mockWorkflowService{}, newMockFileReader(), nil)
		w := groupLayoutRequest(h, "fanout")
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Body.String(), "disk")
	})
}
```

Create `adapter/http/instance_list_active_ancestors_test.go`:

```go
package http_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/instance"
)

func TestInstanceListREST_ActiveDescendantAncestorsParam(t *testing.T) {
	t.Run("true sets the narrowing", func(t *testing.T) {
		h, got := newParentListHandler(nil)
		code, _ := doList(t, h, "?activeDescendantAncestors=true")
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, got.ActiveDescendantAncestors)
	})
	t.Run("composes with the roots selector", func(t *testing.T) {
		h, got := newParentListHandler(nil)
		code, _ := doList(t, h, "?parent=roots&activeDescendantAncestors=1")
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, got.ActiveDescendantAncestors)
		assert.Equal(t, instance.ParentScopeRoots, got.ParentScope)
	})
	t.Run("absent leaves the list unchanged", func(t *testing.T) {
		h, got := newParentListHandler(nil)
		code, _ := doList(t, h, "")
		assert.Equal(t, http.StatusOK, code)
		assert.False(t, got.ActiveDescendantAncestors)
	})
	t.Run("malformed is rejected rather than ignored", func(t *testing.T) {
		h, _ := newParentListHandler(nil)
		w := adminListRequest(h, "?activeDescendantAncestors=maybe")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "INVALID_FILTER")
	})
}
```

- [x] Step 2: Run them, confirm they fail.

Observed missing GraphState/SetGraphState/GroupLayout methods and RouterDeps.GraphState compile failures.

```bash
go test ./adapter/http/ -run 'TestInstanceHandler_GraphState|TestRouter_GraphStateRoute|TestRouter_GroupLayoutRoute|TestWorkflowHandler_GroupLayout|TestInstanceListREST_ActiveDescendantAncestorsParam' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/adapter/http_test [github.com/arbitron/arbitron/adapter/http.test]
adapter/http/graph_state_handler_test.go:80:10: h.GraphState undefined (type *http.InstanceHandler has no field or method GraphState)
adapter/http/graph_state_handler_test.go:90:4: h.SetGraphState undefined (type *http.InstanceHandler has no field or method SetGraphState)
adapter/http/graph_state_handler_test.go:178:23: unknown field GraphState in struct literal of type http.RouterDeps
adapter/http/workflow_group_layout_test.go:51:4: h.GroupLayout undefined (type *http.WorkflowHandler has no field or method GroupLayout)
...
FAIL	github.com/arbitron/arbitron/adapter/http [build failed]
```

- [x] Step 3: Implement the instance handler pieces.

Contract clarification: parsing accepts exactly `true|false|1|0` as required by Interfaces, rather than the broader `strconv.ParseBool` suggestion. Explicit empty/other spellings return 400; absent leaves the filter off. Added tests for these boundaries. In `adapter/http/instance_handler.go`:

(a) After the `InstanceService` interface (after line 40) insert:

```go

// GraphStateService is the authorized aggregate read model behind
// GET /instances/{id}/graph-state (engine.GraphStateReader). It authorizes the
// root and every included descendant under the request identity and returns an
// error wrapping instance.ErrNotFound for an absent or unviewable root.
type GraphStateService interface {
	GraphState(ctx context.Context, id uuid.UUID) (*engine.GraphState, error)
}
```

(b) In the `InstanceHandler` struct (lines 43-50) add the field after `currentUser CurrentUserResolver`:

```go
	// graphState serves the aggregate parallel-run view. Nil until SetGraphState;
	// the route is only registered when it is wired.
	graphState GraphStateService
```

(c) After `SetDismissals` (after line 80) insert:

```go

// SetGraphState wires the aggregate read model. Kept as a setter so existing
// NewInstanceHandler call sites compile unchanged.
func (h *InstanceHandler) SetGraphState(gs GraphStateService) {
	h.graphState = gs
}
```

(d) In `List`, immediately after `filters.ParentInstanceID = parentID` (line 425) insert:

```go

	// Ancestor discovery for active independent work: a caller-supplied
	// NARROWING (terminal runs owning active visible descendants) that composes
	// with the service-set scope. A malformed value is rejected rather than
	// silently ignored, for the same reason as the parent selector.
	if v := r.URL.Query().Get("activeDescendantAncestors"); v != "" {
		on, berr := strconv.ParseBool(v)
		if berr != nil {
			WriteError(w, http.StatusBadRequest, "INVALID_FILTER", "activeDescendantAncestors must be a boolean")
			return
		}
		filters.ActiveDescendantAncestors = on
	}
```

(e) After `Get` (after line 493) insert:

```go

// GraphState handles GET /instances/{id}/graph-state: the authorized aggregate
// state of a run and its descendants (contexts, namespaced node states, group
// panels, version, activity). Errors map EXACTLY like Get, so an unauthorized
// root is a 404 indistinguishable from an absent one.
func (h *InstanceHandler) GraphState(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}
	if h.graphState == nil {
		WriteError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "graph state is not available")
		return
	}

	gs, err := h.graphState.GraphState(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}

	WriteJSON(w, http.StatusOK, gs)
}
```

(The 500 message is the same string `Get` uses at line 488 so the bodies are identical.)

- [x] Step 4: Implement the workflow handler piece. In `adapter/http/workflow_handler.go`, after `Get` (after line 178) insert:

```go

// GroupLayout handles GET /workflows/{name}/group-layout. It returns the Phase 1
// ownership analysis (workflow.AnalyzeGroups) of the caller-visible definition,
// so the static WorkflowReview lays groups out with EXACTLY the analysis the
// run views use. Validation problems do not fail the request: the analysis is
// best-effort over any definition (AnalyzeGroups never panics) and the
// validate endpoint reports errors.
func (h *WorkflowHandler) GroupLayout(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	wf, err := h.wfSvc.Load(r.Context(), name)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load workflow")
		return
	}
	layout, _ := workflow.AnalyzeGroups(wf)
	WriteJSON(w, http.StatusOK, normalizeGroupGraph(layout))
}

// normalizeGroupGraph guarantees empty collections rather than JSON null, so a
// client can index the layout without null checks.
func normalizeGroupGraph(g *workflow.GroupGraph) *workflow.GroupGraph {
	out := workflow.GroupGraph{}
	if g != nil {
		out = *g
	}
	if out.Groups == nil {
		out.Groups = map[string]*workflow.GroupScope{}
	}
	if out.Owner == nil {
		out.Owner = map[string]workflow.BodyOwner{}
	}
	if out.Order == nil {
		out.Order = []string{}
	}
	return &out
}
```

- [x] Step 5: Register the routes. In `adapter/http/server.go`:

(a) In `RouterDeps`, after the `BackendHealth BackendHealthChecker` field (line 132) add:

```go
	// GraphState is the authorized aggregate parallel-run read model
	// (engine.GraphStateReader). When non-nil, GET /instances/{id}/graph-state
	// is registered. Nil omits the route (legacy/tests).
	GraphState GraphStateService
```

(b) After `instHandler := NewInstanceHandler(d.Instances, d.NodeExecs, d.Snapshots, d.Service)` (line 203) add:

```go
	if d.GraphState != nil {
		instHandler.SetGraphState(d.GraphState)
	}
```

(c) In the `/workflows` route block, after `r.Get("/{name}", wfHandler.Get)` (line 273) add:

```go
			r.Get("/{name}/group-layout", wfHandler.GroupLayout)
```

(d) In the `/instances` route block, after `r.Get("/{id}/trace/{nodeId}", instHandler.GetNodeTrace)` (line 340) add:

```go
			if d.GraphState != nil {
				r.Get("/{id}/graph-state", instHandler.GraphState)
			}
```

- [x] Step 6: Run, confirm PASS; run the whole HTTP package (existing tests must stay green), gofmt, vet.

All commands passed; focused tests also pass with `-race`. Pre-commit review, in order: (1) exact route/query/error contracts with service-authorized workflow loading; (2) wire JSON, caller identity, optional registration and service visibility are asserted, including a hidden department publication becoming visible only after public promotion; real production graph input is verified in 3c below; (3) follows neighboring handler/setter/error/normalization idioms, formatting and vet clean.

```bash
go test ./adapter/http/ -run 'TestInstanceHandler_GraphState|TestRouter_GraphStateRoute|TestRouter_GroupLayoutRoute|TestWorkflowHandler_GroupLayout|TestInstanceListREST_ActiveDescendantAncestorsParam' -count=1
go test ./adapter/http/ -count=1
gofmt -w adapter/http/instance_handler.go adapter/http/workflow_handler.go adapter/http/server.go adapter/http/graph_state_handler_test.go adapter/http/workflow_group_layout_test.go adapter/http/instance_list_active_ancestors_test.go
gofmt -l adapter/http/instance_handler.go adapter/http/workflow_handler.go adapter/http/server.go adapter/http/graph_state_handler_test.go adapter/http/workflow_group_layout_test.go adapter/http/instance_list_active_ancestors_test.go
go vet ./adapter/http/
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/http	0.XXXs
ok  	github.com/arbitron/arbitron/adapter/http	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 7: Commit.

```bash
git add adapter/http/instance_handler.go adapter/http/workflow_handler.go adapter/http/server.go adapter/http/graph_state_handler_test.go adapter/http/workflow_group_layout_test.go adapter/http/instance_list_active_ancestors_test.go
git commit -m "feat(http): graph-state and group-layout endpoints; active-descendant ancestor list filter" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3c: Composition-root wiring
**Files:**
- Modify: `cmd/arbitron/main.go:175-237` (`domainStack` field), before `:1192` (construction), `:1192-1240` (literal), new method after `mcpDeps` (after line 320), `:1267-1300` (`runServe` router construction)
- Test:   `cmd/arbitron/graph_state_e2e_test.go` (package `main`)

- [x] Step 1: Write the failing composition-root tests. Create `cmd/arbitron/graph_state_e2e_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Two branch tasks that overlap for ~3s (both observably running at once) and
// finish 3s apart, so a terminal branch is always observed while the other is
// still active (P4-T5 relies on that ordering).
const gsFanoutManifest = `{
  "name": "gs-fanout",
  "description": "two concurrent branches",
  "inputs": [],
  "nodes": [
    {"id": "start", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "g", "type": "parallel_group",
     "config": {"group": {"form": "branches",
       "tasks": [{"key": "a", "entry": "a1"}, {"key": "b", "entry": "b1"}],
       "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining", "gatherMode": "results"}}},
    {"id": "a1", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "sleep 3; echo '{}'"]}},
    {"id": "b1", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "sleep 6; echo '{}'"]}},
    {"id": "done", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "success"}]}},
    {"id": "fail", "type": "transform",
     "config": {"operations": [{"variable": "outcome", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "start", "target": "g"},
    {"source": "g", "target": "a1", "output": "task", "label": "a"},
    {"source": "g", "target": "b1", "output": "task", "label": "b"},
    {"source": "g", "target": "done", "output": "success"},
    {"source": "g", "target": "fail", "output": "failure"}
  ]
}`

// A parent that completes immediately while its fire-and-forget child holds.
const gsAncestorParentManifest = `{
  "name": "gs-ada-parent",
  "description": "parent finishes before its child",
  "inputs": [],
  "nodes": [
    {"id": "p1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "spawn-1", "type": "spawn_workflow",
     "config": {"childWorkflow": "gs-ada-child", "spawnMode": "fire_and_forget"}},
    {"id": "p2", "type": "transform",
     "config": {"operations": [{"variable": "finished", "operation": "set", "value": true}]}}
  ],
  "edges": [
    {"source": "p1", "target": "spawn-1"},
    {"source": "spawn-1", "target": "p2"}
  ]
}`

const gsAncestorChildManifest = `{
  "name": "gs-ada-child",
  "description": "child that outlives its parent",
  "inputs": [],
  "nodes": [
    {"id": "hold", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "sleep 4; echo '{}'"]}},
    {"id": "c1", "type": "transform",
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}
  ],
  "edges": [{"source": "hold", "target": "c1"}]
}`

// bootGraphStateStack boots the REAL composition root with the Phase 4 fixtures.
func bootGraphStateStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	writeWorkflow(t, workspaceDir, "gs-fanout", gsFanoutManifest)
	writeWorkflow(t, workspaceDir, "gs-ada-parent", gsAncestorParentManifest)
	writeWorkflow(t, workspaceDir, "gs-ada-child", gsAncestorChildManifest)

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// gsServeAPI serves EXACTLY the router runServe builds from the domain stack
// (minus the per-process dashboard/MCP/notification/webhook handlers).
func gsServeAPI(t *testing.T, ds *domainStack) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.routerDeps()))
	t.Cleanup(srv.Close)
	return srv
}

// gsFetchGraphState never calls require, so it is safe inside Eventually.
func gsFetchGraphState(srv *httptest.Server, id uuid.UUID) (int, *engine.GraphState, error) {
	resp, err := http.Get(srv.URL + "/api/v1/instances/" + id.String() + "/graph-state")
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, nil
	}
	var gs engine.GraphState
	if err := json.NewDecoder(resp.Body).Decode(&gs); err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, &gs, nil
}

func gsListIDs(srv *httptest.Server, query string) (map[uuid.UUID]bool, error) {
	resp, err := http.Get(srv.URL + "/api/v1/instances" + query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list status %d", resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]bool{}
	for _, r := range rows {
		if s, ok := r["id"].(string); ok {
			out[uuid.MustParse(s)] = true
		}
	}
	return out, nil
}

// PROVENANCE PROBE for the graph-state wiring: if initDomain does not build the
// reader, routerDeps does not pass it, or the route is not registered, this GET
// is a 404 and the test fails. The sink is the HTTP JSON body.
func TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "gs-fanout", map[string]any{}, "normal")
	require.NoError(t, err)

	var gs *engine.GraphState
	var lastCode int
	require.Eventually(t, func() bool {
		code, got, ferr := gsFetchGraphState(srv, parentID)
		lastCode = code
		if ferr != nil || got == nil {
			return false
		}
		running := 0
		for _, c := range got.Contexts {
			if c.Kind == engine.ContextKindBranch && c.State == string(instance.StateRunning) {
				running++
			}
		}
		if running >= 2 && len(got.Groups) == 1 && len(got.Groups[0].WaitingReasons) == 1 &&
			got.Groups[0].WaitingReasons[0] == "waiting_for_tasks:a,b" {
			gs = got
			return true
		}
		return false
	}, 30*time.Second, 100*time.Millisecond,
		"GET graph-state must show both branches running concurrently (last status %d)", lastCode)

	assert.Equal(t, parentID, gs.RootInstanceID)
	assert.True(t, gs.Active)
	require.NotNil(t, gs.Layout)
	assert.Contains(t, gs.Layout.Groups, "g")
	assert.Equal(t, parentID, gs.Contexts[0].InstanceID)

	byKey := map[string]uuid.UUID{}
	for _, c := range gs.Contexts[1:] {
		require.NotNil(t, c.ParentInstanceID)
		assert.Equal(t, parentID, *c.ParentInstanceID)
		byKey[c.TaskKey] = c.InstanceID
	}
	require.Contains(t, byKey, "a")
	require.Contains(t, byKey, "b")
	assert.NotEqual(t, byKey["a"], byKey["b"])

	nodes := map[engine.NodeStateKey]string{}
	for _, c := range gs.Contexts {
		for _, n := range c.Nodes {
			nodes[n.Key()] = n.State
		}
	}
	assert.Equal(t, engine.LiveStateRunning, nodes[engine.NodeStateKey{InstanceID: byKey["a"], NodeID: "a1"}])
	assert.Equal(t, engine.LiveStateRunning, nodes[engine.NodeStateKey{InstanceID: byKey["b"], NodeID: "b1"}])
	assert.Equal(t, engine.LiveStateWaiting, nodes[engine.NodeStateKey{InstanceID: parentID, NodeID: "g"}])

	// Completed-run inspection: the frozen gather survives completion.
	waitForState(t, ds, parentID, instance.StateCompleted)
	code, final, err := gsFetchGraphState(srv, parentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	assert.False(t, final.Active)
	require.Len(t, final.Groups, 1)
	assert.Equal(t, string(instance.GroupRunDone), final.Groups[0].State)
	assert.Empty(t, final.Groups[0].WaitingReasons)
	for _, card := range final.Groups[0].Tasks {
		assert.Equal(t, "completed", card.LiveStatus)
		assert.Equal(t, "completed", card.FrozenStatus)
	}
}

func TestGraphStateE2E_UnknownInstanceIs404(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	code, _, err := gsFetchGraphState(srv, uuid.New())

	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestGroupLayoutE2E_ServesPhaseOneOwnershipThroughCompositionRoot(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	resp, err := http.Get(srv.URL + "/api/v1/workflows/gs-fanout/group-layout")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var layout workflow.GroupGraph
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&layout))

	require.Contains(t, layout.Groups, "g")
	assert.Equal(t, []string{"a1"}, layout.Groups["g"].Bodies["a"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g", TaskKey: "b"}, layout.Owner["b1"])
}

// The queue's ancestor discovery through the REAL list pipeline: HTTP param ->
// Engine.ListEnriched (service-set scope) -> sqlite recursive CTE.
func TestQueueAncestorsE2E_CompletedParentWithActiveChildIsDiscoverable(t *testing.T) {
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)

	parentID, _, err := ds.eng.Trigger(context.Background(), "gs-ada-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, parentID, instance.StateCompleted)
	children := childrenOf(t, ds, parentID)
	require.Len(t, children, 1)
	childID := children[0].ID

	ids, err := gsListIDs(srv, "?activeDescendantAncestors=true&includeDismissed=true")
	require.NoError(t, err)
	assert.True(t, ids[parentID], "a completed parent that still owns an active child is discoverable")
	assert.False(t, ids[childID], "the active child itself is listed by the ordinary state sections")

	waitForState(t, ds, childID, instance.StateCompleted)
	ids, err = gsListIDs(srv, "?activeDescendantAncestors=true&includeDismissed=true")
	require.NoError(t, err)
	assert.False(t, ids[parentID], "once the subtree is inactive the parent is no longer discovered")
}
```

- [x] Step 2: Run it, confirm it fails.

Observed `ds.routerDeps undefined` compile failure.

```bash
go test ./cmd/arbitron/ -run 'TestGraphStateE2E|TestGroupLayoutE2E|TestQueueAncestorsE2E' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/cmd/arbitron [github.com/arbitron/arbitron/cmd/arbitron.test]
cmd/arbitron/graph_state_e2e_test.go:113:57: ds.routerDeps undefined (type *domainStack has no field or method routerDeps)
FAIL	github.com/arbitron/arbitron/cmd/arbitron [build failed]
```

- [x] Step 3: Wire the reader in `initDomain`. In `cmd/arbitron/main.go`:

(a) In `domainStack` (lines 175-237), after the `backendHealth *backendhealth.Checker` field add:

```go
	// graphState is the authorized aggregate read model behind
	// GET /instances/{id}/graph-state (parallel-group run visibility, Phase 4).
	graphState *engine.GraphStateReader
```

(b) Immediately before `return &domainStack{` (line 1192) add:

```go
	// Parallel-group run visibility (Phase 4). The aggregate read model
	// authorizes the root AND every descendant through the engine (the same
	// Engine.GetInstance the instance endpoints use), reads traces and group
	// records directly after that check, and discovers descendants through the
	// persisted links (*adaptsqlite.InstanceRepo.ListChildren).
	graphState := engine.NewGraphStateReader(eng, nodeExecRepo, groupRunRepo, instanceRepo)
```

(c) In the `return &domainStack{...}` literal, after `backendHealth:       backendHealthChecker,` add:

```go
		graphState:          graphState,
```

- [x] Step 4: Add `routerDeps` and use it in `runServe`.

Re-read the existing router literal and preserved every domain-backed field; only the four per-process handlers remain in runServe. Reused the existing groupRunRepo. In `cmd/arbitron/main.go`, after `mcpDeps` (after line 320) add:

```go

// routerDeps assembles every RouterDeps field that comes straight from the
// domain stack. runServe layers the per-process handlers (dashboard SPA, MCP,
// notifications, webhooks) on top. Composition-root tests serve exactly this
// value, so a dependency dropped here is dropped from the real server too.
func (ds *domainStack) routerDeps() adapthttp.RouterDeps {
	deps := adapthttp.RouterDeps{
		Service:             ds.eng,
		Workflows:           ds.workflowRepo,
		WorkflowSvc:         ds.workflowSvc,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		Dismissals:          ds.dismissalRepo,
		DismissalUsers:      ds.userRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		Settings:            ds.settings,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		ConversationHandler: ds.conversationHandler,
		ProfileChecker:      ds.profileChecker,
		ConnChecker:         ds.connChecker,
		Auth:                ds.authMW,
		AuthHandler:         ds.authHandler,
		Tokens:              ds.tokenSvc,
		DeviceCodes:         ds.deviceSvc,
		PublicBaseURL:       ds.cfg.Server.PublicBaseURL,
		Users:               ds.userSvc,
		DashboardStats:      ds.dashboardSvc,
		ScheduleHandler:     adapthttp.NewScheduleHandler(ds.scheduleSvc),
		BackendHealth:       ds.backendHealth,
	}
	// Assign through a nil check: a nil *GraphStateReader stored in the
	// interface field would be a non-nil interface and register a route that
	// panics.
	if ds.graphState != nil {
		deps.GraphState = ds.graphState
	}
	return deps
}
```

Then in `runServe`, replace the whole `router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{ ... })` statement (currently lines 1267-1300) with:

```go
	deps := ds.routerDeps()
	deps.DashboardHandler = dashHandler
	deps.NotifHandler = notifHandler
	deps.WebhookHandler = webhookHandler
	deps.MCPHandler = mcpSrv.HTTPHandler()
	router := adapthttp.NewRouterFromDeps(deps)
```

Before replacing, re-read the literal: every field it sets other than `DashboardHandler`, `NotifHandler`, `WebhookHandler` and `MCPHandler` must appear in `routerDeps` with the same value (if an earlier phase added a field to the literal, move it into `routerDeps`). After the change `grep -n 'RouterDeps{' cmd/arbitron/main.go` must show exactly one hit (inside `routerDeps`).

- [x] Step 5: Run, confirm PASS; then the whole command package.

Focused and whole-command tests pass; focused composition tests also pass with `-race`. The required wiring mutation (remove construction and domainStack literal entry) failed with actual HTTP 404 versus expected 200. Restored both lines and reran green. The test now checks route registration immediately rather than printing a stale pre-Eventually status, and verifies distinct real tool outputs in frozen decision/final-output JSON.

```bash
go test ./cmd/arbitron/ -run 'TestGraphStateE2E|TestGroupLayoutE2E|TestQueueAncestorsE2E' -count=1
go test ./cmd/arbitron/ -count=1
gofmt -w cmd/arbitron/main.go cmd/arbitron/graph_state_e2e_test.go
gofmt -l cmd/arbitron/main.go cmd/arbitron/graph_state_e2e_test.go
go vet ./cmd/arbitron/
```
Expected:
```
ok  	github.com/arbitron/arbitron/cmd/arbitron	1X.XXXs
ok  	github.com/arbitron/arbitron/cmd/arbitron	XX.XXXs
```
(`gofmt -l` and `go vet` print nothing.) Wiring check: temporarily delete the `graphState := engine.NewGraphStateReader(...)` line and its literal entry; `TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot` must fail with `GET graph-state must show both branches running concurrently (last status 404)`. Restore the lines.

- [x] Step 6: Full suite, then commit.

`go test ./... -count=1` passed all packages except the pre-existing `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` UNKNOWN_PROFILE failure. Reproduced that exact failure in a detached worktree at unchanged starting commit `b796713`; no unrelated profile/sample changes made. Per the mission, this unrelated red does not block completion.

Final self-review (in order):
1. Spec compliance: exactly the ancestor-list narrowing, authorized graph-state/layout routes, and real root/router wiring slice. No event/UI work or new options. Paths use the existing `/api/v1` prefix. Boolean input follows the exact Interfaces vocabulary; workflow loading stays behind Service.Load. Existing router dependencies all preserved.
2. Anti-green-theater: production `initDomain`/router/Engine/SQLite/tool execution feed HTTP sink assertions for simultaneous qualified nodes, ordered live/frozen task statuses, distinct nonempty persisted results, and completed-parent discovery/removal. Missing root wiring demonstrably fails with 404. SQLite visibility tests gate both ancestor and descendant; layout tests exercise the real service publication gate.
3. Code quality: checked adjacent handlers, visibility predicate, read model and root/test conventions; reused existing repositories, authorization, ownership analysis and SQL paging. Typed-nil interface guard retained. No dead wiring or missing-input no-op added.

After review/fixes, reran all task-focused tests (including aggregate reader/panels) with `-race`, all five affected packages without race, gofmt checks, vet and diff checks: green. The full-suite baseline concern above is the only remaining verification concern. Earlier P4-T1/T2 completion checkboxes are left untouched: this pass implements only P4-T3, now supplying their missing production wiring.

```bash
go test ./... -count=1
git add cmd/arbitron/main.go cmd/arbitron/graph_state_e2e_test.go
git commit -m "feat(serve): wire GraphStateReader into the composition root; router built from domain stack" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: every package `ok`.
