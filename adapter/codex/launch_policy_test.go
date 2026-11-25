package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

func TestCheckToolScope_SharesTheLaunchPolicy(t *testing.T) {
	require.NoError(t, CheckToolScope(nil), "empty scope is codex's default posture")
	require.NoError(t, CheckToolScope([]string{"Bash", "Read"}))
	for _, bad := range [][]string{{"Read"}, {"Bash", "Bash(gh:*)"}, {"Bash", "mcp__github__create_pr"}} {
		err := CheckToolScope(bad)
		require.ErrorIs(t, err, ErrToolScopeUnsupported, "%v", bad)
		_, launchErr := codexToolPolicy(bad)
		assert.Equal(t, launchErr.Error(), err.Error(), "one predicate for placement and launch")
	}
}

func TestCodexToolPolicy_EmptyScopeIsUnrestricted(t *testing.T) {
	p, err := codexToolPolicy(nil)
	require.NoError(t, err)
	assert.Equal(t, toolPolicy{}, p)
}

func TestCodexToolPolicy_FullCoderScopeKeepsWritesAndDisablesUnlistedSurfaces(t *testing.T) {
	p, err := codexToolPolicy([]string{"Read", "Write", "Edit", "Grep", "Glob", "Bash"})
	require.NoError(t, err)
	assert.True(t, p.Restricted)
	assert.False(t, p.ReadOnly, "a granted write tool keeps the write posture")
	assert.Equal(t, []string{"-c", "features.plugins=false", "-c", "features.apps=false", "-c", `web_search="disabled"`}, p.Overrides)
}

func TestCodexToolPolicy_NoWriteToolIsReadOnlyAndWebSearchKept(t *testing.T) {
	p, err := codexToolPolicy([]string{"Read", "Grep", "Glob", "Bash", "WebSearch", "WebFetch"})
	require.NoError(t, err)
	assert.True(t, p.ReadOnly)
	assert.NotContains(t, p.Overrides, `web_search="disabled"`)
}

func TestCodexToolPolicy_UnenforceableScopesRefuse(t *testing.T) {
	for name, scope := range map[string][]string{
		"no shell, reads":  {"Read", "Grep", "Glob"},
		"no shell, writes": {"Write", "Edit"},
		"mcp tool":         {"Bash", "Read", "mcp__github__create_issue"},
		"unknown tool":     {"Bash", "Teleport"},
		// Pattern-scoped Bash (Claude permission syntax) has no codex equivalent:
		// codex cannot narrow its shell to gh/git, so the scope is refused (user
		// decision 2026-11-23), not widened to plain Bash.
		"pattern-scoped bash": {"Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := codexToolPolicy(scope)
			require.ErrorIs(t, err, ErrToolScopeUnsupported)
		})
	}
}

// The refusal names the offending scope entry, so an operator can fix the profile.
func TestCodexToolPolicy_RefusalNamesTheOffendingTool(t *testing.T) {
	for quoted, scope := range map[string][]string{
		`"Bash(gh:*)"`:                {"Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"},
		`"mcp__github__create_issue"`: {"Bash", "Read", "mcp__github__create_issue"},
		`"Teleport"`:                  {"Bash", "Teleport"},
	} {
		_, err := codexToolPolicy(scope)
		require.ErrorIs(t, err, ErrToolScopeUnsupported)
		assert.Contains(t, err.Error(), quoted, "the message quotes the first unsupported entry")
	}
	_, err := codexToolPolicy([]string{"Read", "Write"})
	require.ErrorIs(t, err, ErrToolScopeUnsupported)
	assert.Contains(t, err.Error(), `"Bash"`, "a missing shell names the tool the profile must add")
}

func TestCodexContextArgs(t *testing.T) {
	rules := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"codex": {Backend: "codex", ContextTiers: []provider.ContextWindow{provider.Context200K}},
	})
	b := codexBinding("gpt-5-codex", nil)
	b.Backend = "codex"

	got, err := codexContextArgs(b, rules)
	require.NoError(t, err)
	assert.Nil(t, got, "an empty tier emits nothing")

	b.ContextWindow = provider.ContextDefault
	got, err = codexContextArgs(b, rules)
	require.NoError(t, err)
	assert.Nil(t, got)

	b.ContextWindow = provider.Context200K
	got, err = codexContextArgs(b, rules)
	require.NoError(t, err)
	assert.Equal(t, []string{"-c", "model_context_window=200000"}, got)

	b.ContextWindow = provider.Context1M
	_, err = codexContextArgs(b, rules)
	require.Error(t, err, "a tier the rules do not allow refuses the launch instead of being dropped")

	got, err = codexContextArgs(b, provider.CapabilityRules{})
	require.NoError(t, err)
	assert.Equal(t, []string{"-c", "model_context_window=1000000"}, got, "an Empty ruleset passes the tier through verbatim")

	b.ContextWindow = provider.ContextWindow("512k")
	_, err = codexContextArgs(b, provider.CapabilityRules{})
	require.ErrorIs(t, err, ErrContextWindowUnsupported)
}
