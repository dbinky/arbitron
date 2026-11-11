package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func agentEvidenceFixture(t *testing.T) (string, string, agentEvidenceInput) {
	t.Helper()
	root, ws := t.TempDir(), t.TempDir()
	started := time.Now().UTC()
	// Produce both workspace sinks with an actual subprocess, not prefilled booleans.
	spec := claudecli.RunSpec{Bin: "/bin/sh", Args: []string{"-c", "printf LUNAROUTE-OK > lunaroute-proof.txt; wc -c < lunaroute-proof.txt > lunaroute-command.txt"}, Dir: ws, Env: []string{"PATH=/usr/bin:/bin"}, MaxResponseBytes: 1024}
	_, err := runCapturedLines(context.Background(), claudecli.NewExecRunner(), spec)
	require.NoError(t, err)
	main := validCaptureRecord()
	main.Backend, main.Path = "claude-code", "/v1/messages"
	main.StartedAt, main.FinishedAt = started, time.Now().UTC()
	helper := main
	helper.RequestID, helper.Category = "request-two", "subagent"
	helper.Operation = &capturedOperation{RunID: main.RunID, Backend: main.Backend, ID: "operation-one", Session: "session-one", Category: helper.Category, Trigger: "forced-subagent", Source: "fixture lifecycle parser", Version: "fixture-1", StartedAt: helper.StartedAt.Add(-time.Second), FinishedAt: helper.FinishedAt.Add(time.Second), RequestIDs: []string{helper.RequestID}}
	ref := evidencePath + "/observations/contract.json"
	path := filepath.Join(root, ref)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(`{"run_id":"run-one","backend":"claude-code","model":"glm-5.3","session_id":"session-one","source":"fixture emitted config"}`), 0600))
	in := agentEvidenceInput{StartedAt: started, FinishedAt: time.Now().UTC(), RunID: main.RunID, Backend: main.Backend, Model: "glm-5.3", Version: "fixture-1", ConfigRef: ref, RequiredCategories: []string{"main", "subagent"}, Records: []capturedInference{main, helper}, Result: &engine.AgentResult{Model: "glm-5.3", SessionID: "session-one", Steps: []engine.AgentStep{{Kind: "tool_call"}, {Kind: "assistant", Summary: "private response"}}, ModelEvidence: []engine.ModelEvidence{{Model: "glm-5.3", Source: "claude_model_usage"}}}}
	return root, ws, in
}

func TestAgentEvidenceCompletedToolTask(t *testing.T) {
	root, ws, in := agentEvidenceFixture(t)
	obs, err := collectAgentEvidence(root, ws, captureFixtureKey, in)
	require.NoError(t, err)
	require.NoError(t, publishAgentEvidence(root, ws, captureFixtureKey, "agent.json", in))
	raw, err := os.ReadFile(filepath.Join(root, evidencePath, "observations/agent.json"))
	require.NoError(t, err)
	var sink agentTaskObservation
	require.NoError(t, json.Unmarshal(raw, &sink))
	assert.True(t, sink.FileOK && sink.CommandOK && sink.Completed)
	assert.Equal(t, obs, sink)
	assert.Equal(t, "glm-5.3", sink.Inference[0].RequestedModel)
	assert.Equal(t, []string{"glm-5.3"}, sink.Inference[1].ReportedModels)
	assert.Equal(t, []string{"tool_call", "assistant"}, sink.StepKinds)
	assert.Equal(t, in.ConfigRef, sink.ConfigRef)
	for _, secret := range []string{captureFixtureKey, "private response", "LUNAROUTE-OK"} {
		assert.NotContains(t, string(raw), secret)
	}
}

