package http_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errTriggerService returns a fixed error from the trigger call.
type errTriggerService struct{ err error }

func (e *errTriggerService) TriggerWithDepartment(_ context.Context, _ string, _ map[string]any, _ string, _ string) (uuid.UUID, bool, error) {
	return uuid.Nil, false, e.err
}

func newTriggerReq(t *testing.T, name, body string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+name+"/trigger", bytes.NewBufferString(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	return r, httptest.NewRecorder()
}

// The optional department field is threaded to the engine.
func TestTrigger_PassesDepartment(t *testing.T) {
	svc := &mockWorkflowService{}
	h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), svc, newMockFileReader(), nil)
	r, w := newTriggerReq(t, "wf", `{"department":"engineering"}`)
	h.Trigger(w, r)
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "engineering", svc.capturedDepartment)
}

// An out-of-department override maps to 400 (validation, not 403/500).
func TestTrigger_InvalidDepartment_400(t *testing.T) {
	svc := &errTriggerService{err: engine.ErrInvalidDepartmentOverride}
	h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), svc, newMockFileReader(), nil)
	r, w := newTriggerReq(t, "wf", `{"department":"finance"}`)
	h.Trigger(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
