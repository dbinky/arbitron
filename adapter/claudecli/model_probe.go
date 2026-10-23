package claudecli

import (
	"errors"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

var errModelExecutionEvidence = errors.New("claudecli: model execution evidence missing or inconsistent")

// buildProbeArgs retains the production print protocol and model/session inputs,
// but removes built-in tools, inherited MCP and customizations. Unlike --bare,
// safe mode preserves subscription auth. Unsupported flags fail closed; never
// retry without them. See testdata/probe-restrictions.md for the source contract.
func buildProbeArgs(req engine.ChatRequest) []string {
	return append(buildArgs(req, argsPolicy{SuppressTools: true}),
		"--tools", "",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--safe-mode", "--setting-sources", "", "--no-chrome")
}

func probeEnv(env []string) []string {
	return append(env,
		"DISABLE_UPDATES=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1")
}

// requiredExecutedModel validates the model that will actually reach --model.
// An empty required model must not select the CLI/config default instead.
func requiredExecutedModel(model string, required bool, binding provider.ResolvedBinding) (string, error) {
	if !required && !binding.Pinned {
		return "", nil
	}
	if strings.TrimSpace(model) == "" || binding.Pinned && binding.Model != model {
		return "", errModelExecutionEvidence
	}
	return model, nil
}
