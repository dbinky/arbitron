package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const job = "6f1c1f0e-8a39-4d0e-9d51-3a4c3c0b6a11"

// fakeProvider records each request and checks the work dir exists while running.
type fakeProvider struct {
	mu        sync.Mutex
	reqs      []engine.ChatRequest
	dirExists bool
	resp      *engine.ChatResponse
	chunks    []engine.ChatChunk
	err       error
}

func (p *fakeProvider) Chat(ctx context.Context, r engine.ChatRequest) (*engine.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, r)
	_, statErr := os.Stat(r.WorkDir)
	p.dirExists = statErr == nil
	return p.resp, p.err
}

func (p *fakeProvider) StreamChat(ctx context.Context, r engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, r)
	chunks := p.chunks
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan engine.ChatChunk)
	go func() {
		defer close(ch)
		for _, c := range chunks {
			select {
			case ch <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// fakeSelector returns a provider per binding Provider and records bindings.
type fakeSelector struct {
	mu       sync.Mutex
	bindings []provider.ResolvedBinding
	byProv   map[string]*fakeProvider
}

func (s *fakeSelector) ChatProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = append(s.bindings, b)
	p, ok := s.byProv[b.Provider]
	if !ok {
		return nil, errors.New("no provider for " + b.Provider)
	}
	return p, nil
}

func chatDispatch(kind string, stream bool) runneragent.Dispatch {
	op := runneragent.OpChat
	if stream {
		op = runneragent.OpStream
	}
	return runneragent.Dispatch{JobID: job, Generation: 2, Op: op, Chat: &runneragent.ChatJob{
		Model: "claude-sonnet-4-5", SystemPrompt: "sys", Prompt: "hi", MaxTokens: 50, Temperature: 0.1,
		OutputSchema: []byte(`{"type":"object"}`), CredentialKind: kind, ContextWindow: "200k", Effort: "high", Stream: stream,
	}}
}

func noEmit(context.Context, string) error { return nil }

func TestExecuteClaudeChatRunsInFreshScratchDirAndRemovesIt(t *testing.T) {
	scratch := t.TempDir()
	p := &fakeProvider{resp: &engine.ChatResponse{Output: "answer", Model: "claude-sonnet-4-5", SessionID: "s1", TokensIn: 4, TokensOut: 2}}
	sel := &fakeSelector{byProv: map[string]*fakeProvider{"": p}}
	e, err := New(sel, Config{ScratchRoot: scratch})
	require.NoError(t, err)

	res := e.Execute(context.Background(), chatDispatch("anthropic_subscription", false), noEmit)
	require.True(t, res.OK, res.ErrorText)
	assert.Equal(t, &runneragent.ChatResult{Output: "answer", Model: "claude-sonnet-4-5", SessionID: "s1", TokensIn: 4, TokensOut: 2}, res.Chat)

	require.Len(t, p.reqs, 1)
	req := p.reqs[0]
	assert.True(t, p.dirExists, "the scratch dir exists while the harness runs")
	assert.Equal(t, scratch, filepath.Dir(req.WorkDir))
	assert.Contains(t, filepath.Base(req.WorkDir), job)
	assert.Equal(t, map[string]any{"type": "object"}, req.OutputSchema)
	assert.Equal(t, "hi", req.Prompt)
	_, statErr := os.Stat(req.WorkDir)
	assert.True(t, os.IsNotExist(statErr), "the scratch dir is removed after the job")

	require.Len(t, sel.bindings, 1)
	b := sel.bindings[0]
	assert.Equal(t, "", b.Provider, "subscription path through the claude-cli arm")
	assert.Equal(t, "claude-sonnet-4-5", b.Model)
	assert.Equal(t, provider.ContextWindow("200k"), b.ContextWindow)
	assert.Equal(t, provider.Effort("high"), b.Effort)
	assert.Nil(t, b.RevealKey)
}

func TestExecuteStreamEmitsTokensInOrder(t *testing.T) {
	p := &fakeProvider{chunks: []engine.ChatChunk{{Token: "he"}, {Token: "llo"}, {Done: true, SessionID: "s2", TokensIn: 1, TokensOut: 2}}}
	e, err := New(&fakeSelector{byProv: map[string]*fakeProvider{"": p}}, Config{ScratchRoot: t.TempDir()})
	require.NoError(t, err)
	var got []string
	res := e.Execute(context.Background(), chatDispatch("anthropic_subscription", true), func(_ context.Context, tok string) error {
		got = append(got, tok)
		return nil
	})
	require.True(t, res.OK, res.ErrorText)
	assert.Equal(t, []string{"he", "llo"}, got)
	assert.Equal(t, "hello", res.Chat.Output)
	assert.Equal(t, "s2", res.Chat.SessionID)
	assert.Equal(t, 2, res.Chat.TokensOut)
}

func TestExecuteEmitFailureStopsTheStream(t *testing.T) {
	p := &fakeProvider{chunks: []engine.ChatChunk{{Token: "a"}, {Token: "b"}, {Token: "c"}, {Done: true}}}
	e, err := New(&fakeSelector{byProv: map[string]*fakeProvider{"": p}}, Config{ScratchRoot: t.TempDir()})
	require.NoError(t, err)
	n := 0
	res := e.Execute(context.Background(), chatDispatch("anthropic_subscription", true), func(context.Context, string) error {
		n++
		return context.Canceled
	})
	assert.False(t, res.OK)
	assert.Equal(t, 1, n, "no tokens are emitted after delivery stops")
}

func TestExecuteLunaRouteUsesRunnerLocalKeyAndConnection(t *testing.T) {
	p := &fakeProvider{resp: &engine.ChatResponse{Output: "lr ok", Model: "gpt-6.1"}}
	sel := &fakeSelector{byProv: map[string]*fakeProvider{provider.CanonicalLunaRoute: p}}
	e, err := New(sel, Config{ScratchRoot: t.TempDir(), LunaRoute: &LunaRoute{ConnectionRef: "lr-main", APIKey: "lr_local_secret"}})
	require.NoError(t, err)
	d := chatDispatch("lunaroute", false)
	d.Chat.Model = "gpt-6.1"
	res := e.Execute(context.Background(), d, noEmit)
	require.True(t, res.OK, res.ErrorText)

	require.Len(t, sel.bindings, 1)
	b := sel.bindings[0]
	assert.Equal(t, provider.CanonicalLunaRoute, b.Provider)
	assert.Equal(t, "lr-main", b.ConnectionRef)
	assert.Equal(t, "", b.BaseURL, "hosted LunaRoute root only")
	require.NotNil(t, b.RevealKey)
	key, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "lr_local_secret", key, "the runner's own key, never a server secret")
}

func TestSupportsRejectsUnsupportedWork(t *testing.T) {
	e, err := New(&fakeSelector{}, Config{ScratchRoot: t.TempDir()})
	require.NoError(t, err)
	ok, reason := e.Supports(chatDispatch("openai_subscription", false))
	assert.False(t, ok)
	assert.Contains(t, reason, "openai_subscription")
	ok, reason = e.Supports(chatDispatch("lunaroute", false))
	assert.False(t, ok, "no LunaRoute key configured on this runner")
	assert.Contains(t, reason, "LunaRoute")
	d := chatDispatch("anthropic_subscription", false)
	d.Chat.Backend = "codex"
	ok, _ = e.Supports(d)
	assert.False(t, ok)
	ok, _ = e.Supports(runneragent.Dispatch{JobID: job, Generation: 1, Op: "agent"})
	assert.False(t, ok)
	ok, _ = e.Supports(chatDispatch("anthropic_subscription", true))
	assert.True(t, ok)
}

func TestExecuteClassifiesOutcomeAndRedactsKey(t *testing.T) {
	reset := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	p := &fakeProvider{err: runner.WithOutcome(errors.New("upstream said lr_local_secret is exhausted"), runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset})}
	e, err := New(&fakeSelector{byProv: map[string]*fakeProvider{provider.CanonicalLunaRoute: p}},
		Config{ScratchRoot: t.TempDir(), LunaRoute: &LunaRoute{ConnectionRef: "lr-main", APIKey: "lr_local_secret"}})
	require.NoError(t, err)
	res := e.Execute(context.Background(), chatDispatch("lunaroute", false), noEmit)
	assert.False(t, res.OK)
	assert.Nil(t, res.Chat)
	assert.Equal(t, runner.OutcomeExhausted, res.Outcome.Class)
	assert.Equal(t, &reset, res.Outcome.ResetAt)
	assert.NotContains(t, res.ErrorText, "lr_local_secret")
	assert.LessOrEqual(t, len(res.ErrorText), maxErrorText)
}

