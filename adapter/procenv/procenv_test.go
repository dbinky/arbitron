package procenv_test

import (
	"os"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseEnv_KeepsAllowlistedDropsOthers(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("ARBITRON_SECRET", "leak-me")
	t.Setenv("ANTHROPIC_API_KEY", "leak-me-too")

	env := procenv.BaseEnv()

	joined := strings.Join(env, "\n")
	assert.Contains(t, joined, "HOME=/home/test")
	assert.NotContains(t, joined, "ARBITRON_SECRET")
	assert.NotContains(t, joined, "ANTHROPIC_API_KEY")
}

func TestBaseEnv_AugmentsPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	env := procenv.BaseEnv()

	var path string
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			path = strings.TrimPrefix(e, "PATH=")
		}
	}
	require.NotEmpty(t, path)
	assert.Contains(t, path, "/usr/bin")
	assert.Contains(t, path, "/usr/local/bin")
}

func TestBaseEnv_KeepsLCPrefix(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	env := procenv.BaseEnv()
	assert.Contains(t, strings.Join(env, "\n"), "LC_ALL=en_US.UTF-8")
}

func TestIsBaseName(t *testing.T) {
	assert.True(t, procenv.IsBaseName("PATH"))
	assert.True(t, procenv.IsBaseName("HOME"))
	assert.True(t, procenv.IsBaseName("LC_CTYPE"))
	assert.False(t, procenv.IsBaseName("ANTHROPIC_API_KEY"))
	assert.False(t, procenv.IsBaseName("LLM_API_KEY"))
}

func TestStandardBinDirs_HomeFirstWhenSet(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	dirs := procenv.StandardBinDirs()
	require.NotEmpty(t, dirs)
	assert.Equal(t, "/testhome/.local/bin", dirs[0])
	assert.Contains(t, dirs, "/usr/local/bin")
}

func TestBaseEnv_ReturnedSliceIsIndependent(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	env := procenv.BaseEnv()
	for i := range env {
		env[i] = "TAMPERED=yes"
	}
	env2 := procenv.BaseEnv()
	assert.Contains(t, strings.Join(env2, "\n"), "HOME=/home/test")
	assert.NotContains(t, strings.Join(env2, "\n"), "TAMPERED")
}

func TestBaseEnv_UnsetParentPath(t *testing.T) {
	orig, had := os.LookupEnv("PATH")
	require.NoError(t, os.Unsetenv("PATH"))
	t.Cleanup(func() {
		if had {
			os.Setenv("PATH", orig)
		}
	})
	env := procenv.BaseEnv()
	var path string
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			path = strings.TrimPrefix(e, "PATH=")
		}
	}
	assert.Contains(t, path, "/usr/local/bin") // standard dirs still present, no leading separator
	assert.False(t, strings.HasPrefix(path, string(os.PathListSeparator)))
}
