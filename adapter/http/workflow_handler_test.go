package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ensure mock repos implement the interface at compile time
var _ workflow.WorkflowRepository = (*mockWorkflowRepo)(nil)
var _ workflow.WorkflowRepository = (*errWorkflowRepo)(nil)

// ---------------------------------------------------------------------------
// Mocks for WorkflowHandler
// ---------------------------------------------------------------------------

// mockWorkflowRepo is scope-aware: the flat `workflows` map is the caller's
// PRIVATE store (existing tests populate it directly); `shared` is the shared
// store. Tests that want a shared workflow populate `shared`.
type mockWorkflowRepo struct {
	workflows map[string]*workflow.Workflow
	shared    map[string]*workflow.Workflow
}

func newMockWorkflowRepo() *mockWorkflowRepo {
	return &mockWorkflowRepo{
		workflows: make(map[string]*workflow.Workflow),
		shared:    make(map[string]*workflow.Workflow),
	}
}

func (r *mockWorkflowRepo) bucket(s workflow.Scope) map[string]*workflow.Workflow {
	if s.Kind == workflow.ScopeShared {
		return r.shared
	}
	return r.workflows
}

func (r *mockWorkflowRepo) Load(_ context.Context, s workflow.Scope, name string) (*workflow.Workflow, error) {
	wf, ok := r.bucket(s)[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return wf, nil
}

func (r *mockWorkflowRepo) List(_ context.Context, s workflow.Scope) ([]workflow.WorkflowSummary, error) {
	var summaries []workflow.WorkflowSummary
	for _, wf := range r.bucket(s) {
		summaries = append(summaries, workflow.WorkflowSummary{
			Name:        wf.Name,
			Description: wf.Description,
			Author:      wf.Author,
			NodeCount:   len(wf.Nodes),
		})
	}
	return summaries, nil
}

func (r *mockWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}

func (r *mockWorkflowRepo) Create(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return nil
}
func (r *mockWorkflowRepo) Update(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return nil
}
func (r *mockWorkflowRepo) Delete(_ context.Context, _ workflow.Scope, _ string) error { return nil }
func (r *mockWorkflowRepo) WriteFile(_ context.Context, _ workflow.Scope, _ string, _ string, _ []byte) error {
	return nil
}
func (r *mockWorkflowRepo) DeleteFile(_ context.Context, _ workflow.Scope, _ string, _ string) error {
	return nil
}
func (r *mockWorkflowRepo) ListFiles(_ context.Context, _ workflow.Scope, _ string) ([]string, error) {
	return nil, nil
}
func (r *mockWorkflowRepo) Exists(_ context.Context, s workflow.Scope, name string) (bool, error) {
	_, ok := r.bucket(s)[name]
	return ok, nil
}
func (r *mockWorkflowRepo) CopyTree(_ context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	wf, ok := r.bucket(src)[srcName]
	if !ok {
		return workflow.ErrNotFound
	}
	if _, exists := r.bucket(dst)[dstName]; exists && !overwrite {
		return workflow.ErrAlreadyExists
	}
	cp := *wf
	cp.Name = dstName
	r.bucket(dst)[dstName] = &cp
	return nil
}

type errWorkflowRepo struct{ err error }

func (r *errWorkflowRepo) Load(_ context.Context, _ workflow.Scope, _ string) (*workflow.Workflow, error) {
	return nil, r.err
}
func (r *errWorkflowRepo) List(_ context.Context, _ workflow.Scope) ([]workflow.WorkflowSummary, error) {
	return nil, r.err
}
func (r *errWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return r.err
}
func (r *errWorkflowRepo) Create(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return r.err
}
func (r *errWorkflowRepo) Update(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return r.err
}
func (r *errWorkflowRepo) Delete(_ context.Context, _ workflow.Scope, _ string) error { return r.err }
func (r *errWorkflowRepo) WriteFile(_ context.Context, _ workflow.Scope, _ string, _ string, _ []byte) error {
	return r.err
}
func (r *errWorkflowRepo) DeleteFile(_ context.Context, _ workflow.Scope, _ string, _ string) error {
	return r.err
}
func (r *errWorkflowRepo) ListFiles(_ context.Context, _ workflow.Scope, _ string) ([]string, error) {
	return nil, r.err
}
func (r *errWorkflowRepo) Exists(_ context.Context, _ workflow.Scope, _ string) (bool, error) {
	return false, r.err
}
func (r *errWorkflowRepo) CopyTree(_ context.Context, _ workflow.Scope, _ string, _ workflow.Scope, _ string, _ bool) error {
	return r.err
}

type mockWorkflowService struct {
	triggerFn          func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
	capturedTriggerCtx context.Context
	capturedDepartment string
}

func (m *mockWorkflowService) TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority string, department string) (uuid.UUID, bool, error) {
	m.capturedTriggerCtx = ctx
	m.capturedDepartment = department
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

func (r *mockFileReader) ReadFile(_ uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	return nil, nil
}

func (r *mockFileReader) ResolvePath(_ uuid.UUID, workflowName, relativePath string) (string, error) {
	return "", nil
}

func (r *mockFileReader) FileExists(_ uuid.UUID, workflowName, relativePath string) bool {
	key := workflowName + "/" + relativePath
	return r.existingFiles[key]
}

// ---------------------------------------------------------------------------
// Helper to create chi context with URL params
// ---------------------------------------------------------------------------

// testUserID is the default authenticated caller injected into handler test
// requests; the Phase 1 auth middleware normally supplies this in production.
var testUserID = uuid.MustParse("99999999-9999-9999-9999-999999999999")

// withTestIdentity injects a default authenticated identity into the request
// context so handlers that resolve the caller's scope via
// identity.MustFromContext do not panic. The production auth middleware
// guarantees this presence.
func withTestIdentity(r *http.Request) *http.Request {
	return r.WithContext(identity.WithIdentity(r.Context(), identity.Identity{
		UserID:   testUserID,
		Username: "tester",
		Role:     identity.RoleUser,
	}))
}

// withAdminIdentity injects an admin identity (testUserID), overriding any
// existing one, so role-gated publish/visibility handler tests can promote to
// public without a lead/admin lookup.
func withAdminIdentity(r *http.Request) *http.Request {
	return r.WithContext(identity.WithIdentity(r.Context(), identity.Identity{
		UserID:   testUserID,
		Username: "admin",
		Role:     identity.RoleAdmin,
	}))
}

func withChiURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		rctx = chi.NewRouteContext()
	}
	rctx.URLParams.Add(key, value)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	r = r.WithContext(ctx)
	// Ensure every handler request carries an authenticated identity.
	if _, ok := identity.FromContext(r.Context()); !ok {
		r = withTestIdentity(r)
	}
	return r
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
		req = withTestIdentity(req)
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
		req = withTestIdentity(req)
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

	t.Run("threads injected profile checker into validation", func(t *testing.T) {
		// A workflow whose llm_call references an agent-kind profile must fail
		// validation with PROFILE_KIND_MISMATCH — but only when a ProfileChecker
		// is wired in. This proves the handler forwards its injected checker to
		// workflow.Validate (rather than passing nil and silently skipping the
		// model_profile pass).
		repo := newMockWorkflowRepo()
		repo.workflows["mismatch-wf"] = &workflow.Workflow{
			Name: "mismatch-wf",
			Nodes: []workflow.Node{
				{
					ID:         "llm",
					Type:       workflow.NodeTypeLLMCall,
					Entrypoint: true,
					Config:     workflow.NodeConfig{ModelProfile: "an-agent"},
				},
			},
		}
		checker := stubProfileChecker{"an-agent": "agent"}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil, checker)

		req := httptest.NewRequest(http.MethodPost, "/workflows/mismatch-wf/validate", nil)
		req = withChiURLParam(req, "name", "mismatch-wf")
		w := httptest.NewRecorder()
		h.Validate(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, false, resp["valid"])
		errs, ok := resp["errors"].([]any)
		require.True(t, ok)
		var codes []string
		for _, e := range errs {
			if m, ok := e.(map[string]any); ok {
				codes = append(codes, m["code"].(string))
			}
		}
		assert.Contains(t, codes, "PROFILE_KIND_MISMATCH")
	})

	t.Run("skips model_profile pass when no checker injected", func(t *testing.T) {
		// Same mismatched workflow, but with no ProfileChecker: the model_profile
		// pass is skipped, so no PROFILE_KIND_MISMATCH appears.
		repo := newMockWorkflowRepo()
		repo.workflows["mismatch-wf"] = &workflow.Workflow{
			Name: "mismatch-wf",
			Nodes: []workflow.Node{
				{
					ID:         "llm",
					Type:       workflow.NodeTypeLLMCall,
					Entrypoint: true,
					Config:     workflow.NodeConfig{ModelProfile: "an-agent"},
				},
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPost, "/workflows/mismatch-wf/validate", nil)
		req = withChiURLParam(req, "name", "mismatch-wf")
		w := httptest.NewRecorder()
		h.Validate(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		errs, _ := resp["errors"].([]any)
		for _, e := range errs {
			if m, ok := e.(map[string]any); ok {
				assert.NotEqual(t, "PROFILE_KIND_MISMATCH", m["code"])
			}
		}
	})
}

// stubProfileChecker is a minimal workflow.ProfileChecker for handler tests: it
// maps an alias to its capability kind. An unknown alias returns ok=false.
type stubProfileChecker map[string]string

func (s stubProfileChecker) ProfileKind(alias string) (string, bool) {
	kind, ok := s[alias]
	return kind, ok
}

// stubConnChecker is a minimal workflow.ConnectionChecker for handler tests.
type stubConnChecker struct {
	refs    map[string]string // alias -> connectionRef
	present map[string]bool   // connectionRef -> exists
}

func (s stubConnChecker) ProfileConnectionRef(alias string) (string, bool) {
	ref, ok := s.refs[alias]
	return ref, ok
}

func (s stubConnChecker) ConnectionExists(name string) bool { return s.present[name] }

// A connection checker injected via SetConnectionChecker is threaded into
// validation: an llm_call whose profile references a missing connection yields
// UNKNOWN_CONNECTION.
func TestWorkflowHandler_Validate_ThreadsConnectionChecker(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.workflows["conn-wf"] = &workflow.Workflow{
		Name: "conn-wf",
		Nodes: []workflow.Node{
			{
				ID:         "llm",
				Type:       workflow.NodeTypeLLMCall,
				Entrypoint: true,
				Config:     workflow.NodeConfig{ModelProfile: "az-chat"},
			},
		},
	}
	pc := stubProfileChecker{"az-chat": "chat"}
	cc := stubConnChecker{
		refs:    map[string]string{"az-chat": "azure-prod"},
		present: map[string]bool{}, // azure-prod missing
	}
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil, pc)
	h.SetConnectionChecker(cc)

	req := httptest.NewRequest(http.MethodPost, "/workflows/conn-wf/validate", nil)
	req = withChiURLParam(req, "name", "conn-wf")
	w := httptest.NewRecorder()
	h.Validate(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, false, resp["valid"])
	errs, _ := resp["errors"].([]any)
	var codes []string
	for _, e := range errs {
		if m, ok := e.(map[string]any); ok {
			codes = append(codes, m["code"].(string))
		}
	}
	assert.Contains(t, codes, "UNKNOWN_CONNECTION")
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
				return uuid.Nil, false, fmt.Errorf("failed to load workflow: %w", fmt.Errorf("%w: unknown", workflow.ErrNotFound))
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

	t.Run("caller identity context is forwarded to service", func(t *testing.T) {
		// Verifies that h.service.Trigger receives r.Context() — not context.Background() —
		// so the auth middleware's identity is present when the engine resolves the owner.
		svc := &mockWorkflowService{}
		h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), svc, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPost, "/workflows/wf/trigger", nil)
		req = withChiURLParam(req, "name", "wf") // also injects testIdentity via withTestIdentity
		w := httptest.NewRecorder()
		h.Trigger(w, req)

		require.NotNil(t, svc.capturedTriggerCtx, "context must be forwarded to service")
		id, ok := identity.FromContext(svc.capturedTriggerCtx)
		require.True(t, ok, "service context must carry the caller identity")
		assert.Equal(t, testUserID, id.UserID, "forwarded identity must be the authenticated caller")
	})
}

