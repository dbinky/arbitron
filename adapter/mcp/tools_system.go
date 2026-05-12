package mcp

import (
	"context"
	"fmt"
	"strconv"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/notification"
)

// RegisterSystemTools registers all system, notification, and stats tools with the server.
func RegisterSystemTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("system_config",
			gomcp.WithDescription("Get the current system configuration and queue status"),
		),
		systemConfigHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("system_update_queue_config",
			gomcp.WithDescription("Update queue configuration caps"),
			gomcp.WithString("globalCap", gomcp.Required(), gomcp.Description("Global concurrency cap (0 = unlimited)")),
			gomcp.WithString("perUserCap", gomcp.Required(), gomcp.Description("Per-user concurrency cap (0 = unlimited)")),
		),
		systemUpdateQueueConfigHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("notification_list",
			gomcp.WithDescription("List notifications with optional dismissed filter"),
			gomcp.WithBoolean("dismissed", gomcp.Description("Filter by dismissed state")),
		),
		notificationListHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("notification_unread_count",
			gomcp.WithDescription("Get the count of unread notifications"),
		),
		notificationUnreadCountHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("notification_mark_read",
			gomcp.WithDescription("Mark all notifications as read"),
		),
		notificationMarkReadHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("notification_dismiss",
			gomcp.WithDescription("Dismiss a notification by ID"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Notification UUID")),
		),
		notificationDismissHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_stats",
			gomcp.WithDescription("Get execution statistics for a workflow"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("from", gomcp.Description("Start time in RFC3339 format")),
			gomcp.WithString("to", gomcp.Description("End time in RFC3339 format")),
		),
		workflowStatsHandler(srv),
	)
}

func systemConfigHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		running, err := srv.deps.QueueRepo.CountRunning(ctx)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to count running: %s", err)), nil
		}
		queued, err := srv.deps.QueueRepo.CountQueued(ctx)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to count queued: %s", err)), nil
		}
		waiting, err := srv.deps.QueueRepo.CountWaiting(ctx)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to count waiting: %s", err)), nil
		}

		result := map[string]any{
			"config": srv.deps.Config,
			"queue": map[string]int{
				"running": running,
				"queued":  queued,
				"waiting": waiting,
			},
		}
		return MarshalResult(result)
	}
}

func systemUpdateQueueConfigHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()

		globalCapStr, _ := args["globalCap"].(string)
		globalCap, err := strconv.Atoi(globalCapStr)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid globalCap: %s", globalCapStr)), nil
		}

		perUserCapStr, _ := args["perUserCap"].(string)
		perUserCap, err := strconv.Atoi(perUserCapStr)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid perUserCap: %s", perUserCapStr)), nil
		}

		srv.deps.QueueManager.UpdateConfig(globalCap, perUserCap)

		if err := srv.deps.QueueManager.Evaluate(ctx); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("config updated but evaluation failed: %s", err)), nil
		}

		return MarshalResult(map[string]any{
			"status":    "updated",
			"globalCap": globalCap,
			"perUserCap": perUserCap,
		})
	}
}

func notificationListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()

		var filters notification.ListFilters
		if dismissed, ok := args["dismissed"]; ok {
			if b, ok := dismissed.(bool); ok {
				filters.Dismissed = &b
			}
		}

		notifications, err := srv.deps.Notifications.List(ctx, filters)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to list notifications: %s", err)), nil
		}
		return MarshalResult(notifications)
	}
}

func notificationUnreadCountHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		count, err := srv.deps.Notifications.CountUnread(ctx)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to count unread: %s", err)), nil
		}
		return MarshalResult(map[string]int{"unreadCount": count})
	}
}

func notificationMarkReadHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if err := srv.deps.Notifications.MarkAllRead(ctx); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to mark read: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "all marked read"})
	}
}

func notificationDismissHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if err := srv.deps.Notifications.Dismiss(ctx, id); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to dismiss notification: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "dismissed", "id": id.String()})
	}
}

func workflowStatsHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		from := time.Time{}
		if fromStr, _ := args["from"].(string); fromStr != "" {
			parsed, err := time.Parse(time.RFC3339, fromStr)
			if err != nil {
				return gomcp.NewToolResultError(fmt.Sprintf("invalid from time: %s", err)), nil
			}
			from = parsed
		}

		to := time.Now()
		if toStr, _ := args["to"].(string); toStr != "" {
			parsed, err := time.Parse(time.RFC3339, toStr)
			if err != nil {
				return gomcp.NewToolResultError(fmt.Sprintf("invalid to time: %s", err)), nil
			}
			to = parsed
		}

		stats, err := srv.deps.Instances.Stats(ctx, name, from, to)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get stats: %s", err)), nil
		}
		return MarshalResult(stats)
	}
}
