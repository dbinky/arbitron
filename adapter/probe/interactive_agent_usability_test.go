package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func interactiveAgentPath() modelcatalog.ExecutionPath {
	p := usabilityPath()
	p.Kind, p.Mode, p.Backend = string(domprovider.ProfileKindAgent), modelcatalog.ExecutionModeInteractive, "claude-code"
	p.Binding.Kind, p.Binding.Provider, p.Binding.Backend = domprovider.ProfileKindAgent, "claude-cli", "claude-code"
	return p
}

func interactiveAgentLaneFixture(t *testing.T, script func(domprovider.ResolvedBinding) fakeStream) (*InteractiveAgentUsabilityLane, *streamCall, string) {
	t.Helper()
	call := &streamCall{}
	workDir := filepath.Join(t.TempDir(), ".model-probe")
	lane, err := NewInteractiveAgentUsabilityLane(InteractiveAgentLaneDeps{
		Streams: fakeStreamSelector{call: call, script: script},
		WithBinding: func(_ context.Context, p modelcatalog.ExecutionPath, model string) (domprovider.ResolvedBinding, error) {
			if model == "" || p.Binding.RevealKey == nil {
				return domprovider.ResolvedBinding{}, errors.New("not an inventory path")
			}
			b := p.Binding
			b.Model = model
			return b, nil
		},
		WorkDir: workDir,
	})
	require.NoError(t, err)
	return lane, call, workDir
}

// Happy: the exact candidate spelling streams pinned and tool-free, with the
// executed-model requirement, in the scratch directory; a clean Done confirms.
func TestUsabilityInteractiveAgentLaneConfirmsOnVerifiedToolFreeStream(t *testing.T) {
	lane, call, workDir := interactiveAgentLaneFixture(t, doneStream)
	require.True(t, lane.Supports(interactiveAgentPath()))
	res, err := lane.Check(context.Background(), interactiveAgentPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5-20261001", res.NativeModel)
	assert.Equal(t, "agent-stream:claude-code:claude-sonnet-5-5-20261001", res.ProofRef)
	assert.NotContains(t, res.ProofRef, "sk-secret")
	assert.False(t, call.binding.Pinned, "RequireExecutedModel, not pin grammar, enforces probes")
	assert.Equal(t, "claude-sonnet-5-5-20261001", call.binding.Model)
	assert.True(t, call.req.RequireExecutedModel)
	assert.True(t, call.req.SuppressTools, "a probe never grants the model tools")
	assert.Empty(t, call.req.ToolScope)
	assert.Empty(t, call.req.SessionID)
	assert.Equal(t, workDir, filepath.Dir(call.req.WorkDir))
	assert.NoDirExists(t, call.req.WorkDir)
	assert.DirExists(t, workDir)
	assert.NotEmpty(t, call.req.Prompt)
}

// Failure/error: anything but a clean terminal Done is unknown.
func TestUsabilityInteractiveAgentLaneUnverifiedStreamsNeverConfirm(t *testing.T) {
	evidence := errors.New("claudecli: pinned model absent from result modelUsage")
	for name, script := range map[string]func(domprovider.ResolvedBinding) fakeStream{
		"evidence error chunk": func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Error: evidence}}}
		},
		"error after tokens": func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Token: "ok"}, {Error: evidence}}}
		},
		"ends without done": func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Token: "ok"}}}
		},
		"empty stream": func(domprovider.ResolvedBinding) fakeStream { return fakeStream{} },
		"open failure": func(domprovider.ResolvedBinding) fakeStream { return fakeStream{open: errors.New("exec refused")} },
	} {
		t.Run(name, func(t *testing.T) {
			lane, _, _ := interactiveAgentLaneFixture(t, script)
			res, err := lane.Check(context.Background(), interactiveAgentPath(), usabilityEntry())
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
			assert.NotContains(t, err.Error(), "exec refused")
			assert.NotContains(t, err.Error(), evidence.Error())
			assert.Empty(t, res.ProofRef)
		})
	}
	t.Run("selector refusal", func(t *testing.T) {
		lane, err := NewInteractiveAgentUsabilityLane(InteractiveAgentLaneDeps{
			Streams: fakeStreamSelector{call: &streamCall{}, err: errors.New("unsupported backend")},
			WithBinding: func(_ context.Context, p modelcatalog.ExecutionPath, m string) (domprovider.ResolvedBinding, error) {
				b := p.Binding
				b.Model = m
				return b, nil
			},
			WorkDir: t.TempDir(),
		})
		require.NoError(t, err)
		_, err = lane.Check(context.Background(), interactiveAgentPath(), usabilityEntry())
		assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
	})
	t.Run("binding refusal", func(t *testing.T) {
		lane, err := NewInteractiveAgentUsabilityLane(InteractiveAgentLaneDeps{
			Streams: fakeStreamSelector{call: &streamCall{}, script: doneStream},
			WithBinding: func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error) {
				return domprovider.ResolvedBinding{}, errors.New("unsupported route")
			},
			WorkDir: t.TempDir(),
		})
		require.NoError(t, err)
		_, err = lane.Check(context.Background(), interactiveAgentPath(), usabilityEntry())
		assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
	})
	t.Run("work dir cannot be created", func(t *testing.T) {
		lane, call, workDir := interactiveAgentLaneFixture(t, doneStream)
		require.NoError(t, os.MkdirAll(filepath.Dir(workDir), 0o700))
		require.NoError(t, os.WriteFile(workDir, []byte("file"), 0o600))
		_, err := lane.Check(context.Background(), interactiveAgentPath(), usabilityEntry())
		assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
		assert.Empty(t, call.req.Prompt, "nothing is streamed without a scratch directory")
	})
	t.Run("cancelled context", func(t *testing.T) {
		lane, _, _ := interactiveAgentLaneFixture(t, func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Token: "ok"}}} // never terminates
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := lane.Check(ctx, interactiveAgentPath(), usabilityEntry())
		assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
	})
}

// Edge: only interactive agent routes on the claude-code backend. Batch agents,
// interactive chat and opencode streams belong elsewhere or nowhere.
func TestUsabilityInteractiveAgentLaneSupportsOnlyClaudeCodeAndCodexStreams(t *testing.T) {
	lane, _, _ := interactiveAgentLaneFixture(t, doneStream)
	with := func(mut func(*modelcatalog.ExecutionPath)) modelcatalog.ExecutionPath {
		p := interactiveAgentPath()
		mut(&p)
		return p
	}
	for backend, want := range map[string]bool{"claude-code": true, "codex": true, "opencode": false, "pi": false, "": false} {
		assert.Equal(t, want, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Backend = backend })), "backend %q", backend)
	}
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Mode = modelcatalog.ExecutionModeBatch })))
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Kind = string(domprovider.ProfileKindChat) })))
}

func TestUsabilityInteractiveAgentLaneRequiresCollaborators(t *testing.T) {
	_, err := NewInteractiveAgentUsabilityLane(InteractiveAgentLaneDeps{})
	assert.Error(t, err)
	_, err = NewInteractiveAgentUsabilityLane(InteractiveAgentLaneDeps{Streams: fakeStreamSelector{}, WithBinding: func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error) {
		return domprovider.ResolvedBinding{}, nil
	}})
	assert.Error(t, err, "the scratch directory is required")
}