// ---------------------------------------------------------------------------
// Tests: internal errors from repo
// ---------------------------------------------------------------------------

func TestWorkflowHandler_List_RepoError(t *testing.T) {
	h := adapthttp.NewWorkflowHandler(&errWorkflowRepo{err: fmt.Errorf("db down")}, &mockWorkflowService{}, newMockFileReader(), nil)

	req := httptest.NewRequest(http.MethodGet, "/workflows", nil)
	req = withTestIdentity(req)
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

// ---------------------------------------------------------------------------
// Helpers: configurable mocks for SaveLayout / Stats
// ---------------------------------------------------------------------------

// capturingWorkflowRepo records the positions passed to SaveNodePositions.
type capturingWorkflowRepo struct {
	mockWorkflowRepo
	savedPositions map[string]workflow.NodePosition
	saveErr        error
}

func newCapturingWorkflowRepo() *capturingWorkflowRepo {
	return &capturingWorkflowRepo{
		mockWorkflowRepo: *newMockWorkflowRepo(),
	}
}

func (r *capturingWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, name string, positions map[string]workflow.NodePosition) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.savedPositions = positions
	return nil
}

// statsInstanceRepo wraps mockInstanceRepo with a configurable Stats implementation.
type statsInstanceRepo struct {
	mockInstanceRepo
	statsFn func(ctx context.Context, workflowName string, from, to time.Time) (*instance.WorkflowStats, error)
}

