package opencode

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// fakeRunner is a claudecli.CommandRunner that replays canned stdout lines and
// records the RunSpec it received. It mirrors the claudecli package's own fake.
// It is used for the version probe (single invocation) and pure-arg tests.
type fakeRunner struct {
	lines     []string
	waitErr   error
	startErr  error
	gotSpec   claudecli.RunSpec
	runCalled bool
	// blockOnCtx, when set, makes the runner honour ctx cancellation: it blocks
	// sending lines until ctx is done, then closes the channel.
	blockOnCtx bool
}

func (f *fakeRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	f.runCalled = true
	f.gotSpec = spec
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		if f.blockOnCtx {
			<-ctx.Done()
			return
		}
		for _, l := range f.lines {
			ch <- l
		}
	}()
	wait := func() error {
		if f.blockOnCtx {
			return ctx.Err()
		}
		return f.waitErr
	}
	return ch, wait, nil
}

// multiRunner replays a sequence of canned invocations (probe, then run...),
// recording each RunSpec. It satisfies claudecli.CommandRunner. It honours ctx
// cancellation when a script sets blockOnCtx.
type multiRunner struct {
	scripts []runScript
	calls   []claudecli.RunSpec
	idx     int
}

type runScript struct {
	lines      []string
	waitErr    error
	startErr   error
	blockOnCtx bool
}

func (m *multiRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	m.calls = append(m.calls, spec)
	var s runScript
	if m.idx < len(m.scripts) {
		s = m.scripts[m.idx]
	}
	m.idx++
	if s.startErr != nil {
		return nil, nil, s.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		if s.blockOnCtx {
			<-ctx.Done()
			return
		}
		for _, l := range s.lines {
			ch <- l
		}
	}()
	wait := func() error {
		if s.blockOnCtx {
			return ctx.Err()
		}
		return s.waitErr
	}
	return ch, wait, nil
}

func compatibleProbe() runScript { return runScript{lines: []string{"opencode " + minOpencodeVersion}} }

func anthropicBinding(sentinel string, err error) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "anthropic",
		BaseURL:  "https://api.anthropic.com",
		Model:    "anthropic/claude-3-5-sonnet",
		RevealKey: func(_ context.Context) (string, error) {
			if err != nil {
				return "", err
			}
			return sentinel, nil
		},
	}
}

func TestProviderImplementsPort(t *testing.T) {
	var _ engine.AgentProvider = (*Provider)(nil)
}

// HAPPY — agent mission: probe passes, then `run -m anthropic/<model>` argv with
// the injected env; canned output folds into an AgentResult.
func TestRunAgent_ModernRunContract(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{"Working.", "Edited hello.txt.", "Done: goodbye."}},
	}}
	p := New(Config{
		Binding:       anthropicBinding("sk-sentinel", nil),
		BinaryPath:    "opencode",
		Runner:        mr,
		AllowAllTools: true,
	})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "edit hello.txt to say goodbye",
		WorkDir: "/tmp/ws",
		Model:   "anthropic/claude-3-5-sonnet",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Contains(t, res.Summary, "goodbye")
	assert.Equal(t, "anthropic/claude-3-5-sonnet", res.Model)
	assert.Empty(t, res.SessionID, "single-shot contract: no session id threaded")

	// Second call is the run; assert modern argv.
	require.GreaterOrEqual(t, len(mr.calls), 2)
	runSpec := mr.calls[1]
	assert.Equal(t, "run", runSpec.Args[0])
	assert.Contains(t, runSpec.Args, "edit hello.txt to say goodbye", "prompt is the run positional")
	m, ok := argValue(runSpec.Args, "-m")
	require.True(t, ok)
	assert.Equal(t, "anthropic/claude-3-5-sonnet", m)
	assert.Equal(t, "/tmp/ws", runSpec.Dir, "WorkDir is the cwd pin")
	assert.Contains(t, runSpec.Args, permissionAllowAllFlag, "open-by-default posture")
	key, ok := envValue(runSpec.Env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, "sk-sentinel", key)
}

