package lunaroute_test

import (
	"net/http"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

const claudeSubagentRoutingSource = "docs/evidence/lunaroute-provider/sources/lr05-subagent-routing.md"

// This is the observed subagent wire budget, not a resolved helper context
// window or a claim that the gateway enforces a maximum. Selection comes from
// the same emitted launch; values come only from attributed captured requests.
type claudeSubagentControlProof struct {
	Sources   []string                       `json:"sources"`
	Version   string                         `json:"version"`
	Session   string                         `json:"session"`
	Model     string                         `json:"model"`
	TaskID    string                         `json:"task_id"`
	RunID     string                         `json:"run_id"`
	ToolUseID string                         `json:"tool_use_id"`
	Selected  provider.Effort                `json:"selected"`
	Requests  []claudeSubagentRequestControl `json:"requests"`
}

type claudeSubagentRequestControl struct {
	RequestID string                `json:"request_id"`
	MaxTokens int                   `json:"max_tokens"`
	Effort    string                `json:"effort"`
	Thinking  claudeRequestThinking `json:"thinking"`
}

// Only the five source-defined routing identities are projected. In particular,
// custom headers, metadata.user_id and prompt content are never serialized.
func projectClaudeRoutingHeaders(headers http.Header, record *capturedInference, key string) error {
	for _, field := range []struct {
		name string
		dest *string
	}{
		{"X-Claude-Code-Session-Id", &record.SessionID},
		{"X-Claude-Code-Agent-Id", &record.ClaudeAgentID},
		{"X-Claude-Code-Parent-Agent-Id", &record.ClaudeParentAgentID},
		{"X-Claude-Code-Request-Class", &record.ClaudeRequestClass},
		{"X-Claude-Code-Agent-Type", &record.ClaudeAgentType},
	} {
		values := headers.Values(field.name)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || !safeAgentField(values[0], key) {
			return errAgentEvidence
		}
		// Source headers percent-encode non-ASCII identities. This deliberately
		// narrow experiment requires unambiguous ASCII ids, not decoded user text.
		for _, ch := range values[0] {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return errAgentEvidence
			}
		}
		*field.dest = values[0]
	}
	return nil
}

// The accepted same-launch lifecycle supplies task/run/tool/session identities.
// Headers supply request attribution; terminal responses supply executed models.
// Neither lifecycle intent nor the request-class header alone proves a helper.
func collectClaudeSubagentRoutingEvidence(s *claudeIsolation, capture *inferenceCapture, result *engine.AgentResult, started, finished time.Time) (claudeIsolationObservation, error) {
	fail := func() (claudeIsolationObservation, error) { return claudeIsolationObservation{}, errAgentEvidence }
	if capture == nil {
		return fail()
	}
	records := capture.Records()
	observation, err := collectClaudeIsolationEvidence(s, records, result, started, finished)
	if err != nil {
		return fail()
	}
	op := capturedOperation{RunID: capture.runID, Backend: "claude-code", ID: s.subagent.ToolUseID,
		Session: s.session, Category: "subagent", Trigger: "synchronous-agent", Source: claudeSubagentRoutingSource, Version: s.subagent.Version,
		StartedAt: finished, FinishedAt: started}
	controls := &claudeSubagentControlProof{
		Sources: []string{claudeLimitsSource, claudeEffortSource, claudeThinkingSource, claudeSubagentRoutingSource},
		Version: s.subagent.Version, Session: s.session, Model: result.Model,
		TaskID: s.subagent.TaskID, RunID: s.subagent.RunID, ToolUseID: s.subagent.ToolUseID, Selected: s.effort.Selected,
	}
	var mainRecords []capturedInference
	for _, record := range records {
		if record.SessionID != s.session || record.Category != "main" || record.Operation != nil || record.ClaudeParentAgentID != "" {
			return fail()
		}
		if record.ClaudeSessionTitle {
			continue // Validated and attributed separately before private publication.
		}
		switch record.ClaudeRequestClass {
		case "main":
			if record.ClaudeAgentID != "" || record.ClaudeAgentType != "" {
				return fail()
			}
			mainRecords = append(mainRecords, record)
		case "subagent":
			if record.ClaudeAgentID != s.subagent.TaskID || record.ClaudeAgentType != "general-purpose" {
				return fail()
			}
			controls.Requests = append(controls.Requests, claudeSubagentRequestControl{
				RequestID: record.RequestID, MaxTokens: record.MaxTokens, Effort: record.ClaudeEffort.Level, Thinking: *record.ClaudeThinking,
			})
			op.RequestIDs = append(op.RequestIDs, record.RequestID)
			if record.StartedAt.Before(op.StartedAt) {
				op.StartedAt = record.StartedAt
			}
			if record.FinishedAt.After(op.FinishedAt) {
				op.FinishedAt = record.FinishedAt
			}
		default:
			return fail()
		}
	}
	if len(mainRecords) == 0 || len(observation.ProjectMemory.RequestIDs) != 1 || observation.ProjectMemory.RequestIDs[0] != mainRecords[0].RequestID || len(op.RequestIDs) == 0 || capture.CorrelateOperation(op) != nil {
		return fail()
	}
	observation.Inference = capture.Records()
	if validateCapturedInference(observation.Inference, result.Model, []string{"main", "subagent"}) != nil {
		return fail()
	}
	// The base collector validates every exchange. Only after lifecycle/header
	// attribution may the publisher split main proofs from subagent controls.
	observation.Limits.RequestIDs = nil
	for _, record := range mainRecords {
		observation.Limits.RequestIDs = append(observation.Limits.RequestIDs, record.RequestID)
	}
	observation.Effort, err = joinClaudeRequestEffort(s.effort, s.session, result.Model, mainRecords)
	if err != nil {
		return fail()
	}
	observation.Thinking, err = joinClaudeRequestThinking(observation.Effort, mainRecords)
	if err != nil {
		return fail()
	}
	observation.SubagentControls = controls
	observation.Scope = "main isolation/controls/memory and synchronous subagent lifecycle, captured routing and request controls only; no helper context-window, other helper or complete contract verification"
	return observation, nil
}
