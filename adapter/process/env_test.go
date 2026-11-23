package process

import (
	"os"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildEnv composes server-side resolution and executing-machine construction.
func buildEnv(passthrough []string, nodeEnv map[string]string, vars *variable.VariableStore) ([]string, error) {
	if err := validateNodeEnvNames(passthrough, nodeEnv); err != nil {
		return nil, err
	}
	return buildEnvFromResolved(passthrough, resolveNodeEnv(nodeEnv, vars))
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	return m
}

func TestBuildEnv_BasePresent_SecretsAbsent(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("MY_SECRET", "leak")
	env, err := buildEnv(nil, nil, variable.NewStore())
	require.NoError(t, err)
	m := envMap(env)
	assert.Equal(t, "/home/test", m["HOME"])
	_, leaked := m["MY_SECRET"]
	assert.False(t, leaked, "non-allowlisted engine var must not leak")
}

func TestBuildEnv_PassthroughPresentWhenSet(t *testing.T) {
	t.Setenv("LLM_API_KEY", "sk-123")
	env, err := buildEnv([]string{"LLM_API_KEY"}, nil, variable.NewStore())
	require.NoError(t, err)
	assert.Equal(t, "sk-123", envMap(env)["LLM_API_KEY"])
}

func TestBuildEnv_PassthroughAbsentWhenUnset(t *testing.T) {
	if orig, had := os.LookupEnv("LLM_ROUTER_URL"); had {
		t.Cleanup(func() { os.Setenv("LLM_ROUTER_URL", orig) })
	}
	os.Unsetenv("LLM_ROUTER_URL")
	env, err := buildEnv([]string{"LLM_ROUTER_URL"}, nil, variable.NewStore())
	require.NoError(t, err)
	_, ok := envMap(env)["LLM_ROUTER_URL"]
	assert.False(t, ok, "unset passthrough must not inject an empty var")
}

func TestBuildEnv_PerNodeEnvSubstituted(t *testing.T) {
	vars := variable.NewStore()
	require.NoError(t, vars.SetInput("model", "opus"))
	env, err := buildEnv(nil, map[string]string{"LLM_MODEL": "{{model}}"}, vars)
	require.NoError(t, err)
	assert.Equal(t, "opus", envMap(env)["LLM_MODEL"])
}

func TestBuildEnv_RejectsCollisionWithBase(t *testing.T) {
	_, err := buildEnv(nil, map[string]string{"PATH": "/evil"}, variable.NewStore())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}

func TestBuildEnv_RejectsCollisionWithPassthrough(t *testing.T) {
	_, err := buildEnv([]string{"LLM_API_KEY"}, map[string]string{"LLM_API_KEY": "fake"}, variable.NewStore())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}

func TestBuildEnv_RejectsCollisionWithLCPrefix(t *testing.T) {
	_, err := buildEnv(nil, map[string]string{"LC_ALL": "en_US.UTF-8"}, variable.NewStore())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}

func TestBuildEnv_PassthroughCannotShadowBase(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	// Operator mistakenly lists PATH as passthrough; it must NOT produce a
	// second PATH entry that overrides the augmented base PATH.
	env, err := buildEnv([]string{"PATH"}, nil, variable.NewStore())
	require.NoError(t, err)
	count := 0
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			count++
		}
	}
	assert.Equal(t, 1, count, "PATH must appear exactly once (base only, not shadowed by passthrough)")
	// And it must be the augmented base PATH (contains a standard bin dir).
	m := envMap(env)
	assert.Contains(t, m["PATH"], "/usr/local/bin")
}
