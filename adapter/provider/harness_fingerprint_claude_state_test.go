package provider_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func claudeAuthState(generation int) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"access-%d","refreshToken":"refresh-%d","expiresAt":%d,"refreshTokenExpiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max","rateLimitTier":"default","clientId":"cli"}}`, generation, generation, generation, generation)
}

func claudeGlobalState(account string, generation int) string {
	return fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":"org-a","emailAddress":"a@example.test","profileFetchedAt":%d},"numStartups":%d,"tipsHistory":{"tip":%d},"skillUsage":{"s":%d},"cachedGrowthBookFeaturesAt":%d,"cachedGrowthBookFeatures":{"flag":"original"},"mcpServers":{"server":{"command":"original"}},"env":{"MODEL":"original"},"projects":{"/work/other":{"allowedTools":["Read"],"mcpServers":{"p":{"command":"original"}},"lastCost":%d,"lastSessionId":"session-%d","lastModelUsage":{"m":{"costUSD":%d}},"lastGracefulShutdown":%t,"history":[%d]}}}`, account, generation, generation, generation, generation, generation, generation, generation, generation, generation%2 == 0, generation)
}

func TestHarnessFingerprintClaudeCountersAndEffectiveConfig(t *testing.T) {
	for _, relocated := range []bool{false, true} {
		t.Run(fmt.Sprint("relocated=", relocated), func(t *testing.T) {
			h := newHarnessHome(t)
			configDir, global := filepath.Join(h.home, ".claude"), filepath.Join(h.home, ".claude.json")
			override := ""
			if relocated {
				configDir = t.TempDir()
				override, global = configDir, filepath.Join(configDir, ".claude.json")
			}
			write := func(path, body string) { t.Helper(); require.NoError(t, os.WriteFile(path, []byte(body), 0600)) }
			auth := filepath.Join(configDir, ".credentials.json")
			write(auth, claudeAuthState(1))
			write(global, claudeGlobalState("account-a", 1))
			fp := adapterprovider.NewHarnessFileFingerprinter(adapterprovider.HarnessFilesConfig{
				Home: h.home, Binaries: map[string]string{workflow.BackendClaudeCode: filepath.Join(h.bin, "claude")},
				Getenv: func(k string) string {
					if k == "CLAUDE_CONFIG_DIR" {
						return override
					}
					return ""
				},
			})
			before, err := fp(workflow.BackendClaudeCode)
			require.NoError(t, err)
			write(global, claudeGlobalState("account-a", 2))
			after, err := fp(workflow.BackendClaudeCode)
			require.NoError(t, err)
			require.Equal(t, before, after, "global counters must not invalidate exact-model proof")
			if relocated {
				write(filepath.Join(h.home, ".claude.json"), `{"oauthAccount":{"accountUuid":"inactive"}}`)
				inactive, err := fp(workflow.BackendClaudeCode)
				require.NoError(t, err)
				require.Equal(t, after, inactive, "inactive default state is not the launch configuration")
			}
			for _, tc := range []struct{ name, path, body string }{
				{"opaque token replacement without metadata update", auth, claudeAuthState(3)},
				{"account", global, claudeGlobalState("account-b", 2)},
				{"organization", global, strings.Replace(claudeGlobalState("account-a", 2), "org-a", "org-b", 1)},
				{"MCP", global, strings.Replace(claudeGlobalState("account-a", 2), `"command":"original"`, `"command":"changed"`, 1)},
				{"env", global, strings.Replace(claudeGlobalState("account-a", 2), `"MODEL":"original"`, `"MODEL":"changed"`, 1)},
				{"unknown global", global, strings.Replace(claudeGlobalState("account-a", 2), `"numStartups":2`, `"futureRouting":"changed"`, 1)},
				{"plan", auth, strings.Replace(claudeAuthState(1), `"max"`, `"pro"`, 1)},
				{"scopes", auth, strings.Replace(claudeAuthState(1), "user:inference", "user:profile", 1)},
				{"tier", auth, strings.Replace(claudeAuthState(1), "default", "other", 1)},
				{"unknown auth", auth, strings.Replace(claudeAuthState(1), `"clientId":"cli"`, `"clientId":"cli","futureRouting":"changed"`, 1)},
				{"settings", filepath.Join(configDir, "settings.json"), `{"model":"other"}`},
				{"project MCP", global, strings.Replace(claudeGlobalState("account-a", 2), `"p":{"command":"original"}`, `"p":{"command":"changed"}`, 1)},
				{"project tools", global, strings.Replace(claudeGlobalState("account-a", 2), `"allowedTools":["Read"]`, `"allowedTools":["Bash"]`, 1)},
				{"unknown project key", global, strings.Replace(claudeGlobalState("account-a", 2), `"lastCost":2`, `"futureRouting":"changed"`, 1)},
				{"feature cache content", global, strings.Replace(claudeGlobalState("account-a", 2), `"flag":"original"`, `"flag":"changed"`, 1)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					old, readErr := os.ReadFile(tc.path)
					require.True(t, readErr == nil || os.IsNotExist(readErr))
					write(tc.path, tc.body)
					changed, err := fp(workflow.BackendClaudeCode)
					require.NoError(t, err)
					require.NotEqual(t, after, changed)
					if readErr == nil {
						write(tc.path, string(old))
					} else {
						require.NoError(t, os.Remove(tc.path))
					}
				})
			}
			// Without a stable account identity, opaque token changes remain load-bearing.
			write(global, `{}`)
			unidentified, err := fp(workflow.BackendClaudeCode)
			require.NoError(t, err)
			write(auth, claudeAuthState(3))
			changed, err := fp(workflow.BackendClaudeCode)
			require.NoError(t, err)
			require.NotEqual(t, unidentified, changed)
		})
	}
}

