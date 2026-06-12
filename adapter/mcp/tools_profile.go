package mcp

import (
	"context"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/provider"
)

// profileMeta is the read shape for profile_list / profile_get and the echo on a
// write. It mirrors the provider.Profile fields the spec exposes — no secrets are
// involved (a profile only references a connection by name).
type profileMeta struct {
	Alias         string   `json:"alias"`
	Kind          string   `json:"kind"`
	ConnectionRef string   `json:"connection_ref,omitempty"`
	Model         string   `json:"model,omitempty"`
	Backend       string   `json:"backend,omitempty"`
	AllowedTools  []string `json:"allowed_tools,omitempty"`
	ContextWindow string   `json:"context_window,omitempty"`
	Effort        string   `json:"effort,omitempty"`
}

func profileMetaOf(p provider.Profile) profileMeta {
	return profileMeta{
		Alias:         p.Alias,
		Kind:          string(p.Kind),
		ConnectionRef: p.ConnectionRef,
		Model:         p.Model,
		Backend:       p.Backend,
		AllowedTools:  p.AllowedTools,
		ContextWindow: string(p.ContextWindow),
		Effort:        string(p.Effort),
	}
}

// effectiveBody is the floored, runtime-effective axis view echoed on a write.
type effectiveBody struct {
	ContextWindow string `json:"context_window"`
	Effort        string `json:"effort"`
}

func effectiveBodyOf(e provider.Effective) effectiveBody {
	return effectiveBody{ContextWindow: string(e.ContextWindow), Effort: string(e.Effort)}
}

// RegisterProfileTools registers the profile-management tools. Mutations
// (create/update/delete) are admin-only; list/get and provider_capabilities are
// read-open to any authenticated identity. The admin gate, capability
// validation, and post-write recache all live in the ProfileService; these
// handlers map its sentinels to clear tool errors (mirroring tools_user.go).
func RegisterProfileTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("profile_create",
			gomcp.WithDescription("Create a model profile (admin only). Validates context_window/effort against the backend/model capability rules and echoes the floored effective values. The response carries both profile.* (your requested axes, as stored) and effective.* (the values that will actually take effect at launch); compare them to detect whether a requested axis was floored to the backend/model ceiling."),
			gomcp.WithString("alias", gomcp.Required(), gomcp.Description("Unique profile alias referenced by workflow nodes")),
			gomcp.WithString("kind", gomcp.Required(), gomcp.Description("'chat' or 'agent'")),
			gomcp.WithString("connection_ref", gomcp.Description("Connection name supplying the endpoint+key; empty = claude-cli no-key subscription")),
			gomcp.WithString("model", gomcp.Description("Model id / Azure deployment / provider-model")),
			gomcp.WithString("backend", gomcp.Description("Agent backend (claude-code | codex | opencode); empty inherits")),
			gomcp.WithArray("allowed_tools", gomcp.Description("Per-profile tool allowlist (agent kind)")),
			gomcp.WithString("context_window", gomcp.Description("Context tier: default | 200k | 1m")),
			gomcp.WithString("effort", gomcp.Description("Reasoning effort: default | none | low | medium | high | xhigh | max")),
		),
		profileCreateHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("profile_update",
			gomcp.WithDescription("Update a model profile by alias (admin only). MERGE, not replace: omitted fields keep their stored value; pass a field (even empty) to change or clear it. The response carries both profile.* (the merged stored axes) and effective.* (the values that will actually take effect at launch); compare them to detect whether a requested axis was floored to the backend/model ceiling."),
			gomcp.WithString("alias", gomcp.Required(), gomcp.Description("Profile alias to update")),
			gomcp.WithString("kind", gomcp.Description("'chat' or 'agent'; omit to keep the stored kind")),
			gomcp.WithString("connection_ref", gomcp.Description("Connection name; empty = claude-cli no-key subscription")),
			gomcp.WithString("model", gomcp.Description("Model id / Azure deployment / provider-model")),
			gomcp.WithString("backend", gomcp.Description("Agent backend; empty inherits")),
			gomcp.WithArray("allowed_tools", gomcp.Description("Per-profile tool allowlist (agent kind)")),
			gomcp.WithString("context_window", gomcp.Description("Context tier: default | 200k | 1m")),
			gomcp.WithString("effort", gomcp.Description("Reasoning effort: default | none | low | medium | high | xhigh | max")),
		),
		profileUpdateHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("profile_delete",
			gomcp.WithDescription("Delete a model profile by alias (admin only)."),
			gomcp.WithString("alias", gomcp.Required(), gomcp.Description("Profile alias")),
		),
		profileDeleteHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("profile_get",
			gomcp.WithDescription("Get a model profile by alias. Readable by any authenticated user."),
			gomcp.WithString("alias", gomcp.Required(), gomcp.Description("Profile alias")),
		),
		profileGetHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("profile_list",
			gomcp.WithDescription("List all model profiles. Readable by any authenticated user."),
		),
		profileListHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("provider_capabilities",
			gomcp.WithDescription("List the per-backend/model allowed context tiers and effort levels so a caller can discover valid axis values before writing. Read-only; no secrets."),
		),
		providerCapabilitiesHandler(srv),
	)
}

