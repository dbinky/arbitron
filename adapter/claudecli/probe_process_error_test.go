package claudecli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing verified-call error shaping exposes the real child's stderr in both
// returned errors and collectResult's diagnostic sink. No harness/model is used.
func TestModelProbeClaudeProcessErrorsAreSecretFree(t *testing.T) {
	const secret = "synthetic-private-probe-stderr-token"
	for _, mode := range []string{"agent probe", "pinned agent", "chat", "agent chat", "chat stream", "agent stream"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			dir := t.TempDir()
			bin := filepath.Join(dir, "harness")
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' '"+secret+"' >&2\nexit 7\n"), 0700))
			cfg := Config{BinaryPath: bin, WorkspaceRoot: dir}
			binding := provider.ResolvedBinding{Model: proofModel}
			req := engine.ChatRequest{Model: proofModel, WorkDir: dir, RequireExecutedModel: true, SuppressTools: true}
			var err error
			switch mode {
			case "agent probe":
				model, _, callErr := NewAgentAdapterForBinding(cfg, binding).ProbeExecutedModel(context.Background(), "identity", dir)
				assert.Empty(t, model)
				err = callErr
			case "pinned agent":
				binding.Pinned = true
				result, callErr := NewAgentAdapterForBinding(cfg, binding).RunAgent(context.Background(), engine.AgentMission{Model: proofModel, WorkDir: dir})
				assert.Nil(t, result)
				err = callErr
			default:
				var chat engine.ChatProvider = NewChatAdapterForBinding(cfg, binding)
				if mode == "agent chat" || mode == "agent stream" {
					chat = NewStreamAgentAdapterForBinding(cfg, binding)
				}
				if mode == "chat" || mode == "agent chat" {
					result, callErr := chat.Chat(context.Background(), req)
					assert.Nil(t, result)
					err = callErr
				} else {
					chunks, callErr := chat.StreamChat(context.Background(), req)
					require.NoError(t, callErr)
					for chunk := range chunks {
						assert.False(t, chunk.Done)
						if chunk.Error != nil {
							err = chunk.Error
						}
					}
				}
			}
			require.Error(t, err)
			assert.NotContains(t, err.Error(), secret)
			assert.NotContains(t, logs.String(), secret)
			var stderr *StderrError
			assert.False(t, errors.As(err, &stderr), "raw stderr must not be recoverable from the surfaced error chain")
		})
	}
}

// Start faults may contain credentials too. Static shaping must retain the
// failure identity for errors.Is without exposing its text or raw stderr.
func TestModelProbeClaudeLaunchAndLimitErrorsAreSecretFree(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, ErrResponseTooLarge} {
		for _, stage := range []string{"launch", "wait"} {
			for _, stream := range []bool{false, true} {
				t.Run(cause.Error()+"/"+stage+"/"+map[bool]string{false: "batch", true: "stream"}[stream], func(t *testing.T) {
					const secret = "synthetic-launch-error-secret"
					raw := &StderrError{Err: cause, Summary: secret, Tail: []byte(secret)}
					runner := &fakeRunner{}
					if stage == "launch" {
						runner.startErr = raw
					} else {
						runner.waitErr = raw
					}
					chat := NewChatAdapter(Config{Runner: runner})
					req := engine.ChatRequest{Model: proofModel, WorkDir: t.TempDir(), RequireExecutedModel: true, SuppressTools: true}
					var err error
					if !stream {
						_, err = chat.Chat(context.Background(), req)
					} else {
						chunks, callErr := chat.StreamChat(context.Background(), req)
						err = callErr
						if err == nil {
							for chunk := range chunks {
								assert.False(t, chunk.Done)
								if chunk.Error != nil {
									err = chunk.Error
								}
							}
						}
					}
					require.ErrorIs(t, err, cause)
					assert.NotContains(t, err.Error(), secret)
				})
			}
		}
	}
}
