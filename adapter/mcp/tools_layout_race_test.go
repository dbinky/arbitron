package mcp_test

import (
	"context"
	"fmt"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only the timing is scripted: all name/revision arguments still reach the keyed port.
type racingMCPLayouts struct {
	adaptmcp.LayoutQueryService
	beforeRead func(context.Context, string)
}

func (r racingMCPLayouts) WorkflowLayout(ctx context.Context, name string) (layout.View, error) {
	r.beforeRead(ctx, name)
	return r.LayoutQueryService.WorkflowLayout(ctx, name)
}

func TestWorkflowGet_RevisionRace(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeat=%t", repeat), func(t *testing.T) {
			base := newFakeMCPLayouts()
			rev, calls := "A", 0
			repo := &stubWorkflowRepo{loadFn: func(ctx context.Context, name string) (*workflow.Workflow, error) {
				return stampedRepo("race", rev).Load(ctx, workflow.Private(mcpTestUserID), name)
			}}
			port := racingMCPLayouts{LayoutQueryService: base, beforeRead: func(_ context.Context, name string) {
				require.Equal(t, "race", name)
				calls++
				if calls == 1 {
					rev = "B"
				} else if repeat {
					rev = "C"
				}
				base.workflowViews[name] = layout.View{SavedRevision: rev, Available: true, DisplayStatus: layout.DisplayComplete,
					Findings: []layout.Finding{{Evidence: "foreign complete status"}}}
			}}
			res := callTool(t, newLayoutToolServer(repo, port), "workflow_get", map[string]any{"name": "race"})
			require.False(t, res.IsError, extractText(t, res))
			out, arr := decodeArrangement(t, extractText(t, res))
			assert.Equal(t, "B", out["presentation"].(map[string]any)["revision"])
			assert.Equal(t, "B", arr["savedRevision"])
			if repeat {
				assert.Equal(t, "unavailable", arr["displayStatus"])
				assert.Equal(t, false, arr["available"])
				assert.Contains(t, arr["availabilityError"], "reload or retry")
				assert.Empty(t, arr["findings"])
			} else {
				assert.Equal(t, "complete", arr["displayStatus"])
			}
			assert.Equal(t, 2, calls, "one bounded authorised reload")
		})
	}
}

func TestWorkflowGet_RevisionRaceLosesAccess(t *testing.T) {
	base := newFakeMCPLayouts()
	accessible := true
	repo := &stubWorkflowRepo{loadFn: func(ctx context.Context, name string) (*workflow.Workflow, error) {
		if !accessible {
			return nil, workflow.ErrNotFound
		}
		return stampedRepo("race", "A").Load(ctx, workflow.Private(mcpTestUserID), name)
	}}
	port := racingMCPLayouts{LayoutQueryService: base, beforeRead: func(_ context.Context, name string) {
		accessible = false
		base.workflowViews[name] = layout.View{SavedRevision: "B", DisplayStatus: layout.DisplayComplete}
	}}
	res := callTool(t, newLayoutToolServer(repo, port), "workflow_get", map[string]any{"name": "race"})
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "no longer accessible")
}
