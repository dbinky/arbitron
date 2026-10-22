package mcp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowGet_QueryMutationPendingIsSafe(t *testing.T) {
	port := newFakeMCPLayouts()
	port.errs["workflow:my-wf"] = fmt.Errorf("/private/registry: %w", workflow.ErrMutationPending)
	res := callTool(t, newLayoutToolServer(stampedRepo("my-wf", "A"), port), "workflow_get", map[string]any{"name": "my-wf"})
	require.False(t, res.IsError, "already-settled manifest may carry unavailable status")
	_, arr := decodeArrangement(t, extractText(t, res))
	assert.Equal(t, "A", arr["savedRevision"])
	assert.Equal(t, "unavailable", arr["displayStatus"])
	assert.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", arr["availabilityError"])
	assert.Empty(t, arr["findings"])
}

func TestWorkflowGet_ReloadMutationPendingIsToolError(t *testing.T) {
	base := newFakeMCPLayouts()
	loads := 0
	repo := &stubWorkflowRepo{loadFn: func(ctx context.Context, name string) (*workflow.Workflow, error) {
		loads++
		if loads > 1 {
			return nil, fmt.Errorf("/private/registry: %w", workflow.ErrMutationPending)
		}
		return stampedRepo("race", "A").Load(ctx, workflow.Private(mcpTestUserID), name)
	}}
	base.workflowViews["race"] = layout.View{SavedRevision: "B"}
	res := callTool(t, newLayoutToolServer(repo, base), "workflow_get", map[string]any{"name": "race"})
	require.True(t, res.IsError)
	assert.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", extractText(t, res))
}
