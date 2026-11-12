package lunaroute_test

import (
	"net/http"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
)

const claudeSubagentRoutingSource = "docs/evidence/lunaroute-provider/sources/lr05-subagent-routing.md"

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
	main := false
	for _, record := range records {
		if record.SessionID != s.session || record.Category != "main" || record.Operation != nil || record.ClaudeParentAgentID != "" {
			return fail()
		}
		switch record.ClaudeRequestClass {
		case "main":
			if record.ClaudeAgentID != "" || record.ClaudeAgentType != "" {
				return fail()
			}
			main = true
		case "subagent":
			if record.ClaudeAgentID != s.subagent.TaskID || record.ClaudeAgentType != "general-purpose" {
				return fail()
			}
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
	if !main || len(op.RequestIDs) == 0 || capture.CorrelateOperation(op) != nil {
		return fail()
	}
	observation.Inference = capture.Records()
	if validateCapturedInference(observation.Inference, result.Model, []string{"main", "subagent"}) != nil {
		return fail()
	}
	observation.Scope = "main isolation/controls/memory and synchronous subagent lifecycle plus captured routing only; no other helper or complete contract verification"
	return observation, nil
}
