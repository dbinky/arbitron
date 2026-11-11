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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexFixtureOutput = `{"type":"thread.started","thread_id":"session-one"}
{"type":"turn.started"}
{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}
`
const codexFixtureRollout = `{"type":"session_meta","payload":{"id":"session-one"}}
{"type":"turn_context","payload":{"model":"glm-5.3","model_provider":"lunaroute"}}
`
const piFixtureOutput = `{"type":"session","id":"session-one"}
{"type":"message_end","message":{"role":"assistant","provider":"lunaroute","model":"glm-5.3","stopReason":"toolUse","content":[{"type":"toolCall","name":"bash"}]}}
{"type":"message_end","message":{"role":"assistant","provider":"lunaroute","model":"glm-5.3","stopReason":"stop","content":[{"type":"text","text":"private-answer"}]}}
{"type":"agent_end"}
`

func backendEvidenceFixture(t *testing.T, backend string, authPresent bool) (string, string, backendEvidenceInput) {
	t.Helper()
	root := t.TempDir()
	home := seedConflictingHome(t)
	if !authPresent {
		for _, p := range []string{"home/.codex/auth.json", "home/.pi/agent/auth.json"} {
			require.NoError(t, os.Remove(filepath.Join(home, p)))
		}
	}
	before, err := snapshotBackendHomes(home)
	require.NoError(t, err)
	ws := filepath.Join(home, "workspace")
	require.NoError(t, requireEmptyBackendProofs(ws))
	private := filepath.Join(home, "private")
	require.NoError(t, os.Mkdir(private, 0700))
	started := time.Now().UTC()
	_, err = runCaptured(t, context.Background(), "/bin/sh", []string{"-c", "printf ok > lunaroute-ok.txt; wc -c < lunaroute-ok.txt > listing.txt"}, []string{"PATH=/usr/bin:/bin"}, ws, "")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(private))
	main := validCaptureRecord()
	main.Backend, main.Path = backend, "/v1/responses"
	if backend == "pi" {
		main.Path = "/v1/chat/completions"
	}
	main.StartedAt, main.FinishedAt = started, time.Now().UTC()
	helper := main
	helper.RequestID, helper.Category = "request-two", "compaction"
	helper.Operation = &capturedOperation{RunID: main.RunID, Backend: backend, ID: "operation-one", Session: "session-one", Category: helper.Category, Trigger: "forced-compaction", Source: "fixture lifecycle", Version: "fixture-1", StartedAt: helper.StartedAt, FinishedAt: helper.FinishedAt, RequestIDs: []string{helper.RequestID}}
	ref := evidencePath + "/observations/config.json"
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, ref)), 0700))
	raw, err := json.Marshal(agentConfigReference{RunID: main.RunID, Backend: backend, Model: "glm-5.3", SessionID: "session-one", Source: "fixture emitted config"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ref), raw, 0600))
	output, rollout := codexFixtureOutput, codexFixtureRollout
	if backend == "pi" {
		output, rollout = piFixtureOutput, ""
	}
	m := provider.LunaRouteModel{ID: "glm-5.3", ContextTokens: provider.LimitFact{Tokens: 202752, Evidence: "fixture#context"}, OutputTokens: provider.LimitFact{Tokens: 32768, Evidence: "fixture#output"}, Reasoning: provider.SupportFact{Support: provider.SupportUnsupported, Evidence: "fixture#reasoning"}}
	return root, ws, backendEvidenceInput{RunID: main.RunID, Backend: backend, Model: m.ID, Version: "fixture-1", ConfigRef: ref, StartedAt: started, FinishedAt: time.Now().UTC(), RequiredCategories: []string{"main", "compaction"}, Records: []capturedInference{main, helper}, Stdout: []byte(output), Rollout: []byte(rollout), Rules: provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{m.ID: m}), Home: home, Before: before, PrivateDir: private}
}

func TestBackendEvidenceCompletedTaskWithAndWithoutSubscription(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		for _, auth := range []bool{true, false} {
			t.Run(backend+map[bool]string{true: "-present", false: "-absent"}[auth], func(t *testing.T) {
				root, ws, in := backendEvidenceFixture(t, backend, auth)
				require.NoError(t, publishBackendEvidence(root, ws, captureFixtureKey, "backend.json", in))
				raw, err := os.ReadFile(filepath.Join(root, evidencePath, "observations/backend.json"))
				require.NoError(t, err)
				var sink backendTaskObservation
				require.NoError(t, json.Unmarshal(raw, &sink))
				assert.True(t, sink.Completed && sink.FileOK && sink.CommandOK && sink.HomesUnchanged && sink.PrivateCleaned)
				require.Len(t, sink.Inference, 2)
				assert.Equal(t, "glm-5.3", sink.Inference[1].RequestedModel)
				assert.Equal(t, []string{"glm-5.3"}, sink.Inference[1].ReportedModels)
				require.NotEmpty(t, sink.ModelEvidence)
				wantSource := "codex_turn_context_intent"
				if backend == "pi" {
					wantSource = "pi_message_end"
				}
				assert.Equal(t, wantSource, sink.ModelEvidence[0].Source)
				for _, secret := range []string{captureFixtureKey, "private-answer", "conflicting-subscription-secret", "toolCall"} {
					assert.NotContains(t, string(raw), secret)
				}
			})
		}
	}
}