// profileErrorResult maps ProfileService sentinels to stable tool error
// messages, mirroring userErrorResult/connErrorResult.
func profileErrorResult(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, provider.ErrForbidden):
		return gomcp.NewToolResultError("forbidden: admin only")
	case errors.Is(err, provider.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, provider.ErrAxisNotApplicable):
		return gomcp.NewToolResultError(err.Error())
	case errors.Is(err, provider.ErrProfileExists):
		return gomcp.NewToolResultError("profile already exists")
	case errors.Is(err, provider.ErrProfileNotFound):
		return gomcp.NewToolResultError("profile not found")
	case errors.Is(err, provider.ErrUnknownBackend),
		errors.Is(err, provider.ErrInvalidEffort),
		errors.Is(err, provider.ErrInvalidContextWindow):
		return gomcp.NewToolResultError(err.Error())
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("profile operation failed: %s", err))
	}
}

// stringSliceArg coerces an MCP array argument into a []string. A nil/absent
// value yields nil; any non-string element is an error.
func stringSliceArg(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("allowed_tools must be an array of strings")
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("allowed_tools values must be strings")
		}
		out = append(out, s)
	}
	return out, nil
}

// profileFromArgs builds a provider.Profile from the create/update arguments.
// context_window/effort are parsed here so a malformed axis string fails before
// the service is reached. The empty string normalizes to the default sentinel.
func profileFromArgs(args map[string]any) (provider.Profile, error) {
	alias, _ := args["alias"].(string)
	if alias == "" {
		return provider.Profile{}, fmt.Errorf("alias is required")
	}
	kind, _ := args["kind"].(string)
	if kind == "" {
		return provider.Profile{}, fmt.Errorf("kind is required")
	}
	allowed, err := stringSliceArg(args["allowed_tools"])
	if err != nil {
		return provider.Profile{}, err
	}
	cwStr, _ := args["context_window"].(string)
	cw, err := provider.ParseContextWindow(cwStr)
	if err != nil {
		return provider.Profile{}, err
	}
	effStr, _ := args["effort"].(string)
	eff, err := provider.ParseEffort(effStr)
	if err != nil {
		return provider.Profile{}, err
	}
	connRef, _ := args["connection_ref"].(string)
	model, _ := args["model"].(string)
	backend, _ := args["backend"].(string)
	return provider.Profile{
		Alias:         alias,
		Kind:          provider.ProfileKind(kind),
		ConnectionRef: connRef,
		Model:         model,
		Backend:       backend,
		AllowedTools:  allowed,
		ContextWindow: cw,
		Effort:        eff,
	}, nil
}

func profileCreateHandler(srv *Server) server.ToolHandlerFunc {
	return profileCreateWriteHandler(srv, func(ctx context.Context, p provider.Profile) (provider.Profile, provider.Effective, error) {
		return srv.deps.ProfileAdmin.Create(ctx, p)
	})
}

