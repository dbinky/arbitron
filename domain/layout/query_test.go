package layout_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
)

var (
	queryRev1  = "0199eb00-0000-7000-8000-000000000001"
	queryRev2  = "0199eb00-0000-7000-8000-000000000002"
	queryOwner = uuid.MustParse("00000000-0000-0000-0000-00000000a001")
	queryNow   = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
)

// --- collaborators -----------------------------------------------------------

// Reuse layouttest.NewFakeJournal (P2T5) with the complete §6.2 receipt,
// quarantine and relocation methods. Never create a second obsolete 3-method fake.
// queryMutationGate implements workflow.MutationCoordinator with a mutex,
// asserts exact Scope/Name targets, adds a private context marker, and passes that
// marked callback context to fn. The before hook gates concurrent saves.
// FakeWorkflowAccess verifies the marked context on the locked second read.
// A nil/empty target or a dropped callback must fail the retry sink assertions.

type queryLockContext struct{}

type queryMutationGate struct {
	mu              sync.Mutex
	t               *testing.T
	access          *fakeWorkflowAccess
	before          func()
	workflowTargets [][]workflow.MutationTarget
	// historical records instance-retry lock sets, which never consult access.
	historical [][]workflow.MutationTarget
}

func (g *queryMutationGate) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	if g.before != nil {
		g.before()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	require.NotEmpty(g.t, targets, "retry must lock its destinations")
	g.access.mu.Lock()
	if !g.access.targetsRead {
		g.access.mu.Unlock()
		g.historical = append(g.historical, slices.Clone(targets))
		return fn(context.WithValue(ctx, queryLockContext{}, targets[0]))
	}
	g.access.targetsRead = false
	g.workflowTargets = append(g.workflowTargets, slices.Clone(targets))
	name := g.access.retryCalls[len(g.access.retryCalls)-1]
	entry := g.access.writable[name]
	want := g.access.lockTargetsLocked(name)
	g.access.expectedLock = &targets[0]
	g.access.mu.Unlock()
	defer func() {
		g.access.mu.Lock()
		g.access.expectedLock = nil
		g.access.mu.Unlock()
	}()
	require.Equal(g.t, want, targets, "retry must lock the whole resolution set the access seam reports")
	require.Contains(g.t, targets, workflow.MutationTarget{Scope: entry.scope, Name: name}, "the resolved destination must be locked")
	return fn(context.WithValue(ctx, queryLockContext{}, targets[0]))
}

type queryAccessEntry struct {
	wf    *workflow.Workflow
	scope workflow.Scope
}

// fakeWorkflowAccess resolves strictly by the requested name and records every
// call, so a QueryService that asks for the wrong name (or uses the read seam
// to authorize a retry) is caught.
type fakeWorkflowAccess struct {
	mu           sync.Mutex
	readable     map[string]queryAccessEntry
	writable     map[string]queryAccessEntry
	retryErr     map[string]error
	lockTargets  map[string][]workflow.MutationTarget
	loadCalls    []string
	retryCalls   []string
	expectedLock *workflow.MutationTarget
	targetsRead  bool
}