func TestBackendEvidenceRejectsIncompleteOrUnsafeInput(t *testing.T) {
	cases := map[string]func(string, *backendEvidenceInput){
		"no traffic":          func(_ string, in *backendEvidenceInput) { in.Records = nil },
		"no helper":           func(_ string, in *backendEvidenceInput) { in.Records = in.Records[:1] },
		"uncorrelated helper": func(_ string, in *backendEvidenceInput) { in.Records[1].Operation = nil },
		"stale session":       func(_ string, in *backendEvidenceInput) { in.Records[1].Operation.Session = "other-session" },
		"wrong provider": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, `"lunaroute"`, `"openai"`))
		},
		"wrong model": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, "glm-5.3", "glm-5.4"))
		},
		"no turn models": func(_ string, in *backendEvidenceInput) { in.Stdout = []byte(`{"type":"session","id":"session-one"}`) },
		"failed terminal": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, `"stop"`, `"error"`))
		},
		"aborted terminal": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, `"stop"`, `"aborted"`))
		},
		"incomplete terminal": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, `"stop"`, `"length"`))
		},
		"last tool only": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, `"stop"`, `"toolUse"`))
		},
		"malformed": func(_ string, in *backendEvidenceInput) {
			in.Stdout = append(in.Stdout, []byte("{broken lr_secret")...)
		},
		"scanner overflow": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.Repeat("x", backendRecordLimit+1))
		},
		"secret model": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, "glm-5.3", "claude-"+captureFixtureKey))
		},
		"metadata absent": func(_ string, in *backendEvidenceInput) { in.Rules = provider.CapabilityRules{} },
		"reasoning unknown": func(_ string, in *backendEvidenceInput) {
			m, _ := in.Rules.LunaRouteModel(in.Model)
			m.Reasoning = provider.SupportFact{}
			in.Rules = in.Rules.WithLunaRouteModels(map[string]provider.LunaRouteModel{m.ID: m})
		},
		"missing config": func(_ string, in *backendEvidenceInput) { in.ConfigRef = evidencePath + "/observations/missing.json" },
		"changed project": func(_ string, in *backendEvidenceInput) {
			require.NoError(t, os.WriteFile(filepath.Join(in.Home, "workspace/.pi/settings.json"), []byte(`{}`), 0600))
		},
		"new user file": func(_ string, in *backendEvidenceInput) {
			require.NoError(t, os.WriteFile(filepath.Join(in.Home, "home/.codex/new"), []byte("unsafe"), 0600))
		},
		"cleanup interrupted": func(_ string, in *backendEvidenceInput) { require.NoError(t, os.Mkdir(in.PrivateDir, 0700)) },
		"missing proof": func(ws string, _ *backendEvidenceInput) {
			require.NoError(t, os.Remove(filepath.Join(ws, "lunaroute-ok.txt")))
		},
		"missing command": func(ws string, _ *backendEvidenceInput) {
			require.NoError(t, os.Remove(filepath.Join(ws, "listing.txt")))
		},
		"wrong command": func(ws string, _ *backendEvidenceInput) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "listing.txt"), []byte("0"), 0600))
		},
		"precreated proof": func(ws string, in *backendEvidenceInput) {
			old := in.StartedAt.Add(-time.Hour)
			require.NoError(t, os.Chtimes(filepath.Join(ws, "lunaroute-ok.txt"), old, old))
		},
		"missing tool trace": func(_ string, in *backendEvidenceInput) {
			in.Stdout = []byte(strings.ReplaceAll(piFixtureOutput, "toolCall", "text"))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			root, ws, in := backendEvidenceFixture(t, "pi", true)
			change(ws, &in)
			err := publishBackendEvidence(root, ws, captureFixtureKey, "rejected.json", in)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
			_, err = os.Stat(filepath.Join(root, evidencePath, "observations/rejected.json"))
			assert.True(t, os.IsNotExist(err))
		})
	}
}

