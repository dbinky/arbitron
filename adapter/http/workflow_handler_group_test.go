package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/workflow"
)

type hgValidation struct {
	Valid  bool                       `json:"valid"`
	Errors []workflow.ValidationError `json:"errors"`
	Code   string                     `json:"code"`
	Error  string                     `json:"error"`
}

func hgCodes(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

func newGroupWorkflowHandler(t *testing.T) (*adapthttp.WorkflowHandler, *filesystem.WorkflowRepo, string) {
	t.Helper()
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	fr := newMockFileReader()
	profiles := stubProfileChecker{"agent-p": "agent", "chat-p": "chat"}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetGroupCheckers(profiles, fr)
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, fr, nil, profiles)
	h.SetWorkflowService(svc)
	return h, repo, root
}

func hgWorkflowsGroup(name, child string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name, Description: "d", Author: "t",
		Nodes: []workflow.Node{
			{ID: "reviews", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form: workflow.GroupFormWorkflows, CompletionRule: workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResults,
				Tasks: []workflow.GroupTask{{Key: "a", Workflow: child}},
			}}},
			{ID: "done", Type: workflow.NodeTypeTransform},
			{ID: "failed", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "reviews", Target: "done", Output: "success"},
			{Source: "reviews", Target: "failed", Output: "failure"},
		},
	}
}

func hgValidate(t *testing.T, h *adapthttp.WorkflowHandler, name string) hgValidation {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/workflows/"+name+"/validate", nil)
	req = withChiURLParam(req, "name", name)
	w := httptest.NewRecorder()
	h.Validate(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var v hgValidation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&v))
	return v
}

func TestWorkflowHandler_Validate_GroupOptionsAppended(t *testing.T) {
	h, repo, _ := newGroupWorkflowHandler(t)
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "orphaned", hgWorkflowsGroup("orphaned", "ghost-child")))

	v := hgValidate(t, h, "orphaned")
	assert.False(t, v.Valid)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW", "REST validate must use the same scoped child reader as MCP")
}

func TestWorkflowHandler_Validate_VisibleChildContractIsChecked(t *testing.T) {
	h, repo, _ := newGroupWorkflowHandler(t)
	child := &workflow.Workflow{Name: "reviewer", Inputs: []workflow.InputField{{Name: "topic", Type: "string"}},
		Nodes: []workflow.Node{{ID: "c", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "reviewer", child))
	wf := hgWorkflowsGroup("parent", "reviewer")
	wf.Nodes[0].Config.Group.Tasks[0].Inputs = map[string]any{"topic": 42}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "parent", wf))
	v := hgValidate(t, h, "parent")
	assert.False(t, v.Valid)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_INPUT_TYPE_MISMATCH", "name-only visibility cannot check the child's declared input type")
}

func TestWorkflowHandler_Validate_MissingFileCheckerIsReported(t *testing.T) {
	_, repo, _ := newGroupWorkflowHandler(t)
	child := &workflow.Workflow{Name: "child", Nodes: []workflow.Node{{ID: "c", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "child", child))
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "parent", hgWorkflowsGroup("parent", "child")))
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, nil, nil, stubProfileChecker{"agent-p": "agent"})
	v := hgValidate(t, h, "parent")
	assert.False(t, v.Valid)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_CHECKER_UNAVAILABLE")
}

func TestWorkflowHandler_Validate_AdvisoryOnlyIsValid(t *testing.T) {
	h, repo, _ := newGroupWorkflowHandler(t)
	child := &workflow.Workflow{Name: "reviewer", Inputs: []workflow.InputField{{Name: "topic", Type: "string", Required: true}},
		Nodes: []workflow.Node{{ID: "c1", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "reviewer", child))
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "inherits", hgWorkflowsGroup("inherits", "reviewer")))

	v := hgValidate(t, h, "inherits")
	assert.True(t, v.Valid, "%v", v.Errors)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_REQUIRED_INPUT_RUNTIME")
}

func TestWorkflowHandler_CreateUpdate_GroupErrorsAre422(t *testing.T) {
	h, repo, root := newGroupWorkflowHandler(t)

	bad := hgWorkflowsGroup("bad", "ghost-child")
	body, err := json.Marshal(bad)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, withTestIdentity(req))

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var v hgValidation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&v))
	assert.Equal(t, "GROUP_VALIDATION_FAILED", v.Code)
	assert.False(t, v.Valid)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	_, statErr := os.Stat(filepath.Join(root, "users", testUserID.String(), "bad"))
	assert.True(t, os.IsNotExist(statErr), "rejected create must not write the folder")

	good := &workflow.Workflow{Name: "edit-me", Nodes: []workflow.Node{{ID: "x", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "edit-me", good))
	manifestPath := filepath.Join(root, "users", testUserID.String(), "edit-me", "workflow.json")
	before, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	upd := hgWorkflowsGroup("edit-me", "ghost-child")
	body, err = json.Marshal(upd)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPut, "/workflows/edit-me", strings.NewReader(string(body)))
	req = withChiURLParam(req, "name", "edit-me")
	w = httptest.NewRecorder()
	h.Update(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	v = hgValidation{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&v))
	assert.Equal(t, "GROUP_VALIDATION_FAILED", v.Code)
	assert.False(t, v.Valid)
	assert.NotEmpty(t, v.Error)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	after, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "rejected update preserves the exact persisted bytes")
	stored, err := repo.Load(context.Background(), workflow.Private(testUserID), "edit-me")
	require.NoError(t, err)
	assert.Len(t, stored.Nodes, 1, "rejected update leaves the stored definition untouched")
}
