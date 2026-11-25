package sources

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/codex"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexBin = "/opt/codex/bin/codex"

func loadedRules(t *testing.T) provider.CapabilityRules {
	t.Helper()
	r, err := configadapter.LoadCapabilityRules("")
	require.NoError(t, err)
	return r
}

func writeCodexCache(t *testing.T, home, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(body), 0o600))
}

func codexRunner(version string) *fakeRunner {
	return &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{version}}, "login status": {lines: []string{"Logged in using ChatGPT"}}}}
}

func nativeInput(t *testing.T, r *fakeRunner) DiscoverInput {
	return DiscoverInput{Native: NativeExecutionInput{
		Codex: codex.Config{BinaryPath: codexBin, Runner: r}, Rules: loadedRules(t), WorkspaceEnabled: true}}
}

func codexFingerprint(t *testing.T) string {
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "codex"})
	require.NoError(t, err)
	return fp
}

func TestDiscover_CodexSourceFromVersionAndAccountModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeCodexCache(t, home, `{"models":[{"slug":"gpt-6.1-sol"},{"slug":"gpt-5-mini"}]}`)
	r := codexRunner("codex-cli 0.171.0")

	specs, harness := Discover(context.Background(), nativeInput(t, r))
	require.Len(t, specs, 1)
	s := specs[0].Source
	assert.Equal(t, runner.CredOpenAISubscription, s.Kind)
	assert.Equal(t, []modelidentity.Family{"openai"}, s.Families)
	assert.Equal(t, []string{"codex"}, s.Backends)
	assert.Equal(t, []string{"gpt-6.1-sol", "gpt-5-mini"}, s.Models, "exact local account models")
	assert.Empty(t, s.UnavailableReason)
	assert.Equal(t, []runner.ExecutionCapability{{
		Op: runner.OpAgent, Backend: "codex", Provider: "claude-cli", EndpointFingerprint: codexFingerprint(t),
		Families: []modelidentity.Family{"openai"}, Models: []string{"gpt-6.1-sol", "gpt-5-mini"},
		ContextWindows: []provider.ContextWindow{"default"}, Efforts: []provider.Effort{"default", "low", "medium", "high", "xhigh"},
		ToolPolicy: runner.ToolPolicyUnrestricted, ExecutedModel: true, // codex cannot restrict tools: no Tools, empty scope only
	}}, s.Capabilities)
	assert.Equal(t, "0.171.0", harness["codex"])
	_, isCodex := specs[0].Prober.(*runnerprobe.CodexProber)
	assert.True(t, isCodex, "the runner's own codex login + rateLimits RPC")
	require.NotEmpty(t, r.specs)
	assert.Equal(t, codexBin, r.specs[0].Bin, "version evidence uses the configured binary")
	assert.Equal(t, []string{"--version"}, r.specs[0].Args, "discovery inspects the version only; login is the prober's job")
}

func TestDiscover_CodexUnavailableReasonsAreVisible(t *testing.T) {
	for _, tc := range []struct {
		name, version, cache string
		workspace            bool
		want                 string
	}{
		{"incompatible version is retained", "codex-cli 0.100.0", `{"models":[{"slug":"gpt-6.1-sol"}]}`, true, "below"},
		{"empty model cache", "codex-cli 0.171.0", `{"models":[]}`, true, ReasonCodexModelInventory},
		{"malformed model cache", "codex-cli 0.171.0", `{"models":`, true, ReasonCodexModelInventory},
		{"no workspace execution", "codex-cli 0.171.0", `{"models":[{"slug":"gpt-6.1-sol"}]}`, false, ReasonCodexWorkspaceDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			writeCodexCache(t, home, tc.cache)
			in := nativeInput(t, codexRunner(tc.version))
			in.Native.WorkspaceEnabled = tc.workspace
			specs, _ := Discover(context.Background(), in)
			require.Len(t, specs, 1, "an installed codex stays visible")
			assert.Contains(t, specs[0].Source.UnavailableReason, tc.want)
			assert.Empty(t, specs[0].Source.Capabilities, "no capability without proof")
			assert.Empty(t, specs[0].Source.Models, "no unrestricted empty allowlist either")
		})
	}
	t.Run("missing binary advertises nothing", func(t *testing.T) {
		in := nativeInput(t, &fakeRunner{answers: map[string]cmdAnswer{}})
		specs, harness := Discover(context.Background(), in)
		assert.Empty(t, specs)
		assert.NotContains(t, harness, "codex")
	})
	t.Run("no native input keeps the phase 2 inventory", func(t *testing.T) {
		specs, _ := Discover(context.Background(), DiscoverInput{})
		assert.Empty(t, specs)
	})
}

