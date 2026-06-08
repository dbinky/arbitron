package claudecli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// gatewayBinding builds an agent binding with a base-url and a RevealKey closure.
func gatewayBinding(baseURL, key string, revealErr error) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "litellm",
		Backend:  "claude-code",
		BaseURL:  baseURL,
		Model:    "gpt-4o",
		RevealKey: func(context.Context) (string, error) {
			if revealErr != nil {
				return "", revealErr
			}
			return key, nil
		},
	}
}

func envValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

// happy — base_url + key present: env carries both.
func TestAgentEnv_GatewayInjectsBaseURLAndKey(t *testing.T) {
	b := gatewayBinding("https://litellm.internal:4000", "sk-test", nil)
	env, err := agentEnv(context.Background(), b)
	require.NoError(t, err)

	got, ok := envValue(env, "ANTHROPIC_BASE_URL")
	require.True(t, ok)
	assert.Equal(t, "https://litellm.internal:4000", got)

	gotKey, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, "sk-test", gotKey)
}

// happy — base_url-only (keyless connection): base url, no key var.
func TestAgentEnv_BaseURLOnlyNoKey(t *testing.T) {
	b := gatewayBinding("https://litellm.internal:4000", "", nil)
	env, err := agentEnv(context.Background(), b)
	require.NoError(t, err)

	_, ok := envValue(env, "ANTHROPIC_BASE_URL")
	assert.True(t, ok)
	_, ok = envValue(env, "ANTHROPIC_API_KEY")
	assert.False(t, ok, "no key var when RevealKey yields empty")
}

// subscription — empty connection (no base-url, nil RevealKey): env equals
// procenv.BaseEnv() (no injection) byte-for-byte.
func TestAgentEnv_SubscriptionUnchanged(t *testing.T) {
	b := provider.ResolvedBinding{} // zero value: no BaseURL, nil RevealKey
	env, err := agentEnv(context.Background(), b)
	require.NoError(t, err)

	assert.Equal(t, procenv.BaseEnv(), env, "empty binding must equal the subscription base env")
	_, ok := envValue(env, "ANTHROPIC_BASE_URL")
	assert.False(t, ok)
	_, ok = envValue(env, "ANTHROPIC_API_KEY")
	assert.False(t, ok)
}

// subscription — a non-nil RevealKey that yields "" with no base-url is still
// the subscription path (no injection).
func TestAgentEnv_EmptyRevealNoBaseURLIsSubscription(t *testing.T) {
	b := provider.ResolvedBinding{
		RevealKey: func(context.Context) (string, error) { return "", nil },
	}
	env, err := agentEnv(context.Background(), b)
	require.NoError(t, err)
	assert.Equal(t, procenv.BaseEnv(), env)
}

// error — a RevealKey failure aborts with no partial env (nil, err).
func TestAgentEnv_RevealErrorNoPartialEnv(t *testing.T) {
	b := gatewayBinding("https://litellm.internal:4000", "", errors.New("decrypt boom"))
	env, err := agentEnv(context.Background(), b)
	require.Error(t, err)
	assert.Nil(t, env, "no partial env on RevealKey failure")
	assert.Contains(t, err.Error(), "decrypt boom")
}

// boundary/security — agentEnv never emits two ANTHROPIC_API_KEY entries.
func TestAgentEnv_SingleKeyEntry(t *testing.T) {
	b := gatewayBinding("https://x:4000", "sk-only-once", nil)
	env, err := agentEnv(context.Background(), b)
	require.NoError(t, err)
	count := 0
	for _, e := range env {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			count++
		}
	}
	assert.Equal(t, 1, count, "exactly one ANTHROPIC_API_KEY entry")
}

// security — RunAgent never logs the key, never places it in argv.
func TestAgentAdapter_KeyNeverInArgsOrLogs(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	const secret = "sk-secret-123"
	fr := &fakeRunner{lines: []string{`{"type":"result","result":"ok","session_id":"a"}`}}
	a := NewAgentAdapterForBinding(
		Config{BinaryPath: "claude", Runner: fr},
		gatewayBinding("https://litellm:4000", secret, nil),
	)

	_, err := a.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)

	argsJoined := strings.Join(fr.gotSpec.Args, " ")
	assert.NotContains(t, argsJoined, secret, "key must never appear in argv")
	assert.NotContains(t, buf.String(), secret, "key must never be logged")

	// The key is present ONLY in the env.
	gotKey, ok := envValue(fr.gotSpec.Env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, secret, gotKey)
}

