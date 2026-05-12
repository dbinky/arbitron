package mcp

import (
	"context"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterStreamingTools registers the event streaming subscription tools
// with the server. These tools allow MCP clients to subscribe/unsubscribe
// to real-time workflow execution events.
func RegisterStreamingTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("instance_subscribe",
			gomcp.WithDescription("Subscribe to real-time events for a workflow instance"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID to subscribe to")),
		),
		instanceSubscribeHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_unsubscribe",
			gomcp.WithDescription("Unsubscribe from events for a workflow instance"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID to unsubscribe from")),
		),
		instanceUnsubscribeHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("system_subscribe",
			gomcp.WithDescription("Subscribe to all system-wide workflow events"),
		),
		systemSubscribeHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("system_unsubscribe",
			gomcp.WithDescription("Unsubscribe from all system-wide workflow events"),
		),
		systemUnsubscribeHandler(srv),
	)
}

func instanceSubscribeHandler(srv *Server) server.ToolHandlerFunc {
	return func(_ context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.subs == nil {
			return gomcp.NewToolResultError("event streaming not available"), nil
		}

		id, err := ParseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if err := srv.subs.SubscribeInstance(id); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("subscribe failed: %s", err)), nil
		}

		return MarshalResult(map[string]any{
			"status":     "subscribed",
			"instanceId": id.String(),
			"active":     srv.subs.ActiveCount(),
		})
	}
}

func instanceUnsubscribeHandler(srv *Server) server.ToolHandlerFunc {
	return func(_ context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.subs == nil {
			return gomcp.NewToolResultError("event streaming not available"), nil
		}

		id, err := ParseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		srv.subs.UnsubscribeInstance(id)

		return MarshalResult(map[string]any{
			"status":     "unsubscribed",
			"instanceId": id.String(),
			"active":     srv.subs.ActiveCount(),
		})
	}
}

func systemSubscribeHandler(srv *Server) server.ToolHandlerFunc {
	return func(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.subs == nil {
			return gomcp.NewToolResultError("event streaming not available"), nil
		}

		if err := srv.subs.SubscribeSystem(); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("subscribe failed: %s", err)), nil
		}

		return MarshalResult(map[string]any{
			"status": "subscribed",
			"scope":  "system",
			"active": srv.subs.ActiveCount(),
		})
	}
}

func systemUnsubscribeHandler(srv *Server) server.ToolHandlerFunc {
	return func(_ context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.subs == nil {
			return gomcp.NewToolResultError("event streaming not available"), nil
		}

		srv.subs.UnsubscribeSystem()

		return MarshalResult(map[string]any{
			"status": "unsubscribed",
			"scope":  "system",
			"active": srv.subs.ActiveCount(),
		})
	}
}
