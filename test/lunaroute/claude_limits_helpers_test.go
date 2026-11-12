package lunaroute_test

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
)

const claudeLimitsSource = "docs/evidence/lunaroute-provider/sources/lr05-limits.md"

// Context is the binary's resolved window, not the emitted env alone. Output
// is accepted only after the publisher joins actual Messages request budgets.
// Result modelUsage.maxOutputTokens is a catalog default, not that budget.
type claudeMainLimitProof struct {
	Source                string   `json:"source"`
	Version               string   `json:"version"`
	Session               string   `json:"session"`
	Model                 string   `json:"model"`
	ContextTokens         int      `json:"context_tokens"`
	RequestedOutputTokens int      `json:"requested_output_tokens"`
	RequestIDs            []string `json:"request_ids"`
}

func collectClaudeMainLimits(spec claudecli.RunSpec, rules provider.CapabilityRules, lines []string) (claudeMainLimitProof, error) {
	fail := func() (claudeMainLimitProof, error) { return claudeMainLimitProof{}, errAgentEvidence }
	if validateClaudeStartup(spec, claudeStartupVersion, lines) != nil {
		return fail()
	}
	model, err := claudeStartupSelection(spec)
	if err != nil {
		return fail()
	}
	facts, ok := rules.LunaRouteModel(model)
	if !ok || !facts.ContextTokens.Known() || !facts.OutputTokens.Known() {
		return fail()
	}
	env := map[string]string{}
	for _, entry := range spec.Env {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	if env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != strconv.Itoa(facts.ContextTokens.Tokens) || env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != strconv.Itoa(facts.OutputTokens.Tokens) {
		return fail()
	}
	var result struct {
		Session string `json:"session_id"`
		Usage   map[string]struct {
			Context int `json:"contextWindow"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal([]byte(lines[len(lines)-1]), &result) != nil || len(result.Usage) != 1 || result.Usage[model].Context != facts.ContextTokens.Tokens {
		return fail()
	}
	return claudeMainLimitProof{Source: claudeLimitsSource, Version: claudeStartupVersion, Session: result.Session, Model: model, ContextTokens: facts.ContextTokens.Tokens, RequestedOutputTokens: facts.OutputTokens.Tokens}, nil
}