// model flag from binding: RunAgent threads the mission Model (= binding.Model)
// into --model.
func TestAgentAdapter_ModelFlagFromBinding(t *testing.T) {
	fr := &fakeRunner{lines: []string{`{"type":"result","result":"ok","session_id":"a"}`}}
	a := NewAgentAdapterForBinding(
		Config{BinaryPath: "claude", Runner: fr},
		gatewayBinding("https://litellm:4000", "sk", nil),
	)

	mission := engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"}
	mission.Model = "gpt-4o"
	_, err := a.RunAgent(context.Background(), mission)
	require.NoError(t, err)

	val, ok := argValue(fr.gotSpec.Args, "--model")
	require.True(t, ok)
	assert.Equal(t, "gpt-4o", val)
}

// back-compat — NewAgentAdapter (no binding) produces the subscription env.
func TestAgentAdapter_BackCompatSubscriptionEnv(t *testing.T) {
	fr := &fakeRunner{lines: []string{`{"type":"result","result":"ok","session_id":"a"}`}}
	a := NewAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

	_, err := a.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)

	_, ok := envValue(fr.gotSpec.Env, "ANTHROPIC_BASE_URL")
	assert.False(t, ok, "subscription adapter must not inject a base-url")
	_, ok = envValue(fr.gotSpec.Env, "ANTHROPIC_API_KEY")
	assert.False(t, ok, "subscription adapter must not inject a key")
}

// security — when the operator has set ANTHROPIC_BASE_URL in the parent process
// (it is allowlisted, so procenv.BaseEnv() passes it through) and the binding
// also provides a BaseURL, the binding's URL must win and exactly ONE
// ANTHROPIC_BASE_URL entry must appear. A duplicate entry would leave the
// binding URL's precedence undefined (OS/exec implementation-specific).
func TestAgentEnv_BindingBaseURLOverridesOperatorEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://operator-default:4000")

	b := gatewayBinding("https://binding-override:9000", "sk-test", nil)
	env, err := agentEnv(context.Background(), b)
	require.NoError(t, err)

	var vals []string
	for _, e := range env {
		if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
			vals = append(vals, strings.TrimPrefix(e, "ANTHROPIC_BASE_URL="))
		}
	}
	require.Len(t, vals, 1, "exactly one ANTHROPIC_BASE_URL entry; got %v", vals)
	assert.Equal(t, "https://binding-override:9000", vals[0],
		"binding URL must override the operator-set parent env URL")
}

// security — a key-only binding (BaseURL empty, key present) does NOT strip
// an operator-set ANTHROPIC_BASE_URL from the base env: the operator URL is the
// intended gateway endpoint when no per-call override is provided. This verifies
// the removeEnvVar call is correctly scoped to the binding.BaseURL != "" path.
func TestAgentEnv_KeyOnlyInheritsOperatorBaseURL(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://operator-default:4000")

	b := provider.ResolvedBinding{
		RevealKey: func(context.Context) (string, error) { return "sk-org-key", nil },
	}
	env, err := agentEnv(context.Background(), b)
	require.NoError(t, err)

	// Operator URL passes through (not stripped).
	urlVal, ok := envValue(env, "ANTHROPIC_BASE_URL")
	require.True(t, ok, "operator ANTHROPIC_BASE_URL must pass through for key-only binding")
	assert.Equal(t, "https://operator-default:4000", urlVal)

	// Per-call key is injected.
	keyVal, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, "sk-org-key", keyVal)
}

// boundary — removeEnvVar must not strip a var whose name is a prefix of the
// target name (e.g. ANTHROPIC_BASE_URL_EXTRA must survive removal of
// ANTHROPIC_BASE_URL because its key is not "ANTHROPIC_BASE_URL").
func TestRemoveEnvVar_PrefixNotStripped(t *testing.T) {
	input := []string{
		"ANTHROPIC_BASE_URL=https://real",
		"ANTHROPIC_BASE_URL_EXTRA=https://extra",
		"HOME=/root",
	}
	got := removeEnvVar(input, "ANTHROPIC_BASE_URL")
	// The real URL entry is gone.
	for _, e := range got {
		if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
			t.Fatalf("removeEnvVar left %q in the result", e)
		}
	}
	// The prefix-extended entry and HOME survive.
	joined := strings.Join(got, "\n")
	assert.Contains(t, joined, "ANTHROPIC_BASE_URL_EXTRA=https://extra")
	assert.Contains(t, joined, "HOME=/root")
}

// error — a RevealKey failure aborts RunAgent before the runner is invoked
// (no partial-env launch).
func TestAgentAdapter_RevealErrorAbortsBeforeLaunch(t *testing.T) {
	fr := &fakeRunner{lines: []string{`{"type":"result","result":"ok"}`}}
	a := NewAgentAdapterForBinding(
		Config{BinaryPath: "claude", Runner: fr},
		gatewayBinding("https://litellm:4000", "", errors.New("reveal failed")),
	)

	_, err := a.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reveal failed")
	assert.False(t, fr.runCalled, "runner must NOT be invoked when env build fails")
}
