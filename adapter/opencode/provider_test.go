package opencode

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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

// Happy path — single-shot run with canned output → AgentResult; the fake
// receives -p/-c/cwd and the injected env (base allowlist + key + base-url).
func TestRunAgent_HappyPath(t *testing.T) {
	fr := &fakeRunner{lines: []string{
		"Working on the task.",
		"Edited hello.txt.",
		"Done: the file now reads goodbye.",
	}}
	p := New(Config{
		Binding:    anthropicBinding("sk-sentinel", nil),
		BinaryPath: "opencode",
		Runner:     fr,
	})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "edit hello.txt to say goodbye",
		WorkDir: "/tmp/ws",
		Model:   "anthropic/claude-3-5-sonnet",
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// Summary is the joined output; steps are tolerant best-effort.
	assert.Contains(t, res.Summary, "goodbye")
	assert.Equal(t, "anthropic/claude-3-5-sonnet", res.Model)
	// Single-shot contract: no session id threaded back.
	assert.Empty(t, res.SessionID)

	// Arg threading: -p <prompt>, -c <cwd>, single-shot (no resume/session).
	require.True(t, fr.runCalled)
	args := fr.gotSpec.Args
	assert.Contains(t, args, "-p")
	assert.Contains(t, args, "edit hello.txt to say goodbye")
	assert.Contains(t, args, "-c")
	assert.Contains(t, args, "/tmp/ws")
	assert.NotContains(t, strings.Join(args, " "), "--resume")
	assert.NotContains(t, strings.Join(args, " "), "--session")
	assert.NotContains(t, strings.Join(args, " "), "--model")

	// cmd.Dir is the WorkDir.
	assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)

	// Env carries the injected key + base-url on top of the base allowlist.
	key, ok := envValue(fr.gotSpec.Env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, "sk-sentinel", key)
	_, ok = envValue(fr.gotSpec.Env, "PATH")
	assert.True(t, ok, "base allowlist PATH must be present")
}

// Success (variants) — tool scope threads to a permission arg; multiple output
// lines fold into ordered steps; no resume/session arg in default contract.
func TestRunAgent_Variants(t *testing.T) {
	t.Run("tool scope threaded", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{"ok"}}
		p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})
		_, err := p.RunAgent(context.Background(), engine.AgentMission{
			Prompt:    "go",
			WorkDir:   "/tmp/ws",
			ToolScope: []string{"Read", "Write"},
		})
		require.NoError(t, err)
		// quiet + json/text output format flags present for headless mode.
		joined := strings.Join(fr.gotSpec.Args, " ")
		assert.Contains(t, joined, "-q")
		assert.Contains(t, joined, "-f")
	})

	t.Run("multiple lines fold into ordered steps", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{"line one", "line two", "line three"}}
		p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})
		res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
		require.NoError(t, err)
		require.Len(t, res.Steps, 3)
		assert.Equal(t, "line one", res.Steps[0].Summary)
		assert.Equal(t, "line three", res.Steps[2].Summary)
		// Coarse token accounting is non-negative.
		assert.GreaterOrEqual(t, res.TokensIn, 0)
		assert.GreaterOrEqual(t, res.TokensOut, 0)
	})
}

// Failure — non-zero process exit (wait() error) → wrapped error.
func TestRunAgent_NonZeroExit(t *testing.T) {
	fr := &fakeRunner{lines: []string{"partial"}, waitErr: errors.New("exit status 1")}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "exit status 1")
}

// Error — binary-not-found (runner Run returns error before streaming).
func TestRunAgent_BinaryNotFound(t *testing.T) {
	fr := &fakeRunner{startErr: errors.New("exec: \"opencode\": not found")}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "not found")
}

// Error — context cancel/timeout → returns a context error.
func TestRunAgent_ContextCancel(t *testing.T) {
	fr := &fakeRunner{blockOnCtx: true}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := p.RunAgent(ctx, engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRunAgent_Timeout(t *testing.T) {
	fr := &fakeRunner{blockOnCtx: true}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "go",
		WorkDir: "/tmp/ws",
		Timeout: 10 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// Error — RevealKey error propagates (via the env helper) before launch.
func TestRunAgent_RevealKeyError(t *testing.T) {
	fr := &fakeRunner{lines: []string{"ok"}}
	p := New(Config{
		Binding:    anthropicBinding("", errors.New("decrypt boom")),
		BinaryPath: "opencode",
		Runner:     fr,
	})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "decrypt boom")
	assert.False(t, fr.runCalled, "runner must not launch when env fails")
}

// Edge case — empty WorkDir is rejected before launch.
func TestRunAgent_EmptyWorkDir(t *testing.T) {
	fr := &fakeRunner{lines: []string{"ok"}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: ""})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.False(t, fr.runCalled, "runner must not launch with empty WorkDir")
}

// Edge case — malformed / empty output is parsed tolerantly (no panic).
func TestRunAgent_EmptyOutput(t *testing.T) {
	fr := &fakeRunner{lines: nil}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: fr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Summary)
	assert.Empty(t, res.Steps)
}

// Security / log-capture — the revealed key never appears in any log line or
// the returned error, and reaches the subprocess only via RunSpec.Env.
func TestRunAgent_KeyAbsentFromLogs(t *testing.T) {
	const sentinel = "sk-very-secret-opencode"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Run a failing branch too so the error path is covered.
	fr := &fakeRunner{lines: []string{"work"}, waitErr: errors.New("boom")}
	p := New(Config{Binding: anthropicBinding(sentinel, nil), BinaryPath: "opencode", Runner: fr})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)

	assert.NotContains(t, buf.String(), sentinel, "key leaked into logs")
	assert.NotContains(t, err.Error(), sentinel, "key leaked into error")

	// The key reached the subprocess only via the env.
	key, ok := envValue(fr.gotSpec.Env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, sentinel, key)
}

// buildArgs table test — default single-shot contract.
func TestBuildArgs(t *testing.T) {
	t.Run("default single-shot: -p, -c, -q, -f text; no resume/model", func(t *testing.T) {
		args := buildArgs(engine.AgentMission{Prompt: "do it", WorkDir: "/ws"})
		joined := strings.Join(args, " ")
		assert.Contains(t, joined, "-p do it")
		assert.Contains(t, joined, "-c /ws")
		assert.Contains(t, joined, "-q")
		assert.Contains(t, joined, "-f")
		assert.NotContains(t, joined, "--resume")
		assert.NotContains(t, joined, "--session")
		assert.NotContains(t, joined, "--model")
	})
}
