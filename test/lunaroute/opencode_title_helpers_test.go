package lunaroute_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	openCodeTitleSource = "docs/evidence/lunaroute-provider/sources/lr06-title-lifecycle.md"
	openCodeTitlePrefix = "Generate a title for this conversation:\n"
	openCodeTitleSystem = "You are a title generator. You output ONLY a thread title. Nothing else."
	openCodeTitleLimit  = 100 // JavaScript UTF-16 units in ensureTitle
	openCodeTitleSuffix = "..."
)

var openCodeThinkingBlock = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// Mirrors ensureTitle in v1.19.5. The text is private comparison material only;
// neither generated titles nor the conversation are observation fields.
func normalizeOpenCodeTitle(text string) string {
	text = openCodeThinkingBlock.ReplaceAllString(text, "")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		units := utf16.Encode([]rune(line))
		if len(units) > openCodeTitleLimit {
			return string(utf16.Decode(units[:openCodeTitleLimit-len(openCodeTitleSuffix)])) + openCodeTitleSuffix
		}
		return line
	}
	return ""
}

func openCodeWireText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) != 1 || parts[0].Type != "text" {
		return "", false
	}
	return parts[0].Text, true
}

func isOpenCodeTitleRequest(raw []byte) bool {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Messages) != 3 {
		return false
	}
	system, systemOK := openCodeWireText(body.Messages[0].Content)
	prefix, firstOK := openCodeWireText(body.Messages[1].Content)
	prompt, secondOK := openCodeWireText(body.Messages[2].Content)
	if !systemOK || !firstOK || !secondOK || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" || body.Messages[2].Role != "user" {
		return false
	}
	return strings.HasPrefix(system, openCodeTitleSystem+"\n") && prefix == openCodeTitlePrefix && prompt == agentMissionPromptFor("opencode")
}

// Called only after the shared parser validates the full selected-model terminal
// response. Read text solely to join ensureTitle's setTitle result to export.
func openCodeTitleResponse(raw []byte, stream bool) (string, error) {
	text, err := openCodeTextResponse(raw, stream)
	if err != nil {
		return "", err
	}
	title := normalizeOpenCodeTitle(text)
	if title == "" {
		return "", errAgentEvidence
	}
	return title, nil
}

func openCodeTextResponse(raw []byte, stream bool) (string, error) {
	var text strings.Builder
	finished := false
	consume := func(raw []byte) bool {
		var chunk struct {
			Choices []struct {
				Index   int `json:"index"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(raw, &chunk) != nil {
			return false
		}
		if len(chunk.Choices) == 0 {
			return stream && finished
		}
		if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 || finished {
			return false
		}
		choice := chunk.Choices[0]
		content := choice.Message.Content
		if stream {
			content = choice.Delta.Content
		}
		text.WriteString(content)
		if choice.Finish != nil {
			if *choice.Finish != "stop" {
				return false
			}
			finished = true
		}
		return true
	}
	if !stream {
		if !consume(raw) {
			return "", errAgentEvidence
		}
	} else {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), captureBodyLimit)
		var data []string
		flush := func() bool {
			if len(data) == 0 {
				return true
			}
			event := strings.Join(data, "\n")
			data = nil
			return event == "[DONE]" && finished || consume([]byte(event))
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if !flush() {
					return "", errAgentEvidence
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if scanner.Err() != nil || !flush() {
			return "", errAgentEvidence
		}
	}
	if !finished || strings.TrimSpace(text.String()) == "" {
		return "", errAgentEvidence
	}
	return strings.TrimSpace(text.String()), nil
}

func correlateOpenCodeTitle(c *inferenceCapture, raw []byte, session, model, version string, started time.Time) error {
	exported, err := decodeOpenCodeSession(raw)
	if err != nil || c == nil || version != "1.19.5" || exported.Info.ID != session || exported.Info.ParentID != "" || started.IsZero() {
		return errAgentEvidence
	}
	records := c.Records()
	if validateCapturedInference(records, model, []string{"main", "subagent"}) != nil {
		return errAgentEvidence
	}
	c.mu.Lock()
	key := c.key
	titles := make(map[string]string, len(c.openCodeTitles))
	for id, title := range c.openCodeTitles {
		titles[id] = title
	}
	c.mu.Unlock()
	if len(titles) != 1 {
		return errAgentEvidence
	}
	for _, record := range records {
		title, exists := titles[record.RequestID]
		if !exists {
			continue
		}
		if title != exported.Info.Title || !safeCaptureText(title, key) || record.Backend != "opencode" || record.Path != "/v1/chat/completions" || record.SessionID != session || record.ParentSessionID != "" || record.StartedAt.Before(started) || record.Operation != nil {
			return errAgentEvidence
		}
		return c.CorrelateOperation(capturedOperation{RunID: c.runID, Backend: "opencode", ID: "title-" + session, Session: session, Category: "title", Trigger: "first-user-title", Source: openCodeTitleSource, Version: version, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt, RequestIDs: []string{record.RequestID}})
	}
	return errAgentEvidence
}
