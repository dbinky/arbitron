package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layoutInstanceRepo honours the instance id: Get returns ErrNotFound for any id
// it was not seeded with, so a seam that loads the wrong id cannot pass. Every
// other InstanceRepository method is the panicking readGetRepo stub.
type layoutInstanceRepo struct {
	readGetRepo
	byID map[uuid.UUID]*instance.Instance
}

func (r *layoutInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return inst, nil
}

var _ instance.InstanceRepository = (*layoutInstanceRepo)(nil)

func newLayoutEngine(insts ...*instance.Instance) *engine.Engine {
	repo := &layoutInstanceRepo{byID: map[uuid.UUID]*instance.Instance{}}
	for _, inst := range insts {
		repo.byID[inst.ID] = inst
	}
	return engine.NewEngine(
		newScopeAwareRepo(), repo, &countingNodeExecRepo{}, &countingSnapshotRepo{}, newMockUserRepo(),
		map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
		"",
	)
}

// snapshotOf reproduces exactly how TriggerWithDepartment captures a snapshot:
// json.Marshal of the loaded workflow, decoded into a generic map.
func layoutSnapshotMap(t *testing.T, wf *workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func stampedSnapshotWorkflow(owner uuid.UUID, rev string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: "snap-wf",
		Nodes: []workflow.Node{
			{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true, Label: "Start"},
			{ID: "b", Type: workflow.NodeTypeTransform, Label: "Finish"},
		},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}},
		Presentation: &workflow.Presentation{
			Revision: rev,
			SavedAt:  time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
			Reason:   workflow.SaveReasonCreate,
			Scope:    workflow.ScopeLabelPrivate,
			OwnerID:  owner,
			Name:     "snap-wf",
			Retained: workflow.RetainedLayout{
				Source:    workflow.RetainedFromManifest,
				Positions: map[string]workflow.NodePosition{"a": {X: 1, Y: 2}},
			},
		},
	}
}

func layoutInstance(t *testing.T, owner uuid.UUID, vis instance.Visibility, dept string, rev string) *instance.Instance {
	t.Helper()
	inst := ownedInstance(owner)
	inst.Visibility = vis
	inst.Department = dept
	inst.WorkflowSnapshot = layoutSnapshotMap(t, stampedSnapshotWorkflow(owner, rev))
	return inst
}

// --- InstanceLayoutSnapshot (view policy) ---

func TestInstanceLayoutSnapshot_Owner_DecodesSnapshotRevision(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev-snap-1")
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(userCtx(owner), inst.ID)
	require.NoError(t, err)
	require.NotNil(t, wf.Presentation)
	assert.Equal(t, "rev-snap-1", wf.SavedRevision())
	assert.Equal(t, workflow.RetainedFromManifest, wf.Presentation.Retained.Source)
	assert.Equal(t, workflow.NodePosition{X: 1, Y: 2}, wf.Presentation.Retained.Positions["a"])
	require.Len(t, wf.Nodes, 2)
	assert.Equal(t, "Finish", wf.Nodes[1].Label)
}

func TestInstanceLayoutSnapshot_AdminAndPublicViewer_Allowed(t *testing.T) {
	owner := uuid.New()
	private := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev-priv")
	public := layoutInstance(t, owner, instance.VisibilityPublic, "", "rev-pub")
	eng := newLayoutEngine(private, public)

	wf, err := eng.InstanceLayoutSnapshot(adminCtx(uuid.New()), private.ID)
	require.NoError(t, err)
	assert.Equal(t, "rev-priv", wf.SavedRevision())

	wf, err = eng.InstanceLayoutSnapshot(userCtx(uuid.New()), public.ID)
	require.NoError(t, err)
	assert.Equal(t, "rev-pub", wf.SavedRevision())
}

func TestInstanceLayoutSnapshot_NonViewer_NotFound(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev-secret")
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(userCtx(uuid.New()), inst.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.Nil(t, wf, "a non-viewer must not receive the snapshot revision")
}

func TestInstanceLayoutSnapshot_NoIdentity_NotFound(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityPublic, "", "rev")
	eng := newLayoutEngine(inst)

	_, err := eng.InstanceLayoutSnapshot(context.Background(), inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestInstanceLayoutSnapshot_UnknownID_NotFound(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityPrivate, "", "rev")
	eng := newLayoutEngine(inst)

	_, err := eng.InstanceLayoutSnapshot(userCtx(owner), uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestInstanceLayoutSnapshot_DepartmentPeer_CanRead(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityDepartment, "engineering", "rev-dept")
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(memberCtx(uuid.New(), "engineering"), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "rev-dept", wf.SavedRevision())
}

// Edge: a legacy snapshot (pre-feature manifest) decodes with a nil
// Presentation and an empty saved revision — the caller renders its own
// positions/dagre fallback.
func TestInstanceLayoutSnapshot_LegacySnapshot_NilPresentation(t *testing.T) {
	owner := uuid.New()
	inst := ownedInstance(owner)
	legacy := &workflow.Workflow{
		Name:  "old",
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true, Position: &workflow.NodePosition{X: 5, Y: 6}}},
	}
	inst.WorkflowSnapshot = layoutSnapshotMap(t, legacy)
	eng := newLayoutEngine(inst)

	wf, err := eng.InstanceLayoutSnapshot(userCtx(owner), inst.ID)
	require.NoError(t, err)
	assert.Nil(t, wf.Presentation)
	assert.Equal(t, "", wf.SavedRevision())
	require.NotNil(t, wf.Nodes[0].Position)
	assert.Equal(t, 5.0, wf.Nodes[0].Position.X)
}

