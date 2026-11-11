package lunaroute_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A private protocol fixture, not the installed Claude binary or LR-05 evidence.
func TestAgentEvidenceCapturedProcessToObservation(t *testing.T) {
	c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string
			Messages []map[string]string
		}
		if r.Method != "POST" || r.Host != captureHost || r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "glm-5.3" || len(body.Messages) != 1 || body.Messages[0]["content"] != "fixture-prompt-nonce" {
			t.Error("missing credential-bearing launch input")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","model":"glm-5.3","stop_reason":"end_turn"}`)
	}, captureLimits{})
	c.backend = "claude-code" // private fixture setup, before any requests exist
	root, ws := t.TempDir(), t.TempDir()
	require.NoError(t, requireEmptyAgentWorkspace(ws))
	ref := evidencePath + "/observations/emitted-config.json"
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, ref)), 0700))
	bin, err := os.Executable()
	require.NoError(t, err)
	binding := liveBinding(captureFixtureKey, "glm-5.3", "claude-code")
	key, err := binding.RevealKey(context.Background())
	require.NoError(t, err)
	spec := claudecli.RunSpec{Bin: c.WrapBinary(t, bin), Args: []string{"-test.run=^TestAgentEvidenceProcessFixture$"}, Stdin: "fixture-prompt-nonce", Dir: ws, MaxResponseBytes: 8192, Env: []string{"PATH=/usr/bin:/bin", "ARBITRON_AGENT_PROCESS_FIXTURE=1", "LUNAROUTE_API_KEY=" + key, "FIXTURE_MODEL=" + binding.Model, "FIXTURE_CONFIG=" + filepath.Join(root, ref)}}
	started := time.Now().UTC()
	lines, err := runCaptured(context.Background(), claudecli.NewExecRunner(), spec)
	require.NoError(t, err)
	var result *engine.AgentResult
	for _, line := range lines {
		var candidate engine.AgentResult
		if json.Unmarshal([]byte(line), &candidate) == nil && candidate.SessionID != "" {
			result = &candidate
		}
	}
	require.NotNil(t, result, "the subprocess must produce its own terminal proof")
	records := c.Records()
	require.Len(t, records, 2)
	// A synthetic lifecycle exercises the correlation API only. Real backend
	// categories require the installed binary's own forced lifecycle parser.
	require.NoError(t, c.CorrelateOperation(capturedOperation{RunID: "run-one", Backend: binding.Backend, ID: "fixture-operation", Session: result.SessionID, Category: "subagent", Trigger: "fixture-second-turn", Source: "fixture lifecycle", Version: "fixture-1", StartedAt: records[1].StartedAt, FinishedAt: records[1].FinishedAt, RequestIDs: []string{records[1].RequestID}}))
	in := agentEvidenceInput{RunID: "run-one", Backend: binding.Backend, Model: binding.Model, Version: "fixture-1", ConfigRef: ref, StartedAt: started, FinishedAt: time.Now().UTC(), RequiredCategories: []string{"main", "subagent"}, Records: c.Records(), Result: result}
	require.NoError(t, publishAgentEvidence(root, ws, key, "captured-agent.json", in))
	raw, err := os.ReadFile(filepath.Join(root, evidencePath, "observations/captured-agent.json"))
	require.NoError(t, err)
	var sink agentTaskObservation
	require.NoError(t, json.Unmarshal(raw, &sink))
	require.Len(t, sink.Inference, 2)
	assert.Equal(t, "glm-5.3", sink.Inference[1].RequestedModel)
	assert.Equal(t, []string{"glm-5.3"}, sink.Inference[1].ReportedModels)
	assert.True(t, sink.FileOK && sink.CommandOK && sink.Completed)
	assert.Contains(t, sink.StepKinds, "tool_call")
	for _, s := range []string{captureFixtureKey, "fixture-prompt-nonce", "private-summary"} {
		assert.NotContains(t, string(raw), s)
	}
	// Both the captured feed and actual workspace sink are load-bearing.
	in.Records = nil
	require.Error(t, publishAgentEvidence(root, ws, key, "empty.json", in))
	require.NoError(t, os.Remove(filepath.Join(ws, "lunaroute-command.txt")))
	in.Records = c.Records()
	require.Error(t, publishAgentEvidence(root, ws, key, "missing-file.json", in))
}

func TestAgentEvidenceProcessFixture(t *testing.T) {
	if os.Getenv("ARBITRON_AGENT_PROCESS_FIXTURE") != "1" {
		return
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil || string(input) != "fixture-prompt-nonce" || os.Getenv("FIXTURE_MODEL") != "glm-5.3" {
		t.Fatal("fixture input missing")
	}
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		t.Fatal("fixture trust missing")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var reported []engine.ModelEvidence
	for range 2 {
		req, err := http.NewRequest("POST", hostedRoot+"/v1/messages", strings.NewReader(`{"model":"glm-5.3","messages":[{"role":"user","content":"fixture-prompt-nonce"}]}`))
		if err != nil {
			t.Fatal("fixture request invalid")
		}
		req.Header.Set("Authorization", "Bearer "+os.Getenv("LUNAROUTE_API_KEY"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("fixture request failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != 200 {
			t.Fatal("fixture response failed")
		}
		models, err := extractCapturedResponse("/v1/messages", body, false)
		if err != nil {
			t.Fatal("fixture response incomplete")
		}
		for _, model := range models {
			reported = append(reported, engine.ModelEvidence{Model: model, Source: "fixture_response"})
		}
	}
	cmd := exec.Command("/bin/sh", "-c", "printf LUNAROUTE-OK > lunaroute-proof.txt; wc -c < lunaroute-proof.txt > lunaroute-command.txt")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if cmd.Run() != nil {
		t.Fatal("fixture tool command failed")
	}
	config := agentConfigReference{RunID: "run-one", Backend: "claude-code", Model: os.Getenv("FIXTURE_MODEL"), SessionID: "fixture-session", Source: "fixture emitted env"}
	raw, err := json.Marshal(config)
	if err != nil || os.WriteFile(os.Getenv("FIXTURE_CONFIG"), raw, 0600) != nil {
		t.Fatal("fixture projection failed")
	}
	result := engine.AgentResult{Model: config.Model, SessionID: config.SessionID, Summary: "private-summary", Steps: []engine.AgentStep{{Kind: "tool_call"}}, ModelEvidence: reported}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		t.Fatal("fixture terminal failed")
	}
}