func TestAgentEvidenceRequiresMainAndHelpers(t *testing.T) {
	for name, change := range map[string]func(*agentEvidenceInput){
		"empty":                     func(in *agentEvidenceInput) { in.Records = nil },
		"main only":                 func(in *agentEvidenceInput) { in.Records = in.Records[:1] },
		"helper only":               func(in *agentEvidenceInput) { in.Records = in.Records[1:] },
		"no required helpers":       func(in *agentEvidenceInput) { in.RequiredCategories = []string{"main"} },
		"unobserved operation":      func(in *agentEvidenceInput) { in.Records[1].Operation = nil },
		"stale run":                 func(in *agentEvidenceInput) { in.RunID = "run-old" },
		"foreign backend":           func(in *agentEvidenceInput) { in.Backend = "opencode" },
		"wrong model":               func(in *agentEvidenceInput) { in.Records[0].RequestedModel = "glm-5.4" },
		"wrong provider":            func(in *agentEvidenceInput) { in.Records[0].Host = "foreign.example" },
		"wrong dialect":             func(in *agentEvidenceInput) { in.Records[0].Path = "/v1/responses" },
		"wrong session":             func(in *agentEvidenceInput) { in.Records[1].Operation.Session = "session-old" },
		"missing reported proof":    func(in *agentEvidenceInput) { in.Result.ModelEvidence = nil },
		"foreign reported model":    func(in *agentEvidenceInput) { in.Result.ModelEvidence[0].Model = "gpt-6.1-sol" },
		"foreign reported provider": func(in *agentEvidenceInput) { in.Result.ModelEvidence[0].Provider = "openai" },
	} {
		t.Run(name, func(t *testing.T) {
			root, ws, in := agentEvidenceFixture(t)
			change(&in)
			require.Error(t, publishAgentEvidence(root, ws, captureFixtureKey, "rejected.json", in))
			_, err := os.Stat(filepath.Join(root, evidencePath, "observations/rejected.json"))
			assert.True(t, os.IsNotExist(err), "no partial observation")
		})
	}
}

func TestAgentEvidenceRejectsIncompleteToolTask(t *testing.T) {
	for name, change := range map[string]func(string, *agentEvidenceInput){
		"nil result":   func(_ string, in *agentEvidenceInput) { in.Result = nil },
		"missing tool": func(_ string, in *agentEvidenceInput) { in.Result.Steps = nil },
		"missing file": func(ws string, _ *agentEvidenceInput) {
			require.NoError(t, os.Remove(filepath.Join(ws, "lunaroute-proof.txt")))
		},
		"wrong file": func(ws string, _ *agentEvidenceInput) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "lunaroute-proof.txt"), []byte("summary echo"), 0600))
		},
		"missing command": func(ws string, _ *agentEvidenceInput) {
			require.NoError(t, os.Remove(filepath.Join(ws, "lunaroute-command.txt")))
		},
		"wrong command": func(ws string, _ *agentEvidenceInput) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "lunaroute-command.txt"), []byte("0"), 0600))
		},
		"symlink": func(ws string, _ *agentEvidenceInput) {
			require.NoError(t, os.Remove(filepath.Join(ws, "lunaroute-proof.txt")))
			require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(ws, "lunaroute-proof.txt")))
		},
		"not completed": func(_ string, in *agentEvidenceInput) { in.Records[0].Completed = false },
		"skipped":       func(_ string, in *agentEvidenceInput) { in.Version = "" },
	} {
		t.Run(name, func(t *testing.T) {
			root, ws, in := agentEvidenceFixture(t)
			change(ws, &in)
			_, err := collectAgentEvidence(root, ws, captureFixtureKey, in)
			require.Error(t, err)
		})
	}
}

func TestAgentEvidenceMissingReference(t *testing.T) {
	root, ws, in := agentEvidenceFixture(t)
	require.NoError(t, os.Remove(filepath.Join(root, in.ConfigRef)))
	require.Error(t, publishAgentEvidence(root, ws, captureFixtureKey, "rejected.json", in))
	_, err := os.Stat(filepath.Join(root, evidencePath, "observations/rejected.json"))
	assert.True(t, os.IsNotExist(err))
}

