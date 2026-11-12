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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These protocol processes prove the collector's wiring, not installed Claude
// precedence, keychain isolation, effective limits or helper routing.
func TestAgentEvidenceClaudeIsolationFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "user", "project", "auth", "new-project-config", "mode", "symlink", "missing-feed", "missing-capture", "missing-file", "stale-capture", "wrong-backend", "wrong-session"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", "success")
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			home := filepath.Join(root, "home")
			isolation, err := seedClaudeIsolation(home, ws)
			require.NoError(t, err)
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			// Mutate only after the actual process has made authenticated inference
			// and created the workspace proofs. The version query must stay untouched.
			mutation := map[string]string{
				"user":               `printf changed > "$HOME/.claude/settings.json"`,
				"project":            `printf changed > .claude/settings.json`,
				"auth":               `printf changed > "$HOME/.claude/.credentials.json"`,
				"new-project-config": `printf '{}' > .claude/new.json`,
				"mode":               `chmod 644 "$HOME/.claude/.credentials.json"`,
				"symlink":            `rm .claude/settings.json; ln -s "$HOME/.claude/.credentials.json" .claude/settings.json`,
			}[mode]
			if mutation != "" {
				wrapper := filepath.Join(t.TempDir(), "mutating-claude")
				require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\n'"+bin+"' \"$@\"\nstatus=$?\nif [ \"${1-}\" != --version ]; then "+mutation+"; fi\nexit $status\n"), 0700))
				bin = wrapper
			}
			client, calls := agentFactoryProbeClient(t)
			factory := liveClaudeIsolationFactoryWithClient(root, bin, agentFactoryRules(t), client, isolation, capture, "")
			started := time.Now().UTC()
			result, err := executePreparedAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "claude-code"), ws)
			if mutation != "" {
				require.Error(t, err)
				assert.Nil(t, result)
				assert.Zero(t, *calls, "local isolation refusal must not invoke diagnosis")
			} else {
				require.NoError(t, err)
			}
			records := capture.Records()
			require.Len(t, records, 1)
			assert.Equal(t, "glm-5.3", records[0].RequestedModel)
			assert.Equal(t, []string{"glm-5.3"}, records[0].ReportedModels)
			assert.True(t, records[0].AuthMatchesSelected && records[0].Completed)
			proof, readErr := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, readErr, "negative must reach real command-created files")
			assert.Equal(t, agentProofText, string(proof))
			switch mode {
			case "missing-feed":
				isolation.session = ""
			case "missing-capture":
				records = nil
			case "missing-file":
				require.NoError(t, os.Remove(filepath.Join(ws, agentCommandFile)))
			case "stale-capture":
				records[0].StartedAt = started.Add(-time.Hour)
				records[0].FinishedAt = started.Add(-time.Minute)
			case "wrong-backend":
				records[0].Backend = "codex"
				records[0].Path = "/v1/responses"
			case "wrong-session":
				result.SessionID = "ses_other"
			}
			obs, err := collectClaudeIsolationEvidence(isolation, records, result, started, time.Now().UTC())
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "claude-isolation.json", obs)
			}
			sink := filepath.Join(root, evidencePath, "observations/claude-isolation.json")
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.True(t, saved.ConfigUnchanged && saved.AuthUnchanged)
			assert.Equal(t, "ses_factory", saved.Session)
			assert.Equal(t, claudeStartupVersion, saved.Version)
			assert.Equal(t, claudeIsolationSource, saved.Source)
			require.Len(t, saved.Inference, 1)
			assert.Equal(t, []string{"glm-5.3"}, saved.Inference[0].ReportedModels)
			for _, private := range []string{root, bin, home, captureFixtureKey, "foreign/project", claudeForeignCredential} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}

