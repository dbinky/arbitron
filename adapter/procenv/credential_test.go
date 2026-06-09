package procenv_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errReveal = errors.New("reveal boom")

// CredentialEnv is the single shared fail-closed skeleton behind the claudecli
// and opencode credential injection. These tests pin the security-relevant
// invariants so the two runners cannot drift: a base-name collision on the key
// var is rejected, and a RevealKey error never emits a partial env.
func TestCredentialEnv_FailClosedInvariants(t *testing.T) {
	tests := []struct {
		name        string
		spec        procenv.CredentialSpec
		wantErr     bool
		errContains string
	}{
		{
			name: "key var base-name collision rejected",
			spec: procenv.CredentialSpec{
				Runner: "claudecli",
				KeyVar: "PATH", // PATH is on the base allowlist.
				Reveal: func(context.Context) (string, error) { return "sk-real", nil },
			},
			wantErr:     true,
			errContains: "shadows the base allowlist",
		},
		{
			name: "extra guarded var base-name collision rejected",
			spec: procenv.CredentialSpec{
				Runner:           "opencode",
				KeyVar:           "AWS_SECRET_ACCESS_KEY",
				ExtraGuardedVars: []string{"HOME"}, // HOME is allowlisted.
				Reveal:           func(context.Context) (string, error) { return "sk-real", nil },
			},
			wantErr:     true,
			errContains: "shadows the base allowlist",
		},
		{
			name: "RevealKey error emits no partial env (fail closed)",
			spec: procenv.CredentialSpec{
				Runner:     "claudecli",
				KeyVar:     "ANTHROPIC_API_KEY",
				BaseURLVar: "ANTHROPIC_BASE_URL",
				BaseURL:    "https://gateway.example",
				Reveal:     func(context.Context) (string, error) { return "", errReveal },
			},
			wantErr: true,
		},
		{
			name: "empty key rejected when policy is EmptyKeyReject",
			spec: procenv.CredentialSpec{
				Runner:   "opencode",
				KeyVar:   "OPENAI_API_KEY",
				Reveal:   func(context.Context) (string, error) { return "", nil },
				EmptyKey: procenv.EmptyKeyReject,
			},
			wantErr:     true,
			errContains: "requires a key",
		},
		{
			name: "keyless launch without BaseURL rejected",
			spec: procenv.CredentialSpec{
				Runner:                    "opencode",
				KeyVar:                    "OLLAMA_API_KEY",
				EmptyKey:                  procenv.EmptyKeyAllowBaseURLOnly,
				RequireBaseURLWhenKeyless: true,
			},
			wantErr:     true,
			errContains: "requires a BaseURL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := procenv.CredentialEnv(context.Background(), tc.spec)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, env, "no partial env must be emitted on error")
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

// Happy paths: the secret lands only in the returned child env (never elsewhere)
// and the base-URL var is strip-then-appended so the child sees exactly one.
func TestCredentialEnv_HappyPaths(t *testing.T) {
	t.Run("key and base-url injected", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "https://operator.example") // inherited
		env, err := procenv.CredentialEnv(context.Background(), procenv.CredentialSpec{
			Runner:     "claudecli",
			KeyVar:     "ANTHROPIC_API_KEY",
			BaseURLVar: "ANTHROPIC_BASE_URL",
			BaseURL:    "https://gateway.example",
			Reveal:     func(context.Context) (string, error) { return "sk-real", nil },
		})
		require.NoError(t, err)
		assert.Contains(t, env, "ANTHROPIC_API_KEY=sk-real")
		assert.Contains(t, env, "ANTHROPIC_BASE_URL=https://gateway.example")
		// Exactly one base-URL entry (inherited one stripped).
		var count int
		for _, e := range env {
			if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
				count++
			}
		}
		assert.Equal(t, 1, count, "the inherited base-URL must be replaced, not duplicated")
	})

	t.Run("subscription path: no key, no base-url, base env unchanged", func(t *testing.T) {
		env, err := procenv.CredentialEnv(context.Background(), procenv.CredentialSpec{
			Runner:     "claudecli",
			KeyVar:     "ANTHROPIC_API_KEY",
			BaseURLVar: "ANTHROPIC_BASE_URL",
			EmptyKey:   procenv.EmptyKeyAllowBaseURLOnly,
			// no Reveal, no BaseURL
		})
		require.NoError(t, err)
		assert.Equal(t, procenv.BaseEnv(), env)
	})

	t.Run("keyless with base-url only", func(t *testing.T) {
		env, err := procenv.CredentialEnv(context.Background(), procenv.CredentialSpec{
			Runner:                    "opencode",
			KeyVar:                    "OLLAMA_API_KEY",
			BaseURLVar:                "OLLAMA_BASE_URL",
			BaseURL:                   "http://localhost:11434",
			EmptyKey:                  procenv.EmptyKeyAllowBaseURLOnly,
			RequireBaseURLWhenKeyless: true,
		})
		require.NoError(t, err)
		assert.Contains(t, env, "OLLAMA_BASE_URL=http://localhost:11434")
		for _, e := range env {
			assert.False(t, strings.HasPrefix(e, "OLLAMA_API_KEY="), "no key var when keyless")
		}
	})
}
