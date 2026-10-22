package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type layoutRequestSentinel struct{}

func TestLayoutHTTP_ForwardsIdentityAndCancellation(t *testing.T) {
	caller := identity.Identity{UserID: uuid.New(), Username: "layout-caller", Role: identity.RoleUser, Departments: []string{"engineering"}}
	sentinel := new(int)
	id := uuid.New()
	for _, action := range []string{"workflow read", "workflow retry", "instance read", "instance retry"} {
		for _, canceled := range []bool{false, true} {
			name := action + "/active"
			if canceled {
				name = action + "/canceled"
			}
			t.Run(name, func(t *testing.T) {
				q := newFakeLayoutQuery()
				q.workflowViews["wf"] = attentionView("rev-workflow")
				q.workflowRevs["wf"] = "rev-workflow"
				q.instanceViews[id] = attentionView("rev-instance")
				q.instanceRevs[id] = "rev-instance"
				h := adapthttp.NewLayoutHandler(q)
				method, key, subject, revision := http.MethodGet, "name", "wf", "rev-workflow"
				handler := h.GetWorkflowLayout
				switch action {
				case "workflow retry":
					method, handler = http.MethodPost, h.RetryWorkflowLayout
				case "instance read":
					key, subject, revision, handler = "id", id.String(), "rev-instance", h.GetInstanceLayout
				case "instance retry":
					method, key, subject, revision, handler = http.MethodPost, "id", id.String(), "rev-instance", h.RetryInstanceLayout
				}
				req := withChiURLParam(httptest.NewRequest(method, "/", strings.NewReader(`{"expectedRevision":"`+revision+`"}`)), key, subject)
				ctx := identity.WithIdentity(context.WithValue(req.Context(), layoutRequestSentinel{}, sentinel), caller)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				if canceled {
					cancel()
				}
				w := httptest.NewRecorder()
				handler(w, req.WithContext(ctx))
				body := decodeBody(t, w)
				if canceled {
					require.Equal(t, http.StatusInternalServerError, w.Code, "a canceled context must not return successful view/ack: %v", body)
					require.Equal(t, "INTERNAL_ERROR", body["code"])
					require.Equal(t, "internal server error", body["error"])
					require.NotContains(t, body, "savedRevision")
					return
				}
				expectedStatus := http.StatusOK
				if method == http.MethodPost {
					expectedStatus = http.StatusAccepted
				}
				require.Equal(t, expectedStatus, w.Code, "%v", body)
				require.Equal(t, revision, body["savedRevision"])
				contexts := q.recordedContexts()
				require.Len(t, contexts, 1)
				got, ok := identity.FromContext(contexts[0])
				require.True(t, ok, "caller identity must reach the authorization boundary")
				require.Equal(t, caller, got)
				require.Same(t, sentinel, contexts[0].Value(layoutRequestSentinel{}), "request-private context values cannot be replaced")
			})
		}
	}
}
