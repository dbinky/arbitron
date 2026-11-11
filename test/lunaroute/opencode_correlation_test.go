package lunaroute_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Requests carry the v1.19.5 request-preparation headers. Timing alone cannot
// distinguish a subagent from simultaneous title/main traffic.
func TestAgentEvidenceOpenCodeSubagentRequestCorrelation(t *testing.T) {
	for _, mode := range []string{"success", "missing session", "wrong child", "wrong parent", "duplicate session", "duplicate parent", "unsafe session", "unsafe parent", "no helper", "wrong response", "stale interval", "wrong version"} {
		t.Run(mode, func(t *testing.T) {
			root, ws, in := agentEvidenceFixture(t)
			in.Backend = "opencode"
			in.Result.SessionID = "ses_parent"
			config, err := json.Marshal(agentConfigReference{RunID: in.RunID, Backend: in.Backend, Model: in.Model, SessionID: in.Result.SessionID, Source: "fixture effective controls"})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, in.ConfigRef), config, 0600))
			var requests atomic.Int32
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != in.Model || len(body.Messages) != 1 || body.Messages[0].Content != "private correlation prompt" {
					t.Error("correlation fixture received incorrect inference inputs")
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				model := in.Model
				if mode == "wrong response" {
					model = "glm-5.3-flash"
				}
				_, _ = io.WriteString(w, `{"model":"`+model+`","choices":[{"message":{"content":"private response"},"finish_reason":"stop"}]}`)
			}, captureLimits{})
			c.backend = "opencode"
			client := captureClient(t, c, true)
			start := time.Now().UTC()
			send := func(session, parent string, duplicate bool) {
				req, err := http.NewRequest("POST", hostedRoot+"/v1/chat/completions", strings.NewReader(`{"model":"glm-5.3","messages":[{"role":"user","content":"private correlation prompt"}]}`))
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+captureFixtureKey)
				req.Header.Set("Content-Type", "application/json")
				if session != "" {
					req.Header.Set("X-Opencode-Session-Id", session)
				}
				if parent != "" {
					req.Header.Set("X-Opencode-Parent-Session-Id", parent)
				}
				if duplicate {
					req.Header.Add("X-Opencode-Session-Id", session)
				}
				if mode == "duplicate parent" && parent != "" {
					req.Header.Add("X-Opencode-Parent-Session-Id", parent)
				}
				res, err := client.Do(req)
				if err == nil {
					_, err = io.Copy(io.Discard, res.Body)
					require.NoError(t, res.Body.Close())
				}
				// Invalid projection may reject the exchange; no raw transport error escapes.
				if mode == "duplicate session" || mode == "duplicate parent" || mode == "unsafe session" || mode == "unsafe parent" {
					return
				}
				require.NoError(t, err)
				require.Equal(t, 200, res.StatusCode)
			}
			send("ses_parent", "", false)
			if mode != "no helper" {
				session, parent := "ses_child", "ses_parent"
				switch mode {
				case "missing session":
					session = ""
				case "wrong child":
					session = "ses_other"
				case "wrong parent":
					parent = "ses_other"
				case "unsafe session":
					session = "ses_" + captureFixtureKey
				case "unsafe parent":
					parent = "ses_" + captureFixtureKey
				}
				send(session, parent, mode == "duplicate session")
			}
			end := time.Now().UTC()
			wantRequests := int32(2)
			if mode == "no helper" || mode == "duplicate session" || mode == "duplicate parent" || mode == "unsafe session" || mode == "unsafe parent" {
				wantRequests = 1
			}
			require.Equal(t, wantRequests, requests.Load(), "invalid identity headers must refuse before upstream I/O")
			parent, child := openCodeSubagentFixture()
			subagentTaskState(parent)["time"] = map[string]any{"start": start.UnixMilli(), "end": end.UnixMilli() + 1}
			child["messages"].([]any)[0].(map[string]any)["info"].(map[string]any)["time"] = map[string]any{"created": start.UnixMilli()}
			subagentChildInfo(child)["time"] = map[string]any{"created": start.UnixMilli(), "completed": end.UnixMilli()}
			tasks, err := parseOpenCodeSubagentTasks(subagentJSON(t, parent), "ses_parent", in.Model)
			require.NoError(t, err)
			proof, err := validateOpenCodeSubagentChild(subagentJSON(t, child), tasks[0])
			require.NoError(t, err)
			version := "1.19.5"
			if mode == "wrong version" {
				version = "1.19.6"
			}
			if mode == "stale interval" {
				tasks[0].FinishedMS = start.UnixMilli()
			}
			err = correlateOpenCodeSubagent(c, tasks[0], proof, version)
			in.Records, in.FinishedAt, in.Version = c.Records(), time.Now().UTC(), "1.19.5"
			if err == nil {
				err = publishAgentEvidence(root, ws, captureFixtureKey, "correlated.json", in)
			}
			path := filepath.Join(root, evidencePath, "observations", "correlated.json")
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(path)
				require.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var obs agentTaskObservation
			require.NoError(t, json.Unmarshal(raw, &obs))
			require.Len(t, obs.Inference, 2)
			assert.Equal(t, "ses_child", obs.Inference[1].SessionID)
			assert.Equal(t, "ses_parent", obs.Inference[1].ParentSessionID)
			assert.Equal(t, "subagent", obs.Inference[1].Category)
			assert.Equal(t, []string{obs.Inference[1].RequestID}, obs.Inference[1].Operation.RequestIDs)
			assert.Equal(t, "1.19.5", obs.Inference[1].Operation.Version)
			assert.Equal(t, openCodeSubagentSource, obs.Inference[1].Operation.Source)
			assert.Equal(t, "ses_parent", obs.Inference[0].SessionID)
			assert.Equal(t, "main", obs.Inference[0].Category)
			assert.NotContains(t, string(raw), captureFixtureKey)
			assert.NotContains(t, string(raw), "private correlation prompt")
			assert.NotContains(t, string(raw), "private response")
		})
	}
}

