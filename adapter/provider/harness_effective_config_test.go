package provider_test

import (
	"os"
	"path/filepath"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are the global files consumed with the launchers' controlled BaseEnv:
// CODEX_HOME and CLAUDE_CONFIG_DIR are forwarded; XDG_CONFIG_HOME and
// PI_CODING_AGENT_DIR are not. Generated per-binding config is already in the
// inventory binding, and session/rollout output is not static configuration.
func TestHarnessEffectiveConfigInvalidatesProof(t *testing.T) {
	for _, tc := range []struct{ backend, file string }{
		{workflow.BackendCodex, ".codex/config.toml"},
		{workflow.BackendClaudeCode, ".claude/settings.json"},
		{workflow.BackendClaudeCode, ".claude/plugins/installed_plugins.json"},
		{workflow.BackendClaudeCode, ".claude.json"},
		{workflow.BackendPi, ".pi/agent/models.json"},
		{workflow.BackendPi, ".pi/agent/settings.json"},
		{workflow.BackendPi, ".pi/agent/mcp.json"},
		{workflow.BackendOpencode, ".config/opencode/config.json"},
		{workflow.BackendOpencode, ".config/opencode/opencode.json"},
		{workflow.BackendOpencode, ".config/opencode/opencode.jsonc"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			h := newHarnessHome(t)
			fp := h.fingerprinter()
			absent, err := fp(tc.backend)
			require.NoError(t, err)
			path := filepath.Join(h.home, tc.file)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			// Global Claude state is structured JSON; use the same valid input
			// shape for every file while retaining the same-size/mtime edit check.
			require.NoError(t, os.WriteFile(path, []byte(`{"config":"config-one-secret"}`), 0o600))
			before, err := fp(tc.backend)
			require.NoError(t, err)
			assert.NotEqual(t, absent, before, "adding optional config invalidates prior proof")
			st, err := os.Stat(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte(`{"config":"config-two-secret"}`), 0o600))
			require.NoError(t, os.Chtimes(path, st.ModTime(), st.ModTime()))
			after, err := fp(tc.backend)
			require.NoError(t, err)
			assert.NotEqual(t, before, after, "same size and mtime cannot hide a config edit")
			assert.NotContains(t, string(after), "config-two-secret")
			restart, err := h.fingerprinter()(tc.backend)
			require.NoError(t, err)
			assert.Equal(t, after, restart)
			output := filepath.Join(h.home, ".codex", "sessions", "rollout.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(output), 0o700))
			require.NoError(t, os.WriteFile(output, []byte("per-turn output"), 0o600))
			unchanged, err := fp(tc.backend)
			require.NoError(t, err)
			assert.Equal(t, after, unchanged, "session growth is not configuration")
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o700)) // unreadable as a config file, even as root
			incomplete, err := fp(tc.backend)
			assert.Error(t, err)
			assert.Empty(t, incomplete)
		})
	}
}

func TestHarnessEffectiveConfigUsesLaunchHome(t *testing.T) {
	for _, tc := range []struct{ backend, binary, env, file string }{
		{workflow.BackendCodex, "codex", "CODEX_HOME", "config.toml"},
		{workflow.BackendClaudeCode, "claude", "CLAUDE_CONFIG_DIR", "settings.json"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			h := newHarnessHome(t)
			launchHome := t.TempDir()
			fp := adapterprovider.NewHarnessFileFingerprinter(adapterprovider.HarnessFilesConfig{
				Home: h.home, Binaries: map[string]string{tc.backend: filepath.Join(h.bin, tc.binary)},
				Getenv: func(k string) string {
					if k == tc.env {
						return launchHome
					}
					return ""
				},
			})
			before, err := fp(tc.backend)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(launchHome, tc.file), []byte("effective config"), 0o600))
			after, err := fp(tc.backend)
			require.NoError(t, err)
			assert.NotEqual(t, before, after)
			require.NoError(t, os.WriteFile(filepath.Join(h.home, "."+tc.binary, tc.file), []byte("ignored config"), 0o600))
			unchanged, err := fp(tc.backend)
			require.NoError(t, err)
			assert.Equal(t, after, unchanged, "do not fingerprint an unused default home")
		})
	}
}
