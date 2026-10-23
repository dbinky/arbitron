package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
)

// RegisterModelIdentityPolicyTools registers the admin-only D2 identity policy tools.
func RegisterModelIdentityPolicyTools(srv *Server) {
	srv.AddTool(gomcp.NewTool("model_identity_policy_get",
		gomcp.WithDescription("Get the model identity pattern policy and its revision (admin only)"),
	), modelIdentityPolicyHandler(srv, false))
	srv.AddTool(gomcp.NewTool("model_identity_policy_update",
		gomcp.WithDescription("Replace the whole model identity pattern policy when expected_revision is current (admin only)"),
		gomcp.WithNumber("expected_revision", gomcp.Required(), gomcp.Description("Revision returned by the last get; a stale value conflicts")),
		gomcp.WithArray("rules", gomcp.Required(), gomcp.Description("Complete rule list; anchored full-match patterns over catalog model IDs"),
			gomcp.Items(map[string]any{
				"type":     "object",
				"required": []string{"providers", "tier", "pattern", "version_groups"},
				"properties": map[string]any{
					"providers":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"tier":           map[string]any{"type": "string"},
					"pattern":        map[string]any{"type": "string"},
					"version_groups": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				},
			})),
	), modelIdentityPolicyHandler(srv, true))
}

func modelIdentityPolicyError(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, identity.ErrForbidden):
		return gomcp.NewToolResultError("admin only")
	case errors.Is(err, modelidentity.ErrInvalid):
		return gomcp.NewToolResultError(err.Error())
	case errors.Is(err, modelidentity.ErrConflict):
		return gomcp.NewToolResultError("identity policy revision conflict: re-read the policy and retry")
	default:
		return gomcp.NewToolResultError("identity policy unavailable")
	}
}

func modelIdentityPolicyHandler(srv *Server, update bool) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.IdentityPolicy == nil {
			return gomcp.NewToolResultError("identity policy unavailable"), nil
		}
		if !update {
			policy, err := srv.deps.IdentityPolicy.Get(ctx)
			if err != nil {
				return modelIdentityPolicyError(err), nil
			}
			return MarshalResult(policy)
		}
		args := request.GetArguments()
		for key := range args {
			if key != "expected_revision" && key != "rules" {
				return gomcp.NewToolResultError("unknown identity policy field"), nil
			}
		}
		expected, err := parseRevision(args["expected_revision"])
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		rules, err := parsePolicyRules(args["rules"])
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		policy, err := srv.deps.IdentityPolicy.Update(ctx, expected, rules)
		if err != nil {
			return modelIdentityPolicyError(err), nil
		}
		return MarshalResult(policy)
	}
}

func parseRevision(raw any) (int64, error) {
	f, ok := raw.(float64)
	if !ok || f != math.Trunc(f) || f < 1 || f >= math.MaxInt64 {
		return 0, errors.New("expected_revision must be a positive integer")
	}
	return int64(f), nil
}

func parsePolicyRules(raw any) ([]modelidentity.PatternRule, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("rules must be an array")
	}
	rules := make([]modelidentity.PatternRule, 0, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("rules[%d] must be an object", i)
		}
		var rule modelidentity.PatternRule
		for key, val := range obj {
			switch key {
			case "providers":
				vals, ok := val.([]any)
				if !ok {
					return nil, fmt.Errorf("rules[%d].providers must be an array of strings", i)
				}
				for _, v := range vals {
					s, ok := v.(string)
					if !ok {
						return nil, fmt.Errorf("rules[%d].providers must be an array of strings", i)
					}
					rule.Providers = append(rule.Providers, s)
				}
			case "tier":
				s, ok := val.(string)
				if !ok {
					return nil, fmt.Errorf("rules[%d].tier must be a string", i)
				}
				rule.Tier = s
			case "pattern":
				s, ok := val.(string)
				if !ok {
					return nil, fmt.Errorf("rules[%d].pattern must be a string", i)
				}
				rule.Pattern = s
			case "version_groups":
				vals, ok := val.([]any)
				if !ok {
					return nil, fmt.Errorf("rules[%d].version_groups must be an array of integers", i)
				}
				for _, v := range vals {
					f, ok := v.(float64)
					if !ok || f != math.Trunc(f) || math.Abs(f) > math.MaxInt32 {
						return nil, fmt.Errorf("rules[%d].version_groups must be an array of integers", i)
					}
					rule.VersionGroups = append(rule.VersionGroups, int(f))
				}
			default:
				return nil, fmt.Errorf("rules[%d] has unknown field %q", i, key)
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