func (r *statsInstanceRepo) Stats(ctx context.Context, workflowName string, from, to time.Time) (*instance.WorkflowStats, error) {
	if r.statsFn != nil {
		return r.statsFn(ctx, workflowName, from, to)
	}
	return &instance.WorkflowStats{}, nil
}

// ---------------------------------------------------------------------------
// Tests: SaveLayout
// ---------------------------------------------------------------------------

func TestWorkflowHandler_SaveLayout(t *testing.T) {
	t.Run("saves positions and returns 200", func(t *testing.T) {
		repo := newCapturingWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"positions":{"A":{"x":100,"y":200},"B":{"x":300,"y":400}}}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/layout", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.SaveLayout(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "saved", resp["status"])
		assert.InDelta(t, 100.0, repo.savedPositions["A"].X, 0.001)
		assert.InDelta(t, 200.0, repo.savedPositions["A"].Y, 0.001)
	})

	t.Run("returns 400 for empty positions map", func(t *testing.T) {
		repo := newCapturingWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"positions":{}}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/layout", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.SaveLayout(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("returns 400 for invalid JSON body", func(t *testing.T) {
		repo := newCapturingWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/layout", strings.NewReader("{not json"))
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.SaveLayout(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 when repo returns not-found error", func(t *testing.T) {
		repo := newCapturingWorkflowRepo()
		repo.saveErr = fmt.Errorf("%w: test-wf", workflow.ErrNotFound)
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"positions":{"A":{"x":10,"y":20}}}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/layout", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.SaveLayout(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
	})

	t.Run("returns 500 when repo returns unexpected error", func(t *testing.T) {
		repo := newCapturingWorkflowRepo()
		repo.saveErr = fmt.Errorf("disk full")
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"positions":{"A":{"x":10,"y":20}}}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/layout", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.SaveLayout(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("returns 404 for unsafe workflow name with path separator", func(t *testing.T) {
		repo := newCapturingWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"positions":{"A":{"x":10,"y":20}}}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/../secret/layout", strings.NewReader(body))
		req = withChiURLParam(req, "name", "../secret")
		w := httptest.NewRecorder()
		h.SaveLayout(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Stats
// ---------------------------------------------------------------------------

func TestWorkflowHandler_Stats(t *testing.T) {
	t.Run("returns stats with default date range", func(t *testing.T) {
		instRepo := &statsInstanceRepo{
			mockInstanceRepo: *newMockInstanceRepo(),
			statsFn: func(_ context.Context, name string, from, to time.Time) (*instance.WorkflowStats, error) {
				assert.Equal(t, "test-wf", name)
				assert.True(t, to.After(from))
				return &instance.WorkflowStats{
					TotalRuns:       10,
					SuccessCount:    8,
					FailureCount:    2,
					AvgDurationSecs: 3.5,
				}, nil
			},
		}
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = &workflow.Workflow{Name: "test-wf"}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/stats", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Stats(w, withTestIdentity(req))

		assert.Equal(t, http.StatusOK, w.Code)
		var resp instance.WorkflowStats
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, 10, resp.TotalRuns)
		assert.Equal(t, 8, resp.SuccessCount)
		assert.Equal(t, 2, resp.FailureCount)
		assert.InDelta(t, 3.5, resp.AvgDurationSecs, 0.001)
	})

	t.Run("parses explicit from and to query params", func(t *testing.T) {
		wantFrom := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		wantTo := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
		instRepo := &statsInstanceRepo{
			mockInstanceRepo: *newMockInstanceRepo(),
			statsFn: func(_ context.Context, _ string, from, to time.Time) (*instance.WorkflowStats, error) {
				assert.Equal(t, wantFrom.UTC(), from.UTC())
				assert.Equal(t, wantTo.UTC(), to.UTC())
				return &instance.WorkflowStats{TotalRuns: 5}, nil
			},
		}
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = &workflow.Workflow{Name: "test-wf"}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/stats?from=2025-01-01T00:00:00Z&to=2025-02-01T00:00:00Z", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Stats(w, withTestIdentity(req))

		assert.Equal(t, http.StatusOK, w.Code)
		var resp instance.WorkflowStats
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, 5, resp.TotalRuns)
	})

	t.Run("returns 400 for invalid from param", func(t *testing.T) {
		instRepo := &statsInstanceRepo{mockInstanceRepo: *newMockInstanceRepo()}
		h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/stats?from=not-a-date", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Stats(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("returns 400 for invalid to param", func(t *testing.T) {
		instRepo := &statsInstanceRepo{mockInstanceRepo: *newMockInstanceRepo()}
		h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/stats?to=not-a-date", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Stats(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 500 when instances repo is nil", func(t *testing.T) {
		h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/stats", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Stats(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("returns 500 when Stats repo call fails", func(t *testing.T) {
		instRepo := &statsInstanceRepo{
			mockInstanceRepo: *newMockInstanceRepo(),
			statsFn: func(_ context.Context, _ string, _, _ time.Time) (*instance.WorkflowStats, error) {
				return nil, fmt.Errorf("db timeout")
			},
		}
		repo := newMockWorkflowRepo()
		repo.workflows["test-wf"] = &workflow.Workflow{Name: "test-wf"}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/stats", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Stats(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	// IDOR / broken-access-control regression: a caller requesting stats for a
	// workflow they cannot see (not their private, not a shared publication they
	// pass CanSeePublication for) must get 404 — never 200, and never reach the
	// stats reader (no run-count / cadence / existence oracle leak).
	t.Run("returns 404 and skips the reader when the workflow is not visible", func(t *testing.T) {
		readerCalled := false
		instRepo := &statsInstanceRepo{
			mockInstanceRepo: *newMockInstanceRepo(),
			statsFn: func(_ context.Context, _ string, _, _ time.Time) (*instance.WorkflowStats, error) {
				readerCalled = true
				return &instance.WorkflowStats{TotalRuns: 99}, nil
			},
		}
		// Empty repo: the workflow exists nowhere the caller can see.
		h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet, "/workflows/someone-elses-wf/stats", nil)
		req = withChiURLParam(req, "name", "someone-elses-wf")
		w := httptest.NewRecorder()
		h.Stats(w, withTestIdentity(req))

		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.False(t, readerCalled, "stats reader must not run for a denied caller")
	})

	t.Run("returns 200 for a workflow the caller owns privately", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["mine"] = &workflow.Workflow{Name: "mine"}
		instRepo := &statsInstanceRepo{
			mockInstanceRepo: *newMockInstanceRepo(),
			statsFn: func(_ context.Context, name string, _, _ time.Time) (*instance.WorkflowStats, error) {
				assert.Equal(t, "mine", name)
				return &instance.WorkflowStats{TotalRuns: 4}, nil
			},
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet, "/workflows/mine/stats", nil)
		req = withChiURLParam(req, "name", "mine")
		w := httptest.NewRecorder()
		h.Stats(w, withTestIdentity(req))

		assert.Equal(t, http.StatusOK, w.Code)
		var resp instance.WorkflowStats
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, 4, resp.TotalRuns)
	})

	t.Run("returns 400 for an inverted from/to range", func(t *testing.T) {
		repo := newMockWorkflowRepo()
		repo.workflows["mine"] = &workflow.Workflow{Name: "mine"}
		instRepo := &statsInstanceRepo{mockInstanceRepo: *newMockInstanceRepo()}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), instRepo)

		req := httptest.NewRequest(http.MethodGet,
			"/workflows/mine/stats?from=2025-02-01T00:00:00Z&to=2025-01-01T00:00:00Z", nil)
		req = withChiURLParam(req, "name", "mine")
		w := httptest.NewRecorder()
		h.Stats(w, withTestIdentity(req))

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Helpers: configurable CRUD mock for Create/Update/Delete/Files
// ---------------------------------------------------------------------------