func TestNativeAgent_CodexChatNeverAdvertised(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeCodexCache(t, home, `{"models":[{"slug":"gpt-6.1-sol"}]}`)
	in := nativeInput(t, codexRunner("codex-cli 0.171.0"))
	in.Native.SessionsEnabled, in.Native.CodexInteractive = true, true
	specs, _ := Discover(context.Background(), in)
	require.Len(t, specs, 1)
	for _, c := range specs[0].Source.Capabilities {
		assert.NotEqual(t, runner.OpChat, c.Op, "no codex chat route")
		assert.Equal(t, "codex", c.Backend, "no backend or family substitution")
		assert.Equal(t, []modelidentity.Family{"openai"}, c.Families)
	}
	var ops []runner.OpType
	for _, c := range specs[0].Source.Capabilities {
		ops = append(ops, c.Op)
	}
	assert.ElementsMatch(t, []runner.OpType{runner.OpAgent, runner.OpStream}, ops, "stream only because sessions AND codex interactive are enabled")

	in.Native.CodexInteractive = false
	specs, _ = Discover(context.Background(), in)
	for _, c := range specs[0].Source.Capabilities {
		assert.NotEqual(t, runner.OpStream, c.Op, "codex interactive is opt-in")
	}
}

func TestCodexAgentCapabilities_SplitsLargeModelListsWithoutTruncating(t *testing.T) {
	var models []string
	for i := 0; i < 70; i++ {
		models = append(models, fmt.Sprintf("gpt-x-%02d", i))
	}
	caps, err := CodexAgentCapabilities(loadedRules(t), models, false, false)
	require.NoError(t, err)
	var all []string
	for _, c := range caps {
		assert.LessOrEqual(t, len(c.Models), MaxCapabilityModels)
		all = append(all, c.Models...)
	}
	assert.Equal(t, models, all, "every model, in order, none dropped")

	_, err = CodexAgentCapabilities(provider.CapabilityRules{}, models, false, false)
	require.Error(t, err, "no rules, no capability")
}

func TestClaudeAgentCapabilities_MirrorProvenChatModelsWithAgentAxes(t *testing.T) {
	chat := []runner.ExecutionCapability{{Op: runner.OpChat, Provider: "claude-cli", EndpointFingerprint: strings.Repeat("a", 64),
		Families: []modelidentity.Family{"anthropic"}, Models: []string{"claude-opus-5-5", "sonnet-4.6"}, Tools: []string{"Read", "Grep"}, ExecutedModel: true}}
	caps, err := ClaudeAgentCapabilities(loadedRules(t), chat, false, false)
	require.NoError(t, err)
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "claude-code"})
	require.NoError(t, err)
	require.Len(t, caps, 2, "sonnet-4.6 has its own effort override, so it forms its own axes group")
	for _, c := range caps {
		assert.Equal(t, runner.OpAgent, c.Op)
		assert.Equal(t, "claude-code", c.Backend)
		assert.Equal(t, fp, c.EndpointFingerprint, "the agent route's own fingerprint, not the chat route's")
		assert.Equal(t, []string{"Read", "Grep"}, c.Tools, "tool evidence comes from BuildCapabilities for the same harness")
		assert.Equal(t, runner.ToolPolicyAllowlist, c.ToolPolicy, "claude-code enforces an explicit allowlist")
		assert.True(t, c.ExecutedModel)
	}
	assert.Equal(t, []string{"claude-opus-5-5"}, caps[0].Models)
	assert.Contains(t, caps[0].Efforts, provider.Effort("xhigh"))
	assert.Equal(t, []string{"sonnet-4.6"}, caps[1].Models)
	assert.NotContains(t, caps[1].Efforts, provider.Effort("xhigh"), "never advertise an axis the rules would floor")

	noTools := []runner.ExecutionCapability{chat[0]}
	noTools[0].Tools = nil
	_, err = ClaudeAgentCapabilities(loadedRules(t), noTools, false, false)
	require.Error(t, err, "an allowlist harness with no proven tools is never advertised as all tools")
}

