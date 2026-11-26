package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/runneragent/executor"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticSources []runneragent.Source

func (s staticSources) Sources(context.Context) []runneragent.Source { return s }
func (s staticSources) Probe(context.Context) []runneragent.Source   { return s }
func claudeAgentSource(t *testing.T) runneragent.Source {
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "claude-code"})
	require.NoError(t, err)
	return runneragent.Source{Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Backends: []string{"", "claude-code"},
		Capabilities: []runner.ExecutionCapability{{Op: runner.OpAgent, Backend: "claude-code", Provider: "claude-cli", EndpointFingerprint: fp,
			Families: []modelidentity.Family{"anthropic"}, Models: []string{"claude-opus-5-5"},
			ContextWindows: []provider.ContextWindow{"default", "200k", "1m"}, Efforts: []provider.Effort{"default", "low", "medium", "high", "xhigh", "max"},
			Tools: []string{"Read", "Edit", "Grep"}, ToolPolicy: runner.ToolPolicyAllowlist, ExecutedModel: true}}, Health: runner.SourceHealth{Auth: "ok"}}
}
func codexAgentSource(t *testing.T) runneragent.Source {
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "codex"})
	require.NoError(t, err)
	return runneragent.Source{Kind: runner.CredOpenAISubscription, Families: []modelidentity.Family{"openai"}, Backends: []string{"codex"},
		Capabilities: []runner.ExecutionCapability{{Op: runner.OpAgent, Backend: "codex", Provider: "claude-cli", EndpointFingerprint: fp,
			Families: []modelidentity.Family{"openai"}, Models: []string{"gpt-6.1-sol"}, ContextWindows: []provider.ContextWindow{"default"},
			Efforts: []provider.Effort{"default", "low", "medium", "high", "xhigh"}, ToolPolicy: runner.ToolPolicyUnrestricted, ExecutedModel: true}}, Health: runner.SourceHealth{Auth: "ok"}}
}
func TestNativeAgent_CodexUnrestrictedAcceptsOnlyTheEmptyScope(t *testing.T) {
	job := &runneragent.AgentJob{Provider: "claude-cli", Model: "gpt-6.1-sol", Prompt: "p", CredentialKind: string(runner.CredOpenAISubscription), Backend: "codex", RequireExecutedModel: true}
	_, c, err := executor.MatchNativeAgent([]runneragent.Source{codexAgentSource(t)}, job, rules(t))
	require.NoError(t, err)
	assert.Equal(t, runner.ToolPolicyUnrestricted, c.ToolPolicy)
	job.ToolScope = []string{"Read"}
	_, _, err = executor.MatchNativeAgent([]runneragent.Source{codexAgentSource(t)}, job, rules(t))
	require.ErrorIs(t, err, executor.ErrCapabilityUnavailable, "a restrictive scope is never accepted by codex")
	allow := codexAgentSource(t)
	allow.Capabilities[0].ToolPolicy = ""
	job.ToolScope = nil
	_, _, err = executor.MatchNativeAgent([]runneragent.Source{allow}, job, rules(t))
	require.ErrorIs(t, err, executor.ErrCapabilityUnavailable, "an agent capability without a tool posture is never accepted")
}
func goodClaudeJob() *runneragent.AgentJob {
	return &runneragent.AgentJob{Provider: "claude-cli", Model: "claude-opus-5-5", Prompt: "edit", ToolScope: []string{"Edit"}, CredentialKind: string(runner.CredAnthropicSubscription), Backend: "claude-code", ContextWindow: "200k", Effort: "high", RequireExecutedModel: true}
}
func rules(t *testing.T) provider.CapabilityRules {
	r, err := configadapter.LoadCapabilityRules("")
	require.NoError(t, err)
	return r
}
func TestNativeAgent_UnsupportedModelAxisOrToolsNeverAccepts(t *testing.T) {
	src := claudeAgentSource(t)
	for _, tc := range []struct {
		name string
		edit func(*runneragent.AgentJob)
	}{
		{"model not proven", func(a *runneragent.AgentJob) { a.Model = "claude-mystery-9" }},
		{"other provider route", func(a *runneragent.AgentJob) { a.Provider = "anthropic-api" }},
		{"other backend", func(a *runneragent.AgentJob) { a.Backend = "opencode" }},
		{"unadvertised context", func(a *runneragent.AgentJob) { a.ContextWindow = "2m" }},
		{"unadvertised effort", func(a *runneragent.AgentJob) { a.Effort = "ultra" }},
		{"unadvertised tool", func(a *runneragent.AgentJob) { a.ToolScope = []string{"Edit", "Bash"} }},
		{"unresolved empty claude scope", func(a *runneragent.AgentJob) { a.ToolScope = nil }},
		{"codex kind on a claude source", func(a *runneragent.AgentJob) {
			a.CredentialKind = string(runner.CredOpenAISubscription)
			a.Backend = "codex"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := goodClaudeJob()
			tc.edit(a)
			_, _, err := executor.MatchNativeAgent([]runneragent.Source{src}, a, rules(t))
			require.ErrorIs(t, err, executor.ErrCapabilityUnavailable)
		})
	}
	t.Run("unavailable or logged-out source", func(t *testing.T) {
		for _, edit := range []func(*runneragent.Source){func(s *runneragent.Source) { s.UnavailableReason = "harness_unsupported" }, func(s *runneragent.Source) { s.Health.Auth = "logged_out" }, func(s *runneragent.Source) { s.Capabilities = nil }} {
			s := claudeAgentSource(t)
			edit(&s)
			_, _, err := executor.MatchNativeAgent([]runneragent.Source{s}, goodClaudeJob(), rules(t))
			require.ErrorIs(t, err, executor.ErrCapabilityUnavailable)
		}
	})
	got, c, err := executor.MatchNativeAgent([]runneragent.Source{src}, goodClaudeJob(), rules(t))
	require.NoError(t, err)
	assert.Equal(t, runner.CredAnthropicSubscription, got.Kind)
	assert.Equal(t, "claude-cli", c.Provider)
	h := &countingHarness{}
	f := newNativeFixture(t, h, staticSources{src})
	ws := f.plan(t)
	bad := goodClaudeJob()
	bad.ToolScope = []string{"Bash"}
	bad.WorkDir = ws.WorkDir
	ok, reason := f.jobs.Supports(runneragent.Dispatch{Op: runneragent.OpAgent, Workspace: ws, Agent: bad})
	assert.False(t, ok)
	assert.True(t, strings.HasPrefix(reason, "capability_unavailable"), reason)
	res := f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpAgent, Workspace: ws, Agent: bad})
	assert.False(t, res.OK)
	assert.Zero(t, h.count(), "SINK: no native launch for an unsupported request")
}
func TestVerifyExecutedModel(t *testing.T) {
	ev := func(m string, k engine.EvidenceKind) engine.ModelEvidence {
		return engine.ModelEvidence{Model: m, Source: "s", Kind: k}
	}
	require.NoError(t, executor.VerifyExecutedModel("claude-opus-5-5", &engine.AgentResult{ModelEvidence: []engine.ModelEvidence{ev("claude-opus-5-5[1m]", engine.EvidenceReportedExecution), ev("claude-haiku-4-5", engine.EvidenceReportedExecution)}}))
	for name, r := range map[string]*engine.AgentResult{
		"label only":     {Model: "claude-opus-5-5"},
		"wrong model":    {Model: "claude-opus-5-5", ModelEvidence: []engine.ModelEvidence{ev("claude-sonnet-4-5", engine.EvidenceReportedExecution)}},
		"routing intent": {ModelEvidence: []engine.ModelEvidence{ev("claude-opus-5-5", engine.EvidenceRoutingIntent)}},
		"unknown kind":   {ModelEvidence: []engine.ModelEvidence{ev("claude-opus-5-5", "")}}, "nil result": nil,
	} {
		assert.ErrorIs(t, executor.VerifyExecutedModel("claude-opus-5-5", r), executor.ErrExecutedModelUnproven, name)
	}
}

