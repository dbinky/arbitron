package http_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
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
			Form:           workflow.GroupFormBranches,
			Tasks:          []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
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

func TestWorkflowHandler_GroupLayout_UsesServiceVisibility(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.shared["fanout"] = groupLayoutWorkflow()
	store := newFakePublishedStore()
	store.pub["fanout"] = workflow.Publication{
		Publisher: uuid.New(), Visibility: workflow.VisibilityDepartment, Department: "secret",
	}
	h := publishHandler(repo, store)

	w := groupLayoutRequest(h, "fanout")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotContains(t, w.Body.String(), "a1")

	pub := store.pub["fanout"]
	pub.Visibility = workflow.VisibilityPublic
	store.pub["fanout"] = pub
	w = groupLayoutRequest(h, "fanout")
	require.Equal(t, http.StatusOK, w.Code)
	var got workflow.GroupGraph
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"a1", "a2"}, got.Groups["g"].Bodies["a"])
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

// The dashboard's task cards link /workflows/{config.tasks[].workflow}; assert
// the served JSON keys and author order for the workflows form, which the
// branches fixtures above never exercise.
func TestWorkflowHandler_GroupLayout_WorkflowsFormServesOrderedChildWorkflows(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.workflows["fan"] = &workflow.Workflow{Name: "fan", Nodes: []workflow.Node{
		{ID: "start", Type: workflow.NodeTypeTransform, Entrypoint: true},
		{ID: "g", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
			Form:           workflow.GroupFormWorkflows,
			Tasks:          []workflow.GroupTask{{Key: "t1", Workflow: "child-b"}, {Key: "t2", Workflow: "child-a"}, {Key: "t3", Workflow: "child-b"}},
			CompletionRule: workflow.GroupRuleAllSucceed, UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
			GatherMode: workflow.GroupGatherResults,
		}}},
		{ID: "done", Type: workflow.NodeTypeTransform}, {ID: "fail", Type: workflow.NodeTypeTransform},
	}, Edges: []workflow.Edge{
		{Source: "start", Target: "g"},
		{Source: "g", Target: "done", Output: workflow.GroupPortSuccess},
		{Source: "g", Target: "fail", Output: workflow.GroupPortFailure},
	}}
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

	w := groupLayoutRequest(h, "fan")

	require.Equal(t, http.StatusOK, w.Code)
	var raw struct {
		Groups map[string]struct {
			Config struct {
				Form  string `json:"form"`
				Tasks []struct {
					Key      string `json:"key"`
					Workflow string `json:"workflow"`
				} `json:"tasks"`
			} `json:"config"`
			SuccessTarget string `json:"successTarget"`
			FailureTarget string `json:"failureTarget"`
		} `json:"groups"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	g, ok := raw.Groups["g"]
	require.True(t, ok)
	assert.Equal(t, "workflows", g.Config.Form)
	var got []string
	for _, task := range g.Config.Tasks {
		got = append(got, task.Key+"="+task.Workflow)
	}
	assert.Equal(t, []string{"t1=child-b", "t2=child-a", "t3=child-b"}, got)
	assert.Equal(t, "done", g.SuccessTarget)
	assert.Equal(t, "fail", g.FailureTarget)
}

// Shared wire fixtures are consumed through the real dashboard client and review
// renderer. Pin them to the actual HTTP producers so malformed ownership cannot
// silently become a hand-written frontend-only shape.
func TestWorkflowHandler_GroupLayout_ReviewWireFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../dashboard/src/graph/testdata/group-layout-wire.json")
	require.NoError(t, err)
	var fixtures []struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
		Layout     json.RawMessage `json:"layout"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	require.Len(t, fixtures, 5)
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			var wf workflow.Workflow
			require.NoError(t, json.Unmarshal(fixture.Definition, &wf))
			repo := newMockWorkflowRepo()
			repo.workflows[wf.Name] = &wf
			h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)
			got := groupLayoutRequest(h, wf.Name)
			require.Equal(t, http.StatusOK, got.Code)
			assert.JSONEq(t, string(fixture.Layout), got.Body.String())
			req := withChiURLParam(httptest.NewRequest(http.MethodGet, "/workflows/"+wf.Name, nil), "name", wf.Name)
			def := httptest.NewRecorder()
			h.Get(def, req)
			require.Equal(t, http.StatusOK, def.Code)
			assert.JSONEq(t, string(fixture.Definition), def.Body.String())
		})
	}
}
