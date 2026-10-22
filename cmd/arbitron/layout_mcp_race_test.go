package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Interleave real authoring saves at the query boundary; the underlying query,
// authorization, filesystem, stamper, requester and SQLite remain production code.
type savingMCPLayouts struct {
	adaptmcp.LayoutQueryService
	beforeRead func(context.Context, string)
}

func (s savingMCPLayouts) WorkflowLayout(ctx context.Context, name string) (layout.View, error) {
	s.beforeRead(ctx, name)
	return s.LayoutQueryService.WorkflowLayout(ctx, name)
}
func TestLayoutMCPE2E_WorkflowGetRevisionRace(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeat=%t", repeat), func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			ds, _ := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.workflowSvc.Create(ctx, "race", queryE2EWorkflow("race", "A")))
			deps := ds.mcpDeps()
			calls := 0
			var returnedRevision string
			deps.Layouts = savingMCPLayouts{LayoutQueryService: deps.Layouts, beforeRead: func(ctx context.Context, name string) {
				require.Equal(t, "race", name)
				calls++
				if calls == 1 || repeat {
					label := "B"
					if calls == 2 {
						label = "C"
					}
					require.NoError(t, ds.workflowSvc.Update(ctx, name, queryE2EWorkflow(name, label)))
					if calls == 1 {
						returnedRevision = manifestRevisionOnDisk(t, ds, name)
					}
				}
			}}
			srv := adaptmcp.NewServerWithDeps("race", "test", deps)
			adaptmcp.RegisterAllTools(srv)
			res := callMCPToolE2E(t, srv, ctx, "workflow_get", map[string]any{"name": "race"})
			require.False(t, res.IsError, mcpE2EText(t, res))
			var out map[string]any
			require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &out))
			assert.Equal(t, returnedRevision, out["presentation"].(map[string]any)["revision"])
			assert.Equal(t, "B", out["nodes"].([]any)[0].(map[string]any)["label"])
			arr := out["arrangement"].(map[string]any)
			assert.Equal(t, returnedRevision, arr["savedRevision"])
			if repeat {
				assert.Equal(t, "unavailable", arr["displayStatus"])
				assert.Equal(t, false, arr["available"])
				assert.Empty(t, arr["findings"])
				assert.Contains(t, arr["availabilityError"], "reload or retry")
			} else {
				assert.Equal(t, true, arr["available"])
			}
			assert.Equal(t, 2, calls)
		})
	}
}
