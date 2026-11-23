// adapter/mcp/tools_runner_key.go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
)

// RunnerKeyService is the admin key-management port (*runner.KeyService).
type RunnerKeyService interface {
	Create(ctx context.Context, name string) (string, runner.RunnerKey, error)
	List(ctx context.Context) ([]runner.RunnerKey, error)
	Revoke(ctx context.Context, id uuid.UUID, ack runner.RevokeAck) error
}

// runnerKeyMeta mirrors adapter/http runnerKeyView json tags exactly.
// SECURITY: no hash, no machine key, no plaintext.
type runnerKeyMeta struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	RunnerID   string  `json:"runnerId"`
	Prefix     string  `json:"prefix"`
	Claimed    bool    `json:"claimed"`
	Revoked    bool    `json:"revoked"`
	CreatedAt  string  `json:"createdAt"`
	ClaimedAt  *string `json:"claimedAt,omitempty"`
	LastUsedAt *string `json:"lastUsedAt,omitempty"`
	RevokedAt  *string `json:"revokedAt,omitempty"`
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func runnerKeyMetaOf(k runner.RunnerKey) runnerKeyMeta {
	return runnerKeyMeta{ID: k.ID.String(), Name: k.Name, RunnerID: string(k.RunnerID), Prefix: k.Prefix,
		Claimed: k.MachinePublicKey != nil, Revoked: k.RevokedAt != nil, CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
		ClaimedAt: rfc3339Ptr(k.ClaimedAt), LastUsedAt: rfc3339Ptr(k.LastUsedAt), RevokedAt: rfc3339Ptr(k.RevokedAt)}
}

const runnerKeyRevokeDescription = "Revoke a runner key (admin). The runner disconnects immediately and is treated as lost: " +
	"its interrupted steps are re-placed automatically and may repeat external effects such as a push or comment. " +
	"Drain the runner first when possible. Requires acknowledge=true."

// RegisterRunnerKeyTools registers runner_key_create/list/revoke (admin only; audited in the service).
func RegisterRunnerKeyTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("runner_key_create",
			gomcp.WithDescription("Create a uniquely named runner key (admin). The plaintext ark_… is returned ONCE with setup guidance; it binds to the first machine that connects."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Runner name: lowercase letters, digits and dashes (2-63 chars)")),
		),
		runnerKeyCreateHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("runner_key_list",
			gomcp.WithDescription("List runner keys (admin): name, runner, claimed or unclaimed, created, last used, revoked. Never returns secrets."),
		),
		runnerKeyListHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("runner_key_revoke",
			gomcp.WithDescription(runnerKeyRevokeDescription),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Runner key id")),
			gomcp.WithBoolean("acknowledge", gomcp.Required(), gomcp.Description("Must be true: confirms immediate disconnect and possibly repeated steps")),
		),
		runnerKeyRevokeHandler(srv),
	)
}

func runnerKeyErrorResult(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, identity.ErrForbidden):
		return gomcp.NewToolResultError("admin role required")
	case errors.Is(err, runner.ErrInvalidKeyName):
		return gomcp.NewToolResultError(err.Error())
	case errors.Is(err, runner.ErrKeyNameTaken):
		return gomcp.NewToolResultError("a runner key with this name already exists")
	case errors.Is(err, runner.ErrKeyNotFound):
		return gomcp.NewToolResultError("runner key not found")
	case errors.Is(err, runner.ErrRevokeNotAcknowledged):
		return gomcp.NewToolResultError(err.Error() + " (pass acknowledge=true)")
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("runner key operation failed: %s", err))
	}
}

func runnerKeyCreateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.RunnerKeys == nil {
			return gomcp.NewToolResultError("runner key service not available"), nil
		}
		name, _ := request.GetArguments()["name"].(string)
		plaintext, k, err := srv.deps.RunnerKeys.Create(ctx, name)
		if err != nil {
			return runnerKeyErrorResult(err), nil
		}
		data, err := json.Marshal(map[string]any{"key": runnerKeyMetaOf(k), "plaintext": plaintext, "setup": runner.KeySetupGuidance})
		if err != nil {
			return nil, err
		}
		return gomcp.NewToolResultText(string(data)), nil
	}
}

func runnerKeyListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.RunnerKeys == nil {
			return gomcp.NewToolResultError("runner key service not available"), nil
		}
		keys, err := srv.deps.RunnerKeys.List(ctx)
		if err != nil {
			return runnerKeyErrorResult(err), nil
		}
		out := make([]runnerKeyMeta, 0, len(keys))
		for _, k := range keys {
			out = append(out, runnerKeyMetaOf(k))
		}
		return MarshalResult(out)
	}
}

func runnerKeyRevokeHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.RunnerKeys == nil {
			return gomcp.NewToolResultError("runner key service not available"), nil
		}
		args := request.GetArguments()
		idStr, _ := args["id"].(string)
		id, err := uuid.Parse(idStr)
		if err != nil {
			return gomcp.NewToolResultError("invalid id"), nil
		}
		ack, _ := args["acknowledge"].(bool)
		if err := srv.deps.RunnerKeys.Revoke(ctx, id, runner.RevokeAck{Acknowledged: ack}); err != nil {
			return runnerKeyErrorResult(err), nil
		}
		return MarshalResult(map[string]any{"status": "revoked", "id": id.String()})
	}
}
