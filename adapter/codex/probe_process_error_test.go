package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A real local child emits a synthetic credential at the version or turn
// boundary. Returning/logging raw runner errors or version stdout fails this.
func TestModelProbeCodexProcessErrorsAreSecretFree(t *testing.T) {
	const secret = "synthetic-private-codex-stderr-token"
	for _, fault := range []string{"turn stderr", "version stderr", "version stdout"} {
		for _, mode := range []string{"agent probe", "pinned agent", "probe chat", "pinned chat", "probe stream", "pinned stream"} {
			t.Run(fault+"/"+mode, func(t *testing.T) {
				var logs bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				t.Cleanup(func() { slog.SetDefault(previous) })
				dir, home := t.TempDir(), t.TempDir()
				t.Setenv("CODEX_HOME", home)
				writeModelsCache(t, home, "gpt-5-sol")
				version := "printf '%s\\n' 'codex-cli 0.163.0'; exit 0"
				if fault == "version stderr" {
					version = "printf '%s\\n' '" + secret + "' >&2; exit 7"
				} else if fault == "version stdout" {
					version = "printf '%s\\n' '" + secret + "'; exit 0"
				}
				bin := filepath.Join(dir, "harness")
				script := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then " + version + "; fi\ncase \"$*\" in *'mcp list'*) printf '[]\\n'; exit 0;; esac\nprintf '%s\\n' '" + secret + "' >&2\nexit 7\n"
				require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
				binding := provider.ResolvedBinding{Model: "gpt-5-sol"}
				probe := mode == "agent probe" || mode == "probe chat" || mode == "probe stream"
				binding.Pinned = !probe
				req := engine.ChatRequest{Model: binding.Model, WorkDir: dir, RequireExecutedModel: true, SuppressTools: probe}
				var err error
				switch mode {
				case "agent probe":
					model, _, callErr := New(Config{Binding: binding, BinaryPath: bin}).ProbeExecutedModel(context.Background(), "identity", dir)
					assert.Empty(t, model)
					err = callErr
				case "pinned agent":
					result, callErr := New(Config{Binding: binding, BinaryPath: bin}).RunAgent(context.Background(), engine.AgentMission{WorkDir: dir})
					assert.Nil(t, result)
					err = callErr
				default:
					chat := NewInteractiveProvider(InteractiveConfig{Binding: binding, BinaryPath: bin})
					if mode == "probe chat" || mode == "pinned chat" {
						result, callErr := chat.Chat(context.Background(), req)
						assert.Nil(t, result)
						err = callErr
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
				}
				require.Error(t, err)
				assert.NotContains(t, err.Error(), secret)
				assert.NotContains(t, logs.String(), secret)
				var stderr *claudecli.StderrError
				assert.False(t, errors.As(err, &stderr), "raw stderr must not escape through Unwrap")
			})
		}
	}
}
