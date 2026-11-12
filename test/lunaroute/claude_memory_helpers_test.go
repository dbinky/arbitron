package lunaroute_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const claudeMemorySource = "docs/evidence/lunaroute-provider/sources/lr05-memory.md"

type claudeProjectMemoryProof struct {
	Source     string   `json:"source"`
	Loaded     bool     `json:"loaded"`
	RequestIDs []string `json:"request_ids"`
}

func newClaudeMemoryText() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", errAgentEvidence
	}
	return "LunaRoute private project memory probe " + hex.EncodeToString(nonce[:]) + ".\n", nil
}

// The reviewed formatter is shared by inline and announced instruction files.
// Match only user text, never assistant echoes, system prompts or tool results.
// No request text, file path or nonce is retained in the public projection.
func claudeMemoryInRequest(raw []byte, expected string) bool {
	if expected == "" || len(raw) > captureBodyLimit || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) {
		return false
	}
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	matches := 0
	invalid := false
	count := func(text string) {
		n := strings.Count(text, expected)
		matches += n
		if n != 1 {
			return
		}
		prefix, tail, _ := strings.Cut(text, expected)
		if prefix != "" && !strings.HasSuffix(prefix, "\n\n") && !strings.HasSuffix(prefix, "<system-reminder>\n") {
			invalid = true
		}
		// The source formatter separates files with a blank line. The inline
		// renderer may close a system reminder after the last file.
		if strings.TrimSpace(tail) != "" && strings.TrimSpace(tail) != "</system-reminder>" && !strings.HasPrefix(tail, "\n\nContents of ") {
			invalid = true
		}
	}
	for _, message := range body.Messages {
		if message.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			count(text)
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			return false
		}
		for _, block := range blocks {
			if block.Type == "text" {
				count(block.Text)
			}
		}
	}
	return matches == 1 && !invalid
}

func (c *inferenceCapture) expectClaudeMemory(s *claudeIsolation) error {
	if c == nil || s == nil || s.memory == "" || s.unchanged() != nil {
		return errAgentEvidence
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.backend != "claude-code" || c.closed || c.failed || c.requestCount != 0 || c.claudeMemory != "" {
		return errAgentEvidence
	}
	c.claudeMemory = s.memory
	return nil
}
