package codex

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing the pinned/verified error shaping must expose this stdout-only
// credential at the consumer's error sink. The runner replaces only process I/O;
// each interactive case uses the production exec session and stream folder.
func TestModelProbeCodexPinnedFailuresAreSecretFree(t *testing.T) {
	const secret = "harness-failure-private-account-token"
	exitErr := errors.New("exit status 7")
	for _, event := range []string{
		`{"type":"error","message":"` + secret + `"}`,
		`{"type":"turn.failed","error":{"message":"` + secret + `"}}`,
	} {
		for _, waitErr := range []error{nil, exitErr} {
			for _, mode := range []string{"batch", "chat", "stream"} {
				t.Run(event+"/"+mode+"/"+fmtExit(waitErr), func(t *testing.T) {
					t.Setenv("CODEX_HOME", t.TempDir())
					runner := &multiRunner{scripts: []runScript{
						compatibleProbe(), {lines: []string{event}, waitErr: waitErr},
					}}
					binding := provider.ResolvedBinding{Model: "gpt-5-sol", Pinned: true}
					ctx := context.Background()
					var err error
					switch mode {
					case "batch":
						result, callErr := New(Config{Binding: binding, Runner: runner}).RunAgent(ctx, engine.AgentMission{WorkDir: t.TempDir()})
						assert.Nil(t, result)
						err = callErr
					case "chat":
						result, callErr := NewInteractiveProvider(InteractiveConfig{Binding: binding, Runner: runner}).Chat(ctx, engine.ChatRequest{WorkDir: t.TempDir()})
						assert.Nil(t, result)
						err = callErr
					case "stream":
						chunks, callErr := NewInteractiveProvider(InteractiveConfig{Binding: binding, Runner: runner}).StreamChat(ctx, engine.ChatRequest{WorkDir: t.TempDir()})
						require.NoError(t, callErr)
						for chunk := range chunks {
							assert.False(t, chunk.Done, "a failed turn must never complete")
							if chunk.Error != nil {
								err = chunk.Error
							}
						}
					}
					require.Error(t, err)
					assert.NotContains(t, err.Error(), secret)
					if waitErr != nil {
						assert.ErrorIs(t, err, exitErr, "retain the process failure identity")
					} else {
						assert.ErrorIs(t, err, ErrModelEvidence)
					}
				})
			}
		}
	}
}

func fmtExit(err error) string {
	if err == nil {
		return "clean exit"
	}
	return "nonzero exit"
}
