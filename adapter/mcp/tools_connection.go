package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/provider"
)

// connectionMeta is the response shape for connection_get / connection_list. It
// deliberately has NO api_key field: the plaintext key never leaves the store
// through these read paths.
type connectionMeta struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	BaseURL  string            `json:"base_url"`
	Extras   map[string]string `json:"extras,omitempty"`
}

func metaOf(c provider.Connection) connectionMeta {
	return connectionMeta{
		Name:     c.Name,
		Provider: c.Provider,
		BaseURL:  c.BaseURL,
		Extras:   c.Extras,
	}
}

// RegisterConnectionTools registers the connection-management CRUD tools. The
// plaintext api_key is accepted only by connection_create / connection_update,
// handed straight to the store (which seals it), and is never echoed in a
// response or logged.
func RegisterConnectionTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("connection_create",
			gomcp.WithDescription("Create a model-endpoint connection. The api_key is sealed immediately and never returned or logged."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Unique connection name (referenced by profiles)")),
			gomcp.WithString("provider", gomcp.Required(), gomcp.Description("Provider id (azure-openai, azure-foundry, openrouter, litellm, ...)")),
			gomcp.WithString("base_url", gomcp.Required(), gomcp.Description("Endpoint base URL")),
			gomcp.WithString("api_key", gomcp.Required(), gomcp.Description("Plaintext API key; sealed at rest, never returned or logged")),
			gomcp.WithObject("extras", gomcp.Description("Provider extras (e.g. api-version for Azure)")),
		),
		connectionCreateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("connection_update",
			gomcp.WithDescription("Update a connection's metadata. Omit api_key to preserve the stored key; supply it to re-seal a new one."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Connection name")),
			gomcp.WithString("provider", gomcp.Required(), gomcp.Description("Provider id")),
			gomcp.WithString("base_url", gomcp.Required(), gomcp.Description("Endpoint base URL")),
			gomcp.WithString("api_key", gomcp.Description("Optional new plaintext API key; omitted preserves the stored key")),
			gomcp.WithObject("extras", gomcp.Description("Provider extras (e.g. api-version for Azure)")),
		),
		connectionUpdateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("connection_delete",
			gomcp.WithDescription("Delete a connection by name"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Connection name")),
		),
		connectionDeleteHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("connection_get",
			gomcp.WithDescription("Get a connection's metadata. Never returns the api_key."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Connection name")),
		),
		connectionGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("connection_list",
			gomcp.WithDescription("List all connections' metadata. Never returns api_keys."),
		),
		connectionListHandler(srv),
	)
}

// connLogger returns the server's logger or the process default. SECURITY: the
// caller logs the connection NAME only, never the api_key.
func connLogger(srv *Server) *slog.Logger {
	if srv.deps != nil && srv.deps.Logger != nil {
		return srv.deps.Logger
	}
	return slog.Default()
}

// connErrorResult maps domain sentinels to a stable tool error message. The
// connection_* tools are admin-only now (routed through ConnectionService), so
// the authz sentinels map to the same strings as the user/profile tools.
func connErrorResult(action, name string, err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, provider.ErrForbidden):
		return gomcp.NewToolResultError("forbidden: admin only")
	case errors.Is(err, provider.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, provider.ErrConnectionExists):
		return gomcp.NewToolResultError(fmt.Sprintf("connection %q already exists", name))
	case errors.Is(err, provider.ErrConnectionNotFound):
		return gomcp.NewToolResultError(fmt.Sprintf("connection %q not found", name))
	case errors.Is(err, provider.ErrAzureAPIVersionRequired):
		return gomcp.NewToolResultError(fmt.Sprintf("connection %q: azure connections require an api-version in extras", name))
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("failed to %s connection %q: %s", action, name, err))
	}
}

// stringExtras coerces an MCP object argument (map[string]any) into the
// map[string]string the domain Connection carries.
func stringExtras(raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("extras must be an object")
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("extras value for %q must be a string", k)
		}
		out[k] = s
	}
	return out, nil
}

func connectionCreateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConnectionAdmin == nil {
			return gomcp.NewToolResultError("connection service not available"), nil
		}
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		providerID, _ := args["provider"].(string)
		baseURL, _ := args["base_url"].(string)
		apiKey, _ := args["api_key"].(string)
		if apiKey == "" {
			return gomcp.NewToolResultError("api_key is required"), nil
		}
		extras, err := stringExtras(args["extras"])
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		conn := provider.Connection{Name: name, Provider: providerID, BaseURL: baseURL, Extras: extras}
		// apiKey is consumed only by Create here (sealed immediately); it is never
		// placed in the response or logged.
		if err := srv.deps.ConnectionAdmin.Create(ctx, conn, apiKey); err != nil {
			return connErrorResult("create", name, err), nil
		}
		// SECURITY: log the connection NAME only, never the key.
		connLogger(srv).Info("connection created", slog.String("connection", name), slog.String("provider", providerID))
		return MarshalResult(map[string]any{"status": "created", "connection": metaOf(conn)})
	}
}

func connectionUpdateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConnectionAdmin == nil {
			return gomcp.NewToolResultError("connection service not available"), nil
		}
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		providerID, _ := args["provider"].(string)
		baseURL, _ := args["base_url"].(string)
		extras, err := stringExtras(args["extras"])
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		// Optional api_key: present -> re-seal; absent -> nil -> preserve stored key.
		var keyPtr *string
		if raw, ok := args["api_key"]; ok {
			if k, ok := raw.(string); ok && k != "" {
				keyPtr = &k
			}
		}

		conn := provider.Connection{Name: name, Provider: providerID, BaseURL: baseURL, Extras: extras}
		if err := srv.deps.ConnectionAdmin.Update(ctx, conn, keyPtr); err != nil {
			return connErrorResult("update", name, err), nil
		}
		// SECURITY: log the name and whether a key was rotated, never the key value.
		connLogger(srv).Info("connection updated",
			slog.String("connection", name),
			slog.Bool("key_rotated", keyPtr != nil))
		return MarshalResult(map[string]any{"status": "updated", "connection": metaOf(conn)})
	}
}

func connectionDeleteHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConnectionAdmin == nil {
			return gomcp.NewToolResultError("connection service not available"), nil
		}
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if err := srv.deps.ConnectionAdmin.Delete(ctx, name); err != nil {
			return connErrorResult("delete", name, err), nil
		}
		connLogger(srv).Info("connection deleted", slog.String("connection", name))
		return MarshalResult(map[string]any{"status": "deleted", "connection": name})
	}
}

func connectionGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConnectionAdmin == nil {
			return gomcp.NewToolResultError("connection service not available"), nil
		}
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		conn, err := srv.deps.ConnectionAdmin.Get(ctx, name)
		if err != nil {
			return connErrorResult("get", name, err), nil
		}
		// Metadata only: connectionMeta has no key field.
		return MarshalResult(metaOf(conn))
	}
}

func connectionListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConnectionAdmin == nil {
			return gomcp.NewToolResultError("connection service not available"), nil
		}
		conns, err := srv.deps.ConnectionAdmin.List(ctx)
		if err != nil {
			return connErrorResult("list", "", err), nil
		}
		metas := make([]connectionMeta, len(conns))
		for i, c := range conns {
			metas[i] = metaOf(c)
		}
		return MarshalResult(metas)
	}
}
