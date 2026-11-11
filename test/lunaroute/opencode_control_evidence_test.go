package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is a protocol executable, not installed-backend contract evidence. Both
// warm-up and continuation must feed the actual same-env debug proofs to disk.
func TestAgentEvidenceOpenCodeControlsToSupportingObservation(t *testing.T) {
	for _, mode := range []string{"success", "absent", "missing-resume", "wrong-session", "foreign-workspace", "limit-drift", "stale", "extra-launch", "no-compaction", "missing-file"} {
		t.Run(mode, func(t *testing.T) {
			started := time.Now().UTC()
			root, bin, capture := agentFactoryFixture(t, "opencode", "correlation-force-success")
			home := filepath.Join(t.TempDir(), "home")
			isolation, err := seedOpenCodeIsolation(home, root)
			require.NoError(t, err)
			t.Setenv("HOME", home)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			journal := &openCodeControlJournal{isolation: isolation}
			client, _ := agentFactoryProbeClient(t)
			factory := liveObservedCompactingFactoryWithClient(root, bin, agentFactoryRules(t), client, capture, []byte(forcingSnapshot), journal)
			result, err := executeAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "opencode"), ws)
			require.NoError(t, err)
			if mode != "success" {
				require.Len(t, journal.checks(), 2, "negative setup must retain both real runner checks")
			}
			switch mode {
			case "absent":
				journal = &openCodeControlJournal{}
			case "missing-resume":
				journal.entries = journal.entries[:1]
			case "wrong-session":
				journal.entries[1].SessionID = "ses_foreign"
			case "foreign-workspace":
				journal.entries[1].dir = t.TempDir()
			case "limit-drift":
				journal.entries[1].Controls.ContextTokens++
			case "stale":
				journal.entries[0].StartedAt = started.Add(-time.Second)
			case "extra-launch":
				journal.entries = append(journal.entries, journal.entries[1])
			case "no-compaction":
				capture.mu.Lock()
				capture.records = capture.records[:len(capture.records)-1]
				capture.mu.Unlock()
			case "missing-file":
				require.NoError(t, os.Remove(filepath.Join(ws, agentProofFile)))
			}
			obs, err := collectOpenCodeSupportingEvidence(ws, capture, journal, result, started, time.Now().UTC())
			sink := filepath.Join(root, evidencePath, "observations/controls-support.json")
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "controls-support.json", obs)
			}
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr), "invalid input must not reach the artifact sink")
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var saved openCodeSupportingObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, "run-one", saved.RunID)
			assert.Equal(t, "ses_factory", saved.SessionID)
			assert.True(t, saved.FileOK && saved.CommandOK)
			assert.True(t, saved.Isolation.ConfigUnchanged && saved.Isolation.AuthUnchanged && saved.Isolation.PrivateHome)
			require.Len(t, saved.Controls, 2)
			assert.Empty(t, saved.Controls[0].SessionID)
			assert.Equal(t, saved.SessionID, saved.Controls[1].SessionID)
			assert.Equal(t, 202752, saved.Controls[0].Controls.ContextTokens)
			assert.Equal(t, 32768, saved.Controls[1].Controls.OutputTokens)
			require.Len(t, saved.Inference, 4)
			assert.Equal(t, "compaction", saved.Inference[3].Category)
			assert.Equal(t, []string{"glm-5.3"}, saved.Inference[3].ReportedModels)
			assert.Equal(t, openCodeControlsSource, saved.ControlsSource)
			assert.Equal(t, saved.Model, saved.Controls[0].Controls.Model)
			assert.Equal(t, saved.Model, saved.Controls[1].Controls.Model)
			for _, private := range []string{captureFixtureKey, root, bin, agentMissionPromptFor("opencode"), "Private compaction summary", "OPENCODE_CONFIG_CONTENT", "apiKey"} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}