func TestDiscover_ClaudeSourceAddsAgentOnlyWithWorkspaceJobs(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700))
	claudeRun := &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{"2.1.400 (Claude Code)"}}}}
	missingCodex := &fakeRunner{answers: map[string]cmdAnswer{}}
	in := DiscoverInput{ClaudeBinary: bin, Runner: claudeRun, Rules: loadedRules(t), // P2 T9 Cycle 7 Step 6: BuildCapabilities' rules
		ClaudeModels: []string{"claude-opus-5-5"}, // decision D1: natively verified, server-requested models (P2 T9 Step 9.4d)
		Native:       NativeExecutionInput{Codex: codex.Config{BinaryPath: codexBin, Runner: missingCodex}, Rules: loadedRules(t), WorkspaceEnabled: true}}
	// Chat capabilities come from P2 T9 Step 9.4d's claudeChatCapabilities over the verified ClaudeModels.

	specs, _ := Discover(context.Background(), in)
	require.Len(t, specs, 1, "one Claude source, not one per operation")
	s := specs[0].Source
	assert.Equal(t, []string{"", "claude-code"}, s.Backends)
	var chat, agent int
	for _, c := range s.Capabilities {
		switch c.Op {
		case runner.OpChat:
			chat++
		case runner.OpAgent:
			agent++
			assert.Equal(t, "claude-code", c.Backend)
		}
	}
	assert.Positive(t, chat, "chat support is kept")
	assert.Positive(t, agent, "agent support is added")

	in.Native.WorkspaceEnabled = false
	specs, _ = Discover(context.Background(), in)
	assert.Equal(t, []string{""}, specs[0].Source.Backends)
	for _, c := range specs[0].Source.Capabilities {
		assert.NotEqual(t, runner.OpAgent, c.Op, "no agent support without workspace jobs")
	}
}

func TestNativeInventory_RefreshRereadsAccountModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeCodexCache(t, home, `{"models":[]}`)
	in := nativeInput(t, codexRunner("codex-cli 0.171.0"))
	inv := NewRefreshing(context.Background(), func(ctx context.Context) ([]Spec, map[string]string) { return Discover(ctx, in) }, time.Now)

	first := inv.Sources(context.Background())
	require.Len(t, first, 1)
	assert.Equal(t, ReasonCodexModelInventory, first[0].UnavailableReason)
	rev := inv.Revision()

	writeCodexCache(t, home, `{"models":[{"slug":"gpt-6.1-sol"}]}`) // native codex refreshed its cache
	after := inv.Probe(context.Background())
	require.Len(t, after, 1)
	assert.Empty(t, after[0].UnavailableReason, "eligibility restored without a restart")
	assert.Equal(t, []string{"gpt-6.1-sol"}, after[0].Models)
	assert.Greater(t, inv.Revision(), rev)
	assert.NotEqual(t, runner.AuthState("unknown"), after[0].Health.Auth, "the refreshed source was probed in the same cycle")

	rev = inv.Revision()
	inv.Probe(context.Background())
	assert.Equal(t, rev, inv.Revision(), "an unchanged rediscovery never forces a reconnect")
}

func TestNativeInventory_VerifiedClaudeModelsReachAgentCapsWithoutReconnect(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700))
	claudeRun := &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{"2.1.400 (Claude Code)"}}}}
	conf := &scriptedConfirm{out: map[string]runner.ConfirmationResult{"claude-opus-5-5": okResult("claude-opus-5-5")}}
	v := NewClaudeModelVerifier(conf, t.TempDir(), nil)
	base := DiscoverInput{ClaudeBinary: bin, Runner: claudeRun, Rules: loadedRules(t),
		Native: NativeExecutionInput{Rules: loadedRules(t), WorkspaceEnabled: true}}
	inv := NewRefreshing(context.Background(), VerifiedDiscover(base, v), nowFixed)
	inv.EnableClaudeVerification(v, base)
	rev := inv.Revision()
	require.Empty(t, inv.Sources(context.Background())[0].Capabilities)

	<-inv.VerifyModels(context.Background(), []string{"claude-opus-5-5"})
	var chat, agent []string
	for _, c := range inv.Sources(context.Background())[0].Capabilities {
		switch c.Op {
		case runner.OpChat:
			chat = append(chat, c.Models...)
		case runner.OpAgent:
			agent = append(agent, c.Models...)
		}
	}
	assert.Equal(t, []string{"claude-opus-5-5"}, chat)
	assert.Equal(t, []string{"claude-opus-5-5"}, agent, "agent caps mirror the verified chat models")
	assert.Equal(t, rev, inv.Revision(), "a verification pass is carried by the next heartbeat, never by a reconnect")

	inv.Probe(context.Background())
	assert.NotEmpty(t, inv.Sources(context.Background())[0].Capabilities, "rediscovery keeps the verified models")
	assert.Equal(t, rev, inv.Revision())
}
