package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Tests: POST /instances/{id}/interact/end — happy path
// ---------------------------------------------------------------------------

func TestInteractiveHandler_EndSession_Success(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()

	nodeID := "review-design"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	instRepo.instances[inst.ID] = inst

	// Create a node execution with output.
	nodeExecRepo.executions = []*instance.NodeExecution{
		{
			ID:         uuid.New(),
			InstanceID: inst.ID,
			NodeID:     "review-design",
			Attempt:    1,
			Status:     instance.NodeStatusSucceeded,
			Output:     map[string]any{"approved": true},
			StartedAt:  time.Now(),
		},
	}

	convo := &mockConversationHandler{}
	h := adapthttp.NewInteractiveHandler(convo, instRepo, nodeExecRepo)

	req := httptest.NewRequest(http.MethodPost, "/instances/"+inst.ID.String()+"/interact/end", nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.EndSession(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp["output"])
}

// ---------------------------------------------------------------------------
// Tests: POST /instances/{id}/interact/end — 404 not found
// ---------------------------------------------------------------------------

func TestInteractiveHandler_EndSession_NotFound(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()

	convo := &mockConversationHandler{
		endSessionFn: func(_ context.Context, _ uuid.UUID) error {
			return fmt.Errorf("conversation: load instance: %w", instance.ErrNotFound)
		},
	}
	h := adapthttp.NewInteractiveHandler(convo, instRepo, nodeExecRepo)

	id := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/interact/end", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.EndSession(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: POST /instances/{id}/interact/end — 409 invalid state
// ---------------------------------------------------------------------------

func TestInteractiveHandler_EndSession_InvalidState(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()

	convo := &mockConversationHandler{
		endSessionFn: func(_ context.Context, id uuid.UUID) error {
			return fmt.Errorf("conversation: instance %s not in interactive wait state (state: running)", id)
		},
	}
	h := adapthttp.NewInteractiveHandler(convo, instRepo, nodeExecRepo)

	id := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/interact/end", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.EndSession(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: POST /instances/{id}/interact/end — 500 unexpected error
// ---------------------------------------------------------------------------

func TestInteractiveHandler_EndSession_InternalError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()

	convo := &mockConversationHandler{
		endSessionFn: func(_ context.Context, _ uuid.UUID) error {
			return fmt.Errorf("db: connection reset by peer")
		},
	}
	h := adapthttp.NewInteractiveHandler(convo, instRepo, nodeExecRepo)

	id := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/interact/end", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.EndSession(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "INTERNAL_ERROR", resp["code"])
}

// ---------------------------------------------------------------------------
// Tests: POST /instances/{id}/interact/end — 400 bad UUID
// ---------------------------------------------------------------------------

func TestInteractiveHandler_EndSession_BadUUID(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	convo := &mockConversationHandler{}
	h := adapthttp.NewInteractiveHandler(convo, instRepo, nodeExecRepo)

	req := httptest.NewRequest(http.MethodPost, "/instances/bad-uuid/interact/end", nil)
	req = withChiURLParam(req, "id", "bad-uuid")
	w := httptest.NewRecorder()
	h.EndSession(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: Route wired in full router
// ---------------------------------------------------------------------------

func TestRouter_InteractEndRoute(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()

	nodeID := "review-design"
	inst := &instance.Instance{
		ID:            uuid.New(),
		OwnerID:       uuid.New(),
		WorkflowName:  "test-wf",
		State:         instance.StateWaiting,
		Priority:      instance.PriorityNormal,
		Variables:     map[string]any{},
		CurrentNodeID: &nodeID,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	instRepo.instances[inst.ID] = inst

	nodeExecRepo.executions = []*instance.NodeExecution{
		{
			ID:         uuid.New(),
			InstanceID: inst.ID,
			NodeID:     "review-design",
			Attempt:    1,
			Status:     instance.NodeStatusSucceeded,
			Output:     map[string]any{"result": "ok"},
			StartedAt:  time.Now(),
		},
	}

	convo := &mockConversationHandler{}
	router := adapthttp.NewRouterWithConversation(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, convo)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+inst.ID.String()+"/interact/end", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp["output"])
}

// ---------------------------------------------------------------------------
// Router integration: newStreamTestServerFull uses chi to mount handlers
// ---------------------------------------------------------------------------

func newInteractiveTestRouter(instRepo *mockInstanceRepo, nodeExecRepo *mockNodeExecRepo, convo *mockConversationHandler) *chi.Mux {
	h := adapthttp.NewInteractiveHandler(convo, instRepo, nodeExecRepo)
	r := chi.NewRouter()
	r.Post("/instances/{id}/interact/end", h.EndSession)
	return r
}
