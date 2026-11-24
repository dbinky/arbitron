package config

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testKey = "ark_c2VjcmV0LXJ1bm5lci1rZXktdmFsdWU"

func writeConfig(t *testing.T, yaml, env string, envMode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, EnvFileName), []byte(env), 0o600))
	require.NoError(t, os.Chmod(filepath.Join(dir, EnvFileName), envMode))
	return p
}

func TestLoadCodexBinaryOverride(t *testing.T) {
	p := writeConfig(t, "workspace_root: /srv/ws\ncodex_binary: \"  /opt/codex/bin/codex  \"\n", baseEnv(), 0o600)
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "/opt/codex/bin/codex", cfg.CodexBinary)
	p = writeConfig(t, "workspace_root: /srv/ws\n", baseEnv(), 0o600)
	cfg, err = Load(p)
	require.NoError(t, err)
	assert.Empty(t, cfg.CodexBinary)
}

func baseEnv() string {
	return "# secrets\nARBITRON_SERVER_URL=https://arbitron.example.com/\nARBITRON_RUNNER_KEY=" + testKey + "\n"
}

func TestLoadValidConfig(t *testing.T) {
	p := writeConfig(t, `
workspace_root: "/srv/arbitron/workspaces"
scratch_root: "/Users/op/.arbitron-runner/scratch"
spool_dir: "/Users/op/.arbitron-runner/spool"
heartbeat_seconds: 7
probe_interval_seconds: 120
output_buffer_frames: 8192
claude_binary: "/opt/homebrew/bin/claude"
lunaroute:
  connection_ref: "lr-main"
  families: ["openai", "deepseek"]
  models: ["gpt-6.1"]
`, baseEnv()+"LUNAROUTE_API_KEY=lr_abc123\nARBITRON_EXTRA_HEADERS=CF-Access-Client-Id: id1;CF-Access-Client-Secret: s3cr3t\n", 0o600)

	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(p), cfg.ConfigDir)
	assert.Equal(t, "https://arbitron.example.com", cfg.ServerURL, "trailing slash trimmed")
	assert.Equal(t, testKey, cfg.RunnerKey)
	assert.Equal(t, "/srv/arbitron/workspaces", cfg.WorkspaceRoot)
	assert.Equal(t, "/Users/op/.arbitron-runner/scratch", cfg.ScratchRoot)
	assert.Equal(t, "/Users/op/.arbitron-runner/spool", cfg.SpoolDir)
	assert.Equal(t, 7*time.Second, cfg.Heartbeat)
	assert.Equal(t, 120*time.Second, cfg.ProbeInterval)
	assert.Equal(t, 8192, cfg.OutputBufferFrames)
	assert.Equal(t, "/opt/homebrew/bin/claude", cfg.ClaudeBinary)
	assert.Equal(t, "id1", cfg.ExtraHeaders.Get("CF-Access-Client-Id"))
	assert.Equal(t, "s3cr3t", cfg.ExtraHeaders.Get("CF-Access-Client-Secret"))
	assert.Equal(t, LunaRoute{ConnectionRef: "lr-main", Families: []modelidentity.Family{"openai", "deepseek"}, Models: []string{"gpt-6.1"}, APIKey: "lr_abc123"}, cfg.LunaRoute)
	assert.True(t, cfg.LunaRoute.Enabled())
	assert.Equal(t, filepath.Join(filepath.Dir(p), MachineKeyFileName), cfg.MachineKeyPath())
}

func TestLoadDefaults(t *testing.T) {
	p := writeConfig(t, "workspace_root: /srv/ws\n", baseEnv(), 0o600)
	cfg, err := Load(p)
	require.NoError(t, err)
	dir := filepath.Dir(p)
	assert.Equal(t, filepath.Join(dir, "scratch"), cfg.ScratchRoot)
	assert.Equal(t, filepath.Join(dir, "spool"), cfg.SpoolDir)
	assert.Equal(t, 5*time.Second, cfg.Heartbeat)
	assert.Equal(t, 60*time.Second, cfg.ProbeInterval)
	assert.Equal(t, 4096, cfg.OutputBufferFrames)
	assert.False(t, cfg.LunaRoute.Enabled())
	assert.Empty(t, cfg.ExtraHeaders)
}

func TestLoadInstallerSkeleton(t *testing.T) {
	// The exact skeleton P2 T11 install.sh writes.
	p := writeConfig(t, `# Arbitron runner configuration. Secrets live in runner.env (mode 0600).
workspace_root: "/Users/op/.arbitron-runner/workspaces"
scratch_root: "/Users/op/.arbitron-runner/scratch"
spool_dir: "/Users/op/.arbitron-runner/spool"
heartbeat_seconds: 5
probe_interval_seconds: 60
output_buffer_frames: 4096
claude_binary: ""
lunaroute:
  connection_ref: ""
  families: []
`, baseEnv()+"# LUNAROUTE_API_KEY=lr_...\n# ARBITRON_EXTRA_HEADERS=CF-Access-Client-Id: <id>;CF-Access-Client-Secret: <secret>\n", 0o600)
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.False(t, cfg.LunaRoute.Enabled())
	assert.Equal(t, "", cfg.ClaudeBinary)
}