func TestHarnessFingerprintClaudePartialStateIsSecretFreeError(t *testing.T) {
	for _, rel := range []string{".claude/.credentials.json", ".claude.json"} {
		for _, body := range []string{"", "{", "null", "[]", `{"secret":"sentinel-secret"} {}`} {
			t.Run(rel+"/"+body, func(t *testing.T) {
				h := newHarnessHome(t)
				require.NoError(t, os.WriteFile(filepath.Join(h.home, rel), []byte(body), 0600))
				got, err := h.fingerprinter()(workflow.BackendClaudeCode)
				require.Error(t, err)
				require.Empty(t, got)
				require.NotContains(t, err.Error(), "sentinel-secret")
			})
		}
	}
}

// Claude 2.1.303 reads exactly one project entry, keyed by the launch
// directory's git root or the directory itself, and saveCurrentProjectConfig
// creates that entry from DEFAULT_PROJECT_CONFIG. Real runs in other workspaces
// therefore add and rewrite entries the probe never reads; only the probe's
// own key (or an ancestor that may be its git root) binds the revision.
func TestHarnessFingerprintClaudeBindsOnlyProbeProjectEntries(t *testing.T) {
	h := newHarnessHome(t)
	tmp := t.TempDir()
	probeDir := filepath.Join(tmp, "ws", ".model-probe")
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	require.NoError(t, err)
	resolvedProbeDir := filepath.Join(resolvedTmp, "ws", ".model-probe")
	global := filepath.Join(h.home, ".claude.json")
	write := func(body string) { t.Helper(); require.NoError(t, os.WriteFile(global, []byte(body), 0600)) }
	state := func(projects string) string {
		return `{"oauthAccount":{"accountUuid":"a"},"mcpServers":{},"projects":{` + projects + `}}`
	}
	entry := func(key, body string) string { return fmt.Sprintf("%q:%s", key, body) }
	const defaults = `{"allowedTools":[],"mcpContextUris":[],"mcpServers":{},"enabledMcpjsonServers":[],"disabledMcpjsonServers":[],"hasTrustDialogAccepted":false,"hasClaudeMdExternalIncludesApproved":false,"hasClaudeMdExternalIncludesWarningShown":false,"lastCost":1,"lastSessionId":"s"}`
	const custom = `{"allowedTools":["Read"],"mcpServers":{"p":{"command":"original"}}}`
	fp := adapterprovider.NewHarnessFileFingerprinter(adapterprovider.HarnessFilesConfig{
		Home: h.home, Binaries: map[string]string{workflow.BackendClaudeCode: filepath.Join(h.bin, "claude")},
		Getenv: func(string) string { return "" }, ProjectDir: probeDir,
	})
	base := state(entry("/work/other", custom) + "," + entry(probeDir, custom))
	write(base)
	before, err := fp(workflow.BackendClaudeCode)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		body    string
		changed bool
	}{
		{"run workspace entry created", state(entry("/work/other", custom) + "," + entry(probeDir, custom) + "," + entry("/work/run-2", defaults)), false},
		{"other workspace MCP edit", state(entry("/work/other", strings.Replace(custom, "original", "changed", 1)) + "," + entry(probeDir, custom)), false},
		{"other workspace removed", state(entry(probeDir, custom)), false},
		{"probe entry MCP edit", state(entry("/work/other", custom) + "," + entry(probeDir, strings.Replace(custom, "original", "changed", 1))), true},
		{"probe entry tools edit", state(entry("/work/other", custom) + "," + entry(probeDir, strings.Replace(custom, "Read", "Bash", 1))), true},
		{"probe entry unknown key", state(entry("/work/other", custom) + "," + entry(probeDir, `{"allowedTools":["Read"],"mcpServers":{"p":{"command":"original"}},"futureRouting":"x"}`)), true},
		{"git root ancestor entry", state(entry("/work/other", custom) + "," + entry(probeDir, custom) + "," + entry(filepath.Dir(filepath.Dir(probeDir)), custom)), true},
		{"probe entry removed", state(entry("/work/other", custom)), true},
		// The probe launches in its own private <probeDir>/check-* child, so
		// that child's entry is the one Claude actually reads (G63).
		{"launch cwd entry MCP edit", state(entry("/work/other", custom) + "," + entry(probeDir, custom) + "," + entry(filepath.Join(probeDir, "check-1"), custom)), true},
		{"resolved launch cwd entry MCP edit", state(entry("/work/other", custom) + "," + entry(probeDir, custom) + "," + entry(filepath.Join(resolvedProbeDir, "check-1"), custom)), true},
		{"launch cwd default entry", state(entry("/work/other", custom) + "," + entry(probeDir, custom) + "," + entry(filepath.Join(probeDir, "check-1"), defaults)), false},
		{"sibling sharing the probe dir prefix", state(entry("/work/other", custom) + "," + entry(probeDir, custom) + "," + entry(probeDir+"-other", custom)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.body)
			got, err := fp(workflow.BackendClaudeCode)
			require.NoError(t, err)
			require.Equal(t, tc.changed, string(before) != string(got))
			write(base)
		})
	}

	// The first probe creates the probe's own entry from defaults (plus its
	// last-session metrics); Claude treats that exactly like a missing entry.
	write(state(entry("/work/other", custom)))
	absent, err := fp(workflow.BackendClaudeCode)
	require.NoError(t, err)
	write(state(entry("/work/other", custom) + "," + entry(probeDir, defaults)))
	created, err := fp(workflow.BackendClaudeCode)
	require.NoError(t, err)
	require.Equal(t, absent, created, "the probe's own default entry must not churn the revision")
	write(state(entry("/work/other", custom) + "," + entry(probeDir, strings.Replace(defaults, `"hasTrustDialogAccepted":false`, `"hasTrustDialogAccepted":true`, 1))))
	trusted, err := fp(workflow.BackendClaudeCode)
	require.NoError(t, err)
	require.NotEqual(t, absent, trusted, "a non-default value in the probe entry stays load-bearing")
}
