package claudecli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestAgentAdapterImplementsPort(t *testing.T) {
	var _ engine.AgentProvider = (*AgentAdapter)(nil)
}

func TestAgentAdapterRunAgent(t *testing.T) {
	// Happy path — transcript folds into steps; arg threading enables tools.
	t.Run("returns result with folded steps, summary, tokens, session id", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"a1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"editing files"}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
			`{"type":"result","is_error":false,"result":"mission complete","session_id":"a1","usage":{"input_tokens":11,"output_tokens":5}}`,
		}}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		res, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt:    "fix the bug",
			WorkDir:   "/tmp/ws",
			Model:     "claude-agent",
			ToolScope: []string{"Read", "Write", "Bash(git status:*)"},
		})
		require.NoError(t, err)
		assert.Equal(t, "mission complete", res.Summary)
		assert.Equal(t, "a1", res.SessionID)
		assert.Equal(t, 11, res.TokensIn)
		assert.Equal(t, 5, res.TokensOut)
		assert.Equal(t, "claude-agent", res.Model)

		require.Len(t, res.Steps, 3)
		assert.Equal(t, "assistant", res.Steps[0].Kind)
		assert.Equal(t, "editing files", res.Steps[0].Summary)
		assert.Equal(t, "tool_call", res.Steps[1].Kind)
		assert.Equal(t, "assistant", res.Steps[2].Kind)
		assert.Equal(t, "done", res.Steps[2].Summary)
	})

	t.Run("invokes claude with skip-permissions and ToolScope allowedTools", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"a"}`,
		}}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt:    "go",
			WorkDir:   "/tmp/ws",
			ToolScope: []string{"Read", "Bash(git status:*)"},
		})
		require.NoError(t, err)

		assert.Contains(t, fr.gotSpec.Args, "--dangerously-skip-permissions")
		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok)
		assert.Equal(t, "Read,Bash(git status:*)", val)
		assert.Equal(t, "go", fr.gotSpec.Stdin)
		assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
	})

	// Success — ToolScope empty falls back to cfg.AllowedTools.
	t.Run("empty ToolScope falls back to cfg.AllowedTools", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"a"}`,
		}}
		a := NewAgentAdapter(Config{
			BinaryPath:   "claude",
			AllowedTools: []string{"Read", "Edit"},
			Runner:       fr,
		})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt:  "go",
			WorkDir: "/tmp/ws",
		})
		require.NoError(t, err)
		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok)
		assert.Equal(t, "Read,Edit", val)
	})

	// Success — SessionID threaded as --resume.
	t.Run("mission SessionID is threaded as --resume", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"a"}`,
		}}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt:    "go",
			WorkDir:   "/tmp/ws",
			SessionID: "resume-7",
			ToolScope: []string{"Read"},
		})
		require.NoError(t, err)
		val, ok := argValue(fr.gotSpec.Args, "--resume")
		require.True(t, ok)
		assert.Equal(t, "resume-7", val)
	})

	// Success — per-mission Timeout derives a context that cancels.
	t.Run("mission Timeout derives a context that cancels the run", func(t *testing.T) {
		// The blocking fake never sends a result; if the derived context did
		// not cancel, RunAgent would hang. A tiny timeout forces cancellation.
		br := &blockingRunner{}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: br})

		done := make(chan error, 1)
		go func() {
			_, err := a.RunAgent(context.Background(), engine.AgentMission{
				Prompt:    "go",
				WorkDir:   "/tmp/ws",
				ToolScope: []string{"Read"},
				Timeout:   20 * time.Millisecond,
			})
			done <- err
		}()

		select {
		case err := <-done:
			require.Error(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("RunAgent did not return after mission Timeout elapsed")
		}
		assert.NotNil(t, br.gotCtx)
		_, hasDeadline := br.gotCtx.Deadline()
		assert.True(t, hasDeadline, "a per-mission timeout must set a context deadline")
	})

	// Failure — is_error result.
	t.Run("result event with is_error true returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":true,"result":"agent failure","session_id":"a"}`,
		}}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent failure")
	})

	// Error — process exit / wait error.
	t.Run("process exit error is surfaced", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"a"}`},
			waitErr: errors.New("exit status 1"),
		}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"},
		})
		require.Error(t, err)
	})

	// Error — start failure (binary not found).
	t.Run("start failure is surfaced", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("binary not found")}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binary not found")
	})

	// Error — missing result event.
	t.Run("missing result event returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		}}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no result event")
	})

	// Error — malformed stream-json.
	t.Run("malformed stream-json line returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{bad json`,
			`{"type":"result","result":"ok","session_id":"a"}`,
		}}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"},
		})
		require.Error(t, err)
	})

	// Error — WorkDir invariants.
	t.Run("empty WorkDir is rejected without invoking the runner", func(t *testing.T) {
		fr := &fakeRunner{}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", ToolScope: []string{"Read"}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkDirRequired)
		assert.False(t, fr.runCalled)
	})

	t.Run("WorkDir outside WorkspaceRoot is rejected", func(t *testing.T) {
		fr := &fakeRunner{}
		a := NewAgentAdapter(Config{
			BinaryPath:    "claude",
			WorkspaceRoot: "/var/lib/arbitron/workspaces",
			Runner:        fr,
		})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/etc", ToolScope: []string{"Read"},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkDirEscapesRoot)
		assert.False(t, fr.runCalled)
	})

	// Edge — only a result event yields a valid result with empty Steps.
	t.Run("transcript with only a result event yields empty steps and a valid summary", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":false,"result":"nothing to do","session_id":"a9"}`,
		}}
		a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		res, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"},
		})
		require.NoError(t, err)
		assert.Empty(t, res.Steps)
		assert.Equal(t, "nothing to do", res.Summary)
		assert.Equal(t, "a9", res.SessionID)
	})

	// Edge — oversized output trips MaxResponseBytes via wait().
	t.Run("oversized output trips MaxResponseBytes via wait", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"a"}`},
			waitErr: ErrResponseTooLarge,
		}
		a := NewAgentAdapter(Config{BinaryPath: "claude", MaxResponseBytes: 8, Runner: fr})

		_, err := a.RunAgent(context.Background(), engine.AgentMission{
			Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrResponseTooLarge)
		// MaxResponseBytes must be threaded onto the RunSpec.
		assert.Equal(t, int64(8), fr.gotSpec.MaxResponseBytes)
	})
}

// blockingRunner is a CommandRunner whose lines channel only closes when its
// ctx is cancelled — it never emits a result event on its own. It records the
// ctx it was handed so tests can assert a deadline was derived.
type blockingRunner struct {
	gotCtx context.Context
}

func (b *blockingRunner) Run(ctx context.Context, _ RunSpec) (<-chan string, func() error, error) {
	b.gotCtx = ctx
	ch := make(chan string)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, func() error { return ctx.Err() }, nil
}