// crudWorkflowRepo wraps mockWorkflowRepo with configurable error responses
// for the new CRUD and file management methods.
type crudWorkflowRepo struct {
	mockWorkflowRepo
	createFn     func(ctx context.Context, name string, wf *workflow.Workflow) error
	updateFn     func(ctx context.Context, name string, wf *workflow.Workflow) error
	deleteFn     func(ctx context.Context, name string) error
	writeFileFn  func(ctx context.Context, name, path string, data []byte) error
	deleteFileFn func(ctx context.Context, name, path string) error
	listFilesFn  func(ctx context.Context, name string) ([]string, error)
}

func newCrudWorkflowRepo() *crudWorkflowRepo {
	return &crudWorkflowRepo{
		mockWorkflowRepo: *newMockWorkflowRepo(),
	}
}

func (r *crudWorkflowRepo) Create(ctx context.Context, _ workflow.Scope, name string, wf *workflow.Workflow) error {
	if r.createFn != nil {
		return r.createFn(ctx, name, wf)
	}
	return nil
}

func (r *crudWorkflowRepo) Update(ctx context.Context, _ workflow.Scope, name string, wf *workflow.Workflow) error {
	if r.updateFn != nil {
		return r.updateFn(ctx, name, wf)
	}
	return nil
}

func (r *crudWorkflowRepo) Delete(ctx context.Context, _ workflow.Scope, name string) error {
	if r.deleteFn != nil {
		return r.deleteFn(ctx, name)
	}
	return nil
}

func (r *crudWorkflowRepo) WriteFile(ctx context.Context, _ workflow.Scope, name, path string, data []byte) error {
	if r.writeFileFn != nil {
		return r.writeFileFn(ctx, name, path, data)
	}
	return nil
}

func (r *crudWorkflowRepo) DeleteFile(ctx context.Context, _ workflow.Scope, name, path string) error {
	if r.deleteFileFn != nil {
		return r.deleteFileFn(ctx, name, path)
	}
	return nil
}

func (r *crudWorkflowRepo) ListFiles(ctx context.Context, _ workflow.Scope, name string) ([]string, error) {
	if r.listFilesFn != nil {
		return r.listFilesFn(ctx, name)
	}
	return []string{}, nil
}

// scopeCapturingWorkflowRepo wraps mockWorkflowRepo and records the last
// non-shared scope passed to Create and List so tests can assert that the
// private workspace scope is derived from the request context identity.
type scopeCapturingWorkflowRepo struct {
	mockWorkflowRepo
	lastPrivateScope workflow.Scope
}

