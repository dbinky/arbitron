package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutHTTPE2E_CanceledRetryCannotAdvanceStore(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "cancel-retry", queryE2EWorkflow("cancel-retry", "Saved input")))
	rev := manifestRevisionOnDisk(t, ds, "cancel-retry")
	before := layoutE2EWaitTerminal(t, ds, rev)
	require.Equal(t, layout.StateFailed, before.State, "absent browser creates a genuinely eligible retry")
	ds.layout.worker.Stop()
	id, _, err := ds.eng.Trigger(ctx, "cancel-retry", nil, "normal")
	require.NoError(t, err)
	require.Equal(t, rev, snapshotRevision(t, ds, id))
	router := newLayoutE2ERouter(t, ds)
	// Resolve/cache the real local principal before cancellation so the test
	// reaches the layout service, not an unrelated first-auth database failure.
	status, view := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/cancel-retry/layout", "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, true, view["retryEligible"])
	for _, subject := range []string{"workflows/cancel-retry", "instances/" + id.String()} {
		t.Run(subject, func(t *testing.T) {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/"+subject+"/layout/retry", strings.NewReader(`{"expectedRevision":"`+rev+`"}`)).WithContext(canceled)
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			assert.NotEqual(t, http.StatusAccepted, response.Code, "canceled retry cannot acknowledge a new generation: %s", response.Body.String())
			after, err := ds.layout.store.Get(context.Background(), rev)
			require.NoError(t, err)
			require.Equal(t, before, after, "cancellation must preserve the entire persisted input/result/job record")
		})
	}
}