// HAPPY (variants) — per-request ToolScope threads into a narrowed tool flag;
// multiple output lines fold into ordered steps.
func TestRunAgent_Variants(t *testing.T) {
	t.Run("tool scope threaded", func(t *testing.T) {
		mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"ok"}}}}
		p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
		_, err := p.RunAgent(context.Background(), engine.AgentMission{
			Prompt:    "go",
			WorkDir:   "/tmp/ws",
			Model:     "anthropic/claude-3-5-sonnet",
			ToolScope: []string{"Read", "Write"},
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(mr.calls), 2)
		tools, ok := argValue(mr.calls[1].Args, toolScopeFlag)
		require.True(t, ok)
		assert.Equal(t, "Read,Write", tools)
	})

	t.Run("multiple lines fold into ordered steps", func(t *testing.T) {
		mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"line one", "line two", "line three"}}}}
		p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
		res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
		require.NoError(t, err)
		require.Len(t, res.Steps, 3)
		assert.Equal(t, "line one", res.Steps[0].Summary)
		assert.Equal(t, "line three", res.Steps[2].Summary)
		assert.GreaterOrEqual(t, res.TokensIn, 0)
		assert.GreaterOrEqual(t, res.TokensOut, 0)
	})
}

// SECURITY (OOM DoS): a configured MaxResponseBytes flows into the run RunSpec.
func TestRunAgent_ThreadsMaxResponseBytes(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"Done."}}}}
	const cap = int64(1 << 20)
	p := New(Config{
		Binding:          anthropicBinding("sk-sentinel", nil),
		BinaryPath:       "opencode",
		Runner:           mr,
		MaxResponseBytes: cap,
		AllowAllTools:    true,
	})
	_, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "x",
		WorkDir: "/tmp/ws",
		Model:   "anthropic/claude-3-5-sonnet",
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mr.calls), 2)
	assert.Equal(t, cap, mr.calls[1].MaxResponseBytes,
		"the configured MaxResponseBytes must reach the run subprocess RunSpec")
}

// SECURITY — RevealKey failure aborts BEFORE any subprocess launch (no probe, no run).
func TestRunAgent_RevealKeyAbortsBeforeLaunch(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"ok"}}}}
	p := New(Config{
		Binding:       anthropicBinding("", errors.New("decrypt boom")),
		BinaryPath:    "opencode",
		Runner:        mr,
		AllowAllTools: true,
	})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "decrypt boom")
	assert.Empty(t, mr.calls, "no subprocess (not even the version probe) may launch when env fails")
}

// ERROR — incompatible binary: probe fails => mission fails closed, run never launches.
func TestRunAgent_IncompatibleBinary(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{lines: []string{"opencode 0.0.55"}}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
	assert.Len(t, mr.calls, 1, "only the probe ran; the run never launched")
}

// INPUT-VALIDATION — empty WorkDir rejected before env build and before any launch.
func TestRunAgent_EmptyWorkDir(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"ok"}}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Empty(t, mr.calls, "no launch on empty WorkDir")
}

// INPUT-VALIDATION — empty model rejected (fail-closed) before any launch.
func TestRunAgent_EmptyModelFailsClosed(t *testing.T) {
	mr := &multiRunner{}
	binding := anthropicBinding("k", nil)
	binding.Model = ""
	p := New(Config{Binding: binding, BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrModelRequired)
	assert.Empty(t, mr.calls, "no launch on empty model")
}

// FAIL-CLOSED — unknown provider names provider+node in the error, no run launches.
func TestRunAgent_UnknownProviderFailsClosed(t *testing.T) {
	mr := &multiRunner{}
	binding := provider.ResolvedBinding{
		Provider:  "mystery",
		Model:     "m",
		RevealKey: func(context.Context) (string, error) { return "k", nil },
	}
	p := New(Config{Binding: binding, BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrUnknownProvider)
	assert.Contains(t, err.Error(), "mystery", "error must name the provider")
	assert.Empty(t, mr.calls, "fail-closed: never a silent tool-less run")
}

// SECURITY — the version probe is memoized: a second mission does NOT re-probe.
func TestRunAgent_ProbeMemoized(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{"first"}},
		{lines: []string{"second"}},
	}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	for i := 0; i < 2; i++ {
		_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
		require.NoError(t, err)
	}
	require.Len(t, mr.calls, 3)
	assert.Contains(t, mr.calls[0].Args, "--version")
	assert.NotContains(t, mr.calls[1].Args, "--version")
	assert.NotContains(t, mr.calls[2].Args, "--version")
}

