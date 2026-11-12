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

func TestAgentEvidenceClaudeRoutingHeaderBoundary(t *testing.T) {
	for _, header := range []string{"X-Claude-Code-Session-Id", "X-Claude-Code-Agent-Id", "X-Claude-Code-Parent-Agent-Id", "X-Claude-Code-Request-Class", "X-Claude-Code-Agent-Type"} {
		for _, mode := range []string{"safe", "empty", "duplicate", "combined", "encoded", "secret", "oversized"} {
			t.Run(header+"/"+mode, func(t *testing.T) {
				var calls atomic.Int32
				capture := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != `{"model":"glm-5.3","max_tokens":1024}` || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || r.URL.Path != "/v1/messages" || r.Header.Get(header) != "safe-id" {
						t.Error("routing capture did not preserve actual request arguments")
						w.WriteHeader(400)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if _, err := io.WriteString(w, `{"type":"message","model":"glm-5.3","stop_reason":"end_turn"}`); err != nil {
						t.Error("cannot write private response")
					}
				}, captureLimits{})
				capture.backend = "claude-code"
				req, err := http.NewRequest("POST", hostedRoot+"/v1/messages", strings.NewReader(`{"model":"glm-5.3","max_tokens":1024}`))
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+captureFixtureKey)
				req.Header.Set("Content-Type", "application/json")
				value := "safe-id"
				switch mode {
				case "empty":
					value = ""
				case "combined":
					value = "safe-id,other"
				case "encoded":
					value = "agent%2Fother"
				case "secret":
					value = captureFixtureKey
				case "oversized":
					value = strings.Repeat("a", 257)
				}
				req.Header.Add(header, value)
				if mode == "duplicate" {
					req.Header.Add(header, value)
				}
				res, err := captureClient(t, capture, true).Do(req)
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, res.Body)
				require.NoError(t, err)
				require.NoError(t, res.Body.Close())
				records := capture.Records()
				root := t.TempDir()
				err = validateCapturedInference(records, "glm-5.3", []string{"main"})
				if err == nil {
					_, err = saveObservation(root, captureFixtureKey, "headers.json", records)
				}
				if mode == "safe" {
					require.NoError(t, err)
					assert.EqualValues(t, 1, calls.Load())
					assert.Equal(t, 200, res.StatusCode)
					raw, err := os.ReadFile(filepath.Join(root, evidencePath, "observations/headers.json"))
					require.NoError(t, err)
					assert.Contains(t, string(raw), "safe-id")
				} else {
					require.Error(t, err)
					assert.Zero(t, calls.Load(), "unsafe headers must refuse before upstream I/O")
					assert.Equal(t, 502, res.StatusCode)
					_, statErr := os.Stat(filepath.Join(root, evidencePath, "observations/headers.json"))
					assert.True(t, os.IsNotExist(statErr))
				}
			})
		}
	}
}

// Protocol executables deliberately simulate the versioned headers/lifecycle.
// They prove the live collector's feeds and sink, not installed helper behavior.
func TestAgentEvidenceClaudeSubagentRoutingFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "missing", "session", "agent", "parent", "class", "type", "response-model", "main-session", "main-class", "main-agent", "missing-capture", "missing-lifecycle", "stale", "wrong-version", "missing-file"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", "claude-route-"+mode)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			binding := liveBinding(captureFixtureKey, "glm-5.3", "claude-code")
			client, calls := agentFactoryProbeClient(t)
			factory := liveClaudeIsolationFactoryWithClient(root, bin, agentFactoryRules(t), client, isolation, capture, binding.Effort)
			started := time.Now().UTC()
			result, err := executePreparedAgentMission(context.Background(), factory, binding, ws)
			require.NoError(t, err)
			finished := time.Now().UTC()
			// Refusal cases must reach actual inference and command-created output.
			proof, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(proof))
			assert.Zero(t, *calls)
			records := capture.Records()
			if mode == "missing" {
				require.Len(t, records, 1)
			} else {
				require.Len(t, records, 2)
			}
			switch mode {
			case "main-session", "main-class", "main-agent":
				capture.mu.Lock()
				if mode == "main-session" {
					capture.records[0].SessionID = ""
				}
				if mode == "main-class" {
					capture.records[0].ClaudeRequestClass = ""
				}
				if mode == "main-agent" {
					capture.records[0].ClaudeAgentID = "agent_one"
				}
				capture.mu.Unlock()
			case "missing-capture":
				capture = nil
			case "missing-lifecycle":
				isolation.subagent = claudeSubagentProof{}
			case "stale":
				started = finished
			case "wrong-version":
				isolation.subagent.Version = "2.1.321"
			case "missing-file":
				require.NoError(t, os.Remove(filepath.Join(ws, agentCommandFile)))
			}
			observation, err := collectClaudeSubagentRoutingEvidence(isolation, capture, result, started, finished)
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "claude-routing.json", observation)
			}
			path := filepath.Join(root, evidencePath, "observations/claude-routing.json")
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(path)
				assert.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			require.Len(t, saved.Inference, 2)
			assert.Equal(t, "main", saved.Inference[0].Category)
			helper := saved.Inference[1]
			assert.Equal(t, "subagent", helper.Category)
			assert.Equal(t, "ses_factory", helper.SessionID)
			assert.Equal(t, "agent_one", helper.ClaudeAgentID)
			assert.Equal(t, "general-purpose", helper.ClaudeAgentType)
			assert.Equal(t, "subagent", helper.ClaudeRequestClass)
			assert.Equal(t, "glm-5.3", helper.RequestedModel)
			assert.Equal(t, []string{"glm-5.3"}, helper.ReportedModels)
			require.NotNil(t, helper.Operation)
			assert.Equal(t, "tool_agent", helper.Operation.ID)
			assert.Equal(t, claudeSubagentRoutingSource, helper.Operation.Source)
			assert.Equal(t, claudeStartupVersion, helper.Operation.Version)
			assert.Equal(t, []string{helper.RequestID}, helper.Operation.RequestIDs)
			for _, secret := range []string{root, bin, captureFixtureKey, agentMissionPrompt, "private report", claudeForeignCredential} {
				assert.NotContains(t, string(raw), secret)
			}
		})
	}
}
