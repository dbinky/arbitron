package lunaroute_test

import (
	"bytes"
	"encoding/json"

	"github.com/arbitron/arbitron/domain/provider"
)

const (
	claudeThinkingSource        = "docs/evidence/lunaroute-provider/sources/lr05-thinking.md"
	claudeMinimumThinkingBudget = 1024
)

// A nonnil projection distinguishes observed absence from a lost capture feed.
// Absence never establishes disabled thinking, even when the env requests it.
type claudeRequestThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type claudeMainThinkingProof struct {
	Source     string                  `json:"source"`
	Version    string                  `json:"version"`
	Session    string                  `json:"session"`
	Model      string                  `json:"model"`
	Selected   provider.Effort         `json:"selected"`
	RequestIDs []string                `json:"request_ids"`
	Observed   []claudeRequestThinking `json:"observed"`
}

func validClaudeThinking(p claudeRequestThinking, maxTokens int) bool {
	switch p.Type {
	case "", "disabled", "adaptive":
		return p.BudgetTokens == 0
	case "enabled":
		return p.BudgetTokens >= claudeMinimumThinkingBudget && p.BudgetTokens < maxTokens
	}
	return false
}

func parseClaudeRequestThinking(raw []byte) (*claudeRequestThinking, error) {
	fail := func() (*claudeRequestThinking, error) { return nil, errAgentEvidence }
	if len(raw) > captureBodyLimit || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) {
		return fail()
	}
	var body struct {
		Thinking  json.RawMessage `json:"thinking"`
		MaxTokens int             `json:"max_tokens"`
		Messages  []struct {
			Thinking json.RawMessage `json:"thinking"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &body) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fail()
	}
	for _, message := range body.Messages {
		if len(message.Thinking) != 0 {
			return fail()
		}
	}
	proof := &claudeRequestThinking{}
	if len(body.Thinking) == 0 {
		return proof, nil
	}
	var thinking struct {
		Type   string          `json:"type"`
		Budget json.RawMessage `json:"budget_tokens"`
	}
	if json.Unmarshal(body.Thinking, &thinking) != nil || thinking.Type == "" {
		return fail()
	}
	proof.Type = thinking.Type
	if len(thinking.Budget) != 0 {
		if thinking.Type != "enabled" || json.Unmarshal(thinking.Budget, &proof.BudgetTokens) != nil {
			return fail()
		}
	}
	if !validClaudeThinking(*proof, body.MaxTokens) {
		return fail()
	}
	return proof, nil
}

func joinClaudeRequestThinking(effort claudeMainEffortProof, records []capturedInference) (claudeMainThinkingProof, error) {
	fail := func() (claudeMainThinkingProof, error) { return claudeMainThinkingProof{}, errAgentEvidence }
	if effort.Source != claudeEffortSource || effort.Version != claudeStartupVersion || effort.Session == "" || effort.Model == "" || len(records) == 0 {
		return fail()
	}
	proof := claudeMainThinkingProof{Source: claudeThinkingSource, Version: effort.Version, Session: effort.Session, Model: effort.Model, Selected: effort.Selected}
	for _, record := range records {
		if record.ClaudeThinking == nil || !validClaudeThinking(*record.ClaudeThinking, record.MaxTokens) || effort.Selected == provider.EffortNone && record.ClaudeThinking.Type != "disabled" {
			return fail()
		}
		proof.RequestIDs = append(proof.RequestIDs, record.RequestID)
		proof.Observed = append(proof.Observed, *record.ClaudeThinking)
	}
	return proof, nil
}
