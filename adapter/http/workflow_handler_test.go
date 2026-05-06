package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ensure mock repos implement the interface at compile time
var _ workflow.WorkflowRepository = (*mockWorkflowRepo)(nil)
var _ workflow.WorkflowRepository = (*errWorkflowRepo)(nil)

// ---------------------------------------------------------------------------
// Mocks for WorkflowHandler
// ---------------------------------------------------------------------------

type mockWorkflowRepo struct {
	workflows map[string]*workflow.Workflow
}

func newMockWorkflowRepo() *mockWorkflowRepo {
	return &mockWorkflowRepo{workflows: make(map[string]*workflow.Workflow)}
}

func (r *mockWorkflowRepo) Load(_ context.Context, name string) (*workflow.Workflow, error) {
	wf, ok := r.workflows[name]
	if !ok {
		return nil, fmt.Errorf("workflow not found: %s", name)
	}
	return wf, nil
}

func (r *mockWorkflowRepo) List(_ context.Context) ([]workflow.WorkflowSummary, error) {
	var summaries []workflow.WorkflowSummary
	for _, wf := range r.workflows {
		summaries = append(summaries, workflow.WorkflowSummary{
			Name:        wf.Name,
			Description: wf.Description,
			Author:      wf.Author,
			NodeCount:   len(wf.Nodes),
		})
	}
	return summaries, nil
}

func (r *mockWorkflowRepo) SaveNodePositions(_ string, _ map[string]workflow.NodePosition) error {
	return nil
}

type errWorkflowRepo struct{ err error }

func (r *errWorkflowRepo) Load(_ context.Context, _ string) (*workflow.Workflow, error)    { return nil, r.err }
func (r *errWorkflowRepo) List(_ context.Context) ([]workflow.WorkflowSummary, error)    { return nil, r.err }
func (r *errWorkflowRepo) SaveNodePositions(_ string, _ map[string]workflow.NodePosition) error { return r.err }

type mockWorkflowService struct {
	triggerFn func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
}

func (m *mockWorkflowService) Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
	if m.triggerFn != nil {
		return m.triggerFn(workflowName, variables, priority)
	}
	return uuid.New(), true, nil
}

type mockFileReader struct {
	existingFiles map[string]bool
}

func newMockFileReader() *mockFileReader {
	return &mockFileReader{existingFiles: make(map[string]bool)}
}

func (r *mockFileReader) ReadFile(workflowName, relativePath string) ([]byte, error) {
	return nil, nil
}

func (r *mockFileReader) ResolvePath(workflowName, relativePath string) (string, error) {
	return "", nil
}

func (r *mockFileReader) FileExists(workflowName, relativePath string) bool {
	key := workflowName + "/" + relativePath
	return r.existingFiles[key]
}

// ---------------------------------------------------------------------------
// Helper to create chi context with URL params
// ---------------------------------------------------------------------------

func withChiURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		rctx = chi.NewRouteContext()
	}
	rctx.URLParams.Add(key, value)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Workflow fixtures
// ---------------------------------------------------------------------------

func testWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "test-wf",
		Description: "A test workflow",
		Author:      "tester",
		Inputs: []workflow.InputField{
			{Name: "name", Type: "string", Required: true},
			{Name: "count", Type: "number", Required: false, Default: float64(10)},
		},
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true, Config: workflow.NodeConfig{ScriptFile: "a.sh"}},
			{ID: "B", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{ScriptFile: "b.sh"}},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: List workflows
// ---------------------------------------------------------------------------

func TestWorkflowHandler_List(t *testing.T) {
	t.Run("returns empty array when no workflows", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []workflow.WorkflowSummary
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Empty(t, resp)
	})

	t.Run("returns workflow summaries", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []workflow.WorkflowSummary
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Len(t, resp, 1)
		assert.Equal(t, "test-wf", resp[0].Name)
	})
}

// ---------------------------------------------------------------------------
// Tests: Get workflow
// ---------------------------------------------------------------------------

