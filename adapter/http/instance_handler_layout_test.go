package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

func getInstance(t *testing.T, h interface {
	Get(http.ResponseWriter, *http.Request)
}, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id, nil)
	req = withChiURLParam(req, "id", id)
	w := httptest.NewRecorder()
	h.Get(w, req)
	return w
}

func TestInstanceHandler_Get_IncludesSnapshotLayout(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateCompleted)
	instRepo.instances[inst.ID] = inst
	other := uuid.New()
	q := newFakeLayoutQuery()
	q.instanceViews[inst.ID] = attentionView("rev-snap")
	q.instanceViews[other] = attentionView("rev-other")
	h.SetLayouts(q)

	w := getInstance(t, h, inst.ID.String())
	require.Equal(t, http.StatusOK, w.Code)
	body := decodeBody(t, w)
	lay, ok := body["layout"].(map[string]any)
	require.True(t, ok, "GET /instances/{id} must embed the layout view")
	assert.Equal(t, "rev-snap", lay["savedRevision"], "the view must be the one for THIS instance id")
	assert.Equal(t, "needs_attention", lay["displayStatus"])
	assert.Equal(t, []string{"InstanceLayout:" + inst.ID.String()}, q.recorded())
}

// A layout query failure never fails the instance read and never pretends the
// run has no arrangement: it is reported in-band as unavailable, keyed by the
// snapshot's own revision with its retained fallback.
func TestInstanceHandler_Get_LayoutError_InBandUnavailable(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning)
	inst.WorkflowSnapshot = map[string]any{
		"name":  "test-wf",
		"nodes": []any{map[string]any{"id": "a", "type": "transform", "entrypoint": true}},
		"edges": []any{},
		"presentation": map[string]any{
			"revision": "rev-snap", "reason": "create", "scope": "private", "name": "test-wf",
			"retained": map[string]any{"source": "manifest", "positions": map[string]any{"a": map[string]any{"x": 3, "y": 4}}},
		},
	}
	instRepo.instances[inst.ID] = inst
	q := newFakeLayoutQuery()
	q.errs["instance:"+inst.ID.String()] = fmt.Errorf("decode: %w", layout.ErrStoreUnavailable)
	h.SetLayouts(q)

	w := getInstance(t, h, inst.ID.String())
	require.Equal(t, http.StatusOK, w.Code)
	lay := decodeBody(t, w)["layout"].(map[string]any)
	assert.Equal(t, "rev-snap", lay["savedRevision"])
	assert.Equal(t, "unavailable", lay["displayStatus"])
	assert.Equal(t, false, lay["available"])
	assert.Equal(t, true, lay["unreviewed"])
	assert.Equal(t, true, lay["retryEligible"])
	assert.Contains(t, lay["availabilityError"], "status store unavailable")
	assert.Equal(t, "retained_positions", lay["provenance"])
	retained := lay["retained"].(map[string]any)
	assert.Equal(t, "manifest", retained["source"])
}

func TestInstanceHandler_Get_LegacySnapshotLayoutError_LegacyProvenance(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning) // no snapshot at all
	instRepo.instances[inst.ID] = inst
	q := newFakeLayoutQuery()
	q.errs["instance:"+inst.ID.String()] = fmt.Errorf("boom")
	h.SetLayouts(q)

	lay := decodeBody(t, getInstance(t, h, inst.ID.String()))["layout"].(map[string]any)
	assert.Equal(t, "", lay["savedRevision"])
	assert.Equal(t, "legacy", lay["provenance"])
	assert.Equal(t, false, lay["available"])
}

func TestInstanceHandler_Get_NoLayoutService_ReportsUnavailable(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	body := decodeBody(t, getInstance(t, h, inst.ID.String()))
	lay := body["layout"].(map[string]any)
	assert.Equal(t, "unavailable", lay["displayStatus"])
	assert.Equal(t, false, lay["available"])
}

