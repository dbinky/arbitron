package http_test

import (
	"context"
	"encoding/json"
	"errors"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func (*mockInstanceService) GetExecutionStatus(context.Context, uuid.UUID) ([]instance.ExecutionStatus, error) {
	return nil, nil
}
func (*routerMockService) GetExecutionStatus(context.Context, uuid.UUID) ([]instance.ExecutionStatus, error) {
	return nil, nil
}

type detailService struct {
	*mockInstanceService
	err   error
	calls int
	want  uuid.UUID
	t     *testing.T
}

func (s *detailService) GetExecutionStatus(_ context.Context, id uuid.UUID) ([]instance.ExecutionStatus, error) {
	s.calls++
	require.Equal(s.t, s.want, id)
	return []instance.ExecutionStatus{{RunID: id, State: instance.ExecWaitingCapacity}}, s.err
}
func TestInstanceHandler_Get_CarriesExecutionStatus(t *testing.T) { testDetailExecution(t, false) }
func TestInstanceDetail_StatusUnavailable(t *testing.T)           { testDetailExecution(t, true) }
func testDetailExecution(t *testing.T, unavailable bool) {
	_, repo, nodes, snaps, base := newInstanceHandler()
	inst := testInstance(instance.StateRunning)
	base.getFn = func(context.Context, uuid.UUID) (*instance.Instance, error) { return inst, nil }
	svc := &detailService{mockInstanceService: base, want: inst.ID, t: t}
	if unavailable {
		svc.err = errors.New("secret database error")
	}
	h := adapthttp.NewInstanceHandler(repo, nodes, snaps, svc)
	req := withChiURLParam(httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil), "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)
	require.Equal(t, 1, svc.calls)
	if unavailable {
		require.Equal(t, 503, w.Code)
		require.NotContains(t, w.Body.String(), "secret database")
		return
	}
	require.Equal(t, 200, w.Code)
	var row struct {
		Execution []instance.ExecutionStatus `json:"execution"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &row))
	require.Len(t, row.Execution, 1)
	require.Equal(t, inst.ID, row.Execution[0].RunID)
	require.Equal(t, instance.ExecWaitingCapacity, row.Execution[0].State)
}