func TestInstanceLayoutSnapshot_MissingSnapshot_Errors(t *testing.T) {
	owner := uuid.New()
	inst := ownedInstance(owner) // WorkflowSnapshot nil
	eng := newLayoutEngine(inst)

	_, err := eng.InstanceLayoutSnapshot(userCtx(owner), inst.ID)
	require.Error(t, err)
	assert.ErrorContains(t, err, "instance has no workflow snapshot")
}

// --- AuthorizeInstanceLayoutRetry (intervention policy) ---

func TestAuthorizeInstanceLayoutRetry_OwnerAdminLead_Allowed(t *testing.T) {
	owner := uuid.New()
	inst := layoutInstance(t, owner, instance.VisibilityDepartment, "engineering", "rev-r")
	eng := newLayoutEngine(inst)

	for name, ctx := range map[string]context.Context{
		"owner": userCtx(owner),
		"admin": adminCtx(uuid.New()),
		"lead":  leadCtx(uuid.New(), "engineering"),
	} {
		t.Run(name, func(t *testing.T) {
			wf, err := eng.AuthorizeInstanceLayoutRetry(ctx, inst.ID)
			require.NoError(t, err)
			assert.Equal(t, "rev-r", wf.SavedRevision())
		})
	}
}

// A department peer can READ the run but cannot retry it: 403, not 404.
func TestAuthorizeInstanceLayoutRetry_DepartmentPeer_Forbidden(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityDepartment, "engineering", "rev-r")
	eng := newLayoutEngine(inst)

	wf, err := eng.AuthorizeInstanceLayoutRetry(memberCtx(uuid.New(), "engineering"), inst.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrForbidden)
	assert.NotErrorIs(t, err, instance.ErrNotFound)
	assert.Nil(t, wf)
}

func TestAuthorizeInstanceLayoutRetry_NonViewer_NotFound(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityPrivate, "", "rev-r")
	eng := newLayoutEngine(inst)

	_, err := eng.AuthorizeInstanceLayoutRetry(userCtx(uuid.New()), inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.NotErrorIs(t, err, instance.ErrForbidden)
}

func TestAuthorizeInstanceLayoutRetry_UnknownID_NotFound(t *testing.T) {
	inst := layoutInstance(t, uuid.New(), instance.VisibilityPrivate, "", "rev-r")
	eng := newLayoutEngine(inst)

	_, err := eng.AuthorizeInstanceLayoutRetry(adminCtx(uuid.New()), uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

// --- DecodeWorkflowSnapshot ---

func TestDecodeWorkflowSnapshot_RoundTripsPresentation(t *testing.T) {
	src := stampedSnapshotWorkflow(uuid.New(), "rev-rt")
	wf, err := engine.DecodeWorkflowSnapshot(layoutSnapshotMap(t, src))
	require.NoError(t, err)
	assert.Equal(t, src.Presentation.Revision, wf.Presentation.Revision)
	assert.Equal(t, src.Presentation.OwnerID, wf.Presentation.OwnerID)
	assert.True(t, src.Presentation.SavedAt.Equal(wf.Presentation.SavedAt))
	assert.Equal(t, src.Edges, wf.Edges)
}

func TestDecodeWorkflowSnapshot_NilMap_Errors(t *testing.T) {
	_, err := engine.DecodeWorkflowSnapshot(nil)
	assert.ErrorContains(t, err, "instance has no workflow snapshot")
}

func TestDecodeWorkflowSnapshot_EmptyOrMissingGraph_Errors(t *testing.T) {
	for _, snapshot := range []map[string]any{{}, {"name": "missing"}, {"nodes": []any{}}} {
		_, err := engine.DecodeWorkflowSnapshot(snapshot)
		require.Error(t, err)
	}
	_, err := engine.DecodeWorkflowSnapshot(map[string]any{"nodes": make(chan int)})
	require.ErrorContains(t, err, "marshalling workflow snapshot")
}

func TestDecodeWorkflowSnapshot_WrongShape_Errors(t *testing.T) {
	_, err := engine.DecodeWorkflowSnapshot(map[string]any{"nodes": "not-a-list"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "parsing workflow snapshot")
}