func TestLoadRefusesGroupOrWorldReadableEnvFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660, 0o644} {
		p := writeConfig(t, "workspace_root: /srv/ws\n", baseEnv(), mode)
		_, err := Load(p)
		require.Error(t, err, "mode %o", mode)
		assert.Contains(t, err.Error(), EnvFileName)
		assert.Contains(t, err.Error(), "0600")
		assert.NotContains(t, err.Error(), testKey, "errors never echo the key")
	}
}

func TestLoadRequiresServerURLAndRunnerKey(t *testing.T) {
	cases := map[string]string{
		"missing url":       "ARBITRON_RUNNER_KEY=" + testKey + "\n",
		"missing key":       "ARBITRON_SERVER_URL=https://a.example.com\n",
		"wrong key shape":   "ARBITRON_SERVER_URL=https://a.example.com\nARBITRON_RUNNER_KEY=sk-not-a-runner-key\n",
		"plain http":        "ARBITRON_SERVER_URL=http://a.example.com\nARBITRON_RUNNER_KEY=" + testKey + "\n",
		"url with query":    "ARBITRON_SERVER_URL=https://a.example.com/?key=" + testKey + "\nARBITRON_RUNNER_KEY=" + testKey + "\n",
		"url with userinfo": "ARBITRON_SERVER_URL=https://u:p@a.example.com\nARBITRON_RUNNER_KEY=" + testKey + "\n",
		"not key=value":     "ARBITRON_SERVER_URL https://a.example.com\n",
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "workspace_root: /srv/ws\n", env, 0o600))
			require.Error(t, err)
			assert.NotContains(t, err.Error(), testKey)
			assert.NotContains(t, err.Error(), "sk-not-a-runner-key")
		})
	}
}

func TestLoadAllowsLoopbackHTTP(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:9000"} {
		cfg, err := Load(writeConfig(t, "workspace_root: /srv/ws\n", "ARBITRON_SERVER_URL="+u+"\nARBITRON_RUNNER_KEY="+testKey+"\n", 0o600))
		require.NoError(t, err, u)
		assert.Equal(t, u, cfg.ServerURL)
	}
}

func TestLoadRejectsBadYAMLValues(t *testing.T) {
	cases := map[string]string{
		"missing workspace root": "scratch_root: /tmp/s\n",
		"relative workspace":     "workspace_root: ws\n",
		"relative scratch":       "workspace_root: /srv/ws\nscratch_root: scratch\n",
		"unknown key":            "workspace_root: /srv/ws\nworkspace_rot: /x\n",
		"heartbeat out of range": "workspace_root: /srv/ws\nheartbeat_seconds: 0\n",
		"probe too frequent":     "workspace_root: /srv/ws\nprobe_interval_seconds: 1\n",
		"buffer too small":       "workspace_root: /srv/ws\noutput_buffer_frames: 3\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, yaml, baseEnv(), 0o600))
			require.Error(t, err)
		})
	}
}

func TestLoadLunaRouteValidation(t *testing.T) {
	lr := "workspace_root: /srv/ws\nlunaroute:\n  connection_ref: lr-main\n  families: [openai]\n"
	_, err := Load(writeConfig(t, lr, baseEnv(), 0o600))
	require.Error(t, err, "configured LunaRoute without a key")
	assert.Contains(t, err.Error(), "LUNAROUTE_API_KEY")

	_, err = Load(writeConfig(t, lr, baseEnv()+"LUNAROUTE_API_KEY=sk-foreign-key\n", 0o600))
	require.Error(t, err, "foreign key shape")
	assert.NotContains(t, err.Error(), "sk-foreign-key")

	_, err = Load(writeConfig(t, "workspace_root: /srv/ws\nlunaroute:\n  connection_ref: lr-main\n  families: [unresolved]\n", baseEnv()+"LUNAROUTE_API_KEY=lr_x\n", 0o600))
	require.Error(t, err, "unresolved family can never be advertised")

	_, err = Load(writeConfig(t, "workspace_root: /srv/ws\nlunaroute:\n  families: [openai]\n", baseEnv()+"LUNAROUTE_API_KEY=lr_x\n", 0o600))
	require.Error(t, err, "families without connection_ref")
}

func TestParseExtraHeaders(t *testing.T) {
	h, err := ParseExtraHeaders(" X-One: 1 ; x-two: two words ;")
	require.NoError(t, err)
	assert.Equal(t, http.Header{"X-One": {"1"}, "X-Two": {"two words"}}, h)

	h, err = ParseExtraHeaders("")
	require.NoError(t, err)
	assert.Empty(t, h)

	for _, bad := range []string{"Authorization: Bearer x", "host: evil", "Upgrade: h2c", "novalue", "Bad Name: v", ": v", "X-A:"} {
		_, err := ParseExtraHeaders(bad)
		assert.Error(t, err, bad)
		if err != nil {
			assert.False(t, strings.Contains(err.Error(), "Bearer x"), "header values never echoed")
		}
	}
}
