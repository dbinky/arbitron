package codex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/procenv"
)

// envValue returns the value of name=... in env, and whether it was present.
func envValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

// SECURITY (the defining codex invariant) — the subscription env carries NO
// OPENAI_API_KEY. codex authenticates via its on-disk subscription credential,
// not an injected key; an OPENAI_API_KEY here would be a credential the operator
// never configured (and a divergence from the ChatGPT-subscription auth path).
func TestCodexEnv_NoOpenAIAPIKey(t *testing.T) {
	// Even if the engine itself was started with an OPENAI_API_KEY in its env,
	// the subscription subprocess env must NOT carry it (BaseEnv strips it; we
	// never inject it).
	t.Setenv("OPENAI_API_KEY", "sk-should-be-stripped")
	env := codexEnv()
	_, present := envValue(env, "OPENAI_API_KEY")
	assert.False(t, present, "codex subscription env must contain NO OPENAI_API_KEY")
}

// SECURITY — HOME from the base allowlist is preserved so codex resolves its own
// ~/.codex/auth.json subscription credential.
func TestCodexEnv_HomePreserved(t *testing.T) {
	t.Setenv("HOME", "/home/codex-user")
	env := codexEnv()
	home, ok := envValue(env, "HOME")
	require.True(t, ok, "HOME must be preserved in the subscription env")
	assert.Equal(t, "/home/codex-user", home)
}

// HAPPY — CODEX_HOME is forwarded when the operator set it in the parent env, so
// the subprocess resolves $CODEX_HOME/auth.json (an explicit relocation of the
// default ~/.codex). It is NOT part of the procenv base allowlist, so codexEnv
// forwards it explicitly.
func TestCodexEnv_CodexHomeForwardedWhenSet(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex/home")
	env := codexEnv()
	v, ok := envValue(env, "CODEX_HOME")
	require.True(t, ok, "CODEX_HOME must be forwarded when set in the parent env")
	assert.Equal(t, "/custom/codex/home", v)
}

// EDGE — CODEX_HOME is NOT synthesized when the operator did not set it (we only
// forward an existing value, never invent one).
func TestCodexEnv_CodexHomeAbsentWhenUnset(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	env := codexEnv()
	_, ok := envValue(env, "CODEX_HOME")
	assert.False(t, ok, "absent CODEX_HOME must not be synthesized")
}

// EDGE — a CODEX_HOME value with surrounding whitespace is trimmed before
// forwarding (consistent with the trim-then-use convention in args.go). Uses the
// local `home` variable in the if-block, not a second os.Getenv call.
func TestCodexEnv_CodexHomeWithSurroundingWhitespaceTrimmed(t *testing.T) {
	t.Setenv("CODEX_HOME", "  /custom/codex/home  ")
	env := codexEnv()
	v, ok := envValue(env, "CODEX_HOME")
	require.True(t, ok, "CODEX_HOME with surrounding whitespace must still be forwarded")
	assert.Equal(t, "/custom/codex/home", v, "forwarded CODEX_HOME value must be trimmed")
}

// EDGE — a whitespace-only CODEX_HOME is treated as unset (not forwarded),
// consistent with the whitespace-only model rejection in args.go.
func TestCodexEnv_CodexHomeWhitespaceOnlyNotForwarded(t *testing.T) {
	t.Setenv("CODEX_HOME", "   ")
	env := codexEnv()
	_, ok := envValue(env, "CODEX_HOME")
	assert.False(t, ok, "whitespace-only CODEX_HOME must not be forwarded (treated as unset)")
}

// SECURITY — the base allowlist PATH augmentation is preserved (StandardBinDirs
// reachable), so codex can find git/gh inside the workspace.
func TestCodexEnv_PreservesBaseEnvPATH(t *testing.T) {
	env := codexEnv()
	path, ok := envValue(env, "PATH")
	require.True(t, ok, "PATH must be present (BaseEnv augmentation)")
	for _, dir := range procenv.StandardBinDirs() {
		assert.Contains(t, path, dir, "StandardBinDirs must be on the augmented PATH")
	}
}
