package codex

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing foldExecStream's completion guard lets incomplete turns and empty
// resumed streams accept output despite separately stored proof of the pin.
// Only subprocess I/O is replaced: the provider, session and verifier are real.
func TestInteractive_ExecutedModelRequiresCompletedTurn(t *testing.T) {
	for _, resume := range []bool{false, true} {
		phase := "fresh"
		if resume {
			phase = "resume"
		}
		for _, streaming := range []bool{false, true} {
			method := "Chat"
			if streaming {
				method = "StreamChat"
			}
			for _, tc := range []struct {
				name  string
				lines []string
				valid bool
			}{
				{"completed", execLines(rolloutThread, "partial output", 1, 1), true},
				{"incomplete", execLines(rolloutThread, "partial output", 1, 1)[:3], false},
				{"empty", nil, false},
			} {
				t.Run(phase+"/"+method+"/"+tc.name, func(t *testing.T) {
					home, workdir := t.TempDir(), t.TempDir()
					t.Setenv("CODEX_HOME", home)
					const model = "gpt-5-sol"
					req := engine.ChatRequest{Model: model, Prompt: "reply ok", WorkDir: workdir}
					if resume {
						req.SessionID = rolloutThread
						writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine(model))
					}
					waited := make(chan struct{})
					runner := &multiRunner{scripts: []runScript{
						compatibleProbe(), {lines: tc.lines, onWait: func() {
							// The matching evidence exists even when stdout is empty or
							// lacks completion. Neither fact alone proves success.
							lines := []string{metaLine(rolloutThread), ctxLine(model)}
							if resume {
								lines = append(lines, ctxLine(model))
							}
							writeRollout(t, home, rolloutThread, lines...)
							close(waited)
						}},
					}}
					p := NewInteractiveProvider(InteractiveConfig{
						Binding:    provider.ResolvedBinding{Model: model, Pinned: true},
						BinaryPath: "/x/codex", Timeout: 5 * time.Second,
					})
					p.core.runner = runner
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if streaming {
						chunks, err := p.StreamChat(ctx, req)
						require.NoError(t, err)
						var done, failures int
						for chunk := range chunks {
							if chunk.Done {
								done++
							}
							if chunk.Error != nil {
								failures++
								assert.ErrorIs(t, chunk.Error, ErrModelEvidence)
							}
						}
						if tc.valid {
							assert.Equal(t, 1, done)
							assert.Zero(t, failures)
						} else {
							assert.Zero(t, done, "an incomplete pinned turn must never publish Done")
							assert.Equal(t, 1, failures)
						}
					} else {
						resp, err := p.Chat(ctx, req)
						if tc.valid {
							require.NoError(t, err)
							require.NotNil(t, resp)
							assert.Equal(t, "partial output", resp.Output)
						} else {
							assert.ErrorIs(t, err, ErrModelEvidence)
							assert.Nil(t, resp, "partial output must not become an accepted result")
						}
					}
					require.NoError(t, ctx.Err(), "the refusal must not depend on the deadline")
					select {
					case <-waited:
					default:
						t.Fatal("the child was not reaped before the terminal outcome")
					}
					require.Len(t, runner.calls, 2)
					launch := runner.calls[1]
					assert.Equal(t, req.Prompt, launch.Stdin)
					assert.Equal(t, workdir, launch.Dir)
					assert.Contains(t, launch.Env, "CODEX_HOME="+home)
					modelIndex := slices.Index(launch.Args, "-m")
					require.GreaterOrEqual(t, modelIndex, 0)
					require.Less(t, modelIndex+1, len(launch.Args))
					assert.Equal(t, model, launch.Args[modelIndex+1])
					if resume {
						assert.Equal(t, []string{"exec", "resume", rolloutThread}, launch.Args[:3])
					}
				})
			}
		}
	}
}