// patchFromArgs builds a sparse provider.ProfilePatch from the update arguments.
// It distinguishes "field absent" (leave the stored value unchanged) from "field
// present but empty" (explicit clear) via `_, present := args[k]`, so a partial
// edit does not silently blank the fields the caller did not touch. alias and kind
// are not patched here: alias is the identity key, kind stays required. The merge
// against the existing profile happens in ProfileService.UpdatePatch (the domain),
// not here, so a future REST surface inherits the same safe semantics.
func patchFromArgs(args map[string]any) (provider.ProfilePatch, error) {
	var patch provider.ProfilePatch
	if v, present := args["kind"]; present {
		s, ok := v.(string)
		if !ok {
			return provider.ProfilePatch{}, fmt.Errorf("kind must be a string")
		}
		patch.Kind = &s
	}
	if v, present := args["connection_ref"]; present {
		s, ok := v.(string)
		if !ok {
			return provider.ProfilePatch{}, fmt.Errorf("connection_ref must be a string")
		}
		patch.ConnectionRef = &s
	}
	if v, present := args["model"]; present {
		s, ok := v.(string)
		if !ok {
			return provider.ProfilePatch{}, fmt.Errorf("model must be a string")
		}
		patch.Model = &s
	}
	if v, present := args["backend"]; present {
		s, ok := v.(string)
		if !ok {
			return provider.ProfilePatch{}, fmt.Errorf("backend must be a string")
		}
		patch.Backend = &s
	}
	if _, present := args["allowed_tools"]; present {
		tools, err := stringSliceArg(args["allowed_tools"])
		if err != nil {
			return provider.ProfilePatch{}, err
		}
		if tools == nil {
			tools = []string{}
		}
		patch.AllowedTools = &tools
	}
	if v, present := args["context_window"]; present {
		s, ok := v.(string)
		if !ok {
			return provider.ProfilePatch{}, fmt.Errorf("context_window must be a string")
		}
		patch.ContextWindow = &s
	}
	if v, present := args["effort"]; present {
		s, ok := v.(string)
		if !ok {
			return provider.ProfilePatch{}, fmt.Errorf("effort must be a string")
		}
		patch.Effort = &s
	}
	return patch, nil
}

// profileUpdateHandler implements profile_update as a MERGE, not a destructive
// full-replace: it builds a sparse patch from the present arguments and lets the
// ProfileService overlay only those onto the stored profile, preserving the rest.
func profileUpdateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ProfileAdmin == nil {
			return gomcp.NewToolResultError("profile service not available"), nil
		}
		args := request.GetArguments()
		alias, _ := args["alias"].(string)
		if alias == "" {
			return gomcp.NewToolResultError("alias is required"), nil
		}
		patch, err := patchFromArgs(args)
		if err != nil {
			return profileErrorResult(err), nil
		}
		stored, eff, err := srv.deps.ProfileAdmin.UpdatePatch(ctx, alias, patch)
		if err != nil {
			return profileErrorResult(err), nil
		}
		return MarshalResult(map[string]any{
			"status":    "updated",
			"profile":   profileMetaOf(stored),
			"effective": effectiveBodyOf(eff),
		})
	}
}

// profileCreateWriteHandler is the create handler: nil-guard, full arg parse
// (axis parse errors map to tool errors), service call, sentinel mapping, and the
// {status, profile, effective} echo on success. Create is a full write (every
// field comes from the request); update uses the sparse-patch merge path instead.
func profileCreateWriteHandler(srv *Server, write func(context.Context, provider.Profile) (provider.Profile, provider.Effective, error)) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ProfileAdmin == nil {
			return gomcp.NewToolResultError("profile service not available"), nil
		}
		p, err := profileFromArgs(request.GetArguments())
		if err != nil {
			return profileErrorResult(err), nil
		}
		stored, eff, err := write(ctx, p)
		if err != nil {
			return profileErrorResult(err), nil
		}
		return MarshalResult(map[string]any{
			"status":    "created",
			"profile":   profileMetaOf(stored),
			"effective": effectiveBodyOf(eff),
		})
	}
}

func profileDeleteHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ProfileAdmin == nil {
			return gomcp.NewToolResultError("profile service not available"), nil
		}
		alias, _ := request.GetArguments()["alias"].(string)
		if alias == "" {
			return gomcp.NewToolResultError("alias is required"), nil
		}
		if err := srv.deps.ProfileAdmin.Delete(ctx, alias); err != nil {
			return profileErrorResult(err), nil
		}
		return MarshalResult(map[string]any{"status": "deleted", "alias": alias})
	}
}

func profileGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ProfileAdmin == nil {
			return gomcp.NewToolResultError("profile service not available"), nil
		}
		alias, _ := request.GetArguments()["alias"].(string)
		if alias == "" {
			return gomcp.NewToolResultError("alias is required"), nil
		}
		p, err := srv.deps.ProfileAdmin.Get(ctx, alias)
		if err != nil {
			return profileErrorResult(err), nil
		}
		return MarshalResult(profileMetaOf(p))
	}
}

func profileListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ProfileAdmin == nil {
			return gomcp.NewToolResultError("profile service not available"), nil
		}
		list, err := srv.deps.ProfileAdmin.List(ctx)
		if err != nil {
			return profileErrorResult(err), nil
		}
		metas := make([]profileMeta, 0, len(list))
		for _, p := range list {
			metas = append(metas, profileMetaOf(p))
		}
		return MarshalResult(metas)
	}
}

// backendCapabilityBody is the read shape for provider_capabilities: per backend,
// the allowed context tiers and effort levels (and any per-model overrides).
type backendCapabilityBody struct {
	Backend        string                         `json:"backend"`
	ContextTiers   []string                       `json:"context_tiers,omitempty"`
	EffortLevels   []string                       `json:"effort_levels,omitempty"`
	ModelOverrides map[string]modelCapabilityBody `json:"model_overrides,omitempty"`
}

type modelCapabilityBody struct {
	ContextTiers []string          `json:"context_tiers,omitempty"`
	EffortLevels []string          `json:"effort_levels,omitempty"`
	EffortFloor  map[string]string `json:"effort_floor,omitempty"`
}

func ctxTierStrings(tiers []provider.ContextWindow) []string {
	if tiers == nil {
		return nil
	}
	out := make([]string, len(tiers))
	for i, t := range tiers {
		out[i] = string(t)
	}
	return out
}

func effortStrings(levels []provider.Effort) []string {
	if levels == nil {
		return nil
	}
	out := make([]string, len(levels))
	for i, e := range levels {
		out[i] = string(e)
	}
	return out
}

func providerCapabilitiesHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ProfileAdmin == nil {
			return gomcp.NewToolResultError("profile service not available"), nil
		}
		rules, err := srv.deps.ProfileAdmin.Capabilities(ctx)
		if err != nil {
			return profileErrorResult(err), nil
		}
		desc := rules.Describe()
		out := make(map[string]backendCapabilityBody, len(desc))
		for name, bc := range desc {
			body := backendCapabilityBody{
				Backend:      bc.Backend,
				ContextTiers: ctxTierStrings(bc.ContextTiers),
				EffortLevels: effortStrings(bc.EffortLevels),
			}
			if len(bc.ModelOverrides) > 0 {
				body.ModelOverrides = make(map[string]modelCapabilityBody, len(bc.ModelOverrides))
				for model, mc := range bc.ModelOverrides {
					mb := modelCapabilityBody{
						ContextTiers: ctxTierStrings(mc.ContextTiers),
						EffortLevels: effortStrings(mc.EffortLevels),
					}
					if len(mc.EffortFloor) > 0 {
						mb.EffortFloor = make(map[string]string, len(mc.EffortFloor))
						for from, to := range mc.EffortFloor {
							mb.EffortFloor[string(from)] = string(to)
						}
					}
					body.ModelOverrides[model] = mb
				}
			}
			out[name] = body
		}
		return MarshalResult(out)
	}
}