type countingHarness struct {
	mu    sync.Mutex
	n     int
	usage string
}

func (h *countingHarness) count() int { h.mu.Lock(); defer h.mu.Unlock(); return h.n }
func (h *countingHarness) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	h.mu.Lock()
	h.n++
	usage := h.usage
	h.mu.Unlock()
	if usage == "" {
		usage = `{"claude-opus-5-5":{}}`
	}
	if err := os.WriteFile(filepath.Join(spec.Dir, "agent-out.md"), []byte("edited"), 0o644); err != nil {
		return nil, nil, err
	}
	ch := make(chan string, 1)
	ch <- `{"type":"result","is_error":false,"result":"done","session_id":"s1","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":` + usage + `}`
	close(ch)
	return ch, func() error { return nil }, nil
}
func TestWorkspaceJobs_WrongExecutedModelFailsBeforePush(t *testing.T) {
	h := &countingHarness{usage: `{"claude-haiku-4-5":{}}`}
	f := newNativeFixture(t, h, staticSources{claudeAgentSource(t)})
	ws := f.plan(t)
	before, err := f.srv.LatestManifest(context.Background())
	require.NoError(t, err)
	a := goodClaudeJob()
	a.WorkDir = ws.WorkDir
	res := f.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpAgent, Workspace: ws, Agent: a})
	assert.False(t, res.OK, "a wrong executed model never completes the step")
	assert.Equal(t, 1, h.count())
	after, err := f.srv.LatestManifest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before.Version, after.Version, "SINK: nothing was pushed or committed")
	_, err = f.srv.ReadLatest(context.Background(), "agent-out.md")
	assert.Error(t, err, "the edit never reached the server checkpoint")
	h2 := &countingHarness{}
	f2 := newNativeFixture(t, h2, staticSources{claudeAgentSource(t)})
	ws2 := f2.plan(t)
	a2 := goodClaudeJob()
	a2.WorkDir = ws2.WorkDir
	res = f2.jobs.Execute(context.Background(), runneragent.Dispatch{Op: runneragent.OpAgent, Workspace: ws2, Agent: a2})
	require.True(t, res.OK, res.ErrorText)
	require.NotNil(t, res.Agent)
	assert.Equal(t, []runneragent.ModelEvidence{{Model: "claude-opus-5-5", Source: "claude_model_usage", Kind: "reported_execution"}}, res.Agent.ModelEvidence)
	b, err := f2.srv.ReadLatest(context.Background(), "agent-out.md")
	require.NoError(t, err)
	assert.Equal(t, "edited", string(b))
}