func newScopeCapturingWorkflowRepo() *scopeCapturingWorkflowRepo {
	return &scopeCapturingWorkflowRepo{mockWorkflowRepo: *newMockWorkflowRepo()}
}

func (r *scopeCapturingWorkflowRepo) Create(_ context.Context, s workflow.Scope, _ string, _ *workflow.Workflow) error {
	r.lastPrivateScope = s
	return nil
}

func (r *scopeCapturingWorkflowRepo) List(_ context.Context, s workflow.Scope) ([]workflow.WorkflowSummary, error) {
	if s.Kind == workflow.ScopePrivate {
		r.lastPrivateScope = s
	}
	return nil, nil
}

// TestWorkflowHandler_ScopeFromContextIdentity pins the FA-30 paired invariant:
// the private workspace scope used by every operation is derived solely from the
// authenticated caller's identity in the request context — not from any URL
// parameter or request body field. Two callers with different identities have
// completely separate private scopes.
func TestWorkflowHandler_ScopeFromContextIdentity(t *testing.T) {
	repo := newScopeCapturingWorkflowRepo()
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

	userA := uuid.New()
	userB := uuid.New()

	// User A creates a workflow — private scope OwnerID must be userA.
	reqA := httptest.NewRequest(http.MethodPost, "/workflows",
		strings.NewReader(`{"name":"wf-a","description":"A's workflow"}`))
	reqA = reqA.WithContext(identity.WithIdentity(reqA.Context(), identity.Identity{
		UserID: userA, Role: identity.RoleUser,
	}))
	wA := httptest.NewRecorder()
	h.Create(wA, reqA)
	require.Equal(t, http.StatusCreated, wA.Code)
	assert.Equal(t, workflow.ScopePrivate, repo.lastPrivateScope.Kind)
	assert.Equal(t, userA, repo.lastPrivateScope.OwnerID,
		"private scope must be keyed to user A (the authenticated caller)")

	// User B makes a List call — the private list scope must be userB, not userA.
	reqB := httptest.NewRequest(http.MethodGet, "/workflows", nil)
	reqB = reqB.WithContext(identity.WithIdentity(reqB.Context(), identity.Identity{
		UserID: userB, Role: identity.RoleUser,
	}))
	wB := httptest.NewRecorder()
	h.List(wB, reqB)
	require.Equal(t, http.StatusOK, wB.Code)
	assert.Equal(t, workflow.ScopePrivate, repo.lastPrivateScope.Kind)
	assert.Equal(t, userB, repo.lastPrivateScope.OwnerID,
		"private scope must switch to user B's identity — scope is context-derived, not global")
	assert.NotEqual(t, userA, repo.lastPrivateScope.OwnerID,
		"user B must not inherit user A's scope")
}

// contentFileReader extends mockFileReader with configurable ReadFile content.
// capturedOwner records the last owner UUID passed to ReadFile so tests can
// verify that callerOwner(ctx) is correctly threaded through.
type contentFileReader struct {
	mockFileReader
	readFileFn    func(workflowName, relativePath string) ([]byte, error)
	capturedOwner uuid.UUID
}

func newContentFileReader() *contentFileReader {
	return &contentFileReader{
		mockFileReader: *newMockFileReader(),
	}
}

