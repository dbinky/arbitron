package lunaroute_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The helper lifecycle is a protocol fixture, not an installed-helper proof.
// The main record must come from actual authenticated CONNECT/TLS traffic and
// reach the existing agent publisher with command-created workspace proofs.
func TestCaptureMessagesBlocksToObservation(t *testing.T) {
	event := func(typ, fields string) string {
		return `data: {"type":"` + typ + `"` + fields + "}\n\n"
	}
	block := func(typ, index, fields string) string {
		return event("content_block_"+typ, `,"index":`+index+fields)
	}
	startBlock := func(index string) string {
		return block("start", index, `,"content_block":{"type":"text","text":""}`)
	}
	deltaBlock := func(index string) string {
		return block("delta", index, `,"delta":{"type":"text_delta","text":"private-response"}`)
	}
	stopBlock := func(index string) string { return block("stop", index, "") }
	toolBlock := block("start", "0", `,"content_block":{"type":"tool_use","id":"tool-one","name":"Read","input":{}}`)
	thinkingBlock := block("start", "0", `,"content_block":{"type":"thinking","thinking":""}`)
	delta := func(typ, fields string) string {
		return block("delta", "0", `,"delta":{"type":"`+typ+`"`+fields+`}`)
	}
	start := event("message_start", `,"message":{"model":"glm-5.3"}`)
	finish := event("message_delta", `,"delta":{"stop_reason":"end_turn"}`)
	stop := event("message_stop", "")
	text := startBlock("0") + deltaBlock("0") + stopBlock("0")
	for _, tc := range []struct {
		name, blocks string
		valid        bool
	}{
		{"text", text, true},
		{"empty-message", "", true},
		{"missing-capture-feed", text, false},
		{"sequential", text + startBlock("1") + deltaBlock("1") + stopBlock("1"), true},
		{"interleaved", startBlock("0") + startBlock("1") + deltaBlock("1") + stopBlock("1") + deltaBlock("0") + stopBlock("0"), true},
		{"tool-and-thinking", block("start", "0", `,"content_block":{"type":"tool_use","id":"tool-one","name":"Read","input":{}}`) +
			block("delta", "0", `,"delta":{"type":"input_json_delta","partial_json":"{}"}`) + stopBlock("0") +
			block("start", "1", `,"content_block":{"type":"thinking","thinking":""}`) +
			block("delta", "1", `,"delta":{"type":"thinking_delta","thinking":"private-thought"}`) +
			block("delta", "1", `,"delta":{"type":"signature_delta","signature":"private-signature"}`) + stopBlock("1"), true},
		{"empty-text-delta", startBlock("0") + delta("text_delta", `,"text":""`) + stopBlock("0"), true},
		{"fragmented-tool-json", toolBlock + delta("input_json_delta", `,"partial_json":"{\"path\":"`) + delta("input_json_delta", `,"partial_json":"\"private-path\"}"`) + stopBlock("0"), true},
		{"server-tool-json", strings.Replace(toolBlock, `"tool_use"`, `"server_tool_use"`, 1) + delta("input_json_delta", `,"partial_json":"{}"`) + stopBlock("0"), true},
		{"interleaved-block-kinds", toolBlock + startBlock("1") + delta("input_json_delta", `,"partial_json":""`) + deltaBlock("1") + stopBlock("0") + stopBlock("1"), true},
		{"delta-for-wrong-block-kind", toolBlock + startBlock("1") + deltaBlock("0") + stopBlock("0") + stopBlock("1"), false},
		{"text-delta-on-tool", toolBlock + deltaBlock("0") + stopBlock("0"), false},
		{"text-delta-on-thinking", thinkingBlock + deltaBlock("0") + stopBlock("0"), false},
		{"tool-delta-on-text", startBlock("0") + delta("input_json_delta", `,"partial_json":"{}"`) + stopBlock("0"), false},
		{"tool-delta-on-thinking", thinkingBlock + delta("input_json_delta", `,"partial_json":"{}"`) + stopBlock("0"), false},
		{"thinking-delta-on-text", startBlock("0") + delta("thinking_delta", `,"thinking":"private-thought"`) + stopBlock("0"), false},
		{"thinking-delta-on-tool", toolBlock + delta("thinking_delta", `,"thinking":"private-thought"`) + stopBlock("0"), false},
		{"signature-delta-on-text", startBlock("0") + delta("signature_delta", `,"signature":"private-signature"`) + stopBlock("0"), false},
		{"signature-delta-on-tool", toolBlock + delta("signature_delta", `,"signature":"private-signature"`) + stopBlock("0"), false},
		{"missing-text", startBlock("0") + delta("text_delta", "") + stopBlock("0"), false},
		{"null-text", startBlock("0") + delta("text_delta", `,"text":null`) + stopBlock("0"), false},
		{"nonstring-text", startBlock("0") + delta("text_delta", `,"text":{}`) + stopBlock("0"), false},
		{"missing-partial-json", toolBlock + delta("input_json_delta", "") + stopBlock("0"), false},
		{"null-partial-json", toolBlock + delta("input_json_delta", `,"partial_json":null`) + stopBlock("0"), false},
		{"nonstring-partial-json", toolBlock + delta("input_json_delta", `,"partial_json":{}`) + stopBlock("0"), false},
		{"missing-thinking", thinkingBlock + delta("thinking_delta", "") + stopBlock("0"), false},
		{"null-thinking", thinkingBlock + delta("thinking_delta", `,"thinking":null`) + stopBlock("0"), false},
		{"nonstring-thinking", thinkingBlock + delta("thinking_delta", `,"thinking":[]`) + stopBlock("0"), false},
		{"missing-signature", thinkingBlock + delta("signature_delta", "") + stopBlock("0"), false},
		{"null-signature", thinkingBlock + delta("signature_delta", `,"signature":null`) + stopBlock("0"), false},
		{"nonstring-signature", thinkingBlock + delta("signature_delta", `,"signature":false`) + stopBlock("0"), false},
		{"delta-without-start", deltaBlock("0"), false},
		{"stop-without-start", stopBlock("0"), false},
		{"duplicate-start", startBlock("0") + text, false},
		{"reused-index", text + text, false},
		{"duplicate-stop", text + stopBlock("0"), false},
		{"delta-after-stop", text + deltaBlock("0"), false},
		{"wrong-stop-index", startBlock("0") + stopBlock("1"), false},
		{"unfinished", startBlock("0") + deltaBlock("0"), false},
		{"unfinished-second", text + startBlock("1"), false},
		{"missing-index", event("content_block_start", `,"content_block":{"type":"text","text":""}`) + stopBlock("0"), false},
		{"null-index", startBlock("null") + stopBlock("0"), false},
		{"negative-index", startBlock("-1") + stopBlock("-1"), false},
		{"fractional-index", startBlock("0.5") + stopBlock("0.5"), false},
		{"string-index", startBlock(`"0"`) + stopBlock(`"0"`), false},
		{"missing-start-payload", block("start", "0", "") + stopBlock("0"), false},
		{"missing-start-type", block("start", "0", `,"content_block":{"text":"private-response"}`) + stopBlock("0"), false},
		{"missing-delta-payload", startBlock("0") + block("delta", "0", "") + stopBlock("0"), false},
		{"missing-delta-type", startBlock("0") + block("delta", "0", `,"delta":{"text":"private-response"}`) + stopBlock("0"), false},
		{"null-start-payload", block("start", "0", `,"content_block":null`) + stopBlock("0"), false},
		{"nonobject-delta", startBlock("0") + block("delta", "0", `,"delta":[]`) + stopBlock("0"), false},
		{"missing-delta-index", startBlock("0") + event("content_block_delta", `,"delta":{"type":"text_delta","text":"private-response"}`) + stopBlock("0"), false},
		{"null-stop-index", startBlock("0") + block("stop", "null", ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, ws, in := agentEvidenceFixture(t)
			var calls atomic.Int32
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Model     string
					Stream    bool
					MaxTokens int `json:"max_tokens"`
					Messages  []struct{ Role, Content string }
				}
				if r.Method != "POST" || r.Host != captureHost || r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != in.Model || !body.Stream || body.MaxTokens != discoveryChatTokens || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "private-nonce" {
					t.Error("missing selected credential-bearing request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := io.WriteString(w, start+tc.blocks+finish+stop); err != nil {
					t.Error("cannot write stream fixture")
				}
			}, captureLimits{})
			c.backend = in.Backend
			requestBody, err := json.Marshal(map[string]any{"model": in.Model, "stream": true, "max_tokens": discoveryChatTokens, "messages": []map[string]string{{"role": "user", "content": "private-nonce"}}})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest("POST", hostedRoot+"/v1/messages", strings.NewReader(string(requestBody)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+captureFixtureKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := captureClient(t, c, true).Do(req)
			if err != nil {
				t.Fatal("captured request failed")
			}
			_, readErr := io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil || resp.StatusCode != http.StatusOK || calls.Load() != 1 {
				t.Fatal("fixture did not exercise a complete HTTP response")
			}
			records := c.Records()
			if tc.name == "missing-capture-feed" {
				records = nil
			}
			in.Records = append(records, in.Records[1])
			in.FinishedAt = time.Now().UTC()
			err = publishAgentEvidence(root, ws, captureFixtureKey, "blocks.json", in)
			path := filepath.Join(root, evidencePath, "observations/blocks.json")
			if !tc.valid {
				if err == nil {
					t.Fatal("malformed content-block lifecycle published as completed evidence")
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatal("rejected stream wrote an observation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var sink agentTaskObservation
			if json.Unmarshal(raw, &sink) != nil || len(sink.Inference) != 2 || sink.Inference[0].RequestedModel != in.Model || !slices.Equal(sink.Inference[0].ReportedModels, []string{in.Model}) || !sink.Inference[0].AuthMatchesSelected || !sink.Inference[0].Completed || !sink.Completed || !sink.FileOK || !sink.CommandOK {
				t.Fatal("persisted observation lost actual completion/model/workspace proof")
			}
			for _, secret := range []string{captureFixtureKey, "private-response", "private-nonce", "private-thought", "private-signature", "private-path"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("raw gateway content persisted")
				}
			}
		})
	}
}