func TestNewValidatesConfig(t *testing.T) {
	_, err := New(nil, Config{ScratchRoot: t.TempDir()})
	assert.Error(t, err)
	_, err = New(&fakeSelector{}, Config{ScratchRoot: "relative"})
	assert.Error(t, err)
}

// The real factory + claudecli chat adapter run the step inside the scratch dir.
func TestExecuteRealClaudeAdapterThroughFactory(t *testing.T) {
	scratch := t.TempDir()
	dir := t.TempDir()
	pwdLog := filepath.Join(dir, "pwd.log")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"pwd -P > '" + pwdLog + "'\n" +
		"cat > /dev/null\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	f := providersadapter.NewFactory(providersadapter.Deps{ClaudeChatConfig: claudecli.Config{BinaryPath: bin, WorkspaceRoot: scratch, MaxResponseBytes: 1 << 20}})
	e, err := New(f, Config{ScratchRoot: scratch})
	require.NoError(t, err)

	d := chatDispatch("anthropic_subscription", false)
	d.Chat.OutputSchema = nil
	res := e.Execute(context.Background(), d, noEmit)
	require.True(t, res.OK, res.ErrorText)
	assert.Equal(t, "ok", res.Chat.Output)

	ran, err := os.ReadFile(pwdLog)
	require.NoError(t, err)
	realScratch, err := filepath.EvalSymlinks(scratch)
	require.NoError(t, err)
	assert.Equal(t, realScratch, filepath.Dir(strings.TrimSpace(string(ran))), "claude ran in a per-job dir directly under the scratch root")
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	assert.Empty(t, entries, "the per-job dir was removed")
}
