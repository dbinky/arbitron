package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelProbeCodexRolloutErrorsAreSecretFree(t *testing.T) {
	const secret = "account-access-token-not-for-errors"
	const pin = "gpt-5-sol"
	for _, phase := range []string{"before resume", "after turn"} {
		for _, field := range []string{"model", "session", "thread", "missing rollout", "ambiguous rollout", "unreadable rollout", "directory rollout", "no contexts", "no new contexts"} {
			if phase == "before resume" && field == "no new contexts" {
				continue
			}
			t.Run(phase+"/"+field, func(t *testing.T) {
				home := t.TempDir()
				thread := rolloutThread
				lines := []string{metaLine(thread), ctxLine(pin)}
				switch field {
				case "model":
					lines[1] = ctxLine(secret)
				case "session":
					lines[0] = metaLine(secret)
				case "thread":
					thread = secret + "/invalid"
				case "missing rollout", "ambiguous rollout", "unreadable rollout", "directory rollout", "no contexts", "no new contexts":
					thread = secret // A syntactically valid ID is still untrusted.
					lines = []string{metaLine(thread), ctxLine(pin)}
					if field == "no contexts" {
						lines = lines[:1]
					}
				}
				if field != "missing rollout" && field != "thread" {
					path := writeRollout(t, home, thread, lines...)
					switch field {
					case "ambiguous rollout":
						duplicate := filepath.Join(filepath.Dir(path), "rollout-other-"+thread+".jsonl")
						require.NoError(t, os.WriteFile(duplicate, []byte("{}\n"), 0o600))
					case "unreadable rollout":
						require.NoError(t, os.Remove(path))
						require.NoError(t, os.Symlink(filepath.Join(home, "missing"), path))
					case "directory rollout":
						require.NoError(t, os.Remove(path))
						require.NoError(t, os.Mkdir(path, 0o700))
					}
				}
				v := &rolloutVerifier{home: home}
				var err error
				if phase == "before resume" {
					_, err = v.beforeResume(thread, pin)
				} else {
					priorTurns := 0
					if field == "no new contexts" {
						priorTurns = 1
					}
					err = v.afterTurn(thread, pin, priorTurns)
				}
				require.ErrorIs(t, err, ErrModelEvidence)
				assert.NotContains(t, err.Error(), secret)
				if field == "model" {
					assert.Contains(t, err.Error(), pin, "retain the trusted requested-model diagnostic")
				}

				// A refusal must not poison later exact evidence on the same account.
				writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine(pin))
				n, err := v.beforeResume(rolloutThread, pin)
				require.NoError(t, err)
				assert.Equal(t, 1, n)
				require.NoError(t, v.afterTurn(rolloutThread, pin, 0))
			})
		}
	}
}
