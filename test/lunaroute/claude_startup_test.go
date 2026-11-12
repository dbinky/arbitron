package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claudeStartupFixture(t *testing.T) (claudecli.RunSpec, []string) {
	t.Helper()
	ws, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	spec := claudecli.RunSpec{Bin: "fixture", Dir: ws, Stdin: agentMissionPrompt, MaxResponseBytes: captureBodyLimit,
		Args: []string{"--print", "--verbose", "--output-format", "stream-json", "--model", "glm-5.3", "--setting-sources", ""},
		Env:  []string{"ANTHROPIC_BASE_URL=" + hostedRoot, "ANTHROPIC_AUTH_TOKEN=" + captureFixtureKey, "ANTHROPIC_MODEL=glm-5.3"}}
	init, err := json.Marshal(map[string]any{"type": "system", "subtype": "init", "model": "glm-5.3", "claude_code_version": "2.1.322", "cwd": ws, "session_id": "ses_factory"})
	require.NoError(t, err)
	return spec, []string{string(init), `{"type":"user","message":{"content":[{"type":"tool_result"}]}}`, `{"type":"result","is_error":false,"session_id":"ses_factory","modelUsage":{"glm-5.3":{}},"result":"done"}`}
}

func TestAgentEvidenceClaudeStartupValidation(t *testing.T) {
	spec, lines := claudeStartupFixture(t)
	require.NoError(t, validateClaudeStartup(spec, "2.1.322", lines))
	for _, mode := range []string{"absent", "duplicate", "late", "model", "version", "cwd", "session", "empty-session", "secret", "malformed", "duplicate-json", "missing-result", "duplicate-result", "failed-result", "missing-error-flag", "post-result", "revoked-model"} {
		t.Run(mode, func(t *testing.T) {
			copy := append([]string(nil), lines...)
			switch mode {
			case "absent":
				copy = copy[1:]
			case "duplicate":
				copy = append([]string{copy[0]}, copy...)
			case "late":
				copy[0], copy[1] = copy[1], copy[0]
			case "model":
				copy[0] = strings.ReplaceAll(copy[0], "glm-5.3", "glm-5.3-flash")
			case "version":
				copy[0] = strings.ReplaceAll(copy[0], "2.1.322", "2.1.321")
			case "cwd":
				copy[0] = strings.ReplaceAll(copy[0], spec.Dir, "/unrelated")
			case "session":
				copy[0] = strings.ReplaceAll(copy[0], "ses_factory", "ses_other")
			case "empty-session":
				copy[0] = strings.ReplaceAll(copy[0], "ses_factory", "")
			case "secret":
				copy[0] = strings.ReplaceAll(copy[0], "ses_factory", captureFixtureKey)
			case "malformed":
				copy[1] = "{"
			case "duplicate-json":
				copy[0] = strings.Replace(copy[0], `"model":`, `"model":"glm-5.3-flash","model":`, 1)
			case "missing-result":
				copy = copy[:2]
			case "duplicate-result":
				copy = append(copy, copy[2])
			case "failed-result":
				copy[2] = strings.Replace(copy[2], `"is_error":false`, `"is_error":true`, 1)
			case "missing-error-flag":
				copy[2] = strings.Replace(copy[2], `"is_error":false,`, "", 1)
			case "post-result":
				copy = append(copy, copy[1])
			case "revoked-model":
				copy[0] = strings.ReplaceAll(copy[0], "glm-5.3", "claude-sonnet-5-lr1")
			}
			err := validateClaudeStartup(spec, "2.1.322", copy)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
			assert.NotContains(t, err.Error(), spec.Dir)
		})
	}
}

func TestAgentEvidenceClaudeStartupLaunchBoundary(t *testing.T) {
	for _, mode := range []string{"missing-env", "duplicate-key", "foreign-key", "foreign-base", "model-drift", "missing-isolation", "duplicate-isolation", "settings-override", "missing-model", "empty-cwd", "unbounded-output"} {
		t.Run(mode, func(t *testing.T) {
			spec, lines := claudeStartupFixture(t)
			switch mode {
			case "missing-env":
				spec.Env = nil
			case "duplicate-key":
				spec.Env = append(spec.Env, "ANTHROPIC_AUTH_TOKEN="+captureFixtureKey)
			case "foreign-key":
				spec.Env[1] = "ANTHROPIC_AUTH_TOKEN=sk-foreign"
			case "foreign-base":
				spec.Env[0] = "ANTHROPIC_BASE_URL=https://foreign.example"
			case "model-drift":
				spec.Env[2] = "ANTHROPIC_MODEL=glm-5.3-flash"
			case "missing-isolation":
				spec.Args = spec.Args[:6]
			case "duplicate-isolation":
				spec.Args = append(spec.Args, "--setting-sources", "")
			case "settings-override":
				spec.Args = append(spec.Args, "--settings", `{"model":"foreign"}`)
			case "missing-model":
				spec.Args = append(spec.Args[:4], spec.Args[6:]...)
			case "empty-cwd":
				spec.Dir = ""
			case "unbounded-output":
				spec.MaxResponseBytes = 0
			}
			require.Error(t, validateClaudeStartup(spec, "2.1.322", lines))
		})
	}
}