func TestAgentEvidenceOpenCodeSubagentCorrelatedFactoryPublication(t *testing.T) {
	for _, mode := range []string{"correlation-success", "correlation-parent", "correlation-version"} {
		t.Run(mode, func(t *testing.T) {
			started := time.Now().UTC()
			root, bin, capture := agentFactoryFixture(t, "opencode", mode)
			client, _ := agentFactoryProbeClient(t)
			result, err := executeAgentMission(context.Background(), liveCapturedFactoryWithClient(root, bin, agentFactoryRules(t), client, capture), liveBinding(captureFixtureKey, "glm-5.3", "opencode"), root)
			// These files exist even when lifecycle/request/version provenance refuses.
			require.FileExists(t, filepath.Join(root, agentProofFile))
			ref := evidencePath + "/observations/controls.json"
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, ref)), 0700))
			config, marshalErr := json.Marshal(agentConfigReference{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", SessionID: "ses_factory", Source: "fixture effective controls"})
			require.NoError(t, marshalErr)
			require.NoError(t, os.WriteFile(filepath.Join(root, ref), config, 0600))
			input := agentEvidenceInput{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", Version: "1.19.5", ConfigRef: ref, StartedAt: started, FinishedAt: time.Now().UTC(), RequiredCategories: []string{"main", "subagent"}, Records: capture.Records(), Result: result}
			if err == nil {
				err = publishAgentEvidence(root, root, captureFixtureKey, "factory-correlated.json", input)
			}
			path := filepath.Join(root, evidencePath, "observations/factory-correlated.json")
			if mode != "correlation-success" {
				require.Error(t, err)
				_, statErr := os.Stat(path)
				require.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var obs agentTaskObservation
			require.NoError(t, json.Unmarshal(raw, &obs))
			require.Len(t, obs.Inference, 2)
			assert.True(t, obs.Completed && obs.CommandOK && obs.FileOK)
			assert.Equal(t, "ses_child", obs.Inference[1].SessionID)
			assert.Equal(t, "ses_factory", obs.Inference[1].Operation.Session)
			assert.Equal(t, "1.19.5", obs.Inference[1].Operation.Version)
			assert.Equal(t, []string{"glm-5.3"}, obs.Inference[1].ReportedModels)
			assert.NotContains(t, string(raw), captureFixtureKey)
			assert.NotContains(t, string(raw), agentMissionPromptFor("opencode"))
		})
	}
}

func TestAgentEvidenceOpenCodeSubagentCapturedFactorySink(t *testing.T) {
	root, bin, capture := agentFactoryFixture(t, "opencode", "success")
	client, _ := agentFactoryProbeClient(t)
	factory := liveCapturedFactoryWithClient(root, bin, agentFactoryRules(t), client, capture)
	result, err := executeAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "opencode"), root)
	// Existing source fixture has main traffic only: complete files and a valid
	// child export must not make it a completed captured-helper mission.
	require.Error(t, err)
	assert.Nil(t, result)
	require.FileExists(t, filepath.Join(root, agentProofFile))
	require.Len(t, capture.Records(), 1)
}