func (f *fakeWorkflowAccess) LoadForLayout(ctx context.Context, name string) (*workflow.Workflow, workflow.Scope, error) {
	if err := ctx.Err(); err != nil {
		return nil, workflow.Scope{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadCalls = append(f.loadCalls, name)
	e, ok := f.readable[name]
	if !ok {
		return nil, workflow.Scope{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return e.wf, e.scope, nil
}

func (f *fakeWorkflowAccess) AuthorizeLayoutRetry(ctx context.Context, name string) (*workflow.Workflow, workflow.Scope, error) {
	if err := ctx.Err(); err != nil {
		return nil, workflow.Scope{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retryCalls = append(f.retryCalls, name)
	if f.expectedLock != nil {
		if ctx.Value(queryLockContext{}) != *f.expectedLock {
			return nil, workflow.Scope{}, errors.New("locked authorization lost coordinator context")
		}
	}
	if err := f.retryErr[name]; err != nil {
		return nil, workflow.Scope{}, err
	}
	e, ok := f.writable[name]
	if !ok {
		return nil, workflow.Scope{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return e.wf, e.scope, nil
}

func (f *fakeWorkflowAccess) lockTargetsLocked(name string) []workflow.MutationTarget {
	return slices.Clone(f.lockTargets[name])
}

func (f *fakeWorkflowAccess) LayoutRetryTargets(ctx context.Context, name string) ([]workflow.MutationTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targetsRead = true
	return f.lockTargetsLocked(name), nil
}

func (f *fakeWorkflowAccess) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.loadCalls) + len(f.retryCalls)
}

// fakeInstanceAccess resolves strictly by instance id.
type fakeInstanceAccess struct {
	mu        sync.Mutex
	viewable  map[uuid.UUID]*workflow.Workflow
	retryable map[uuid.UUID]*workflow.Workflow
	retryErr  map[uuid.UUID]error
}

func (f *fakeInstanceAccess) InstanceLayoutSnapshot(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	wf, ok := f.viewable[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return wf, nil
}

func (f *fakeInstanceAccess) AuthorizeInstanceLayoutRetry(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.retryErr[id]; err != nil {
		return nil, err
	}
	wf, ok := f.retryable[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return wf, nil
}

var (
	_ layout.WorkflowAccess = (*fakeWorkflowAccess)(nil)
	_ layout.InstanceAccess = (*fakeInstanceAccess)(nil)
)

// --- harness -----------------------------------------------------------------

type queryHarness struct {
	store     *layouttest.MemStore
	workflows *fakeWorkflowAccess
	instances *fakeInstanceAccess
	q         *layout.QueryService
}

func newQueryHarness(t *testing.T) *queryHarness {
	t.Helper()
	store := layouttest.NewMemStore()
	reader := layout.NewReader(store, layouttest.NewFakeJournal(), nil)
	requester := layout.NewRequester(store, 3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wa := &fakeWorkflowAccess{
		readable: map[string]queryAccessEntry{}, writable: map[string]queryAccessEntry{}, retryErr: map[string]error{},
		// Explicit fixtures, never inferred from the currently resolved scope.
		lockTargets: map[string][]workflow.MutationTarget{},
	}
	for _, name := range []string{"wf", "old", "pub"} {
		wa.lockTargets[name] = []workflow.MutationTarget{
			{Scope: workflow.Private(queryOwner), Name: name}, {Scope: workflow.Shared(), Name: name},
		}
	}
	ia := &fakeInstanceAccess{viewable: map[uuid.UUID]*workflow.Workflow{}, retryable: map[uuid.UUID]*workflow.Workflow{}, retryErr: map[uuid.UUID]error{}}
	return &queryHarness{store: store, workflows: wa, instances: ia,
		q: layout.NewQueryService(reader, requester, wa, ia, &queryMutationGate{t: t, access: wa})}
}

// stamped builds a linear transform workflow with presentation metadata.
func queryStamped(name, rev, scopeLabel string, nodeIDs ...string) *workflow.Workflow {
	wf := &workflow.Workflow{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(name)), Name: name}
	for i, id := range nodeIDs {
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: id, Type: workflow.NodeTypeTransform, Label: "Step " + id, Entrypoint: i == 0})
		if i > 0 {
			wf.Edges = append(wf.Edges, workflow.Edge{Source: nodeIDs[i-1], Target: id})
		}
	}
	owner := queryOwner
	if scopeLabel == workflow.ScopeLabelShared {
		owner = uuid.Nil
	}
	wf.Presentation = &workflow.Presentation{
		Revision: rev, SavedAt: queryNow, Reason: workflow.SaveReasonUpdate, Scope: scopeLabel,
		OwnerID: owner, Name: name, Retained: workflow.RetainedLayout{Source: workflow.RetainedNone},
	}
	return wf
}

// snapshotCopy reproduces the engine snapshot round-trip (json) so instance
// definitions are distinct values with identical content.
func querySnapshotCopy(t *testing.T, wf *workflow.Workflow) *workflow.Workflow {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var out workflow.Workflow
	require.NoError(t, json.Unmarshal(b, &out))
	return &out
}

func (h *queryHarness) request(t *testing.T, wf *workflow.Workflow, scope workflow.Scope) layout.Record {
	t.Helper()
	manifest, err := json.Marshal(wf)
	require.NoError(t, err)
	rec, created, err := h.store.Request(context.Background(), layout.NewRequest{
		Revision:     wf.SavedRevision(),
		Destination:  layout.Destination{Scope: scope, Name: wf.Presentation.Name, WorkflowID: wf.ID},
		Reason:       wf.Presentation.Reason,
		Manifest:     manifest,
		Retained:     wf.Presentation.Retained,
		AttemptLimit: 3,
		RequestedAt:  queryNow,
	})
	require.NoError(t, err)
	require.True(t, created)
	return rec
}

func (h *queryHarness) claim(t *testing.T, wf *workflow.Workflow, scope workflow.Scope) layout.Fence {
	t.Helper()
	h.request(t, wf, scope)
	rec, err := h.store.Claim(context.Background(), wf.SavedRevision(), "query-test", queryNow, queryNow.Add(time.Minute))
	require.NoError(t, err)
	return layout.Fence{Revision: wf.SavedRevision(), Generation: rec.JobGeneration}
}

// completePassed stores Arrange geometry with synthetic port evidence for query
// behavior only. This fixture is never production visual acceptance.
func (h *queryHarness) completePassed(t *testing.T, wf *workflow.Workflow, scope workflow.Scope) layout.Geometry {
	t.Helper()
	fence := h.claim(t, wf, scope)
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	c := layout.DefaultContract()
	geo, err := layout.Arrange(context.Background(), g, layouttest.Measurements(g, c), c, layout.DefaultOptions())
	require.NoError(t, err)
	res := layout.StoredResult{
		Revision: wf.SavedRevision(), Generation: fence.Generation, Geometry: geo,
		Review: &layout.Review{Rendered: true, Findings: []layout.Finding{}, Evidence: layout.Evidence{
			GeometryDigest: layout.GeometryDigest(geo), RendererVersion: geo.Contract.Version,
			Contract: geo.Contract, FontsReady: true, Zoom: geo.Contract.PracticalZoom,
			Tiles: 1, Artifacts: layouttest.Artifacts(geo), ReviewedAt: queryNow,
		}},
		Verdict: layout.VerdictPassed, Score: layout.ScoreOf(1, nil, len(geo.Crossings)), CreatedAt: queryNow,
	}
	require.NoError(t, h.store.Complete(context.Background(), fence, layout.Completion{
		State: layout.StatePassed, AttemptsUsed: 1, Result: &res, ReplaceBest: true,
	}, queryNow))
	return geo
}

func (h *queryHarness) completeFailed(t *testing.T, wf *workflow.Workflow, scope workflow.Scope) {
	t.Helper()
	fence := h.claim(t, wf, scope)
	require.NoError(t, h.store.Complete(context.Background(), fence, layout.Completion{
		State: layout.StateFailed, AttemptsUsed: 1, FailureStage: layout.StageRendering, FailureReason: "renderer unavailable",
	}, queryNow))
}

func (h *queryHarness) get(t *testing.T, rev string) layout.Record {
	t.Helper()
	rec, err := h.store.Get(context.Background(), rev)
	require.NoError(t, err)
	return rec
}

// --- WorkflowLayout ----------------------------------------------------------

func TestQueryService_WorkflowLayout_ReturnsViewOfAuthorisedRevision(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	h.workflows.readable["wf"] = queryAccessEntry{wf, scope}
	geo := h.completePassed(t, wf, scope)

	v, err := h.q.WorkflowLayout(context.Background(), "wf")
	require.NoError(t, err)
	assert.Equal(t, queryRev1, v.SavedRevision)
	assert.Equal(t, layout.DisplayComplete, v.DisplayStatus)
	require.NotNil(t, v.Geometry)
	assert.Equal(t, layout.GeometryDigest(geo), layout.GeometryDigest(*v.Geometry))
	assert.Equal(t, []string{"wf"}, h.workflows.loadCalls)
	assert.Empty(t, h.workflows.retryCalls, "a read must not use the retry authorization seam")
}

func TestQueryService_WorkflowLayout_InaccessibleName_NotFound(t *testing.T) {
	h := newQueryHarness(t)
	h.workflows.readable["wf"] = queryAccessEntry{queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a"), workflow.Private(queryOwner)}

	_, err := h.q.WorkflowLayout(context.Background(), "other")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	assert.Equal(t, []string{"other"}, h.workflows.loadCalls, "the requested name must be passed through unchanged")
}

func TestQueryService_WorkflowLayout_PendingRequest(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	h.workflows.readable["wf"] = queryAccessEntry{wf, scope}
	h.request(t, wf, scope)

	v, err := h.q.WorkflowLayout(context.Background(), "wf")
	require.NoError(t, err)
	assert.Equal(t, layout.DisplayPending, v.DisplayStatus)
	assert.True(t, layout.IsActive(v.DisplayStatus))
}

func TestQueryService_WorkflowLayout_LegacyManifest(t *testing.T) {
	h := newQueryHarness(t)
	legacy := &workflow.Workflow{Name: "old", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	h.workflows.readable["old"] = queryAccessEntry{legacy, workflow.Private(queryOwner)}

	v, err := h.q.WorkflowLayout(context.Background(), "old")
	require.NoError(t, err)
	assert.Equal(t, "", v.SavedRevision)
	assert.Equal(t, layout.DisplayLegacy, v.DisplayStatus)
}

// --- InstanceLayout ----------------------------------------------------------

// The instance view is keyed by the SNAPSHOT revision and never substitutes the
// workflow's latest revision or geometry.
func TestQueryService_InstanceLayout_UsesSnapshotRevision_NeverLatest(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	r1 := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	r2 := queryStamped("wf", queryRev2, workflow.ScopeLabelPrivate, "a", "b", "c")
	geo1 := h.completePassed(t, r1, scope)
	geo2 := h.completePassed(t, r2, scope)
	require.NotEqual(t, layout.GeometryDigest(geo1), layout.GeometryDigest(geo2))
	h.workflows.readable["wf"] = queryAccessEntry{r2, scope}
	id := uuid.New()
	h.instances.viewable[id] = querySnapshotCopy(t, r1)

	vi, err := h.q.InstanceLayout(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, queryRev1, vi.SavedRevision)
	require.NotNil(t, vi.Geometry)
	assert.Equal(t, layout.GeometryDigest(geo1), layout.GeometryDigest(*vi.Geometry))
	assert.Len(t, vi.Geometry.Nodes, 2)
	assert.Zero(t, h.workflows.calls(), "an instance read must not consult the current workflow")

	vw, err := h.q.WorkflowLayout(context.Background(), "wf")
	require.NoError(t, err)
	assert.Equal(t, queryRev2, vw.SavedRevision)
	assert.Equal(t, layout.GeometryDigest(geo2), layout.GeometryDigest(*vw.Geometry))
}

// Same saved revision ⇒ identical view from both surfaces (spec: workflow and
// instance views show the same arrangement for the same saved revision).
func TestQueryService_SameRevision_WorkflowAndInstanceViewsIdentical(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	r1 := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	h.completePassed(t, r1, scope)
	h.workflows.readable["wf"] = queryAccessEntry{r1, scope}
	id := uuid.New()
	h.instances.viewable[id] = querySnapshotCopy(t, r1)

	vw, err := h.q.WorkflowLayout(context.Background(), "wf")
	require.NoError(t, err)
	vi, err := h.q.InstanceLayout(context.Background(), id)
	require.NoError(t, err)

	bw, err := json.Marshal(vw)
	require.NoError(t, err)
	bi, err := json.Marshal(vi)
	require.NoError(t, err)
	assert.JSONEq(t, string(bw), string(bi))
}

func TestQueryService_InstanceLayout_UnknownInstance_NotFound(t *testing.T) {
	h := newQueryHarness(t)
	h.instances.viewable[uuid.New()] = queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")

	_, err := h.q.InstanceLayout(context.Background(), uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

// --- RetryWorkflow -----------------------------------------------------------

func TestQueryService_RetryWorkflowConcurrentSave(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	old := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	newer := queryStamped("wf", queryRev2, workflow.ScopeLabelPrivate, "a", "b")
	h.workflows.writable["wf"] = queryAccessEntry{old, scope}
	h.completeFailed(t, old, scope)
	before := h.get(t, queryRev1)
	// Save wins after initial authorization but before the retry lock.
	gate := &queryMutationGate{t: t, access: h.workflows, before: func() {
		done := make(chan struct{})
		go func() {
			h.workflows.mu.Lock()
			h.workflows.writable["wf"] = queryAccessEntry{newer, scope}
			h.workflows.mu.Unlock()
			close(done)
		}()
		<-done
	}}
	h.q = layout.NewQueryService(layout.NewReader(h.store, nil, nil), layout.NewRequester(h.store, 3, nil), h.workflows, h.instances, gate)
	_, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.ErrorIs(t, err, layout.ErrRevisionConflict)
	assert.Equal(t, before, h.get(t, queryRev1))
}

func TestQueryService_RetryWorkflow_FailedRevision_BumpsGeneration(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
	h.completeFailed(t, wf, scope)
	before := h.get(t, queryRev1)

	ack, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.NoError(t, err)
	assert.Equal(t, queryRev1, ack.SavedRevision)
	assert.Equal(t, before.JobGeneration+1, ack.JobGeneration)
	assert.Equal(t, layout.StatePending, ack.State)
	assert.Equal(t, layout.DisplayPending, ack.DisplayStatus)

	after := h.get(t, queryRev1)
	assert.Equal(t, layout.StatePending, after.State)
	assert.Equal(t, ack.JobGeneration, after.JobGeneration)
	assert.Equal(t, []string{"wf", "wf"}, h.workflows.retryCalls)
	assert.Empty(t, h.workflows.loadCalls, "retry must authorize through AuthorizeLayoutRetry, not the read seam")
}

// A shared resolution can only be re-resolved inside the lock if the private
// candidate was locked too: the filesystem coordinator rejects set expansion.
func TestQueryService_RetryWorkflow_LocksEveryResolutionCandidate(t *testing.T) {
	h := newQueryHarness(t)
	shared := workflow.Shared()
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelShared, "a")
	h.workflows.writable["wf"] = queryAccessEntry{wf, shared}
	h.workflows.lockTargets = map[string][]workflow.MutationTarget{"wf": {
		{Scope: workflow.Private(queryOwner), Name: "wf"}, {Scope: shared, Name: "wf"},
	}}
	h.completeFailed(t, wf, shared)
	before := h.get(t, queryRev1)

	ack, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.NoError(t, err)
	assert.Equal(t, before.JobGeneration+1, ack.JobGeneration)
}

// A private shadow created between authorization and the lock may carry the
// same token, so the locked reload must also reject the changed scope.
func TestQueryService_RetryWorkflow_PrivateShadowSameToken_Conflict(t *testing.T) {
	h := newQueryHarness(t)
	shared, private := workflow.Shared(), workflow.Private(queryOwner)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelShared, "a")
	h.workflows.writable["wf"] = queryAccessEntry{wf, shared}
	h.workflows.lockTargets = map[string][]workflow.MutationTarget{"wf": {
		{Scope: private, Name: "wf"}, {Scope: shared, Name: "wf"},
	}}
	h.completeFailed(t, wf, shared)
	before := h.get(t, queryRev1)
	gate := &queryMutationGate{t: t, access: h.workflows, before: func() {
		h.workflows.mu.Lock()
		h.workflows.writable["wf"] = queryAccessEntry{wf, private}
		h.workflows.mu.Unlock()
	}}
	h.q = layout.NewQueryService(layout.NewReader(h.store, nil, nil), layout.NewRequester(h.store, 3, nil), h.workflows, h.instances, gate)

	_, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.ErrorIs(t, err, layout.ErrRevisionConflict)
	assert.Equal(t, before, h.get(t, queryRev1))
}

func TestQueryService_RetryWorkflow_StaleOrEmptyExpected_Conflict(t *testing.T) {
	for _, expected := range []string{"rev-0", ""} {
		t.Run("expected="+expected, func(t *testing.T) {
			h := newQueryHarness(t)
			scope := workflow.Private(queryOwner)
			wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
			h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
			h.completeFailed(t, wf, scope)
			before := h.get(t, queryRev1)

			_, err := h.q.RetryWorkflow(context.Background(), "wf", expected)
			assert.ErrorIs(t, err, layout.ErrRevisionConflict)
			assert.Equal(t, before, h.get(t, queryRev1), "a stale retry must not touch the row")
		})
	}
}

func TestQueryService_RetryWorkflow_PassedRevision_RetryConflict(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
	h.completePassed(t, wf, scope)

	_, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	assert.ErrorIs(t, err, layout.ErrRetryConflict)
}

func TestQueryService_RetryWorkflow_LegacyManifest_LegacyRevision(t *testing.T) {
	h := newQueryHarness(t)
	legacy := &workflow.Workflow{Name: "old", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	h.workflows.writable["old"] = queryAccessEntry{legacy, workflow.Private(queryOwner)}

	_, err := h.q.RetryWorkflow(context.Background(), "old", "")
	assert.ErrorIs(t, err, layout.ErrLegacyRevision)
}

func TestQueryService_RetryWorkflow_AuthorizationRefused_Propagates(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Shared()
	wf := queryStamped("pub", queryRev1, workflow.ScopeLabelShared, "a")
	h.completeFailed(t, wf, scope)
	h.workflows.retryErr["pub"] = fmt.Errorf("%w: pub", workflow.ErrSharedReadOnly)
	before := h.get(t, queryRev1)

	_, err := h.q.RetryWorkflow(context.Background(), "pub", queryRev1)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.Equal(t, before, h.get(t, queryRev1))
}

func TestQueryService_RetryWorkflow_StoreUnavailable_Propagates(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
	h.completeFailed(t, wf, scope)
	h.store.SetFailure(errors.New("database is locked"))

	_, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	assert.ErrorIs(t, err, layout.ErrStoreUnavailable)

	h.store.SetFailure(nil)
	assert.Equal(t, layout.StateFailed, h.get(t, queryRev1).State)
}

// A saved revision whose request never reached the store (enqueue failure) is
// re-requested against the AUTHORISED destination with the exact saved manifest.
func TestQueryService_RetryWorkflow_MissingRow_ReRequestsAuthorisedDestination(t *testing.T) {
	h := newQueryHarness(t)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	wf.Presentation.Retained = workflow.RetainedLayout{
		Source:    workflow.RetainedFromManifest,
		Positions: map[string]workflow.NodePosition{"a": {X: 10, Y: 20}},
	}
	// An admin editing a shared-only workflow in place: the authorised scope is
	// Shared() even though the presentation label says private.
	h.workflows.writable["wf"] = queryAccessEntry{wf, workflow.Shared()}

	ack, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.NoError(t, err)

	rec := h.get(t, queryRev1)
	assert.Equal(t, layout.Destination{Scope: workflow.Shared(), Name: "wf", WorkflowID: wf.ID}, rec.Destination)
	assert.Equal(t, workflow.SaveReasonUpdate, rec.Reason)
	assert.Equal(t, wf.Presentation.Retained, rec.Retained)
	assert.True(t, rec.RequestedAt.Equal(wf.Presentation.SavedAt), "the re-request must acknowledge the saved time, not the retry time")
	var captured workflow.Workflow
	require.NoError(t, json.Unmarshal(rec.Manifest, &captured))
	assert.Equal(t, queryRev1, captured.SavedRevision())
	assert.Len(t, captured.Nodes, 2)

	assert.Equal(t, queryRev1, ack.SavedRevision)
	assert.Equal(t, rec.JobGeneration, ack.JobGeneration)
	assert.Equal(t, layout.StatePending, ack.State)
}

// --- RetryInstance -----------------------------------------------------------

func TestQueryService_RetryInstance_RetriesSnapshotRevisionOnly(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	r1 := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	r2 := queryStamped("wf", queryRev2, workflow.ScopeLabelPrivate, "a", "b", "c")
	h.completeFailed(t, r1, scope)
	h.completePassed(t, r2, scope)
	h.workflows.readable["wf"] = queryAccessEntry{r2, scope}
	h.workflows.writable["wf"] = queryAccessEntry{r2, scope}
	id := uuid.New()
	h.instances.retryable[id] = querySnapshotCopy(t, r1)
	before1 := h.get(t, queryRev1)
	before2 := h.get(t, queryRev2)

	ack, err := h.q.RetryInstance(context.Background(), id, queryRev1)
	require.NoError(t, err)
	assert.Equal(t, queryRev1, ack.SavedRevision)
	assert.Equal(t, before1.JobGeneration+1, ack.JobGeneration)
	assert.Equal(t, layout.StatePending, h.get(t, queryRev1).State)
	assert.Equal(t, before2, h.get(t, queryRev2), "the current workflow's revision must be untouched")
	assert.Zero(t, h.workflows.calls(), "an instance retry must never consult the current workflow")
}

func TestQueryService_RetryInstance_ExpectedMustBeSnapshotRevision(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	r1 := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	h.completeFailed(t, r1, scope)
	id := uuid.New()
	h.instances.retryable[id] = querySnapshotCopy(t, r1)

	_, err := h.q.RetryInstance(context.Background(), id, queryRev2)
	assert.ErrorIs(t, err, layout.ErrRevisionConflict)
}

// A historical revision with no row is re-requested against the snapshot's own
// destination and does NOT supersede or alter the current revision's job.
func TestQueryService_RetryInstance_MissingRow_ReRequestsSnapshotDestination(t *testing.T) {
	h := newQueryHarness(t)
	r1 := queryStamped("wf-shared", queryRev1, workflow.ScopeLabelShared, "a", "b")
	r2 := queryStamped("wf-shared", queryRev2, workflow.ScopeLabelShared, "a", "b", "c")
	h.request(t, r2, workflow.Shared()) // the current revision is still pending
	before2 := h.get(t, queryRev2)
	id := uuid.New()
	h.instances.retryable[id] = querySnapshotCopy(t, r1)

	admin := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
	ack, err := h.q.RetryInstance(admin, id, queryRev1)
	require.NoError(t, err)
	assert.Equal(t, queryRev1, ack.SavedRevision)

	rec1 := h.get(t, queryRev1)
	assert.Equal(t, layout.Destination{Scope: workflow.Shared(), Name: "wf-shared", WorkflowID: r1.ID}, rec1.Destination)
	assert.Equal(t, rec1.JobGeneration, ack.JobGeneration)
	assert.True(t, rec1.RequestedAt.Equal(r1.Presentation.SavedAt), "a historical re-request must acknowledge the snapshot's saved time")

	after2 := h.get(t, queryRev2)
	assert.Equal(t, layout.StatePending, after2.State, "a historical re-request must not supersede the current revision")
	assert.Equal(t, before2.JobGeneration, after2.JobGeneration)
}

// Owning a run of a shared workflow grants no right over the shared revision
// row: only an admin may re-queue it, matching AuthorizeLayoutRetry.
func TestQueryService_RetryInstance_SharedSnapshot_RequiresAdmin(t *testing.T) {
	h := newQueryHarness(t)
	r1 := queryStamped("wf-shared", queryRev1, workflow.ScopeLabelShared, "a", "b")
	h.completeFailed(t, r1, workflow.Shared())
	id := uuid.New()
	h.instances.retryable[id] = querySnapshotCopy(t, r1)
	before := h.get(t, queryRev1)

	owner := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleUser})
	_, err := h.q.RetryInstance(owner, id, queryRev1)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.Equal(t, before, h.get(t, queryRev1), "a refused retry must not touch the shared row")

	admin := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
	ack, err := h.q.RetryInstance(admin, id, queryRev1)
	require.NoError(t, err)
	assert.Equal(t, before.JobGeneration+1, ack.JobGeneration)
}

func TestQueryService_RetryInstance_Forbidden_Propagates(t *testing.T) {
	h := newQueryHarness(t)
	scope := workflow.Private(queryOwner)
	r1 := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	h.completeFailed(t, r1, scope)
	id := uuid.New()
	h.instances.retryErr[id] = fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
	before := h.get(t, queryRev1)

	_, err := h.q.RetryInstance(context.Background(), id, queryRev1)
	assert.ErrorIs(t, err, instance.ErrForbidden)
	assert.Equal(t, before, h.get(t, queryRev1))
}

func TestQueryService_RetryInstance_LegacySnapshot_LegacyRevision(t *testing.T) {
	h := newQueryHarness(t)
	id := uuid.New()
	h.instances.retryable[id] = &workflow.Workflow{Name: "old", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true}}}

	_, err := h.q.RetryInstance(context.Background(), id, "")
	assert.ErrorIs(t, err, layout.ErrLegacyRevision)
}

func TestQueryService_RetryInstance_UnknownInstance_NotFound(t *testing.T) {
	h := newQueryHarness(t)
	_, err := h.q.RetryInstance(context.Background(), uuid.New(), queryRev1)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}
