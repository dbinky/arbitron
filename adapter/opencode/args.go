package opencode

import (
	"errors"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrModelRequired is returned by buildArgs when the binding carries no model.
// Modern opencode routes via `-m <provider>/<model>`, so a model is mandatory.
var ErrModelRequired = errors.New("opencode: Model is required for run -m routing")

const (
	runSubcommand = "run"
	skipPermsFlag = "--dangerously-skip-permissions"
	formatFlag    = "--format"
	formatJSON    = "json"
)

// buildArgs builds the 1.17.x `opencode run` argv from an already-resolved
// descriptor (RunAgent resolves the provider once and threads it down).
// Permissions/base-url are NOT here — they ride OPENCODE_CONFIG_CONTENT
// (config.go). Fail-closed: an empty model returns (nil, err) with NO launch.
//
// The WorkDir is NOT an arg: it is the process cwd (RunSpec.Dir), the
// {{workspace}} pin; the empty-WorkDir guard lives in provider.go.
//
// SECURITY: only model/provider/prompt strings reach argv here; secrets are
// env-only (env.go), never argv or logs.
func buildArgs(mission engine.AgentMission, binding provider.ResolvedBinding, d providerDescriptor) ([]string, error) {
	if strings.TrimSpace(binding.Model) == "" {
		return nil, ErrModelRequired
	}
	modelRef := d.opencodeID + "/" + modelLeaf(binding.Model, d.opencodeID)
	return []string{
		runSubcommand, mission.Prompt,
		"-m", modelRef,
		skipPermsFlag,
		formatFlag, formatJSON,
	}, nil
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