func TestAgentEvidenceRejectsMalformedConfigAndSecrets(t *testing.T) {
	for _, body := range []string{"", `null`, `{`, `{"run_id":"run-old"}`, `{"run_id":"run-one","run_id":"run-one"}`, `{"run_id":"run-one","backend":"claude-code","model":"glm-5.3","session_id":"session-one","source":"fixture","env":{"ANTHROPIC_AUTH_TOKEN":"lr_capture_private_key"}}`} {
		root, ws, in := agentEvidenceFixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, in.ConfigRef), []byte(body), 0600))
		err := publishAgentEvidence(root, ws, captureFixtureKey, "rejected.json", in)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), captureFixtureKey)
	}
	for _, field := range []string{"model", "provider", "source", "version", "ref", "category"} {
		t.Run(field, func(t *testing.T) {
			root, ws, in := agentEvidenceFixture(t)
			switch field {
			case "model":
				in.Result.ModelEvidence[0].Model = "claude-" + captureFixtureKey
			case "provider":
				in.Result.ModelEvidence[0].Provider = captureFixtureKey
			case "source":
				in.Result.ModelEvidence[0].Source = captureFixtureKey
			case "version":
				in.Version = captureFixtureKey
			case "ref":
				in.ConfigRef = "../../go.mod"
			case "category":
				in.RequiredCategories = append(in.RequiredCategories, captureFixtureKey)
			}
			err := publishAgentEvidence(root, ws, captureFixtureKey, "rejected.json", in)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
		})
	}
}

func TestAgentEvidenceBoundedCommandAndVersion(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fixture")
	for _, tc := range []struct {
		script string
		valid  bool
	}{{"printf '2.1.322 (Claude Code)\\n'", true}, {"printf 'Bearer foreign-secret\\n'", false}, {"printf '\\033[31munsafe'", false}, {"echo lr_private_stderr >&2; exit 1", false}, {"yes x", false}} {
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700))
		got, err := queryVersion(context.Background(), bin)
		if tc.valid {
			require.NoError(t, err)
			assert.Equal(t, "2.1.322 (Claude Code)", got)
		} else {
			require.Error(t, err)
			assert.Empty(t, got)
			assert.NotContains(t, err.Error(), "foreign-secret")
			assert.NotContains(t, err.Error(), "lr_private_stderr")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := runCapturedLines(ctx, claudecli.NewExecRunner(), claudecli.RunSpec{Bin: "/bin/sh", Args: []string{"-c", "sleep 5"}, Env: []string{"PATH=/usr/bin:/bin"}, MaxResponseBytes: 1024})
	require.Error(t, err)
}

func TestAgentEvidenceExplicitModelPrerequisites(t *testing.T) {
	for _, value := range []string{"", "latest", "glm-999.9", "GLM-5.3"} {
		_, err := explicitLiveModel(func(string) (string, bool) { return value, true }, "MODEL")
		require.Error(t, err)
	}
	_, err := explicitLiveModel(func(string) (string, bool) { return "glm-5.3", false }, "MODEL")
	require.Error(t, err)
	model, err := explicitLiveModel(func(name string) (string, bool) { assert.Equal(t, "MODEL", name); return "glm-5.3", true }, "MODEL")
	require.NoError(t, err)
	assert.Equal(t, "glm-5.3", model)
	// Use the actual testing.T in a child: a missing input skips locally but fails release mode.
	for _, release := range []string{"0", "1", "invalid"} {
		bin, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.Command(bin, "-test.run=^TestAgentEvidencePrerequisiteChild$", "-test.v")
		modelValue := ""
		if release == "invalid" {
			modelValue = "glm-999.9"
		}
		cmd.Env = append(os.Environ(), "ARBITRON_AGENT_PREREQ_CHILD=1", "ARBITRON_AGENT_MODEL_TEST="+modelValue, "ARBITRON_LUNAROUTE_RELEASE_GATE="+release)
		out, err := cmd.CombinedOutput()
		if release == "0" {
			require.NoError(t, err)
			assert.Contains(t, string(out), "SKIP")
		} else {
			require.Error(t, err)
			assert.Contains(t, string(out), "FAIL")
		}
		assert.Contains(t, string(out), "release acceptance remains pending")
	}
}
func TestAgentEvidencePrerequisiteChild(t *testing.T) {
	if os.Getenv("ARBITRON_AGENT_PREREQ_CHILD") != "1" {
		return
	}
	liveModel(t, "ARBITRON_AGENT_MODEL_TEST")
	t.Fatal("missing input was silently defaulted")
}