func TestBackendEvidenceCannotWeakenRequiredCategories(t *testing.T) {
	root, ws, in := backendEvidenceFixture(t, "pi", false)
	in.RequiredCategories = []string{"main", "main"}
	in.Records = in.Records[:1]
	require.Error(t, publishBackendEvidence(root, ws, captureFixtureKey, "rejected.json", in))
}

func TestBackendEvidenceSnapshotBoundary(t *testing.T) {
	for _, kind := range []string{"symlink", "oversized", "missing"} {
		t.Run(kind, func(t *testing.T) {
			home := seedConflictingHome(t)
			path := filepath.Join(home, "workspace/.pi/settings.json")
			require.NoError(t, os.Remove(path))
			switch kind {
			case "symlink":
				require.NoError(t, os.Symlink(filepath.Join(home, "home/.pi/agent/auth.json"), path))
			case "oversized":
				require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", backendTreeLimit+1)), 0600))
			case "missing":
				require.NoError(t, os.RemoveAll(filepath.Join(home, "workspace/.pi")))
			}
			_, err := snapshotBackendHomes(home)
			require.Error(t, err)
		})
	}
}

func TestBackendEvidencePiSeparateUnknownLimitsRefused(t *testing.T) {
	for _, axis := range []string{"context", "output"} {
		t.Run(axis, func(t *testing.T) {
			root, ws, in := backendEvidenceFixture(t, "pi", false)
			m, _ := in.Rules.LunaRouteModel(in.Model)
			if axis == "context" {
				m.ContextTokens = provider.LimitFact{}
			} else {
				m.OutputTokens = provider.LimitFact{}
			}
			in.Rules = in.Rules.WithLunaRouteModels(map[string]provider.LunaRouteModel{m.ID: m})
			require.Error(t, publishBackendEvidence(root, ws, captureFixtureKey, "rejected.json", in))
		})
	}
}

func TestBackendEvidenceReviewedAliasDrift(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		t.Run(backend, func(t *testing.T) {
			root, ws, in := backendEvidenceFixture(t, backend, false)
			in.Model = "claude-sonnet-5-lr1"
			in.Stdout = []byte(strings.ReplaceAll(string(in.Stdout), "glm-5.3", "claude-sonnet-5-5"))
			in.Rollout = []byte(strings.ReplaceAll(string(in.Rollout), "glm-5.3", "claude-sonnet-5-5"))
			m, _ := in.Rules.LunaRouteModel("glm-5.3")
			m.ID = in.Model
			in.Rules = in.Rules.WithLunaRouteModels(map[string]provider.LunaRouteModel{m.ID: m})
			for i := range in.Records {
				in.Records[i].RequestedModel = in.Model
				in.Records[i].ReportedModels = []string{"claude-sonnet-5-5"}
			}
			raw, err := json.Marshal(agentConfigReference{RunID: in.RunID, Backend: backend, Model: in.Model, SessionID: "session-one", Source: "fixture emitted configuration"})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, in.ConfigRef), raw, 0600))
			require.NoError(t, publishBackendEvidence(root, ws, captureFixtureKey, "alias.json", in))
			raw, err = os.ReadFile(filepath.Join(root, evidencePath, "observations/alias.json"))
			require.NoError(t, err)
			var sink backendTaskObservation
			require.NoError(t, json.Unmarshal(raw, &sink))
			assert.Equal(t, in.Model, sink.Model)
			assert.Equal(t, "claude-sonnet-5-5", sink.ModelEvidence[0].Model)
		})
	}
}

