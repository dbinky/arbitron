package lunaroute_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
)

const claudeEffortSource = "docs/evidence/lunaroute-provider/sources/lr05-effort.md"

// A nonnil projection proves that the actual request was parsed, including an
// absent effort parameter. It never supplies a missing value from env intent.
type claudeRequestEffort struct {
	Level string `json:"level"`
}

type claudeMainEffortProof struct {
	Source         string          `json:"source"`
	Version        string          `json:"version"`
	Session        string          `json:"session"`
	Model          string          `json:"model"`
	Selected       provider.Effort `json:"selected"`
	ObservedLevels []string        `json:"observed_levels"`
	RequestIDs     []string        `json:"request_ids"`
}

func claudeGradedEffort(level string) bool {
	switch provider.Effort(level) {
	case provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh, provider.EffortMax:
		return true
	}
	return false
}

func parseClaudeRequestEffort(raw []byte) (*claudeRequestEffort, error) {
	if len(raw) > captureBodyLimit || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) {
		return nil, errAgentEvidence
	}
	var body struct {
		Output   json.RawMessage `json:"output_config"`
		Messages []struct {
			Output json.RawMessage `json:"output_config"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &body) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errAgentEvidence
	}
	read := func(raw json.RawMessage) (json.RawMessage, error) {
		if len(raw) == 0 {
			return nil, nil
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return nil, errAgentEvidence
		}
		return fields["effort"], nil
	}
	// Per-message effort can override the request-wide field. This narrow main
	// collector refuses that separate protocol rather than misreporting it.
	for _, message := range body.Messages {
		level, err := read(message.Output)
		if err != nil || len(level) != 0 {
			return nil, errAgentEvidence
		}
	}
	level, err := read(body.Output)
	if err != nil {
		return nil, errAgentEvidence
	}
	proof := &claudeRequestEffort{}
	if len(level) != 0 && (json.Unmarshal(level, &proof.Level) != nil || !claudeGradedEffort(proof.Level)) {
		return nil, errAgentEvidence
	}
	return proof, nil
}

func collectClaudeEffortSelection(spec claudecli.RunSpec, rules provider.CapabilityRules, selected provider.Effort) (claudeMainEffortProof, error) {
	fail := func() (claudeMainEffortProof, error) { return claudeMainEffortProof{}, errAgentEvidence }
	model, err := claudeStartupSelection(spec)
	if err != nil {
		return fail()
	}
	if selected == "" {
		selected = provider.EffortDefault
	}
	env := map[string]string{}
	for _, entry := range spec.Env {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	for _, arg := range spec.Args {
		if arg == "--effort" || strings.HasPrefix(arg, "--effort=") || arg == "--thinking" || strings.HasPrefix(arg, "--thinking=") {
			return fail()
		}
	}
	if _, exists := env["MAX_THINKING_TOKENS"]; exists {
		// EffortNone requires a separate effective-thinking proof, not mere
		// absence of output_config.effort on a model without effort support.
		return fail()
	}
	if selected == provider.EffortDefault {
		if _, exists := env["CLAUDE_CODE_EFFORT_LEVEL"]; exists {
			return fail()
		}
	} else {
		facts, ok := rules.LunaRouteModel(model)
		effort := facts.Effort("claude-code")
		if !claudeGradedEffort(string(selected)) || !ok || !effort.Known || !slices.Contains(effort.Levels, selected) || env["CLAUDE_CODE_EFFORT_LEVEL"] != string(selected) {
			return fail()
		}
	}
	return claudeMainEffortProof{Source: claudeEffortSource, Version: claudeStartupVersion, Model: model, Selected: selected}, nil
}

func joinClaudeRequestEffort(proof claudeMainEffortProof, session, model string, records []capturedInference) (claudeMainEffortProof, error) {
	fail := func() (claudeMainEffortProof, error) { return claudeMainEffortProof{}, errAgentEvidence }
	if proof.Source != claudeEffortSource || proof.Version != claudeStartupVersion || proof.Session != session || proof.Model != model || len(records) == 0 || proof.Selected != provider.EffortDefault && !claudeGradedEffort(string(proof.Selected)) {
		return fail()
	}
	proof.RequestIDs, proof.ObservedLevels = nil, nil
	for _, record := range records {
		if record.ClaudeEffort == nil {
			return fail()
		}
		level := record.ClaudeEffort.Level
		if level != "" && !claudeGradedEffort(level) || proof.Selected != provider.EffortDefault && level != string(proof.Selected) {
			return fail()
		}
		proof.RequestIDs = append(proof.RequestIDs, record.RequestID)
		proof.ObservedLevels = append(proof.ObservedLevels, level)
	}
	return proof, nil
}
