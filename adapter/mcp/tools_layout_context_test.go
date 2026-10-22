package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

type layoutContextKey struct{}

func callLayoutWithContext(t *testing.T, srv *adaptmcp.Server, ctx context.Context, tool string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	response := srv.MCPServer().HandleMessage(ctx, mustMarshal(t, map[string]any{
		"jsonrpc": "2.0", "id": "context", "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	}))
	b, err := json.Marshal(response)
	require.NoError(t, err)
	var wire struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(b, &wire))
	return &wire.Result
}

func TestLayoutMCP_ForwardsIdentityAndCancellation(t *testing.T) {
	id := identity.Identity{UserID: mcpTestUserID, Username: "layout-owner", Role: identity.RoleUser, Departments: []string{"engineering"}}
	sentinel := &struct{ value string }{"private request value"}
	instanceID := uuid.New()
	for _, tool := range []string{"workflow_get", "workflow_layout_retry", "instance_layout_retry"} {
		for _, canceled := range []bool{false, true} {
			name := "active"
			if canceled {
				name = "canceled"
			}
			t.Run(tool+"/"+name, func(t *testing.T) {
				layouts := newFakeMCPLayouts()
				layouts.workflowViews["context-wf"] = layout.View{SavedRevision: "revision", Available: true, DisplayStatus: layout.DisplayComplete}
				layouts.workflowRevs["context-wf"] = "revision"
				layouts.instanceRevs[instanceID] = "revision"
				srv := newLayoutToolServer(stampedRepo("context-wf", "revision"), layouts)
				ctx, cancel := context.WithCancel(identity.WithIdentity(context.WithValue(context.Background(), layoutContextKey{}, sentinel), id))
				defer cancel()
				if canceled {
					cancel()
				}
				res := callLayoutWithContext(t, srv, ctx, tool, map[string]any{"name": "context-wf", "id": instanceID.String(), "expectedRevision": "revision"})
				if tool == "workflow_get" || !canceled {
					require.False(t, res.IsError, extractText(t, res)) // workflow_get degrades status, not the manifest
				}
				if canceled {
					if tool == "workflow_get" {
						_, summary := decodeArrangement(t, extractText(t, res))
						require.Equal(t, false, summary["available"])
						require.Equal(t, "unavailable", summary["displayStatus"])
						require.Equal(t, "layout status unavailable; reload or retry", summary["availabilityError"])
					} else {
						require.True(t, res.IsError)
						require.Contains(t, extractText(t, res), context.Canceled.Error())
						require.NotContains(t, extractText(t, res), "jobGeneration")
					}
				} else if tool == "workflow_get" {
					_, summary := decodeArrangement(t, extractText(t, res))
					require.Equal(t, true, summary["available"])
					require.Equal(t, "revision", summary["savedRevision"])
				} else {
					var ack layout.RetryAck
					require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &ack))
					require.Equal(t, "revision", ack.SavedRevision)
					require.Positive(t, ack.JobGeneration)
				}
				contexts := layouts.recordedContexts()
				require.Len(t, contexts, 1)
				got, ok := identity.FromContext(contexts[0])
				require.True(t, ok)
				require.Equal(t, id, got)
				require.Same(t, sentinel, contexts[0].Value(layoutContextKey{}))
			})
		}
	}
}

// MCP currently exposes no instance-layout read tool. Keep the entire shared
// fake port honest without inventing one: instance reads must consume id/context
// and reject cancellation just like the three methods reached through tools.
func TestFakeMCPLayouts_InstanceReadContext(t *testing.T) {
	f := newFakeMCPLayouts()
	id := uuid.New()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), layoutContextKey{}, id))
	defer cancel()
	_, err := f.InstanceLayout(ctx, id)
	require.ErrorContains(t, err, id.String())
	cancel()
	_, err = f.InstanceLayout(ctx, id)
	require.ErrorIs(t, err, context.Canceled)
	contexts := f.recordedContexts()
	require.Len(t, contexts, 2)
	require.Equal(t, id, contexts[0].Value(layoutContextKey{}))
	require.ErrorIs(t, contexts[1].Err(), context.Canceled)
}