func TestBackendEvidenceCodexRequiresCorrelatedRolloutAndFinalTurn(t *testing.T) {
	for name, change := range map[string]func(*backendEvidenceInput){
		"empty rollout": func(in *backendEvidenceInput) { in.Rollout = nil },
		"no models": func(in *backendEvidenceInput) {
			in.Rollout = []byte(`{"type":"session_meta","payload":{"id":"session-one"}}`)
		},
		"wrong thread": func(in *backendEvidenceInput) {
			in.Rollout = []byte(strings.ReplaceAll(codexFixtureRollout, "session-one", "other-session"))
		},
		"wrong provider": func(in *backendEvidenceInput) {
			in.Rollout = []byte(strings.ReplaceAll(codexFixtureRollout, "lunaroute", "openai"))
		},
		"wrong model": func(in *backendEvidenceInput) {
			in.Rollout = []byte(strings.ReplaceAll(codexFixtureRollout, "glm-5.3", "gpt-6.1-sol"))
		},
		"failed after completed": func(in *backendEvidenceInput) { in.Stdout = append(in.Stdout, []byte(`{"type":"turn.failed"}`)...) },
		"new incomplete turn":    func(in *backendEvidenceInput) { in.Stdout = append(in.Stdout, []byte(`{"type":"turn.started"}`)...) },
		"no terminal": func(in *backendEvidenceInput) {
			in.Stdout = []byte(`{"type":"thread.started","thread_id":"session-one"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, ws, in := backendEvidenceFixture(t, "codex", false)
			change(&in)
			require.Error(t, publishBackendEvidence(root, ws, captureFixtureKey, "rejected.json", in))
		})
	}
}

// A protocol subprocess exercises the credential-bearing seam, not an installed
// codex/pi contract. Its synthetic compaction lifecycle verifies plumbing only.
func TestBackendEvidenceCapturedProcessToObservation(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		t.Run(backend, func(t *testing.T) {
			root, ws, in := backendEvidenceFixture(t, backend, true)
			for _, name := range []string{backendProofFile, backendCommandFile} {
				require.NoError(t, os.Remove(filepath.Join(ws, name)))
			}
			require.NoError(t, requireEmptyBackendProofs(ws))
			require.NoError(t, os.Mkdir(in.PrivateDir, 0700))
			c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string
					Input string
				}
				path := "/v1/responses"
				if backend == "pi" {
					path = "/v1/chat/completions"
				}
				if r.Method != "POST" || r.Host != captureHost || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "glm-5.3" || body.Input != "fixture-backend-nonce" {
					t.Error("subprocess launch feed missing")
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if backend == "codex" {
					io.WriteString(w, `{"model":"glm-5.3","status":"completed"}`)
				} else {
					io.WriteString(w, `{"model":"glm-5.3","choices":[{"finish_reason":"stop"}]}`)
				}
			}, captureLimits{})
			c.backend = backend
			bin, err := os.Executable()
			require.NoError(t, err)
			in.StartedAt = time.Now().UTC()
			in.Stdout, err = runCaptured(t, context.Background(), c.WrapBinary(t, bin), []string{"-test.run=^TestBackendEvidenceProcessFixture$"}, []string{"PATH=/usr/bin:/bin", "ARBITRON_BACKEND_FIXTURE=1", "FIXTURE_BACKEND=" + backend, "FIXTURE_MODEL=" + in.Model, "LUNAROUTE_API_KEY=" + captureFixtureKey, "FIXTURE_CONFIG=" + filepath.Join(root, in.ConfigRef), "FIXTURE_PRIVATE=" + in.PrivateDir, "FIXTURE_WORKSPACE=" + ws}, ws, "fixture-backend-nonce")
			require.NoError(t, err)
			in.Rollout = nil
			if backend == "codex" {
				in.Rollout, err = os.ReadFile(filepath.Join(in.PrivateDir, "rollout.jsonl"))
				require.NoError(t, err)
			}
			records := c.Records()
			require.Len(t, records, 2)
			require.NoError(t, c.CorrelateOperation(capturedOperation{RunID: in.RunID, Backend: backend, ID: "fixture-compact", Session: "session-one", Category: "compaction", Trigger: "fixture-second-request", Source: "fixture lifecycle", Version: in.Version, StartedAt: records[1].StartedAt, FinishedAt: records[1].FinishedAt, RequestIDs: []string{records[1].RequestID}}))
			in.Records = c.Records()
			require.NoError(t, os.RemoveAll(in.PrivateDir))
			in.FinishedAt = time.Now().UTC()
			require.NoError(t, publishBackendEvidence(root, ws, captureFixtureKey, "captured-backend.json", in))
			raw, err := os.ReadFile(filepath.Join(root, evidencePath, "observations/captured-backend.json"))
			require.NoError(t, err)
			var sink backendTaskObservation
			require.NoError(t, json.Unmarshal(raw, &sink))
			require.Len(t, sink.Inference, 2)
			assert.Equal(t, in.Model, sink.Inference[0].RequestedModel)
			assert.Equal(t, []string{"glm-5.3"}, sink.Inference[1].ReportedModels)
			assert.Equal(t, "session-one", sink.SessionID)
			assert.True(t, sink.Completed && sink.FileOK && sink.CommandOK && sink.HomesUnchanged && sink.PrivateCleaned)
			assert.NotContains(t, string(raw), captureFixtureKey)
			assert.NotContains(t, string(raw), "fixture-backend-nonce")
			in.Records = nil
			require.Error(t, publishBackendEvidence(root, ws, captureFixtureKey, "missing-capture.json", in))
		})
	}
}

func TestBackendEvidenceProcessFixture(t *testing.T) {
	if os.Getenv("ARBITRON_BACKEND_FIXTURE") != "1" {
		return
	}
	backend := os.Getenv("FIXTURE_BACKEND")
	if backend != "codex" && backend != "pi" || os.Getenv("FIXTURE_MODEL") != "glm-5.3" {
		t.Fatal("invalid fixture route")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	cwd, cwdErr := os.Getwd()
	wantCwd, pathErr := filepath.EvalSymlinks(os.Getenv("FIXTURE_WORKSPACE"))
	if err != nil || string(input) != "fixture-backend-nonce" || cwdErr != nil || pathErr != nil || cwd != wantCwd {
		t.Fatal("fixture cwd/stdin missing")
	}
	dir := os.Getenv("FIXTURE_PRIVATE")
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		t.Fatal("fixture private directory missing")
	}
	for _, name := range []string{"auth.json", "config.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("fixture inherited auth/config")
		}
	}
	ca, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		t.Fatal("fixture trust missing")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	path := "/v1/responses"
	if backend == "pi" {
		path = "/v1/chat/completions"
	}
	for range 2 {
		req, err := http.NewRequest("POST", hostedRoot+path, strings.NewReader(`{"model":"glm-5.3","input":"fixture-backend-nonce"}`))
		if err != nil {
			t.Fatal("fixture request invalid")
		}
		req.Header.Set("Authorization", "Bearer "+os.Getenv("LUNAROUTE_API_KEY"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("fixture inference failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		models, parseErr := extractCapturedResponse(path, raw, false)
		if readErr != nil || resp.StatusCode != 200 || parseErr != nil || len(models) != 1 || models[0] != "glm-5.3" {
			t.Fatal("fixture response invalid")
		}
	}
	transport.CloseIdleConnections()
	cmd := exec.Command("/bin/sh", "-c", "printf ok > lunaroute-ok.txt; wc -c < lunaroute-ok.txt > listing.txt")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if cmd.Run() != nil {
		t.Fatal("fixture tool failed")
	}
	projection := agentConfigReference{RunID: "run-one", Backend: backend, Model: os.Getenv("FIXTURE_MODEL"), SessionID: "session-one", Source: "fixture emitted configuration"}
	raw, err := json.Marshal(projection)
	if err != nil || os.WriteFile(os.Getenv("FIXTURE_CONFIG"), raw, 0600) != nil {
		t.Fatal("fixture config projection failed")
	}
	output := piFixtureOutput
	if backend == "codex" {
		output = codexFixtureOutput
		if os.WriteFile(filepath.Join(dir, "rollout.jsonl"), []byte(codexFixtureRollout), 0600) != nil {
			t.Fatal("fixture rollout failed")
		}
	}
	if _, err := io.WriteString(os.Stdout, output); err != nil {
		t.Fatal("fixture stdout failed")
	}
	os.Exit(0)
}

func TestCapturedBackendRunPreservesInputsAndSanitizesFailures(t *testing.T) {
	ws := t.TempDir()
	raw, err := runCaptured(t, context.Background(), "/bin/sh", []string{"-c", `[ "$1" = nonce-arg ] && [ "$FIXTURE_KEY" = expected-key ] && [ "$(cat)" = nonce-stdin ] && printf ok > actual-file && printf '{"type":"done"}\n'`, "fixture", "nonce-arg"}, []string{"PATH=/usr/bin:/bin", "FIXTURE_KEY=expected-key"}, ws, "nonce-stdin")
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"done"}`, string(raw))
	file, err := os.ReadFile(filepath.Join(ws, "actual-file"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(file))
	// This finite successful process must fail solely on the output cap, not
	// because a short caller deadline eventually kills an infinite producer.
	raw, err = runCaptured(t, context.Background(), "/bin/sh", []string{"-c", "head -c " + strconv.Itoa(backendOutputLimit+1) + " /dev/zero"}, []string{"PATH=/usr/bin:/bin"}, ws, "")
	require.Error(t, err)
	assert.Empty(t, raw)
	for _, script := range []string{"echo lr_private_secret >&2; exit 1", "sleep 5"} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		raw, err := runCaptured(t, ctx, "/bin/sh", []string{"-c", script}, []string{"PATH=/usr/bin:/bin"}, ws, "")
		cancel()
		require.Error(t, err)
		assert.Empty(t, raw)
		assert.NotContains(t, err.Error(), "lr_private_secret")
	}
	_, err = runCaptured(t, context.Background(), "/missing/lr_private_secret", nil, []string{"PATH=/usr/bin:/bin"}, ws, "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "lr_private_secret")
}
