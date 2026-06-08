package procenv_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy path: a positive duration derives a context carrying a deadline.
func TestWithTimeout_PositiveSetsDeadline(t *testing.T) {
	ctx, cancel := procenv.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, ok := ctx.Deadline()
	assert.True(t, ok, "a positive duration must set a deadline")
}

// Edge: a non-positive duration returns the parent ctx unchanged (no deadline)
// with a no-op cancel that is safe to call.
func TestWithTimeout_NonPositiveReturnsParent(t *testing.T) {
	for _, d := range []time.Duration{0, -1 * time.Second} {
		parent := context.Background()
		ctx, cancel := procenv.WithTimeout(parent, d)
		_, ok := ctx.Deadline()
		assert.False(t, ok, "a non-positive duration must not set a deadline (d=%v)", d)
		assert.Equal(t, parent, ctx, "the parent ctx is returned unchanged")
		cancel() // must be a safe no-op
	}
}

// Success: cancelling the returned cancel func cancels the derived context.
func TestWithTimeout_CancelPropagates(t *testing.T) {
	ctx, cancel := procenv.WithTimeout(context.Background(), time.Hour)
	cancel()
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

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

// org-readiness Phase 2: ANTHROPIC_BASE_URL is a NON-SECRET endpoint var, so it
// is on the base allowlist (an operator-set one may be inherited). The KEY var
// stays off the allowlist — it is injected per call only, never inherited.
func TestIsBaseName_AnthropicBaseURLAllowedKeyNot(t *testing.T) {
	assert.True(t, procenv.IsBaseName("ANTHROPIC_BASE_URL"),
		"ANTHROPIC_BASE_URL (non-secret endpoint) must be allowlisted")
	assert.False(t, procenv.IsBaseName("ANTHROPIC_API_KEY"),
		"ANTHROPIC_API_KEY (secret) must NEVER be allowlisted")
}

// A parent ANTHROPIC_BASE_URL is propagated to the child env; a parent
// ANTHROPIC_API_KEY is still stripped (per-call injection only).
func TestBaseEnv_AnthropicBaseURLPassthroughKeyStripped(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://operator.example:4000")
	t.Setenv("ANTHROPIC_API_KEY", "sk-leak")

	joined := strings.Join(procenv.BaseEnv(), "\n")
	assert.Contains(t, joined, "ANTHROPIC_BASE_URL=https://operator.example:4000")
	assert.NotContains(t, joined, "ANTHROPIC_API_KEY")
	assert.NotContains(t, joined, "sk-leak")
}

func TestStandardBinDirs_HomeFirstWhenSet(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	dirs := procenv.StandardBinDirs()
	require.NotEmpty(t, dirs)
	assert.Equal(t, "/testhome/.local/bin", dirs[0])
	assert.Contains(t, dirs, "/usr/local/bin")
}

// RemoveVar removes all entries matching the exact key, preserves order, and
// does not mutate the input slice. Prefix-extended names (e.g.
// ANTHROPIC_BASE_URL_OTHER) survive removal of ANTHROPIC_BASE_URL.
func TestRemoveVar(t *testing.T) {
	t.Run("removes matching entry", func(t *testing.T) {
		got := procenv.RemoveVar([]string{"ANTHROPIC_BASE_URL=https://a", "HOME=/root"}, "ANTHROPIC_BASE_URL")
		assert.Equal(t, []string{"HOME=/root"}, got)
	})
	t.Run("removes all occurrences", func(t *testing.T) {
		got := procenv.RemoveVar([]string{"X=1", "X=2", "Y=3"}, "X")
		assert.Equal(t, []string{"Y=3"}, got)
	})
	t.Run("prefix-extended name survives", func(t *testing.T) {
		got := procenv.RemoveVar([]string{"ANTHROPIC_BASE_URL=a", "ANTHROPIC_BASE_URL_OTHER=b"}, "ANTHROPIC_BASE_URL")
		assert.Equal(t, []string{"ANTHROPIC_BASE_URL_OTHER=b"}, got)
	})
	t.Run("no match returns copy", func(t *testing.T) {
		in := []string{"HOME=/root"}
		got := procenv.RemoveVar(in, "MISSING")
		assert.Equal(t, in, got)
		// Must not be the same slice.
		assert.NotSame(t, &in[0], &got[0])
	})
	t.Run("empty input", func(t *testing.T) {
		got := procenv.RemoveVar(nil, "X")
		assert.Empty(t, got)
	})
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
