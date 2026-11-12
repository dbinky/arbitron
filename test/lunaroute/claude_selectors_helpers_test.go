package lunaroute_test

import (
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

const claudeSelectorsSource = "docs/evidence/lunaroute-provider/sources/lr05-selectors.md"

// This is launch intent, not evidence that a particular helper honors a selector.
// Actual executed models remain in lifecycle-correlated gateway records.
type claudeSelectorProof struct {
	Kind    string            `json:"kind"`
	Source  string            `json:"source"`
	Version string            `json:"version"`
	Session string            `json:"session"`
	Model   string            `json:"model"`
	Values  map[string]string `json:"values"`
}

func claudeSelectorNames() []string {
	return []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"}
}

func collectClaudeSelectors(spec claudecli.RunSpec) (claudeSelectorProof, error) {
	model, err := claudeStartupSelection(spec)
	if err != nil {
		return claudeSelectorProof{}, errAgentEvidence
	}
	values := make(map[string]string)
	for _, name := range claudeSelectorNames() {
		for _, entry := range spec.Env {
			if value, ok := strings.CutPrefix(entry, name+"="); ok {
				if value != model {
					return claudeSelectorProof{}, errAgentEvidence
				}
				values[name] = value
			}
		}
		if values[name] != model {
			return claudeSelectorProof{}, errAgentEvidence
		}
	}
	return claudeSelectorProof{Kind: "configured_intent", Source: claudeSelectorsSource, Model: model, Values: values}, nil
}

func validateClaudeSelectors(proof claudeSelectorProof, session, model string) error {
	if proof.Kind != "configured_intent" || proof.Source != claudeSelectorsSource || proof.Version != claudeStartupVersion || proof.Session != session || proof.Model != model || len(proof.Values) != len(claudeSelectorNames()) {
		return errAgentEvidence
	}
	for _, name := range claudeSelectorNames() {
		if proof.Values[name] != model {
			return errAgentEvidence
		}
	}
	return nil
}