func TestAgentEvidenceClaudeStartupMissingIsolationCannotBypass(t *testing.T) {
	spec, _ := claudeStartupFixture(t)
	spec.Args = spec.Args[:6]
	called := false
	runner := claudeStartupRunner{next: controlsRunnerFunc(func(context.Context, claudecli.RunSpec) (<-chan string, func() error, error) {
		called = true
		return nil, nil, errAgentEvidence
	})}
	_, _, err := runner.Run(context.Background(), spec)
	require.Error(t, err)
	assert.False(t, called, "a missing isolation control must refuse before query or mission")
}

func TestAgentEvidenceClaudeStartupSameLaunchQuery(t *testing.T) {
	spec, lines := claudeStartupFixture(t)
	calls := 0
	runner := claudeStartupRunner{next: controlsRunnerFunc(func(ctx context.Context, actual claudecli.RunSpec) (<-chan string, func() error, error) {
		calls++
		assert.Equal(t, spec.Bin, actual.Bin)
		assert.True(t, reflect.DeepEqual(spec.Env, actual.Env), "query must preserve the emitted env")
		assert.Equal(t, spec.Dir, actual.Dir)
		_, bounded := ctx.Deadline()
		assert.True(t, bounded)
		output := lines
		if calls == 1 {
			assert.Equal(t, []string{"--version"}, actual.Args)
			assert.Empty(t, actual.Stdin)
			assert.EqualValues(t, agentVersionLimit, actual.MaxResponseBytes)
			output = []string{"2.1.322 (Claude Code)"}
		} else {
			assert.True(t, reflect.DeepEqual(spec, actual), "mission launch must remain unchanged")
		}
		out := make(chan string, len(output))
		for _, line := range output {
			out <- line
		}
		close(out)
		return out, func() error { return nil }, nil
	})}
	actual, err := runCapturedLines(context.Background(), runner, spec)
	require.NoError(t, err)
	assert.Equal(t, lines, actual)
	assert.Equal(t, 2, calls)
}

func TestAgentEvidenceClaudeStartupFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "startup-absent", "startup-model", "startup-version", "startup-cwd", "startup-session", "startup-duplicate", "startup-late", "startup-malformed", "startup-version-query"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", mode)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			client, calls := agentFactoryProbeClient(t)
			factory := liveFactoryWithClient(root, bin, agentFactoryRules(t), client)
			result, err := executeAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "claude-code"), ws)
			if mode != "success" {
				require.Error(t, err, "bad startup must refuse even with completed files and inference")
				assert.Nil(t, result)
				assert.NotContains(t, err.Error(), captureFixtureKey)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "ses_factory", result.SessionID)
				proof, err := os.ReadFile(filepath.Join(ws, agentProofFile))
				require.NoError(t, err)
				assert.Equal(t, agentProofText, string(proof))
				command, err := os.ReadFile(filepath.Join(ws, agentCommandFile))
				require.NoError(t, err)
				assert.Equal(t, "12", strings.TrimSpace(string(command)))
				require.Len(t, result.Steps, 1)
				assert.Equal(t, "tool_call", result.Steps[0].Kind)
			}
			assert.Zero(t, *calls, "local collector failures must not diagnose through outbound probes")
			versionMarker, markerErr := os.ReadFile(filepath.Join(root, "version-marker"))
			require.NoError(t, markerErr)
			assert.Equal(t, "queried", string(versionMarker))
			if mode == "startup-version-query" {
				assert.Empty(t, capture.Records())
				_, err := os.Stat(filepath.Join(root, "launch-marker"))
				require.True(t, os.IsNotExist(err))
				return
			}
			require.Len(t, capture.Records(), 1)
			assert.True(t, capture.Records()[0].Completed)
			assert.Equal(t, []string{"glm-5.3"}, capture.Records()[0].ReportedModels)
			// Negative cases must really reach the command-created sink, not merely fail
			// at preflight or because the fixture did not implement the protocol.
			proof, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(proof))
		})
	}
}
