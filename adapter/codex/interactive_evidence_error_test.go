package codex

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Echoing either ID leaks untrusted evidence; dropping the identity guard accepts
// a foreign thread even when that thread independently proves the requested model.
// Only subprocess I/O is replaced: public methods use the real session/verifier.
func TestInteractive_ResumeMismatchErrorsAreSecretFree(t *testing.T) {
	const requested = "requested-account-secret-marker"
	const observed = "observed-account-secret-marker"
	const model = "gpt-5-sol"
	prior, err := os.ReadFile("testdata/resume-mismatch-prior.jsonl")
	require.NoError(t, err)
	completed, err := os.ReadFile("testdata/resume-mismatch-turn.jsonl")
	require.NoError(t, err)

	for _, method := range []string{"Chat", "StreamChat"} {
		t.Run(method, func(t *testing.T) {
			home, workdir := t.TempDir(), t.TempDir()
			t.Setenv("CODEX_HOME", home)
			writeRollout(t, home, requested, strings.TrimSpace(string(prior)))
			waited := make(chan struct{})
			runner := &multiRunner{scripts: []runScript{
				compatibleProbe(), {lines: strings.Split(strings.TrimSpace(string(completed)), "\n"), onWait: func() {
					// Both threads prove the model and have a new context. A missing
					// mismatch guard must fail this test by accepting the foreign turn.
					writeRollout(t, home, requested, strings.TrimSpace(string(prior)), ctxLine(model))
					writeRollout(t, home, observed, metaLine(observed), ctxLine(model), ctxLine(model))
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
			req := engine.ChatRequest{Model: model, SessionID: requested, Prompt: "reply ok", WorkDir: workdir}
			checkError := func(err error) {
				t.Helper()
				require.ErrorIs(t, err, ErrModelEvidence)
				assert.NotContains(t, err.Error(), requested)
				assert.NotContains(t, err.Error(), observed)
				assert.NotContains(t, err.Error(), "secret-marker")
			}
			if method == "StreamChat" {
				chunks, err := p.StreamChat(ctx, req)
				require.NoError(t, err)
				var failures int
				for chunk := range chunks {
					assert.False(t, chunk.Done, "a foreign thread must never publish Done")
					assert.Empty(t, chunk.SessionID, "the rejected ID must not escape in a result")
					if chunk.Error != nil {
						failures++
						checkError(chunk.Error)
					}
				}
				assert.Equal(t, 1, failures)
			} else {
				resp, err := p.Chat(ctx, req)
				assert.Nil(t, resp, "a foreign turn must not become an accepted result")
				checkError(err)
			}
			require.NoError(t, ctx.Err(), "refusal must not depend on the deadline")
			select {
			case <-waited:
			default:
				t.Fatal("child was not reaped before the refusal")
			}
			require.Len(t, runner.calls, 2)
			launch := runner.calls[1]
			assert.Equal(t, []string{"exec", "resume", requested}, launch.Args[:3])
			assert.Contains(t, launch.Args, model)
			assert.Equal(t, workdir, launch.Dir)
			assert.Equal(t, req.Prompt, launch.Stdin)
			assert.Contains(t, launch.Env, "CODEX_HOME="+home)
		})
	}
}