func TestAgentEvidenceAliasDriftAndOpencode(t *testing.T) {
	for _, backend := range []string{"claude-code", "opencode"} {
		t.Run(backend, func(t *testing.T) {
			root, ws, in := agentEvidenceFixture(t)
			in.Backend, in.Model = backend, "claude-sonnet-5-lr1"
			in.Result.Model = in.Model
			in.Result.ModelEvidence = []engine.ModelEvidence{{Model: "claude-sonnet-5-5", Provider: "anthropic", Source: "fixture_reported_identity"}}
			for i := range in.Records {
				r := &in.Records[i]
				r.Backend = backend
				r.RequestedModel = in.Model
				r.ReportedModels = []string{"claude-sonnet-5-5"}
				if backend == "opencode" {
					r.Path = "/v1/chat/completions"
				}
				if r.Operation != nil {
					r.Operation.Backend = backend
				}
			}
			config := agentConfigReference{RunID: in.RunID, Backend: backend, Model: in.Model, SessionID: in.Result.SessionID, Source: "fixture emitted config"}
			raw, err := json.Marshal(config)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, in.ConfigRef), raw, 0600))
			obs, err := collectAgentEvidence(root, ws, captureFixtureKey, in)
			require.NoError(t, err)
			assert.Equal(t, "claude-sonnet-5-5", obs.ModelEvidence[0].Model, "preserve reported drift, not configured alias")
			in.Records[1].ReportedModels = []string{"glm-5.3"}
			require.Error(t, publishAgentEvidence(root, ws, captureFixtureKey, "foreign-helper.json", in))
		})
	}
}

func TestAgentEvidenceLiveInputHelpers(t *testing.T) {
	override := filepath.Join(t.TempDir(), "capabilities.yaml")
	require.NoError(t, os.WriteFile(override, []byte("lunaroute_models:\n  glm-5.3:\n    context_tokens: {tokens: 202752, evidence: fixture#context}\n    output_tokens: {tokens: 32768, evidence: fixture#output}\n"), 0600))
	t.Setenv("ARBITRON_LUNAROUTE_E2E_CAPABILITIES", override)
	rules := liveRules(t)
	model, ok := rules.LunaRouteModel("glm-5.3")
	require.True(t, ok)
	assert.Equal(t, 202752, model.ContextTokens.Tokens)
	assert.Equal(t, 32768, model.OutputTokens.Tokens)
	t.Setenv("ARBITRON_AGENT_MODEL_TEST", "glm-5.3")
	assert.Equal(t, "glm-5.3", liveModel(t, "ARBITRON_AGENT_MODEL_TEST"))
	assert.True(t, filepath.IsAbs(liveBinary(t, "sh")))
	bin := filepath.Join(t.TempDir(), "version")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'fixture-1\\n'\n"), 0700))
	assert.Equal(t, "fixture-1", versionOf(t, bin))
	binding := liveBinding("sk-foreign", "glm-5.3", "claude-code")
	assert.True(t, binding.Pinned && binding.BackendExplicit)
	assert.NotEmpty(t, binding.AllowedTools)
	_, err := binding.RevealKey(context.Background())
	require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
	assert.NotContains(t, err.Error(), "sk-foreign")
}

func TestAgentEvidenceRejectsPrecreatedProof(t *testing.T) {
	root, ws, in := agentEvidenceFixture(t)
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(ws, "lunaroute-proof.txt"), past, past))
	require.Error(t, publishAgentEvidence(root, ws, captureFixtureKey, "rejected.json", in), "a tool trace cannot bless a precreated proof")
}

func TestAgentEvidenceFreshWorkspace(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, requireEmptyAgentWorkspace(ws))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "lunaroute-proof.txt"), []byte("precreated"), 0600))
	require.Error(t, requireEmptyAgentWorkspace(ws))
	assert.False(t, strings.Contains(errAgentEvidence.Error(), "lr_"))
}
