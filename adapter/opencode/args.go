package opencode

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrModelRequired is returned by buildArgs when the binding carries no model.
// Modern opencode routes via `-m <provider>/<model>`, so a model is mandatory.
var ErrModelRequired = errors.New("opencode: Model is required for run -m routing")

// Flag spellings for the modern (SST) opencode `run` permission/tool surface.
// Verified against opencode-ai >= 0.4.0 (npm install -g opencode-ai).
//
// LOCALIZED HERE so a binary re-verification can adjust spelling in ONE place
// without touching buildArgs' structure (a binary change is a one-line edit).
// permissionAllowAllFlag expresses the open-by-default (skip-permissions) stance
// — the modern equivalent of claudecli's --dangerously-skip-permissions;
// toolScopeFlag narrows the tool set to a comma-joined allowlist.
const (
	runSubcommand          = "run"
	permissionAllowAllFlag = "--allow-all-tools"
	toolScopeFlag          = "--allowed-tools"
)

// buildArgs assembles the modern `opencode run` argv for an agent mission.
//
//	opencode run "<prompt>" -m <provider>/<model> [permission/tool flags]
//
// The WorkDir is NOT an arg: it is the process cwd (RunSpec.Dir), the
// {{workspace}} pin; the empty-WorkDir guard lives in provider.go.
//
// Model routing maps binding.Provider -> opencode provider id and pairs it with
// the model leaf. Tool/permission selection follows the resolved Posture, with
// mission.ToolScope (per-request) preferred over posture.AllowedTools
// (per-profile), mirroring claudecli's ToolScope-over-global precedence.
//
// Fail-closed: an unknown provider or empty model returns (nil, err) so the
// caller never launches a mis-routed or tool-less run.
//
// SECURITY: only model/provider/prompt strings reach argv here; secrets are
// env-only (env.go), never argv or logs.
func buildArgs(mission engine.AgentMission, binding provider.ResolvedBinding, posture Posture) ([]string, error) {
	opencodeID, ok := mapProviderID(binding.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, binding.Provider)
	}
	if strings.TrimSpace(binding.Model) == "" {
		return nil, ErrModelRequired
	}
	modelRef := opencodeID + "/" + modelLeaf(binding.Model, opencodeID)

	args := []string{runSubcommand, mission.Prompt, "-m", modelRef}

	// Tool/permission selection. Per-request mission.ToolScope wins over the
	// per-profile posture.AllowedTools (same precedence as claudecli).
	scope := mission.ToolScope
	if len(scope) == 0 {
		scope = posture.AllowedTools
	}
	switch {
	case len(scope) > 0:
		// Narrowed run: emit only the named tools, never allow-all.
		args = append(args, toolScopeFlag, strings.Join(scope, ","))
	case posture.AllowAll:
		// Open-by-default: grant every tool/permission.
		args = append(args, permissionAllowAllFlag)
	default:
		// Config-disabled allow-all with no allowlist: emit nothing and let
		// opencode's own default permissioning apply.
	}
	return args, nil
}

// modelLeaf strips a leading "<opencodeID>/" prefix from a model string so a
// binding.Model already in provider/model form is not double-prefixed. Only a
// leading segment EQUAL to the mapped opencode id is stripped; an unrelated
// leading segment (e.g. an OpenRouter org like "meta-llama") is preserved.
func modelLeaf(model, opencodeID string) string {
	prefix := opencodeID + "/"
	if strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return model
}
