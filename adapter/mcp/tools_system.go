package mcp

import (
	"context"
	"fmt"
	"net/url"
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
		gomcp.NewTool("system_update_teams_config",
			gomcp.WithDescription("Update the global Teams notification webhook URL and enable toggle"),
			gomcp.WithString("webhookUrl", gomcp.Description("Teams Workflows webhook URL (omit to leave unchanged; empty string clears it)")),
			gomcp.WithBoolean("enabled", gomcp.Description("Enable/disable Teams forwarding (omit to leave unchanged)")),
		),
		systemUpdateTeamsConfigHandler(srv),
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
		if srv.deps.QueueRepo == nil {
			return gomcp.NewToolResultError("queue repository not available"), nil
		}
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

		// Marshal a value-copy with the Teams webhook redacted so the raw URL is
		// never echoed on read, matching the HTTP GET /system/config behavior.
		redacted := *srv.deps.Config
		if redacted.Notifications.Teams.WebhookURL != "" {
			redacted.Notifications.Teams.WebhookURL = "**REDACTED**"
		}

		result := map[string]any{
			"config": redacted,
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
		if srv.deps.QueueManager == nil {
			return gomcp.NewToolResultError("queue manager not available"), nil
		}
		args := request.GetArguments()

		globalCapStr, _ := args["globalCap"].(string)
		globalCap, err := strconv.Atoi(globalCapStr)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid globalCap: %s", globalCapStr)), nil
		}
		if globalCap < 0 {
			return gomcp.NewToolResultError("globalCap must be >= 0 (0 = unlimited)"), nil
		}

		perUserCapStr, _ := args["perUserCap"].(string)
		perUserCap, err := strconv.Atoi(perUserCapStr)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid perUserCap: %s", perUserCapStr)), nil
		}
		if perUserCap < 0 {
			return gomcp.NewToolResultError("perUserCap must be >= 0 (0 = unlimited)"), nil
		}

		// Record previous caps for conditional evaluation.
		oldGlobalCap := srv.deps.Config.Queue.GlobalCap
		oldPerUserCap := srv.deps.Config.Queue.PerUserCap

		srv.deps.QueueManager.UpdateConfig(globalCap, perUserCap)

		// Keep the in-memory Config consistent so system_config reads reflect
		// the new values.
		srv.deps.Config.Queue.GlobalCap = globalCap
		srv.deps.Config.Queue.PerUserCap = perUserCap

		// Only evaluate the queue when the cap was raised, matching the HTTP
		// handler's conditional logic. Lowering a cap does not free slots.
		capRaised := (globalCap == 0 && oldGlobalCap > 0) ||
			(globalCap > oldGlobalCap) ||
			(perUserCap == 0 && oldPerUserCap > 0) ||
			(perUserCap > oldPerUserCap)
		if capRaised {
			if err := srv.deps.QueueManager.Evaluate(ctx); err != nil {
				return gomcp.NewToolResultError(fmt.Sprintf("config updated but evaluation failed: %s", err)), nil
			}
		}

		return MarshalResult(map[string]any{
			"status":     "updated",
			"globalCap":  globalCap,
			"perUserCap": perUserCap,
		})
	}
}

// validTeamsWebhookURL reports whether raw is acceptable as a Teams webhook
// URL: an empty string (clears it) or an absolute http(s) URL with a host.
func validTeamsWebhookURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

func systemUpdateTeamsConfigHandler(srv *Server) server.ToolHandlerFunc {
	return func(_ context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Config == nil {
			return gomcp.NewToolResultError("config not available"), nil
		}
		args := request.GetArguments()

		if raw, ok := args["webhookUrl"]; ok {
			webhookURL, ok := raw.(string)
			if !ok {
				return gomcp.NewToolResultError("webhookUrl must be a string"), nil
			}
			if !validTeamsWebhookURL(webhookURL) {
				return gomcp.NewToolResultError("webhookUrl must be an absolute http(s) URL"), nil
			}
			srv.deps.Config.Notifications.Teams.WebhookURL = webhookURL
		}

		if raw, ok := args["enabled"]; ok {
			enabled, ok := raw.(bool)
			if !ok {
				return gomcp.NewToolResultError("enabled must be a boolean"), nil
			}
			srv.deps.Config.Notifications.Teams.Enabled = enabled
		}

		redactedWebhook := ""
		if srv.deps.Config.Notifications.Teams.WebhookURL != "" {
			redactedWebhook = "**REDACTED**"
		}

		return MarshalResult(map[string]any{
			"status":     "updated",
			"enabled":    srv.deps.Config.Notifications.Teams.Enabled,
			"webhookUrl": redactedWebhook,
		})
	}
}

func notificationListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Notifications == nil {
			return gomcp.NewToolResultError("notifications not available"), nil
		}
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
		if notifications == nil {
			notifications = []notification.Notification{}
		}
		return MarshalResult(notifications)
	}
}

func notificationUnreadCountHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Notifications == nil {
			return gomcp.NewToolResultError("notifications not available"), nil
		}
		count, err := srv.deps.Notifications.CountUnread(ctx)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to count unread: %s", err)), nil
		}
		return MarshalResult(map[string]int{"unreadCount": count})
	}
}

func notificationMarkReadHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Notifications == nil {
			return gomcp.NewToolResultError("notifications not available"), nil
		}
		if err := srv.deps.Notifications.MarkAllRead(ctx); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to mark read: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "all marked read"})
	}
}

func notificationDismissHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Notifications == nil {
			return gomcp.NewToolResultError("notifications not available"), nil
		}
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
