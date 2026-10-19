package http_test

import (
	"fmt"
	"net/http"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
)

func TestTrigger_GroupExecutionUnsupported_422(t *testing.T) {
	svc := &errTriggerService{err: fmt.Errorf("%w: workflow %q node %q (gatherMode %q): this server cannot execute parallel_group yet",
		engine.ErrGroupExecutionUnsupported, "wf", "fanout", "results")}
	h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), svc, newMockFileReader(), nil)
	r, w := newTriggerReq(t, "wf", `{}`)
	h.Trigger(w, r)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "UNSUPPORTED_NODE_TYPE")
	assert.Contains(t, w.Body.String(), "this server cannot execute parallel_group yet", "the actionable message reaches the caller")
}
