package mcp

import (
	"context"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// userMeta is the read shape for user_list. It exposes only id/username/role —
// never the IdP subject or email.
type userMeta struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func userMetaOf(u instance.User) userMeta {
	return userMeta{ID: u.ID.String(), Username: u.Username, Role: string(u.Role)}
}

// RegisterUserTools registers the admin-only user/role-management tools. The
// admin gate and demotion floors live in the domain UserService; these handlers
// map its sentinels to clear tool errors.
func RegisterUserTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("user_list",
			gomcp.WithDescription("List all users (admin only). Returns id, username, role."),
		),
		userListHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("user_set_role",
			gomcp.WithDescription("Set a user's role to 'user' or 'admin' (admin only). Cannot demote a bootstrap admin or the last admin."),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Target user id")),
			gomcp.WithString("role", gomcp.Required(), gomcp.Description("'user' or 'admin'")),
		),
		userSetRoleHandler(srv),
	)
}

// userErrorResult maps role-management sentinels to stable tool error messages.
func userErrorResult(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, instance.ErrForbidden):
		return gomcp.NewToolResultError("forbidden: admin only")
	case errors.Is(err, instance.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, instance.ErrInvalidRole):
		return gomcp.NewToolResultError("role must be 'user' or 'admin'")
	case errors.Is(err, instance.ErrNotFound):
		return gomcp.NewToolResultError("user not found")
	case errors.Is(err, instance.ErrBootstrapAdminProtected):
		return gomcp.NewToolResultError("cannot demote a bootstrap admin")
	case errors.Is(err, instance.ErrLastAdmin):
		return gomcp.NewToolResultError("cannot demote the last admin")
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("user operation failed: %s", err))
	}
}

func userListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Users == nil {
			return gomcp.NewToolResultError("user service not available"), nil
		}
		users, err := srv.deps.Users.List(ctx)
		if err != nil {
			return userErrorResult(err), nil
		}
		metas := make([]userMeta, 0, len(users))
		for _, u := range users {
			metas = append(metas, userMetaOf(u))
		}
		return MarshalResult(metas)
	}
}

func userSetRoleHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Users == nil {
			return gomcp.NewToolResultError("user service not available"), nil
		}
		args := request.GetArguments()
		idStr, _ := args["id"].(string)
		if idStr == "" {
			return gomcp.NewToolResultError("id is required"), nil
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return gomcp.NewToolResultError("invalid id"), nil
		}
		role, _ := args["role"].(string)
		if role == "" {
			return gomcp.NewToolResultError("role is required"), nil
		}
		if err := srv.deps.Users.SetRole(ctx, id, role); err != nil {
			return userErrorResult(err), nil
		}
		return MarshalResult(map[string]any{"status": "ok", "id": idStr, "role": role})
	}
}
