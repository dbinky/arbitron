package lunaroute_test

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	openCodeCompactionSource      = "docs/evidence/lunaroute-provider/sources/lr06-compaction-lifecycle.md"
	openCodeCompactionSystem      = "You are a context summarization agent. You are given a conversation between a user and an agent. Your goal is to produce a structured summary matching the format specified so another coding agent can continue the work."
	openCodeCompactionPrefix      = "Here is the conversation so far:\n\n<conversation>\n"
	openCodeCompactionInstruction = "Create a new anchored summary from the conversation history in the <conversation> tags above so another coding agent can continue the work."
	openCodeCompactionTemplate    = `Output exactly the Markdown structure shown inside <template> and keep the section order unchanged. Do not include the <template> tags in your response.
<template>
## Objective
- [one or two brief sentences describing what the user is trying to accomplish]

## Important Details
- [constraints/preferences, decisions and why, important facts/assumptions, exact context needed to continue, or "(none)"]

## Work State
### Completed
- [finished work, verified facts, or changes made; otherwise "(none)"]

### Active
- [current work, partial changes, or investigation state; otherwise "(none)"]

### Blocked
- [blockers, failing commands, or unknowns; otherwise "(none)"]

## Next Move
1. [immediate concrete action, or "(none)"]
2. [next action if known, or "(none)"]

## Relevant Files
- [file or directory path: why it matters, or "(none)"]
</template>

Rules:
- Keep every section, even when empty.
- Use terse bullets, not prose paragraphs.
- Preserve exact file paths, symbols, commands, error strings, URLs, and identifiers when known.
- Do not mention the summary process or that context was compacted.`
)

// First automatic compaction only: no plugin-replaced prompts, prior summaries
// or caller-provided categories. Compare content privately against export.
func isOpenCodeCompactionRequest(raw []byte) bool {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Messages) != 2 || len(body.Tools) != 0 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
		return false
	}
	system, ok := openCodeWireText(body.Messages[0].Content)
	prompt, promptOK := openCodeWireText(body.Messages[1].Content)
	if !ok || !promptOK || !strings.HasPrefix(system, openCodeCompactionSystem+"\n") || !strings.HasPrefix(prompt, openCodeCompactionPrefix) {
		return false
	}
	conversation, instructions, found := strings.Cut(strings.TrimPrefix(prompt, openCodeCompactionPrefix), "\n</conversation>\n\n")
	return found && strings.Contains(conversation, "[User]: "+agentMissionPromptFor("opencode")) && instructions == openCodeCompactionInstruction+"\n\n"+openCodeCompactionTemplate
}

type openCodeCompactionProof struct {
	MessageID, PartID     string
	StartedMS, FinishedMS int64
	text                  string
}

func parseOpenCodeCompaction(raw []byte, session, model, key string, started, finished time.Time) (openCodeCompactionProof, error) {
	fail := func() (openCodeCompactionProof, error) { return openCodeCompactionProof{}, errAgentEvidence }
	out, err := decodeOpenCodeSession(raw)
	if err != nil || !openCodeID(session, "ses_") || out.Info.ID != session || out.Info.ParentID != "" || started.IsZero() || !finished.After(started) {
		return fail()
	}
	if _, err := explicitLiveModel(func(string) (string, bool) { return model, true }, "model"); err != nil {
		return fail()
	}
	var proof openCodeCompactionProof
	parent := ""
	seen := map[string]bool{}
	for _, message := range out.Messages {
		info := message.Info
		hasCompaction := false
		for _, part := range message.Parts {
			hasCompaction = hasCompaction || part.Type == "compaction"
		}
		if !hasCompaction && !info.Summary {
			continue
		}
		if !openCodeID(info.ID, "msg_") || seen[info.ID] || info.SessionID != session || info.Time.Created < started.UnixMilli() || info.Time.Created > finished.UnixMilli() {
			return fail()
		}
		seen[info.ID] = true
		if hasCompaction {
			if parent != "" || info.Role != "user" || info.Model.ProviderID != "lunaroute" || info.Model.ModelID != model || len(message.Parts) != 1 {
				return fail()
			}
			part := message.Parts[0]
			if part.Type != "compaction" || part.Auto == nil || !*part.Auto || !openCodeID(part.ID, "prt_") || part.MessageID != info.ID || part.SessionID != session {
				return fail()
			}
			parent = info.ID
			proof.PartID = part.ID
			proof.StartedMS = info.Time.Created
			continue
		}
		if parent == "" || proof.MessageID != "" || info.Role != "assistant" || info.ParentID != parent || info.Agent != "compaction" || info.Mode != "compaction" || info.ProviderID != "lunaroute" || info.ModelID != model || info.Finish != "stop" || len(info.Error) > 0 && string(info.Error) != "null" || info.Time.Created < proof.StartedMS || info.Time.Completed <= info.Time.Created || info.Time.Completed > finished.UnixMilli() {
			return fail()
		}
		var texts []string
		partIDs := map[string]bool{}
		for _, part := range message.Parts {
			if !openCodeID(part.ID, "prt_") || partIDs[part.ID] || part.MessageID != info.ID || part.SessionID != session {
				return fail()
			}
			partIDs[part.ID] = true
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				texts = append(texts, strings.TrimSpace(part.Text))
			}
			if part.Type == "tool" {
				return fail()
			}
		}
		proof.MessageID = info.ID
		proof.StartedMS = info.Time.Created
		proof.FinishedMS = info.Time.Completed
		proof.text = strings.Join(texts, "\n\n")
	}
	if proof.MessageID == "" || !safeCaptureText(proof.text, key) {
		return fail()
	}
	return proof, nil
}

func correlateOpenCodeCompaction(c *inferenceCapture, raw []byte, session, model, version string, started time.Time) error {
	if c == nil || version != "1.19.5" {
		return errAgentEvidence
	}
	records := c.Records()
	if validateCapturedInference(records, model, []string{"main", "subagent", "title"}) != nil {
		return errAgentEvidence
	}
	c.mu.Lock()
	key := c.key
	summaries := make(map[string]string, len(c.openCodeCompactions))
	for id, text := range c.openCodeCompactions {
		summaries[id] = text
	}
	c.mu.Unlock()
	proof, err := parseOpenCodeCompaction(raw, session, model, key, started, time.Now().UTC())
	if err != nil || len(summaries) != 1 {
		return errAgentEvidence
	}
	for _, record := range records {
		text, exists := summaries[record.RequestID]
		if !exists {
			continue
		}
		if text != proof.text || record.Backend != "opencode" || record.Path != "/v1/chat/completions" || record.SessionID != session || record.ParentSessionID != "" || record.Operation != nil || record.StartedAt.Before(time.UnixMilli(proof.StartedMS)) || !record.FinishedAt.Before(time.UnixMilli(proof.FinishedMS).Add(time.Millisecond)) {
			return errAgentEvidence
		}
		return c.CorrelateOperation(capturedOperation{RunID: c.runID, Backend: "opencode", ID: proof.MessageID, Session: session, Category: "compaction", Trigger: "auto-compaction", Source: openCodeCompactionSource, Version: version, StartedAt: time.UnixMilli(proof.StartedMS), FinishedAt: time.UnixMilli(proof.FinishedMS).Add(time.Millisecond), RequestIDs: []string{record.RequestID}})
	}
	return errAgentEvidence
}