func TestAgentEvidenceClaudeIsolationLaunchBoundary(t *testing.T) {
	for _, mode := range []string{"foreign-home", "relocated-config", "changed-before-launch", "missing-snapshot", "missing-memory-source", "missing-memory-capture", "late-memory-registration", "wrong-memory-backend"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", "success")
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			switch mode {
			case "foreign-home":
				t.Setenv("HOME", t.TempDir())
			case "relocated-config":
				t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			case "changed-before-launch":
				require.NoError(t, os.Remove(filepath.Join(isolation.home, ".claude/.credentials.json")))
			case "missing-snapshot":
				isolation.before = nil
			case "missing-memory-source":
				isolation.memory = ""
			case "late-memory-registration":
				capture.requestCount = 1
			case "wrong-memory-backend":
				capture.backend = "codex"
			}
			client, calls := agentFactoryProbeClient(t)
			memoryCapture := capture
			if mode == "missing-memory-capture" {
				memoryCapture = nil
			}
			factory := liveClaudeIsolationFactoryWithClient(root, bin, agentFactoryRules(t), client, isolation, memoryCapture, "")
			result, err := executePreparedAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "claude-code"), ws)
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Zero(t, *calls)
			assert.Empty(t, capture.Records())
			for _, marker := range []string{"launch-marker", "version-marker", "mission/" + agentProofFile} {
				_, err := os.Stat(filepath.Join(root, marker))
				require.True(t, os.IsNotExist(err), "must refuse before version/mission")
			}
		})
	}
}

func TestAgentEvidenceClaudeIsolationSnapshotBounds(t *testing.T) {
	for _, mode := range []string{"oversized", "unreadable", "home-mode", "ancestor-link"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			ws := filepath.Join(root, "workspace")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			switch mode {
			case "oversized":
				require.NoError(t, os.WriteFile(filepath.Join(ws, ".claude/settings.json"), []byte(strings.Repeat("x", backendTreeLimit+1)), 0600))
			case "unreadable":
				require.NoError(t, os.Chmod(filepath.Join(ws, ".claude/settings.json"), 0000))
			case "home-mode":
				require.NoError(t, os.Chmod(isolation.home, 0755))
			case "ancestor-link":
				require.NoError(t, os.Rename(filepath.Join(isolation.home, ".claude"), filepath.Join(isolation.home, "moved")))
				require.NoError(t, os.Symlink("moved", filepath.Join(isolation.home, ".claude")))
			}
			require.Error(t, isolation.unchanged())
		})
	}
}

func TestAgentEvidenceClaudeIsolationEntryPrerequisites(t *testing.T) {
	bin, err := os.Executable()
	require.NoError(t, err)
	for _, release := range []string{"0", "1"} {
		ctx, cancel := context.WithTimeout(context.Background(), agentVersionTimeout)
		cmd := exec.CommandContext(ctx, bin, "-test.run=^TestLiveClaudeCodeIsolation$", "-test.v")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "ARBITRON_LUNAROUTE_E2E_KEY=") && !strings.HasPrefix(entry, "ARBITRON_LUNAROUTE_RELEASE_GATE=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "ARBITRON_LUNAROUTE_RELEASE_GATE="+release)
		out, runErr := cmd.CombinedOutput()
		contextErr := ctx.Err()
		cancel()
		require.NoError(t, contextErr)
		if release == "0" {
			require.NoError(t, runErr)
			assert.Contains(t, string(out), "--- SKIP: TestLiveClaudeCodeIsolation")
		} else {
			require.Error(t, runErr)
			assert.Contains(t, string(out), "--- FAIL: TestLiveClaudeCodeIsolation")
		}
		assert.Contains(t, string(out), "release acceptance remains pending")
	}
}

func TestAgentEvidenceClaudeIsolationSameLaunchInputs(t *testing.T) {
	spec, _ := claudeStartupFixture(t)
	root := t.TempDir()
	isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), spec.Dir)
	require.NoError(t, err)
	spec.Env = append(spec.Env, "HOME="+isolation.home, "CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3", "ANTHROPIC_DEFAULT_HAIKU_MODEL=glm-5.3")
	require.NoError(t, isolation.validateLaunch(spec))
	for _, name := range []string{"HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		changed := spec
		changed.Env = append(append([]string(nil), spec.Env...), name+"=foreign")
		require.Error(t, isolation.validateLaunch(changed))
	}
	called := false
	runner := claudeIsolationRunner{isolation: isolation, capture: &inferenceCapture{backend: "claude-code"}, next: controlsRunnerFunc(func(_ context.Context, actual claudecli.RunSpec) (<-chan string, func() error, error) {
		called = true
		assert.Equal(t, spec.Dir, actual.Dir)
		assert.Equal(t, spec.Bin, actual.Bin)
		assert.True(t, strings.Join(spec.Env, "\x00") == strings.Join(actual.Env, "\x00"), "do not reconstruct or alter emitted env")
		return nil, nil, errAgentEvidence
	})}
	_, _, err = runner.Run(context.Background(), spec)
	require.Error(t, err)
	assert.True(t, called)
}