func (r *contentFileReader) ReadFile(owner uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	r.capturedOwner = owner
	if r.readFileFn != nil {
		return r.readFileFn(workflowName, relativePath)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Tests: Create workflow
// ---------------------------------------------------------------------------

func TestWorkflowHandler_Create(t *testing.T) {
	t.Run("creates workflow and returns 201", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"new-wf","description":"A new workflow","author":"tester","nodes":[],"edges":[]}`
		req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, withTestIdentity(req))

		assert.Equal(t, http.StatusCreated, w.Code)
		var resp workflow.Workflow
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "new-wf", resp.Name)
	})

	t.Run("returns 409 for duplicate workflow", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.createFn = func(_ context.Context, _ string, _ *workflow.Workflow) error {
			return fmt.Errorf("%w: new-wf", workflow.ErrAlreadyExists)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"new-wf","description":"dup"}`
		req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, withTestIdentity(req))

		assert.Equal(t, http.StatusConflict, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "CONFLICT", resp.Code)
	})

	t.Run("returns 400 for malformed JSON", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader("{not json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, withTestIdentity(req))

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 400 for invalid workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"../bad-name","description":"traversal"}`
		req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, withTestIdentity(req))

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("returns 400 for empty workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"","description":"no name"}`
		req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, withTestIdentity(req))

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 400 for dot workflow name", func(t *testing.T) {
		// "." resolves to the workspace root — must be rejected as a bad request,
		// not allowed through to the repo where it would return an opaque 500.
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":".","description":"dot name"}`
		req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, withTestIdentity(req))

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("returns 500 for unexpected repo error", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.createFn = func(_ context.Context, _ string, _ *workflow.Workflow) error {
			return fmt.Errorf("disk full")
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"new-wf","description":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, withTestIdentity(req))

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Update workflow
// ---------------------------------------------------------------------------

func TestWorkflowHandler_Update(t *testing.T) {
	t.Run("updates workflow and returns 200", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"test-wf","description":"Updated description"}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Update(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp workflow.Workflow
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "Updated description", resp.Description)
	})

	t.Run("returns 404 for non-existent workflow", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.updateFn = func(_ context.Context, _ string, _ *workflow.Workflow) error {
			return fmt.Errorf("%w: unknown", workflow.ErrNotFound)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"unknown","description":"test"}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/unknown", strings.NewReader(body))
		req = withChiURLParam(req, "name", "unknown")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Update(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
	})

	t.Run("returns 400 for malformed JSON", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf", strings.NewReader("{bad"))
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Update(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unsafe workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"test"}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/../secret", strings.NewReader(body))
		req = withChiURLParam(req, "name", "../secret")
		w := httptest.NewRecorder()
		h.Update(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 for unexpected repo error", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.updateFn = func(_ context.Context, _ string, _ *workflow.Workflow) error {
			return fmt.Errorf("disk full")
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"test-wf","description":"test"}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf", strings.NewReader(body))
		req = withChiURLParam(req, "name", "test-wf")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Update(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("URL name wins when body name differs", func(t *testing.T) {
		// If the body contains a different name than the URL, the URL is authoritative.
		// This prevents workflow.json from getting a name that diverges from its directory.
		var capturedWF *workflow.Workflow
		repo := newCrudWorkflowRepo()
		repo.updateFn = func(_ context.Context, _ string, wf *workflow.Workflow) error {
			capturedWF = wf
			return nil
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"wrong-name","description":"updated"}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/real-name", strings.NewReader(body))
		req = withChiURLParam(req, "name", "real-name")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Update(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, capturedWF)
		assert.Equal(t, "real-name", capturedWF.Name)

		var resp workflow.Workflow
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "real-name", resp.Name)
	})

	// Failure: updating a shared-only workflow returns 409 Conflict, not 404.
	// The service maps ErrNotFound-on-private + Exists-in-shared → ErrSharedReadOnly.
	t.Run("returns 409 when workflow is shared-only", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.updateFn = func(_ context.Context, _ string, _ *workflow.Workflow) error {
			return fmt.Errorf("%w: pub", workflow.ErrSharedReadOnly)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		body := `{"name":"pub","description":"changed"}`
		req := httptest.NewRequest(http.MethodPut, "/workflows/pub", strings.NewReader(body))
		req = withChiURLParam(req, "name", "pub")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Update(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "CONFLICT", resp.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Delete workflow
// ---------------------------------------------------------------------------

func TestWorkflowHandler_Delete(t *testing.T) {
	t.Run("deletes workflow and returns 204", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/test-wf", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Delete(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("returns 404 for non-existent workflow", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.deleteFn = func(_ context.Context, _ string) error {
			return fmt.Errorf("%w: unknown", workflow.ErrNotFound)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/unknown", nil)
		req = withChiURLParam(req, "name", "unknown")
		w := httptest.NewRecorder()
		h.Delete(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 404 for unsafe workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/../secret", nil)
		req = withChiURLParam(req, "name", "../secret")
		w := httptest.NewRecorder()
		h.Delete(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 for unexpected repo error", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.deleteFn = func(_ context.Context, _ string) error {
			return fmt.Errorf("disk full")
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/test-wf", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.Delete(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	// Failure: deleting a shared-only workflow returns 409 Conflict, not 404.
	t.Run("returns 409 when workflow is shared-only", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.deleteFn = func(_ context.Context, _ string) error {
			return fmt.Errorf("%w: pub", workflow.ErrSharedReadOnly)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/pub", nil)
		req = withChiURLParam(req, "name", "pub")
		w := httptest.NewRecorder()
		h.Delete(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "CONFLICT", resp.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: ListFiles
// ---------------------------------------------------------------------------

func TestWorkflowHandler_ListFiles(t *testing.T) {
	t.Run("returns file list as JSON array", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.listFilesFn = func(_ context.Context, _ string) ([]string, error) {
			return []string{"workflow.yaml", "scripts/run.sh"}, nil
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.ListFiles(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, []string{"workflow.yaml", "scripts/run.sh"}, resp)
	})

	t.Run("returns empty array when no files", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		// Default listFilesFn returns []string{}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.ListFiles(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Empty(t, resp)
	})

	t.Run("returns empty array when repo returns nil slice", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.listFilesFn = func(_ context.Context, _ string) ([]string, error) {
			return nil, nil
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.ListFiles(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Empty(t, resp)
	})

	t.Run("returns 404 for non-existent workflow", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.listFilesFn = func(_ context.Context, _ string) ([]string, error) {
			return nil, fmt.Errorf("%w: unknown", workflow.ErrNotFound)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/unknown/files", nil)
		req = withChiURLParam(req, "name", "unknown")
		w := httptest.NewRecorder()
		h.ListFiles(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 for unexpected repo error", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.listFilesFn = func(_ context.Context, _ string) ([]string, error) {
			return nil, fmt.Errorf("disk error")
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files", nil)
		req = withChiURLParam(req, "name", "test-wf")
		w := httptest.NewRecorder()
		h.ListFiles(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("returns 404 for unsafe workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/../secret/files", nil)
		req = withChiURLParam(req, "name", "../secret")
		w := httptest.NewRecorder()
		h.ListFiles(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: ReadFile
// ---------------------------------------------------------------------------

func TestWorkflowHandler_ReadFile(t *testing.T) {
	t.Run("returns raw file bytes with octet-stream content type", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		fr := newContentFileReader()
		fr.readFileFn = func(wfName, path string) ([]byte, error) {
			assert.Equal(t, "test-wf", wfName)
			assert.Equal(t, "scripts/run.sh", path)
			return []byte("#!/bin/bash\necho hello"), nil
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, fr, nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files/scripts/run.sh", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "scripts/run.sh")
		w := httptest.NewRecorder()
		h.ReadFile(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/octet-stream", w.Header().Get("Content-Type"))
		assert.Equal(t, "#!/bin/bash\necho hello", w.Body.String())
	})

	t.Run("returns 404 for non-existent file", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		fr := newContentFileReader()
		fr.readFileFn = func(_, _ string) ([]byte, error) {
			return nil, fmt.Errorf("%w: missing.sh", workflow.ErrFileNotFound)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, fr, nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files/missing.sh", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "missing.sh")
		w := httptest.NewRecorder()
		h.ReadFile(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for empty file path", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files/", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "")
		w := httptest.NewRecorder()
		h.ReadFile(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 500 for unexpected read error", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		fr := newContentFileReader()
		fr.readFileFn = func(_, _ string) ([]byte, error) {
			return nil, fmt.Errorf("permission denied")
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, fr, nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files/secret.txt", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "secret.txt")
		w := httptest.NewRecorder()
		h.ReadFile(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("returns 404 for unsafe workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/../secret/files/data.txt", nil)
		req = withChiURLParam(req, "name", "../secret")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "data.txt")
		w := httptest.NewRecorder()
		h.ReadFile(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	// Edge: verify that the caller's UserID is threaded from the request context
	// through callerOwner() into the FileReader — a regression here would silently
	// serve files from the wrong owner's partition without any other test catching it.
	t.Run("caller identity UserID is forwarded to file reader as owner", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		fr := newContentFileReader()
		fr.readFileFn = func(_, _ string) ([]byte, error) { return []byte("data"), nil }
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, fr, nil)

		req := httptest.NewRequest(http.MethodGet, "/workflows/test-wf/files/readme.txt", nil)
		req = withTestIdentity(req) // injects testUserID
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "readme.txt")
		w := httptest.NewRecorder()
		h.ReadFile(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, testUserID, fr.capturedOwner,
			"ReadFile must receive the caller's UserID from context, not uuid.Nil or a different user")
	})
}

// ---------------------------------------------------------------------------
// Tests: WriteFile
// ---------------------------------------------------------------------------

func TestWorkflowHandler_WriteFile(t *testing.T) {
	t.Run("writes file and returns 200", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		var capturedData []byte
		repo.writeFileFn = func(_ context.Context, name, path string, data []byte) error {
			assert.Equal(t, "test-wf", name)
			assert.Equal(t, "scripts/run.sh", path)
			capturedData = data
			return nil
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		content := "#!/bin/bash\necho hello"
		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/files/scripts/run.sh", strings.NewReader(content))
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "scripts/run.sh")
		w := httptest.NewRecorder()
		h.WriteFile(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, content, string(capturedData))
	})

	t.Run("returns 400 for path traversal", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.writeFileFn = func(_ context.Context, _, _ string, _ []byte) error {
			return fmt.Errorf("%w: ../../etc/passwd", workflow.ErrInvalidFilePath)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/files/../../etc/passwd", strings.NewReader("bad"))
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "../../etc/passwd")
		w := httptest.NewRecorder()
		h.WriteFile(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("writes empty body successfully", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		var capturedData []byte
		repo.writeFileFn = func(_ context.Context, _, _ string, data []byte) error {
			capturedData = data
			return nil
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/files/empty.txt", strings.NewReader(""))
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "empty.txt")
		w := httptest.NewRecorder()
		h.WriteFile(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, capturedData)
	})

	t.Run("returns 400 for empty file path", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/files/", strings.NewReader("data"))
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "")
		w := httptest.NewRecorder()
		h.WriteFile(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 500 for unexpected repo error", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.writeFileFn = func(_ context.Context, _, _ string, _ []byte) error {
			return fmt.Errorf("disk full")
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPut, "/workflows/test-wf/files/test.txt", strings.NewReader("data"))
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "test.txt")
		w := httptest.NewRecorder()
		h.WriteFile(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("returns 404 for unsafe workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodPut, "/workflows/../secret/files/data.txt", strings.NewReader("data"))
		req = withChiURLParam(req, "name", "../secret")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "data.txt")
		w := httptest.NewRecorder()
		h.WriteFile(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: DeleteFile
// ---------------------------------------------------------------------------

func TestWorkflowHandler_DeleteFile(t *testing.T) {
	t.Run("deletes file and returns 204", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/test-wf/files/old.sh", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "old.sh")
		w := httptest.NewRecorder()
		h.DeleteFile(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("returns 404 for non-existent file", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.deleteFileFn = func(_ context.Context, _, _ string) error {
			return fmt.Errorf("%w: missing.sh", workflow.ErrFileNotFound)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/test-wf/files/missing.sh", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "missing.sh")
		w := httptest.NewRecorder()
		h.DeleteFile(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for empty file path", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/test-wf/files/", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "")
		w := httptest.NewRecorder()
		h.DeleteFile(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 400 for path traversal", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.deleteFileFn = func(_ context.Context, _, _ string) error {
			return fmt.Errorf("%w: ../../etc/passwd", workflow.ErrInvalidFilePath)
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/test-wf/files/../../etc/passwd", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "../../etc/passwd")
		w := httptest.NewRecorder()
		h.DeleteFile(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "BAD_REQUEST", resp.Code)
	})

	t.Run("returns 500 for unexpected repo error", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		repo.deleteFileFn = func(_ context.Context, _, _ string) error {
			return fmt.Errorf("io error")
		}
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/test-wf/files/test.txt", nil)
		req = withChiURLParam(req, "name", "test-wf")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "test.txt")
		w := httptest.NewRecorder()
		h.DeleteFile(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("returns 404 for unsafe workflow name", func(t *testing.T) {
		repo := newCrudWorkflowRepo()
		h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)

		req := httptest.NewRequest(http.MethodDelete, "/workflows/../secret/files/data.txt", nil)
		req = withChiURLParam(req, "name", "../secret")
		rctx := chi.RouteContext(req.Context())
		rctx.URLParams.Add("*", "data.txt")
		w := httptest.NewRecorder()
		h.DeleteFile(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Publish / Copy
// ---------------------------------------------------------------------------

// fakePublishedStore is a workflow.PublishedStore for handler publish tests.
type fakePublishedStore struct {
	pub map[string]workflow.Publication
}

func newFakePublishedStore() *fakePublishedStore {
	return &fakePublishedStore{pub: map[string]workflow.Publication{}}
}

func (p *fakePublishedStore) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	pub, ok := p.pub[name]
	return pub.Publisher, ok, nil
}
func (p *fakePublishedStore) PublicationOf(_ context.Context, name string) (workflow.Publication, bool, error) {
	pub, ok := p.pub[name]
	return pub, ok, nil
}
func (p *fakePublishedStore) Upsert(_ context.Context, name string, publisher uuid.UUID, sourceName string, visibility workflow.Visibility, department string) error {
	if p.pub == nil {
		p.pub = map[string]workflow.Publication{}
	}
	p.pub[name] = workflow.Publication{Publisher: publisher, SourceName: sourceName, Visibility: visibility, Department: department}
	return nil
}
func (p *fakePublishedStore) Delete(_ context.Context, name string) error {
	delete(p.pub, name)
	return nil
}
func (p *fakePublishedStore) SetWorkflowID(_ context.Context, _ string, _ uuid.UUID) error {
	return nil
}

// publishHandler builds a WorkflowHandler whose service is backed by the given
// repo and publisher store.
func publishHandler(repo *mockWorkflowRepo, store *fakePublishedStore) *adapthttp.WorkflowHandler {
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, newMockFileReader(), nil)
	h.SetWorkflowService(workflow.NewWorkflowService(repo, store))
	return h
}

func TestWorkflowHandler_Publish_HappyPath(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.workflows["src"] = testWorkflow() // caller's private
	store := newFakePublishedStore()
	h := publishHandler(repo, store)

	body := `{"publishedName":"pub"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/src/publish", strings.NewReader(body))
	req = withChiURLParam(req, "name", "src")
	req = withAdminIdentity(req)
	w := httptest.NewRecorder()
	h.Publish(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	_, ok := repo.shared["pub"]
	assert.True(t, ok, "workflow copied into shared")
}

func TestWorkflowHandler_Publish_NotFound(t *testing.T) {
	repo := newMockWorkflowRepo()
	store := newFakePublishedStore()
	h := publishHandler(repo, store)

	body := `{"publishedName":"pub"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/ghost/publish", strings.NewReader(body))
	req = withChiURLParam(req, "name", "ghost")
	req = withAdminIdentity(req)
	w := httptest.NewRecorder()
	h.Publish(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestWorkflowHandler_Publish_DifferentPublisher_Forbidden(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.workflows["src"] = testWorkflow()
	repo.shared["pub"] = testWorkflow()
	store := newFakePublishedStore()
	store.pub["pub"] = workflow.Publication{Publisher: uuid.New(), Visibility: workflow.VisibilityPublic} // owned by someone else (not testUserID)
	h := publishHandler(repo, store)

	body := `{"publishedName":"pub"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/src/publish", strings.NewReader(body))
	req = withChiURLParam(req, "name", "src")
	req = withAdminIdentity(req)
	w := httptest.NewRecorder()
	h.Publish(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestWorkflowHandler_Publish_InvalidPublishedName(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.workflows["src"] = testWorkflow()
	h := publishHandler(repo, newFakePublishedStore())

	body := `{"publishedName":"../escape"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/src/publish", strings.NewReader(body))
	req = withChiURLParam(req, "name", "src")
	req = withAdminIdentity(req)
	w := httptest.NewRecorder()
	h.Publish(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestWorkflowHandler_Publish_MemberToPublic_Forbidden(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.workflows["src"] = testWorkflow()
	store := newFakePublishedStore()
	h := publishHandler(repo, store)

	// Default identity (withChiURLParam) is RoleUser with no led departments, so
	// promoting to public is forbidden.
	body := `{"publishedName":"pub","visibility":"public"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/src/publish", strings.NewReader(body))
	req = withChiURLParam(req, "name", "src")
	w := httptest.NewRecorder()
	h.Publish(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestWorkflowHandler_SetVisibility_LeadPromotesToPublic(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.shared["pub"] = testWorkflow()
	store := newFakePublishedStore()
	store.pub["pub"] = workflow.Publication{Publisher: testUserID, SourceName: "src", Visibility: workflow.VisibilityDepartment, Department: "engineering"}
	h := publishHandler(repo, store)

	body := `{"visibility":"public"}`
	req := httptest.NewRequest(http.MethodPut, "/workflows/pub/visibility", strings.NewReader(body))
	req = withChiURLParam(req, "name", "pub")
	// A lead of engineering may promote to public.
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{
		UserID: testUserID, Username: "lead", Role: identity.RoleUser,
		Departments: []string{"engineering"}, LedDepartments: []string{"engineering"},
	}))
	w := httptest.NewRecorder()
	h.SetVisibility(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, workflow.VisibilityPublic, store.pub["pub"].Visibility)
}

func TestWorkflowHandler_SetVisibility_MemberToPublic_Forbidden(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.shared["pub"] = testWorkflow()
	store := newFakePublishedStore()
	store.pub["pub"] = workflow.Publication{Publisher: testUserID, SourceName: "src", Visibility: workflow.VisibilityDepartment, Department: "engineering"}
	h := publishHandler(repo, store)

	body := `{"visibility":"public"}`
	req := httptest.NewRequest(http.MethodPut, "/workflows/pub/visibility", strings.NewReader(body))
	req = withChiURLParam(req, "name", "pub") // RoleUser, no led depts
	w := httptest.NewRecorder()
	h.SetVisibility(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestWorkflowHandler_Copy_HappyPath(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.shared["orig"] = testWorkflow()
	h := publishHandler(repo, newFakePublishedStore())

	body := `{"newName":"mine"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/orig/copy", strings.NewReader(body))
	req = withChiURLParam(req, "name", "orig")
	w := httptest.NewRecorder()
	h.Copy(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	_, ok := repo.workflows["mine"]
	assert.True(t, ok, "shared workflow copied into caller's private")
}

func TestWorkflowHandler_Copy_DuplicatePrivateName_Conflict(t *testing.T) {
	repo := newMockWorkflowRepo()
	repo.shared["orig"] = testWorkflow()
	repo.workflows["mine"] = testWorkflow() // already exists privately
	h := publishHandler(repo, newFakePublishedStore())

	body := `{"newName":"mine"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/orig/copy", strings.NewReader(body))
	req = withChiURLParam(req, "name", "orig")
	w := httptest.NewRecorder()
	h.Copy(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestWorkflowHandler_Copy_SharedMissing_NotFound(t *testing.T) {
	repo := newMockWorkflowRepo()
	h := publishHandler(repo, newFakePublishedStore())

	body := `{"newName":"mine"}`
	req := httptest.NewRequest(http.MethodPost, "/workflows/ghost/copy", strings.NewReader(body))
	req = withChiURLParam(req, "name", "ghost")
	w := httptest.NewRecorder()
	h.Copy(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
