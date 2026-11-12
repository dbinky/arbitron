package lunaroute_test

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

const claudeSubagentSource = "docs/evidence/lunaroute-provider/sources/lr05-subagent-lifecycle.md"

// This is a harness-reported lifecycle, not gateway response-model evidence.
// Request attribution is a separate, still-required collector.
type claudeSubagentProof struct {
	Source               string `json:"source"`
	Version              string `json:"version"`
	Session              string `json:"session"`
	TaskID               string `json:"task_id"`
	RunID                string `json:"run_id"`
	ToolUseID            string `json:"tool_use_id"`
	HarnessReportedModel string `json:"harness_reported_model"`
}

type claudeSubagentEvent struct {
	Type         string `json:"type"`
	Subtype      string `json:"subtype"`
	Session      string `json:"session_id"`
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	ToolUseID    string `json:"tool_use_id"`
	TaskType     string `json:"task_type"`
	SubagentType string `json:"subagent_type"`
	Background   *bool  `json:"is_backgrounded"`
	Depth        int    `json:"spawn_depth"`
	ParentTask   string `json:"parent_task_id"`
	ParentTool   string `json:"parent_tool_use_id"`
	Historical   bool   `json:"historical"`
	Replay       bool   `json:"isReplay"`
	Status       string `json:"status"`
	Patch        struct {
		Status     string `json:"status"`
		Background *bool  `json:"is_backgrounded"`
	} `json:"patch"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Output *struct {
		Status    string   `json:"status"`
		AgentID   string   `json:"agentId"`
		AgentType string   `json:"agentType"`
		Model     string   `json:"resolvedModel"`
		Models    []string `json:"modelsUsed"`
		Handback  string   `json:"handback"`
		Duration  *int     `json:"totalDurationMs"`
		Tokens    *int     `json:"totalTokens"`
		Tools     *int     `json:"totalToolUseCount"`
	} `json:"tool_use_result"`
}

func collectClaudeSubagentLifecycle(lines []string, session, model string) (claudeSubagentProof, error) {
	fail := func() (claudeSubagentProof, error) { return claudeSubagentProof{}, errAgentEvidence }
	if !safeAgentField(session, "") || len(lines) == 0 {
		return fail()
	}
	if _, err := explicitLiveModel(func(string) (string, bool) { return model, true }, "model"); err != nil {
		return fail()
	}
	proof := claudeSubagentProof{Source: claudeSubagentSource, Version: claudeStartupVersion, Session: session}
	const (
		awaitingCall = iota
		awaitingRegistration
		awaitingResult
		completed
	)
	state, total := awaitingCall, 0
	for _, line := range lines {
		total += len(line) + 1
		if total > captureBodyLimit {
			return fail()
		}
		decoder := json.NewDecoder(bytes.NewBufferString(line))
		var extra any
		if !uniqueJSONFields(decoder) || decoder.Decode(&extra) != io.EOF {
			return fail()
		}
		var event claudeSubagentEvent
		if json.Unmarshal([]byte(line), &event) != nil || event.Type == "" {
			return fail()
		}
		current := event.Session == session && event.ParentTool == "" && !event.Historical && !event.Replay
		if event.Type == "system" && event.Subtype == "task_started" {
			if state != awaitingRegistration || !current || event.TaskType != "local_agent" || event.SubagentType != "general-purpose" || event.Background == nil || *event.Background || event.Depth != 1 || event.ParentTask != "" || event.ToolUseID != proof.ToolUseID || !safeAgentField(event.TaskID, "") || !safeAgentField(event.RunID, "") {
				return fail()
			}
			proof.TaskID, proof.RunID = event.TaskID, event.RunID
			state = awaitingResult
			continue
		}
		if event.Type == "system" && (event.Subtype == "task_updated" || event.Subtype == "task_notification") {
			if state < awaitingResult || !current || event.TaskID != proof.TaskID || event.RunID != proof.RunID {
				return fail()
			}
			if event.Subtype == "task_updated" {
				if (event.Patch.Background != nil && *event.Patch.Background) || (event.Patch.Status != "" && event.Patch.Status != "running" && event.Patch.Status != "completed") {
					return fail()
				}
			} else if event.ToolUseID != proof.ToolUseID || event.Status != "completed" {
				return fail()
			}
			continue
		}
		if event.Type != "assistant" && event.Type != "user" {
			continue
		}
		// Ordinary user text is not a tool-result block; it cannot supply a helper.
		var blocks []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
			Input     struct {
				SubagentType string `json:"subagent_type"`
				Background   *bool  `json:"run_in_background"`
				Isolation    string `json:"isolation"`
				Resume       string `json:"resume"`
			} `json:"input"`
		}
		if len(event.Message.Content) == 0 || event.Message.Content[0] != '[' {
			if event.Output != nil && event.Output.AgentID != "" {
				return fail()
			}
			continue
		}
		if json.Unmarshal(event.Message.Content, &blocks) != nil {
			return fail()
		}
		matched := false
		for _, block := range blocks {
			if block.Type == "tool_use" && block.Name == "Agent" {
				if state != awaitingCall || event.Type != "assistant" || !current || !safeAgentField(block.ID, "") || block.Input.SubagentType != "general-purpose" || block.Input.Background == nil || *block.Input.Background || block.Input.Isolation != "" || block.Input.Resume != "" {
					return fail()
				}
				proof.ToolUseID, state = block.ID, awaitingRegistration
			}
			if block.Type == "tool_result" && block.ToolUseID == proof.ToolUseID && proof.ToolUseID != "" {
				output := event.Output
				if state != awaitingResult || event.Type != "user" || !current || block.IsError || matched || output == nil || output.Status != "completed" || output.AgentID != proof.TaskID || output.AgentType != "general-purpose" || output.Model != model || (output.Handback != "" && output.Handback != "send") || output.Duration == nil || *output.Duration < 0 || output.Tokens == nil || *output.Tokens <= 0 || output.Tools == nil || *output.Tools <= 0 {
					return fail()
				}
				for _, used := range output.Models {
					if used != model {
						return fail()
					}
				}
				proof.HarnessReportedModel, state, matched = output.Model, completed, true
			}
		}
		if event.Output != nil && event.Output.AgentID != "" && !matched {
			return fail()
		}
	}
	if state != completed {
		return fail()
	}
	return proof, nil
}

func collectClaudeSubagentSelection(spec claudecli.RunSpec) error {
	model, err := claudeStartupSelection(spec)
	if err != nil {
		return errAgentEvidence
	}
	selector := 0
	for _, entry := range spec.Env {
		if entry == "CLAUDE_CODE_SUBAGENT_MODEL="+model {
			selector++
		}
	}
	if selector != 1 {
		return errAgentEvidence
	}
	return nil
}