// Failure — non-zero process exit (run wait() error) → wrapped error.
func TestRunAgent_NonZeroExit(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"partial"}, waitErr: errors.New("exit status 1")}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "exit status 1")
}

// Error — run binary-not-found (runner Run returns error on the run invocation).
func TestRunAgent_BinaryNotFound(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {startErr: errors.New("exec: \"opencode\": not found")}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "not found")
}

// Error — context cancel → returns a context error.
func TestRunAgent_ContextCancel(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := p.RunAgent(ctx, engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.Canceled)
}

// Error — timeout → returns a context deadline error.
func TestRunAgent_Timeout(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "go",
		WorkDir: "/tmp/ws",
		Model:   "anthropic/m",
		Timeout: 10 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// Edge case — malformed / empty output is parsed tolerantly (no panic).
func TestRunAgent_EmptyOutput(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: nil}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Summary)
	assert.Empty(t, res.Steps)
}

// Security / log-capture — the revealed key never appears in any log line or the
// returned error, and reaches the subprocess only via RunSpec.Env (never argv).
func TestRunAgent_KeyAbsentFromLogs(t *testing.T) {
	const sentinel = "sk-very-secret-opencode"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"work"}, waitErr: errors.New("boom")}}}
	p := New(Config{Binding: anthropicBinding(sentinel, nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)

	assert.NotContains(t, buf.String(), sentinel, "key leaked into logs")
	assert.NotContains(t, err.Error(), sentinel, "key leaked into error")

	// The key reached the run subprocess only via the env, never argv.
	require.GreaterOrEqual(t, len(mr.calls), 2)
	runSpec := mr.calls[1]
	key, ok := envValue(runSpec.Env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, sentinel, key)
	for _, a := range runSpec.Args {
		assert.NotContains(t, a, sentinel, "secret must never appear in argv")
	}
}

// parseOutput table test — covers the whitespace-filtering, token-counting,
// and step-folding logic directly so the pure function's contract is auditable.
func TestParseOutput(t *testing.T) {
	tests := []struct {
		name          string
		lines         []string
		wantSummary   string
		wantStepCount int
		wantTokensOut int
	}{
		{
			name:          "empty input yields empty output",
			lines:         nil,
			wantSummary:   "",
			wantStepCount: 0,
			wantTokensOut: 0,
		},
		{
			name:          "whitespace-only lines are filtered, not folded into steps",
			lines:         []string{"", "   ", "\t"},
			wantSummary:   "",
			wantStepCount: 0,
			wantTokensOut: 0,
		},
		{
			name:          "content lines produce one step per line",
			lines:         []string{"line one", "line two"},
			wantSummary:   "line one\nline two",
			wantStepCount: 2,
			wantTokensOut: 4, // 2 words each
		},
		{
			name:          "mixed blank and content lines: blanks dropped from steps",
			lines:         []string{"first", "", "second", "   "},
			wantSummary:   "first\nsecond",
			wantStepCount: 2,
			wantTokensOut: 2,
		},
		{
			name:          "multi-word lines accumulate coarse token count",
			lines:         []string{"one two three four five"},
			wantSummary:   "one two three four five",
			wantStepCount: 1,
			wantTokensOut: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary, steps, tokensOut := parseOutput(tc.lines)
			assert.Equal(t, tc.wantSummary, summary)
			assert.Len(t, steps, tc.wantStepCount)
			assert.Equal(t, tc.wantTokensOut, tokensOut)
			for _, s := range steps {
				assert.Equal(t, "assistant", s.Kind)
			}
		})
	}
}
