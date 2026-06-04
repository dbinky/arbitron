package mcp

import (
	"context"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// tokenMeta is the read shape for token_list. SECURITY: it has NO plaintext and
// NO token_hash field — the secret material never leaves through a read path.
type tokenMeta struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
}

func tokenMetaOf(t identity.Token) tokenMeta {
	m := tokenMeta{
		ID:        t.ID.String(),
		Label:     t.Label,
		Prefix:    t.Prefix,
		CreatedAt: t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if t.LastUsedAt != nil {
		s := t.LastUsedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		m.LastUsedAt = &s
	}
	return m
}

// RegisterTokenTools registers the personal-API-token tools. token_create
// returns the plaintext arb_... exactly once; token_list/token_revoke never
// expose secret material. Every operation is scoped by the service to the
// calling user's identity (carried on the tool ctx by the auth middleware on the
// /mcp mount). On the stdio transport with no HTTP-injected identity, the
// service reports the request as unauthenticated.
func RegisterTokenTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("token_create",
			gomcp.WithDescription("Mint a personal API token for the calling user. The plaintext arb_... is returned ONCE and never recoverable."),
			gomcp.WithString("label", gomcp.Required(), gomcp.Description("Human label to tell tokens apart")),
		),
		tokenCreateHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("token_list",
			gomcp.WithDescription("List the calling user's tokens (metadata only; never returns the secret or hash)."),
		),
		tokenListHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("token_revoke",
			gomcp.WithDescription("Revoke (delete) one of the calling user's tokens by id."),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Token id")),
		),
		tokenRevokeHandler(srv),
	)
}

// tokenErrorResult maps domain sentinels to stable tool error messages.
func tokenErrorResult(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, identity.ErrInvalidLabel):
		return gomcp.NewToolResultError("label is required")
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, identity.ErrNotFound):
		return gomcp.NewToolResultError("token not found")
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("token operation failed: %s", err))
	}
}

func tokenCreateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Tokens == nil {
			return gomcp.NewToolResultError("token service not available"), nil
		}
		args := request.GetArguments()
		label, _ := args["label"].(string)
		if label == "" {
			return gomcp.NewToolResultError("label is required"), nil
		}
		plaintext, tok, err := srv.deps.Tokens.Mint(ctx, label)
		if err != nil {
			return tokenErrorResult(err), nil
		}
		// The plaintext is returned EXACTLY ONCE here; it is never logged and never
		// echoed by token_list.
		return MarshalResult(map[string]any{
			"token":     tokenMetaOf(*tok),
			"plaintext": plaintext,
		})
	}
}

func tokenListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Tokens == nil {
			return gomcp.NewToolResultError("token service not available"), nil
		}
		toks, err := srv.deps.Tokens.List(ctx)
		if err != nil {
			return tokenErrorResult(err), nil
		}
		metas := make([]tokenMeta, 0, len(toks))
		for _, t := range toks {
			metas = append(metas, tokenMetaOf(t))
		}
		return MarshalResult(metas)
	}
}

func tokenRevokeHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Tokens == nil {
			return gomcp.NewToolResultError("token service not available"), nil
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
		if err := srv.deps.Tokens.Revoke(ctx, id); err != nil {
			return tokenErrorResult(err), nil
		}
		return MarshalResult(map[string]any{"status": "revoked", "id": idStr})
	}
}
