package probe

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production adapters satisfy the domain port; no new harness code exists.
var (
	_ engine.ExecutedModelProber  = (*claudecli.AgentAdapter)(nil)
	_ engine.ExecutedModelProber  = (*codex.Provider)(nil)
	_ runner.ConfirmationExecutor = (*HarnessConfirmationExecutor)(nil)
)

type ceProber struct {
	mu                sync.Mutex
	executed, session string
	err               error
	prompts, dirs     []string
}

func (p *ceProber) RunAgent(context.Context, engine.AgentMission) (*engine.AgentResult, error) {
	panic("confirmation must never call unrestricted RunAgent")
}

func (p *ceProber) ProbeExecutedModel(_ context.Context, prompt, workDir string) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts, p.dirs = append(p.prompts, prompt), append(p.dirs, workDir)
	return p.executed, p.session, p.err
}

type ceRunOnly struct{}

func (ceRunOnly) RunAgent(context.Context, engine.AgentMission) (*engine.AgentResult, error) {
	panic("confirmation must never call unrestricted RunAgent")
}

type ceSelector struct {
	mu   sync.Mutex
	got  []provider.ResolvedBinding
	prov engine.AgentProvider
	err  error
}

func (s *ceSelector) AgentProviderFor(b provider.ResolvedBinding) (engine.AgentProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, b)
	return s.prov, s.err
}

func TestConfirmationExecutor_UsesClonedAgentBindingAndNativeProber(t *testing.T) {
	for _, tc := range []struct{ backend, model string }{
		{workflow.BackendClaudeCode, "claude-opus-4-1"},
		{workflow.BackendCodex, "gpt-5.2-codex"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			prober := &ceProber{executed: tc.model, session: "sess-" + tc.backend}
			sel := &ceSelector{prov: prober}
			in := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Backend: tc.backend, Model: tc.model,
				AllowedTools: []string{"Read"}, ContextWindow: provider.Context1M, Effort: provider.EffortHigh, Pinned: true}
			dir := t.TempDir()
			res, err := NewConfirmationExecutor(sel).Confirm(context.Background(), in, dir)
			require.NoError(t, err)
			assert.Equal(t, runner.ConfirmationResult{RequestedModel: tc.model, ExecutedModel: tc.model, SessionID: "sess-" + tc.backend}, res)
			require.Len(t, sel.got, 1)
			b := sel.got[0]
			assert.Equal(t, provider.ProfileKindAgent, b.Kind, "the native verifier lives on the agent adapter")
			assert.Equal(t, tc.backend, b.Backend)
			assert.Equal(t, tc.model, b.Model)
			assert.Equal(t, "claude-cli", b.Provider)
			assert.Equal(t, provider.Context1M, b.ContextWindow)
			assert.Equal(t, provider.EffortHigh, b.Effort)
			assert.Empty(t, b.AllowedTools, "the probe's own restrictions apply, never a widened profile scope")
			assert.False(t, b.Pinned)
			assert.Equal(t, []string{"Read"}, in.AllowedTools, "the caller's binding is cloned, not mutated")
			assert.Equal(t, []string{runner.ConfirmationPrompt}, prober.prompts)
			assert.Equal(t, []string{dir}, prober.dirs)
		})
	}
}

func TestConfirmationExecutor_RefusesWithoutExactNativeEvidence(t *testing.T) {
	ctx := context.Background()
	b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Backend: workflow.BackendClaudeCode, Model: "claude-opus-4-1"}
	dir := t.TempDir()

	_, err := NewConfirmationExecutor(&ceSelector{prov: &ceProber{executed: "claude-sonnet-4-5", session: "s"}}).Confirm(ctx, b, dir)
	assert.ErrorIs(t, err, runner.ErrConfirmationModelMismatch, "a same-family substitute is not recovery")
	_, err = NewConfirmationExecutor(&ceSelector{prov: &ceProber{executed: "claude-opus-4-1"}}).Confirm(ctx, b, dir)
	assert.ErrorIs(t, err, runner.ErrConfirmationModelMismatch, "no harness session is no proof")
	_, err = NewConfirmationExecutor(&ceSelector{prov: ceRunOnly{}}).Confirm(ctx, b, dir)
	assert.ErrorIs(t, err, runner.ErrConfirmationUnsupported, "an adapter without a native verifier cannot confirm")

	quota := runner.WithOutcome(errors.New("usage limit"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
	_, err = NewConfirmationExecutor(&ceSelector{prov: &ceProber{err: quota}}).Confirm(ctx, b, dir)
	assert.Equal(t, runner.OutcomeExhausted, runner.ClassifyError(err).Class, "P1 structured outcomes survive")

	for name, mutate := range map[string]func(*provider.ResolvedBinding, *string){
		"opencode backend": func(b *provider.ResolvedBinding, _ *string) { b.Backend = workflow.BackendOpencode },
		"connection route": func(b *provider.ResolvedBinding, _ *string) { b.ConnectionRef = "azure" },
		"lunaroute route":  func(b *provider.ResolvedBinding, _ *string) { b.Provider = provider.CanonicalLunaRoute },
		"no model":         func(b *provider.ResolvedBinding, _ *string) { b.Model = "" },
		"no scratch":       func(_ *provider.ResolvedBinding, d *string) { *d = "" },
	} {
		sel := &ceSelector{prov: &ceProber{executed: "claude-opus-4-1", session: "s"}}
		bb, dd := b, dir
		mutate(&bb, &dd)
		_, err := NewConfirmationExecutor(sel).Confirm(ctx, bb, dd)
		assert.Error(t, err, name)
		assert.Empty(t, sel.got, "%s: refused before any harness is built", name)
	}
}

func TestNewConfirmationExecutor_RejectsNilAndTypedNil(t *testing.T) {
	assert.Panics(t, func() { NewConfirmationExecutor(nil) })
	var typed *ceSelector
	assert.Panics(t, func() { NewConfirmationExecutor(typed) })
}
