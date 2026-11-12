package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentEvidenceClaudeMemoryWire(t *testing.T) {
	needle := "Contents of /private/mission/CLAUDE.md (project instructions, checked into the codebase):\n\nPrivate memory nonce 1234567890abcdef."
	for _, mode := range []string{"string", "text-block", "reminder", "preamble", "other-file", "missing", "assistant", "tool-result", "system", "wrong-path", "wrong-content", "suffix-drift", "quoted-frame", "nonce-only", "duplicate", "malformed", "duplicate-json", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			content := any(needle)
			role := "user"
			switch mode {
			case "text-block":
				content = []any{map[string]string{"type": "text", "text": needle}}
			case "reminder":
				content = "<system-reminder>\n" + needle + "\n</system-reminder>"
			case "preamble":
				content = "Codebase and user instructions are shown below.\n\n" + needle
			case "other-file":
				content = needle + "\n\nContents of /other/CLAUDE.md (project instructions, checked into the codebase):\n\nOther instructions."
			case "missing":
				content = agentMissionPrompt
			case "assistant":
				role = "assistant"
			case "tool-result":
				content = []any{map[string]string{"type": "tool_result", "content": needle}}
			case "system":
				role = "system"
			case "wrong-path":
				content = strings.Replace(needle, "/private/mission/", "/other/", 1)
			case "wrong-content":
				content = strings.Replace(needle, "1234567890abcdef", "fedcba0987654321", 1)
			case "quoted-frame":
				content = "Please echo " + needle
			case "suffix-drift":
				content = needle + "-substituted"
			case "nonce-only":
				content = "Private memory nonce 1234567890abcdef."
			case "duplicate":
				content = needle + "\n\n" + needle
			}
			raw, err := json.Marshal(map[string]any{"model": "glm-5.3", "messages": []any{map[string]any{"role": role, "content": content}}})
			require.NoError(t, err)
			switch mode {
			case "malformed":
				raw = []byte(`{"messages":[`)
			case "duplicate-json":
				raw = []byte(`{"messages":[],` + string(raw[1:]))
			case "oversized":
				raw = append(raw, []byte(strings.Repeat(" ", captureBodyLimit))...)
			}
			want := mode == "string" || mode == "text-block" || mode == "reminder" || mode == "preamble" || mode == "other-file"
			assert.Equal(t, want, claudeMemoryInRequest(raw, needle))
			assert.False(t, claudeMemoryInRequest(raw, ""), "an absent memory source cannot match")
		})
	}
}

// Actual Factory -> runner -> CONNECT/TLS -> workspace -> private JSON sink.
// These protocol processes validate collection, not the installed CLI contract.
func TestAgentEvidenceClaudeMemoryFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "memory-absent", "memory-drift", "memory-assistant", "memory-late", "missing-capture-feed", "changed-memory", "missing-memory", "symlink-memory"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", mode)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			memory, err := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
			require.NoError(t, err)
			require.NotEmpty(t, memory)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			client, calls := agentFactoryProbeClient(t)
			factory := liveClaudeIsolationFactoryWithClient(root, bin, agentFactoryRules(t), client, isolation, capture, "")
			started := time.Now().UTC()
			result, err := executePreparedAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "claude-code"), ws)
			require.NoError(t, err)
			records := capture.Records()
			require.NotEmpty(t, records)
			actualFile, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(actualFile), "negative must reach real inference and workspace sinks")
			assert.Zero(t, *calls)
			for _, record := range records {
				assert.True(t, record.Completed && record.AuthMatchesSelected)
				assert.Equal(t, []string{"glm-5.3"}, record.ReportedModels)
			}
			switch mode {
			case "missing-capture-feed":
				records[0].ClaudeProjectMemory = false
			case "changed-memory":
				require.NoError(t, os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("changed"), 0600))
			case "missing-memory":
				require.NoError(t, os.Remove(filepath.Join(ws, "CLAUDE.md")))
			case "symlink-memory":
				require.NoError(t, os.Rename(filepath.Join(ws, "CLAUDE.md"), filepath.Join(ws, "moved-memory")))
				require.NoError(t, os.Symlink("moved-memory", filepath.Join(ws, "CLAUDE.md")))
			}
			obs, err := collectClaudeIsolationEvidence(isolation, records, result, started, time.Now().UTC())
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "claude-memory.json", obs)
			}
			path := filepath.Join(root, evidencePath, "observations/claude-memory.json")
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
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.True(t, saved.ProjectMemory.Loaded)
			assert.Equal(t, claudeMemorySource, saved.ProjectMemory.Source)
			assert.Equal(t, []string{records[0].RequestID}, saved.ProjectMemory.RequestIDs)
			assert.True(t, saved.Inference[0].ClaudeProjectMemory)
			assert.Equal(t, "ses_factory", saved.Session)
			for _, private := range []string{root, bin, captureFixtureKey, strings.TrimSpace(string(memory)), agentMissionPrompt, claudeForeignCredential} {
				assert.NotContains(t, string(raw), private)
			}
			require.NoError(t, capture.Close())
			assert.Empty(t, capture.claudeMemory, "private comparison material must be discarded on close")
		})
	}
}
