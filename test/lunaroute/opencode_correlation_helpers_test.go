package lunaroute_test

import "time"

const openCodeSubagentSource = "docs/evidence/lunaroute-provider/sources/lr06-subagent-lifecycle.md"

// The v1.19.5 request-preparation source emits these session headers even for
// openai-compatible providers. Join both identities and the actual exchange
// interval; overlapping main/title requests alone cannot prove a child call.
func correlateOpenCodeSubagent(c *inferenceCapture, task openCodeSubagentTask, proof openCodeSubagentProof, version string) error {
	if c == nil || version != "1.19.5" || proof.ChildSession != task.ChildSession || proof.ToolID != task.ToolID || len(proof.MessageIDs) == 0 {
		return errAgentEvidence
	}
	records := c.Records()
	if validateCapturedInference(records, task.Model, []string{"main"}) != nil {
		return errAgentEvidence
	}
	op := capturedOperation{RunID: c.runID, Backend: "opencode", ID: task.ToolID, Session: task.ParentSession, Category: "subagent", Trigger: "synchronous-task", Source: openCodeSubagentSource, Version: version, StartedAt: time.UnixMilli(task.StartedMS), FinishedAt: time.UnixMilli(task.FinishedMS).Add(time.Millisecond)}
	main := false
	for _, record := range records {
		if record.Backend != "opencode" || record.Path != "/v1/chat/completions" {
			return errAgentEvidence
		}
		if record.SessionID == task.ParentSession && record.ParentSessionID == "" {
			main = true
		}
		if record.SessionID != task.ChildSession {
			continue
		}
		if record.ParentSessionID != task.ParentSession || record.StartedAt.Before(op.StartedAt) || !record.FinishedAt.Before(op.FinishedAt) {
			return errAgentEvidence
		}
		op.RequestIDs = append(op.RequestIDs, record.RequestID)
	}
	if !main || len(op.RequestIDs) == 0 {
		return errAgentEvidence
	}
	return c.CorrelateOperation(op)
}
