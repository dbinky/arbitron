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

// Only the main record comes from the authenticated stream. The synthetic
// helper and command-created workspace proofs exercise the existing publisher,
// not installed Codex helper behavior or contract verification.
func TestCaptureResponsesLifecycleToObservation(t *testing.T) {
	event := func(typ, fields string) string {
		return `event: ` + typ + "\ndata: {\"type\":\"" + typ + `",` + fields + "}\n\n"
	}
	start := event("response.created", `"response":{"model":"glm-5.3","status":"in_progress"}`)
	progress := event("response.in_progress", `"response":{"model":"glm-5.3","status":"in_progress"}`)
	content := event("response.output_text.delta", `"delta":"private-response"`)
	tool := event("response.function_call_arguments.delta", `"delta":"private-tool-argument"`)
	complete := event("response.completed", `"response":{"model":"glm-5.3","status":"completed","error":null}`)
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"ordered", start + progress + content + complete, true},
		{"minimal", start + complete, true},
		{"tool-terminal", start + tool + complete, true},
		{"sse-comments", ": keepalive\n\n" + start + content + complete + ": drained\n\n", true},
		{"missing-start", complete, false},
		{"content-before-start", content + start + complete, false},
		{"progress-before-start", progress + start + complete, false},
		{"duplicate-start", start + start + complete, false},
		{"start-after-progress", start + progress + start + complete, false},
		{"missing-start-envelope", event("response.created", `"response":null`) + complete, false},
		{"missing-start-model", event("response.created", `"response":{"status":"in_progress"}`) + complete, false},
		{"invalid-start-status", event("response.created", `"response":{"model":"glm-5.3","status":"completed"}`) + complete, false},
		{"missing-progress-envelope", start + event("response.in_progress", `"response":null`) + complete, false},
		{"invalid-progress-status", start + event("response.in_progress", `"response":{"model":"glm-5.3","status":"failed"}`) + complete, false},
		{"missing-progress-model", start + event("response.in_progress", `"response":{"status":"in_progress"}`) + complete, false},
		{"foreign-event", start + event("message_delta", `"delta":{}`) + complete, false},
		{"empty-type", start + `data: {"delta":"private-response"}` + "\n\n" + complete, false},
		{"content-after-terminal", start + complete + content, false},
		{"duplicate-terminal", start + complete + complete, false},
		{"missing-terminal", start + content, false},
		{"failed", start + event("response.failed", `"response":{"model":"glm-5.3","status":"failed"}`) + complete, false},
		{"incomplete", start + event("response.incomplete", `"response":{"model":"glm-5.3","status":"incomplete"}`) + complete, false},
		{"malformed", start + "data: {broken private-response}\n\n" + complete, false},
		{"truncated-terminal", start + strings.TrimSuffix(complete, "\n\n"), false},
		{"model-substitution", start + strings.ReplaceAll(complete, "glm-5.3", "glm-5.3-flash"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, ws, in := backendEvidenceFixture(t, "codex", false)
			var calls atomic.Int32
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Model, Input    string
					Stream          bool
					MaxOutputTokens int `json:"max_output_tokens"`
				}
				if r.Method != "POST" || r.Host != captureHost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != in.Model || !body.Stream || body.MaxOutputTokens != discoveryChatTokens || body.Input != "private-nonce" {
					t.Error("missing selected credential-bearing request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, tc.body)
			}, captureLimits{})
			requestBody, err := json.Marshal(map[string]any{"model": in.Model, "stream": true, "max_output_tokens": discoveryChatTokens, "input": "private-nonce"})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest("POST", hostedRoot+"/v1/responses", strings.NewReader(string(requestBody)))
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
			in.Records = append(c.Records(), in.Records[1])
			in.FinishedAt = time.Now().UTC()
			err = publishBackendEvidence(root, ws, captureFixtureKey, "responses-stream.json", in)
			path := filepath.Join(root, evidencePath, "observations/responses-stream.json")
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid Responses lifecycle published as completed evidence")
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
			var sink backendTaskObservation
			if json.Unmarshal(raw, &sink) != nil || len(sink.Inference) != 2 || sink.Inference[0].Path != "/v1/responses" || sink.Inference[0].RequestedModel != in.Model || !slices.Equal(sink.Inference[0].ReportedModels, []string{in.Model}) || !sink.Inference[0].AuthMatchesSelected || !sink.Inference[0].Completed || !sink.Completed || !sink.FileOK || !sink.CommandOK || !sink.HomesUnchanged || !sink.PrivateCleaned {
				t.Fatal("persisted observation lost actual completion/model proof")
			}
			for _, secret := range []string{captureFixtureKey, "private-response", "private-nonce", "private-tool-argument"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("raw gateway content persisted")
				}
			}
		})
	}
}