func TestWorkflowHandler_Get(t *testing.T) {
	t.Run("returns workflow definition", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Get(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp workflow.Workflow
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "test-wf", resp.Name)
		assert.Len(t, resp.Nodes, 2)
	})

	t.Run("returns 404 for unknown workflow", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/unknown", nil)
		req = withChiURLParam(req, "name", "unknown")
		w := httptest.NewRecorder()
		h.Get(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)

		var resp adapthttp.ErrorResponse
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "NOT_FOUND", resp.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Validate workflow
// ---------------------------------------------------------------------------

func TestWorkflowHandler_Validate(t *testing.T) {
	t.Run("returns valid for correct workflow", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()
		fr := newMockFileReader()
		fr.existingFiles["test-wf/a.sh"] = true
		fr.existingFiles["test-wf/b.sh"] = true
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, fr, nil)

		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/validate", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Validate(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, true, resp["valid"])
		// errors should be empty array
		errors, ok := resp["errors"].([]any)
		require.True(t, ok)
		assert.Empty(t, errors)
	})

	t.Run("returns invalid with errors for bad workflow", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		// Workflow with no entrypoint
		repo.workflows["bad-wf"] = &workflow.Workflow{
			Name: "bad-wf",
			Nodes: []workflow.Node{
				{ID: "A", Type: workflow.NodeTypeScript},
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPost, "/workflows/bad-wf/validate", nil)
		req = withChiURLParam(req, "name", "bad-wf")
		w := httptest.NewRecorder()
		h.Validate(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, false, resp["valid"])
		errors, ok := resp["errors"].([]any)
		require.True(t, ok)
		assert.NotEmpty(t, errors)
	})

	t.Run("returns 404 for unknown workflow", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPost, "/workflows/unknown/validate", nil)
		req = withChiURLParam(req, "name", "unknown")
		w := httptest.NewRecorder()
		h.Validate(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Trigger workflow
// ---------------------------------------------------------------------------

func TestWorkflowHandler_Trigger(t *testing.T) {
	t.Run("triggers workflow and returns instance ID", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()

		expectedID := uuid.New()
		svc := &mockWorkflowService{
			triggerFn: func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
				assert.Equal(t, "test-wf", workflowName)
				assert.Equal(t, "Alice", variables["name"])
				assert.Equal(t, "normal", priority)
				return expectedID, true, nil
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, svc, newMockFileReader(), nil)

		body := `{"variables":{"name":"Alice"},"priority":"normal"}`
		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/trigger", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, expectedID.String(), resp["instanceId"])
		assert.Equal(t, "running", resp["state"])
	})

	t.Run("returns 400 for invalid JSON body", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{invalid json`
		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/trigger", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unknown workflow", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		svc := &mockWorkflowService{
			triggerFn: func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
				return uuid.Nil, false, fmt.Errorf("failed to load workflow: workflow not found: unknown")
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, svc, newMockFileReader(), nil)

		body := `{"variables":{}}`
		req := httptest.NewRequest(http.MethodPost, "/workflows/unknown/trigger", strings.NewReader(body))
		req = withChiURLParam(req, "name", "unknown")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for missing required input", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()

		svc := &mockWorkflowService{
			triggerFn: func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
				return uuid.Nil, false, fmt.Errorf("missing required input: name")
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, svc, newMockFileReader(), nil)

		body := `{"variables":{}}`
		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/trigger", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("defaults priority to normal when empty", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()

		svc := &mockWorkflowService{
			triggerFn: func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
				assert.Equal(t, "normal", priority)
				return uuid.New(), true, nil
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, svc, newMockFileReader(), nil)

		body := `{"variables":{"name":"Alice"}}`
		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/trigger", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("triggers with empty body", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()

		svc := &mockWorkflowService{
			triggerFn: func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
				return uuid.New(), true, nil
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, svc, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/trigger", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("returns 202 Accepted with state=queued when not started immediately", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()

		expectedID := uuid.New()
		svc := &mockWorkflowService{
			triggerFn: func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
				return expectedID, false, nil // started=false means queued
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, svc, newMockFileReader(), nil)

		body := `{"variables":{"name":"Alice"}}`
		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/trigger", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, expectedID.String(), resp["instanceId"])
		assert.Equal(t, "queued", resp["state"])
	})

	t.Run("returns 500 when service fails with unexpected error", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = testWorkflow()
		svc := &mockWorkflowService{
			triggerFn: func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
				return uuid.Nil, false, fmt.Errorf("database connection lost")
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, svc, newMockFileReader(), nil)

		body := `{"variables":{"name":"Alice"}}`
		req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/trigger", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "INTERNAL_ERROR", resp.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: internal errors from repo
// ---------------------------------------------------------------------------

func TestWorkflowHandler_List_RepoError(t *testing.T) {
	h := adapthttp.NewWorkflowHandler(&errWorkflowRepo{err: fmt.Errorf("db down")}, &mockWorkflowService{}, newMockFileReader(), nil)

	req := httptest.NewRequest(http.MethodGet, "/workflows", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "INTERNAL_ERROR", resp.Code)
}

func TestWorkflowHandler_Get_InternalError(t *testing.T) {
	h := adapthttp.NewWorkflowHandler(&errWorkflowRepo{err: fmt.Errorf("db down")}, &mockWorkflowService{}, newMockFileReader(), nil)

	req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf", nil)
	req = withChiURLParam(req, "name", "test-wf")
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "INTERNAL_ERROR", resp.Code)
}

func TestWorkflowHandler_Validate_InternalError(t *testing.T) {
	h := adapthttp.NewWorkflowHandler(&errWorkflowRepo{err: fmt.Errorf("db down")}, &mockWorkflowService{}, newMockFileReader(), nil)

	req := httptest.NewRequest(http.MethodPost, "/workflows/test-wf/validate", nil)
	req = withChiURLParam(req, "name", "test-wf")
	w := httptest.NewRecorder()
	h.Validate(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "INTERNAL_ERROR", resp.Code)
}
