package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/google/uuid"
)

// RegisterSystemTools registers all system, notification, and stats tools with the server.
func RegisterSystemTools(srv *Server) {
	srv.AddTool(gomcp.NewTool("system_settings_get", gomcp.WithDescription("Get persisted model catalog and announcement settings (admin only)")), systemSettingsHandler(srv, false))
	srv.AddTool(gomcp.NewTool("system_settings_update",
		gomcp.WithDescription("Update model catalog and announcement settings (admin only)"),
		gomcp.WithString("catalog_url", gomcp.Description("Absolute HTTPS catalog URL; omit to retain")),
		gomcp.WithString("teams_webhook_url", gomcp.Description("Replacement announcement webhook; omit to retain, empty to clear")),
	), systemSettingsHandler(srv, true))
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

func systemSettingsError(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, identity.ErrForbidden):
		return gomcp.NewToolResultError("admin only")
	case errors.Is(err, settings.ErrInvalid):
		return gomcp.NewToolResultError(err.Error())
	default:
		return gomcp.NewToolResultError("system settings unavailable")
	}
}

func systemSettingsHandler(srv *Server, update bool) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.SystemSettings == nil {
			return gomcp.NewToolResultError("system settings unavailable"), nil
		}
		if !update {
			values, err := srv.deps.SystemSettings.Get(ctx)
			if err != nil {
				return systemSettingsError(err), nil
			}
			return MarshalResult(values)
		}
		var patch settings.Patch
		for key, raw := range request.GetArguments() {
			value, ok := raw.(string)
			if !ok {
				return gomcp.NewToolResultError("settings fields must be strings"), nil
			}
			switch key {
			case "catalog_url":
				patch.CatalogURL = &value
			case "teams_webhook_url":
				patch.TeamsWebhookURL = &value
			default:
				return gomcp.NewToolResultError("unknown settings field"), nil
			}
		}
		values, err := srv.deps.SystemSettings.Update(ctx, patch)
		if err != nil {
			return systemSettingsError(err), nil
		}
		return MarshalResult(values)
	}
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

		// Project into the shared positive allow-list so secrets (LLM API key,
		// Teams webhook) are never echoed and a newly added Config field defaults
		// to NOT exposed. This is the same projection the HTTP GET /system/config
		// surface uses.
		redacted := config.Redact(srv.deps.Config, srv.deps.Settings.GetTeams())
		redacted.Queue.RunningCount = running
		redacted.Queue.QueuedCount = queued
		redacted.Queue.WaitingCount = waiting
		// Attach backend-scoped health (codex install/login readiness) so a run can
		// be pre-flighted. Served from the SHARED, TTL-cached component injected at
		// the composition root (the SAME instance the HTTP surface uses), so the
		// probe forks at most once per TTL across both surfaces. Nil seam ⇒ omit the
		// list (legacy/tests).
		if srv.deps.BackendHealth != nil {
			redacted.Backends = srv.deps.BackendHealth.Backends(ctx)
		}
		return MarshalResult(redacted)
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

func systemUpdateTeamsConfigHandler(srv *Server) server.ToolHandlerFunc {
	return func(_ context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Settings == nil {
			return gomcp.NewToolResultError("settings not available"), nil
		}
		args := request.GetArguments()

		// Build the partial update, validating before any mutation so a bad input
		// leaves the stored config unchanged.
		var webhookPtr *string
		var enabledPtr *bool

		if raw, ok := args["webhookUrl"]; ok {
			webhookURL, ok := raw.(string)
			if !ok {
				return gomcp.NewToolResultError("webhookUrl must be a string"), nil
			}
			if err := config.ValidateTeamsWebhookURL(webhookURL); err != nil {
				return gomcp.NewToolResultError(err.Error()), nil
			}
			webhookPtr = &webhookURL
		}

		if raw, ok := args["enabled"]; ok {
			enabled, ok := raw.(bool)
			if !ok {
				return gomcp.NewToolResultError("enabled must be a boolean"), nil
			}
			enabledPtr = &enabled
		}

		// Apply atomically through the synchronized store (shared with HTTP and
		// the notifier).
		result := srv.deps.Settings.SetTeams(webhookPtr, enabledPtr)

		return MarshalResult(map[string]any{
			"status":     "updated",
			"enabled":    result.Enabled,
			"webhookUrl": config.RedactWebhookURL(result.WebhookURL),
		})
	}
}

// mcpNotifOwnerID returns the owner restriction for the calling MCP identity:
// nil for admins (see all), &userID for non-admins (see own only).
//
// A MISSING identity fails CLOSED — it restricts to a non-matching owner
// (uuid.Nil), never the unrestricted admin scope (nil). Today the auth middleware
// always supplies an identity in org mode, but collapsing !ok into the admin-wide
// scope was a latent cross-user disclosure the moment any path reached this
// handler without the middleware. This matches the fail-closed convention every
// other authz predicate uses (domain/engine/authz.go) (PR #21).
func mcpNotifOwnerID(ctx context.Context) *uuid.UUID {
	id, ok := identity.FromContext(ctx)
	if !ok {
		// No identity ⇒ fail closed: restrict to an owner that matches no rows.
		nilOwner := uuid.Nil
		return &nilOwner
	}
	if id.IsAdmin() {
		return nil // admin: no owner restriction (see all)
	}
	uid := id.UserID
	return &uid
}

func notificationListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Notifications == nil {
			return gomcp.NewToolResultError("notifications not available"), nil
		}
		args := request.GetArguments()

		filters := notification.ListFilters{OwnerID: mcpNotifOwnerID(ctx)}
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
		count, err := srv.deps.Notifications.CountUnread(ctx, mcpNotifOwnerID(ctx))
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
		if err := srv.deps.Notifications.MarkAllRead(ctx, mcpNotifOwnerID(ctx)); err != nil {
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

		if err := srv.deps.Notifications.Dismiss(ctx, id, mcpNotifOwnerID(ctx)); err != nil {
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
