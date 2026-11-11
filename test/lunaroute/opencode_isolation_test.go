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

// Protocol executables prove the experiment's composition and refusal boundary,
// not installed OpenCode isolation or completed live contract evidence.
func TestAgentEvidenceOpenCodeIsolationToSupportingObservation(t *testing.T) {
	for _, mode := range []string{"success", "changed-user", "changed-project", "changed-auth", "new-config", "removed-auth", "mode-change", "symlink", "missing-snapshot", "missing-launch-feed"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "opencode", "correlation-force-success")
			home := filepath.Join(t.TempDir(), "home")
			isolation, err := seedOpenCodeIsolation(home, root)
			require.NoError(t, err)
			t.Setenv("HOME", home)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			started := time.Now().UTC()
			journal := &openCodeControlJournal{isolation: isolation}
			client, _ := agentFactoryProbeClient(t)
			factory := liveObservedCompactingFactoryWithClient(root, bin, agentFactoryRules(t), client, capture, []byte(forcingSnapshot), journal)
			result, err := executeAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "opencode"), ws)
			require.NoError(t, err, "negative cases must first complete the real mission and all helper traffic")
			require.Len(t, capture.Records(), 4)
			userConfig := filepath.Join(home, ".config/opencode/opencode.json")
			auth := filepath.Join(home, ".local/share/opencode/auth.json")
			switch mode {
			case "changed-user":
				require.NoError(t, os.WriteFile(userConfig, []byte(`{"model":"changed/private"}`), 0600))
			case "changed-project":
				require.NoError(t, os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{}`), 0600))
			case "changed-auth":
				require.NoError(t, os.WriteFile(auth, []byte(captureFixtureKey), 0600))
			case "new-config":
				require.NoError(t, os.WriteFile(filepath.Join(home, ".config/opencode/opencode.jsonc"), []byte(`{}`), 0600))
			case "removed-auth":
				require.NoError(t, os.Remove(auth))
			case "mode-change":
				require.NoError(t, os.Chmod(auth, 0644))
			case "symlink":
				require.NoError(t, os.Remove(userConfig))
				require.NoError(t, os.Symlink(auth, userConfig))
			case "missing-snapshot":
				journal.isolation = nil
			case "missing-launch-feed":
				journal.entries[1].isolation = nil
			}
			obs, err := collectOpenCodeSupportingEvidence(ws, capture, journal, result, started, time.Now().UTC())
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "isolation-support.json", obs)
			}
			sink := filepath.Join(root, evidencePath, "observations/isolation-support.json")
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr), "changed or absent isolation input must prevent publication")
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var saved openCodeSupportingObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.True(t, saved.Isolation.ConfigUnchanged)
			assert.True(t, saved.Isolation.AuthUnchanged)
			assert.True(t, saved.Isolation.PrivateHome)
			assert.Equal(t, openCodeIsolationSource, saved.Isolation.Source)
			assert.Equal(t, []string{"main", "subagent", "title", "compaction"}, saved.RequiredCategories)
			assert.Equal(t, "glm-5.3", saved.Inference[3].ReportedModels[0])
			for _, private := range []string{home, root, bin, captureFixtureKey, openCodeForeignCredential, "auth.json", "foreign/project"} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}

func TestAgentEvidenceOpenCodeIsolationLaunchBoundary(t *testing.T) {
	for _, mode := range []string{"foreign-home", "changed-before-launch"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "opencode", "correlation-force-success")
			home := filepath.Join(t.TempDir(), "home")
			isolation, err := seedOpenCodeIsolation(home, root)
			require.NoError(t, err)
			t.Setenv("HOME", home)
			if mode == "foreign-home" {
				t.Setenv("HOME", t.TempDir())
			} else {
				require.NoError(t, os.Remove(filepath.Join(home, ".local/share/opencode/auth.json")))
			}
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			journal := &openCodeControlJournal{isolation: isolation}
			client, _ := agentFactoryProbeClient(t)
			factory := liveObservedCompactingFactoryWithClient(root, bin, agentFactoryRules(t), client, capture, []byte(forcingSnapshot), journal)
			result, err := executeAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "opencode"), ws)
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Empty(t, capture.Records())
			_, statErr := os.Stat(filepath.Join(root, "launch-marker"))
			require.True(t, os.IsNotExist(statErr))
			_, statErr = os.Stat(filepath.Join(ws, agentProofFile))
			require.True(t, os.IsNotExist(statErr))
			_, statErr = os.Stat(filepath.Join(root, "controls-marker"))
			require.True(t, os.IsNotExist(statErr), "unprotected roots must be refused before even debug config runs")
		})
	}
}

func TestAgentEvidenceOpenCodeInstalledIsolationSeeds(t *testing.T) {
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("installed schema unavailable; runtime verification remains pending")
	}
	root := t.TempDir()
	home, ws := filepath.Join(root, "home"), filepath.Join(root, "project")
	require.NoError(t, os.Mkdir(ws, 0700))
	isolation, err := seedOpenCodeIsolation(home, ws)
	require.NoError(t, err)
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "OPENCODE_DISABLE_MODELS_FETCH=true", "LUNAROUTE_API_KEY=" + captureFixtureKey}
	spec := claudecli.RunSpec{Bin: bin, Args: []string{"debug", "config", "--pure"}, Dir: ws, Env: env, MaxResponseBytes: openCodeControlsLimit}
	ctx, cancel := context.WithTimeout(context.Background(), openCodeControlsTimeout)
	defer cancel()
	lines, err := runCapturedLines(ctx, claudecli.NewExecRunner(), spec)
	require.NoError(t, err)
	inherited, err := decodeOpenCodeControls([]byte(strings.Join(lines, "\n")))
	require.NoError(t, err)
	// Without the overlay the project selectors and distinct user provider
	// definition must both load. A typo in a seeded path cannot prove isolation.
	assert.Equal(t, "foreign/project", inherited.Model)
	assert.Equal(t, "foreign/project-helper", inherited.SmallModel)
	assert.Equal(t, "@ai-sdk/anthropic", inherited.Providers["lunaroute"].NPM)
	assert.Equal(t, "https://foreign.example/v1", inherited.Providers["lunaroute"].Options.BaseURL)
	spec.Args = []string{"run", "not launched", "-m", "lunaroute/glm-5.3", "--pure"}
	spec.Env = append(env, "OPENCODE_CONFIG_CONTENT="+controlsOverlay)
	proof, err := queryOpenCodeEffectiveControls(ctx, claudecli.NewExecRunner(), spec)
	require.NoError(t, err)
	assert.Equal(t, "glm-5.3", proof.Model)
	require.NoError(t, isolation.validateLaunch(spec))
	unchanged, err := isolation.proof()
	require.NoError(t, err)
	assert.True(t, unchanged.ConfigUnchanged && unchanged.AuthUnchanged)
}

func TestAgentEvidenceOpenCodeIsolationEnvBinding(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := t.TempDir()
	isolation, err := seedOpenCodeIsolation(home, project)
	require.NoError(t, err)
	for _, env := range [][]string{
		nil,
		{"HOME=" + home, "HOME=" + home},
		{"HOME=" + home, "XDG_DATA_HOME=" + home},
		{"HOME=" + home, "XDG_CONFIG_HOME=" + home},
		{"HOME=" + home, "OPENCODE_AUTH_CONTENT={}"},
		{"HOME=" + home, "OPENCODE_CONFIG=/unprotected"},
		{"HOME=" + home, "OPENCODE_CONFIG_DIR=/unprotected"},
		{"HOME=" + home, "OPENCODE_TEST_HOME=/unprotected"},
	} {
		require.Error(t, isolation.validateLaunch(claudecli.RunSpec{Dir: project, Env: env}))
	}
	require.Error(t, isolation.validateLaunch(claudecli.RunSpec{Dir: t.TempDir(), Env: []string{"HOME=" + home}}))
	require.NoError(t, isolation.validateLaunch(claudecli.RunSpec{Dir: project, Env: []string{"HOME=" + home}}))
}

func TestAgentEvidenceOpenCodeIsolationSnapshotBounds(t *testing.T) {
	for _, mode := range []string{"oversized", "ancestor-symlink", "unreadable-tree"} {
		t.Run(mode, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			isolation, err := seedOpenCodeIsolation(home, t.TempDir())
			require.NoError(t, err)
			config := filepath.Join(home, ".config/opencode")
			switch mode {
			case "oversized":
				f, err := os.Create(filepath.Join(config, "large"))
				require.NoError(t, err)
				require.NoError(t, f.Truncate(backendTreeLimit+1))
				require.NoError(t, f.Close())
			case "ancestor-symlink":
				original := filepath.Join(home, ".config")
				require.NoError(t, os.Rename(original, original+"-moved"))
				require.NoError(t, os.Symlink(original+"-moved", original))
			case "unreadable-tree":
				require.NoError(t, os.Chmod(config, 0000))
				t.Cleanup(func() { require.NoError(t, os.Chmod(config, 0700)) })
			}
			_, err = isolation.proof()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), home)
		})
	}
}