func TestInstanceHandler_Get_NotFound_DoesNotQueryLayout(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	q := newFakeLayoutQuery()
	h.SetLayouts(q)

	assert.Equal(t, http.StatusNotFound, getInstance(t, h, uuid.New().String()).Code)
	assert.Empty(t, q.recorded())
}

// The fallback is still response data for the authorized frozen snapshot:
// dropping Groups or copying a diagnostic cause into JSON breaks this contract.
func TestInstanceHandler_Get_LayoutFallbackPreservesGroupsAndConcealsCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"unconfigured", nil, "layout service not configured"},
		{"store", layout.ErrStoreUnavailable, "layout status store unavailable; retry later"},
		{"pending", workflow.ErrMutationPending, "workflow destination is awaiting durable mutation recovery; retry shortly"},
		{"canceled", context.Canceled, "layout status unavailable; reload or retry"},
		{"deadline", context.DeadlineExceeded, "layout status unavailable; reload or retry"},
		{"unexpected", errors.New("disk failure"), "layout status unavailable; reload or retry"},
	} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", tc.name, legacy), func(t *testing.T) {
				h, repo, _, _, _ := newInstanceHandler()
				inst := testInstance(instance.StateCompleted)
				wf := &workflow.Workflow{
					Name: "frozen",
					Nodes: []workflow.Node{
						{ID: "fan", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
							Form: workflow.GroupFormWorkflows, CompletionRule: workflow.GroupRuleFirstSuccess,
							UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently, GatherMode: workflow.GroupGatherResults,
							Tasks: []workflow.GroupTask{{Key: "second", Workflow: "child", Inputs: map[string]any{"value": "frozen"}}, {Key: "first", Workflow: "child"}},
						}}},
						{ID: "done"}, {ID: "oops"},
					},
					Edges:        []workflow.Edge{{Source: "fan", Target: "done", Output: "success"}, {Source: "fan", Target: "oops", Output: "failure"}},
					Presentation: &workflow.Presentation{Revision: "frozen-revision", Retained: workflow.RetainedLayout{Source: workflow.RetainedFromManifest}},
				}
				if legacy {
					wf.Presentation = nil
				}
				raw, err := json.Marshal(wf)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &inst.WorkflowSnapshot))
				repo.instances[inst.ID] = inst
				if tc.err != nil {
					q := newFakeLayoutQuery()
					q.errs["instance:"+inst.ID.String()] = fmt.Errorf("/private/secret.db: %w", tc.err)
					h.SetLayouts(q)
				}
				w := getInstance(t, h, inst.ID.String())
				require.Equal(t, http.StatusOK, w.Code)
				require.NotContains(t, w.Body.String(), "/private/secret.db")
				body := decodeBody(t, w)
				encoded, err := json.Marshal(body["layout"])
				require.NoError(t, err)
				var view layout.View
				require.NoError(t, json.Unmarshal(encoded, &view))
				assert.Equal(t, tc.want, view.AvailabilityError)
				assert.Equal(t, wf.SavedRevision(), view.SavedRevision)
				assert.Equal(t, layout.DisplayUnavailable, view.DisplayStatus)
				assert.False(t, view.Available)
				assert.True(t, view.Unreviewed)
				assert.Nil(t, view.Geometry)
				assert.Nil(t, view.Evidence)
				require.NotNil(t, view.Groups, "the fallback must derive ownership even for an unstamped snapshot")
				assert.Equal(t, []string{"fan"}, view.Groups.Order)
				cfg := view.Groups.Groups["fan"].Config
				require.Len(t, cfg.Tasks, 2)
				assert.Equal(t, "second", cfg.Tasks[0].Key)
				assert.Equal(t, "first", cfg.Tasks[1].Key)
				assert.Equal(t, "frozen", cfg.Tasks[0].Inputs["value"])
				assert.Equal(t, workflow.GroupRuleFirstSuccess, cfg.CompletionRule)
				assert.Equal(t, !legacy, view.RetryEligible)
			})
		}
	}
}
