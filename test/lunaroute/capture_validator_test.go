package lunaroute_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCaptureResponseSchemas(t *testing.T) {
	cases := []struct {
		path, body, models string
		stream             bool
	}{
		{"/v1/chat/completions", `{"model":"glm-5.3","choices":[{"finish_reason":"tool_calls"}]}`, "glm-5.3", false},
		{"/v1/messages", `{"type":"message","model":"claude-sonnet-5-5","stop_reason":"tool_use"}`, "claude-sonnet-5-5", false},
		{"/v1/responses", `{"model":"glm-5.3","status":"completed"}`, "glm-5.3", false},
		{"/v1/responses", `{"model":"glm-5.3","status":"completed","error":null}`, "glm-5.3", false},
		{"/v1/chat/completions", "data: {\"model\":\"glm-5.3\",\"choices\":[{\"finish_reason\":null}]}\n\ndata: {\"model\":\"glm-5.3\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "glm-5.3", true},
		{"/v1/messages", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-5-5\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "claude-sonnet-5-5", true},
		{"/v1/responses", "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"glm-5.3\",\"status\":\"in_progress\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"glm-5.3\",\"status\":\"completed\"}}\n\n", "glm-5.3", true},
	}
	for _, tc := range cases {
		t.Run(tc.path+tc.models, func(t *testing.T) {
			models, err := extractCapturedResponse(tc.path, []byte(tc.body), tc.stream)
			if err != nil || strings.Join(models, ",") != tc.models {
				t.Fatal("valid terminal response not extracted")
			}
		})
	}
}

func TestCaptureRejectsMalformedOrIncompleteResponses(t *testing.T) {
	cases := []struct {
		path, body string
		stream     bool
	}{
		{"/v1/chat/completions", `{"choices":[{"finish_reason":"stop"}]}`, false},
		{"/v1/chat/completions", `{"model":"glm-5.3","model":"gpt-6.1","choices":[{"finish_reason":"stop"}]}`, false},
		{"/v1/messages", `{"type":"message","model":"glm-5.3","stop_reason":null}`, false},
		{"/v1/responses", `{"model":"glm-5.3","status":"incomplete"}`, false},
		{"/v1/responses", `{"error":{"message":"lr_secret"}}`, false},
		{"/v1/foreign", `{}`, false},
		{"/v1/chat/completions", "data: [DONE]\n\n", true},
		{"/v1/chat/completions", "data: {\"model\":\"glm-5.3\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n", true},
		{"/v1/chat/completions", "data: {broken lr_secret}\n\n", true},
		{"/v1/chat/completions", "data: " + strings.Repeat("x", captureBodyLimit+1) + "\n\n", true},
		{"/v1/messages", "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"lr_secret\"}}\n\n", true},
		{"/v1/messages", "data: {\"type\":\"message_stop\"}\n\n", true},
		{"/v1/responses", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"/v1/responses", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"glm-5.3\"}}", true},
	}
	for i, tc := range cases {
		models, err := extractCapturedResponse(tc.path, []byte(tc.body), tc.stream)
		if err == nil || len(models) != 0 || strings.Contains(err.Error(), "lr_secret") {
			t.Fatalf("unsafe or incomplete response accepted: case %d", i)
		}
	}
}

func TestCaptureRejectsFalseTerminalEvidence(t *testing.T) {
	cases := []struct {
		path, body string
		stream     bool
	}{
		{"/v1/messages", `{"type":"message","model":"glm-5.3","stop_reason":"tool_calls"}`, false},
		{"/v1/chat/completions", `{"model":"glm-5.3","choices":[{"finish_reason":"end_turn"}]}`, false},
		{"/v1/chat/completions", "data: {\"model\":\"glm-5.3\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: {\"type\":\"error\",\"message\":\"private-error\"}\n\ndata: [DONE]\n\n", true},
		{"/v1/chat/completions", "data: {\"model\":\"glm-5.3\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: {\"model\":null,\"choices\":\"broken\"}\n\ndata: [DONE]\n\n", true},
	}
	for i, tc := range cases {
		if _, err := extractCapturedResponse(tc.path, []byte(tc.body), tc.stream); err == nil {
			t.Errorf("false terminal evidence accepted: %d", i)
		}
	}
	r := validCaptureRecord()
	r.Category = "compaction"
	r.Operation = &capturedOperation{RunID: r.RunID, Backend: r.Backend, ID: "operation", Session: "session", Category: r.Category, Trigger: "compact", Source: "lifecycle", Version: "fixture", StartedAt: r.StartedAt.Add(-time.Second), FinishedAt: r.FinishedAt.Add(time.Second), RequestIDs: []string{r.RequestID, "unobserved-request"}}
	if validateCapturedInference([]capturedInference{r}, "glm-5.3", []string{"compaction"}) == nil {
		t.Error("unobserved operation request accepted")
	}
}

func validCaptureRecord() capturedInference {
	now := time.Now().UTC()
	return capturedInference{RunID: "run-one", Backend: "codex", Connection: "connection-one", Category: "main", RequestID: "request-one", Method: "POST", Host: "gw.lunaroute.com", Path: "/v1/responses", RequestedModel: "glm-5.3", ReportedModels: []string{"glm-5.3"}, AuthMatchesSelected: true, AuthHeaderNames: []string{"Authorization"}, Completed: true, StartedAt: now, FinishedAt: now.Add(time.Second)}
}
func TestCaptureValidatorModelsAndRoutes(t *testing.T) {
	good := validCaptureRecord()
	if err := validateCapturedInference([]capturedInference{good}, "glm-5.3", []string{"main"}); err != nil {
		t.Fatal(err)
	}
	alias := good
	alias.RequestedModel = "claude-sonnet-5-lr1"
	alias.ReportedModels = []string{"claude-sonnet-5-6"}
	if err := validateCapturedInference([]capturedInference{alias}, alias.RequestedModel, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*capturedInference){
		func(r *capturedInference) { r.Host = "foreign.example" }, func(r *capturedInference) { r.Path = "/v1/models" },
		func(r *capturedInference) { r.Method = "GET" }, func(r *capturedInference) { r.AuthMatchesSelected = false },
		func(r *capturedInference) { r.AuthHeaderNames = append(r.AuthHeaderNames, "x-api-key") },
		func(r *capturedInference) { r.ReportedModels = nil }, func(r *capturedInference) { r.ReportedModels = []string{"glm-5.4"} },
		func(r *capturedInference) { r.RequestedModel = "glm-5.4" }, func(r *capturedInference) { r.Completed = false },
		func(r *capturedInference) { r.RunID = "" }, func(r *capturedInference) { r.Connection = "" },
		func(r *capturedInference) { r.Failure = "transport" }, func(r *capturedInference) { r.FinishedAt = r.StartedAt.Add(-time.Second) },
	}
	for i, mutate := range mutations {
		r := validCaptureRecord()
		mutate(&r)
		if validateCapturedInference([]capturedInference{r}, "glm-5.3", []string{"main"}) == nil {
			t.Fatalf("invalid capture accepted: %d", i)
		}
	}
	alias.ReportedModels = []string{"glm-5.3"}
	if validateCapturedInference([]capturedInference{alias}, alias.RequestedModel, []string{"main"}) == nil {
		t.Fatal("cross-vendor alias accepted")
	}
}
func TestCaptureRequiresCorrelatedHelpers(t *testing.T) {
	main := validCaptureRecord()
	helper := validCaptureRecord()
	helper.RequestID = "request-two"
	helper.Category = "compaction"
	if validateCapturedInference(nil, "glm-5.3", []string{"main"}) == nil {
		t.Fatal("empty capture accepted")
	}
	if validateCapturedInference([]capturedInference{main}, "glm-5.3", []string{"main", "compaction"}) == nil {
		t.Fatal("missing helper accepted")
	}
	if validateCapturedInference([]capturedInference{main, helper}, "glm-5.3", []string{"main", "compaction"}) == nil {
		t.Fatal("self-labelled helper accepted")
	}
	helper.Operation = &capturedOperation{RunID: main.RunID, Backend: main.Backend, ID: "operation-one", Session: "session-one", Category: "compaction", Trigger: "forced-compaction", Source: "backend lifecycle", Version: "fixture-1", StartedAt: helper.StartedAt.Add(-time.Second), FinishedAt: helper.FinishedAt.Add(time.Second), RequestIDs: []string{helper.RequestID}}
	if err := validateCapturedInference([]capturedInference{main, helper}, "glm-5.3", []string{"main", "compaction"}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*capturedInference){func(r *capturedInference) { r.RunID = "stale-run" }, func(r *capturedInference) { r.RequestID = main.RequestID }, func(r *capturedInference) { r.Operation = nil }, func(r *capturedInference) { op := *r.Operation; op.RequestIDs = nil; r.Operation = &op }, func(r *capturedInference) { op := *r.Operation; op.Session = ""; r.Operation = &op }} {
		r := helper
		mutate(&r)
		if validateCapturedInference([]capturedInference{main, r}, "glm-5.3", []string{"main", "compaction"}) == nil {
			t.Fatal("uncorrelated helper accepted")
		}
	}
	raw, err := json.Marshal([]capturedInference{main, helper})
	if err != nil {
		t.Fatal(err)
	}
	if validateObservationJSON(raw, "lr_private_fixture") != nil {
		t.Fatal("sanitized records not accepted by observation sink")
	}
}
