package claudecli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
)

func TestBuildArgs(t *testing.T) {
	t.Run("base args are always present (skip-permissions off, no allowed tools)", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.Equal(t, []string{
			"--print", "--verbose", "--output-format", "stream-json",
		}, args)
	})

	t.Run("skip-permissions emits --dangerously-skip-permissions when true", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{SkipPermissions: true})
		assert.Contains(t, args, "--dangerously-skip-permissions")
	})

	t.Run("skip-permissions omits the flag when false", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{SkipPermissions: false})
		assert.NotContains(t, args, "--dangerously-skip-permissions")
	})

	t.Run("allowed tools are forwarded as a comma-joined --allowedTools", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{AllowedTools: []string{"Read", "Bash(git status:*)"}})
		assert.Contains(t, args, "--allowedTools")
		for i, a := range args {
			if a == "--allowedTools" {
				assert.Equal(t, "Read,Bash(git status:*)", args[i+1])
				return
			}
		}
		t.Fatal("--allowedTools not found")
	})

	t.Run("empty allowed tools omits the flag", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.NotContains(t, args, "--allowedTools")
	})

	t.Run("request model overrides default model", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{Model: "claude-opus-4-7"}, argsPolicy{DefaultModel: "claude-sonnet-4-6"})
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-opus-4-7")
		assert.NotContains(t, args, "claude-sonnet-4-6")
	})

	t.Run("default model used when request model is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{DefaultModel: "claude-sonnet-4-6"})
		assert.Contains(t, args, "--model")
		assert.Contains(t, args, "claude-sonnet-4-6")
	})

	t.Run("no --model flag when both are empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.NotContains(t, args, "--model")
	})

	t.Run("session id adds --resume", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SessionID: "sess-9"}, argsPolicy{})
		assert.Contains(t, args, "--resume")
		assert.Contains(t, args, "sess-9")
	})

	t.Run("no --resume when session id is empty", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{}, argsPolicy{})
		assert.NotContains(t, args, "--resume")
	})

	t.Run("system prompt adds --append-system-prompt", func(t *testing.T) {
		args := buildArgs(engine.LLMRequest{SystemPrompt: "be terse"}, argsPolicy{})
		assert.Contains(t, args, "--append-system-prompt")
		assert.Contains(t, args, "be terse")
	})
}

func TestAugmentedEnv(t *testing.T) {
	t.Run("appends a standard dir even when a substring-prefix dir is present", func(t *testing.T) {
		// PATH contains /usr/local/bin-custom, which substring-contains
		// "/usr/local/bin" but is NOT the same entry.
		t.Setenv("PATH", "/usr/local/bin-custom")
		env := procenv.BaseEnv()

		var pathVal string
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				pathVal = strings.TrimPrefix(e, "PATH=")
			}
		}
		entries := filepath.SplitList(pathVal)
		assert.Contains(t, entries, "/usr/local/bin",
			"/usr/local/bin must be added as a distinct entry")
		assert.Contains(t, entries, "/usr/local/bin-custom")
	})

	t.Run("does not duplicate a dir already present as an exact entry", func(t *testing.T) {
		t.Setenv("PATH", "/usr/local/bin")
		env := procenv.BaseEnv()

		var pathVal string
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				pathVal = strings.TrimPrefix(e, "PATH=")
			}
		}
		count := 0
		for _, p := range filepath.SplitList(pathVal) {
			if p == "/usr/local/bin" {
				count++
			}
		}
		assert.Equal(t, 1, count, "/usr/local/bin must appear exactly once")
	})

	t.Run("only the allowlisted env vars are propagated", func(t *testing.T) {
		// SECURITY: PR #8 thread 3291942736. Wholesale os.Environ() leaks
		// every parent-process secret to the agent subprocess. Only the
		// allowlisted vars survive.
		t.Setenv("ANTHROPIC_API_KEY", "should-be-stripped")
		t.Setenv("GITHUB_TOKEN", "should-be-stripped")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "should-be-stripped")
		t.Setenv("ARBITRON_INTERNAL", "should-be-stripped")
		t.Setenv("PATH", "/usr/bin")
		t.Setenv("HOME", "/home/test")
		t.Setenv("LC_CTYPE", "en_US.UTF-8")

		env := procenv.BaseEnv()

		joined := strings.Join(env, "\n")
		assert.NotContains(t, joined, "should-be-stripped",
			"no non-allowlisted env value may appear in the propagated env")

		// Allowlisted vars MUST be present (so the agent can find its
		// home dir, locale, etc.).
		var keys []string
		for _, e := range env {
			if i := strings.IndexByte(e, '='); i > 0 {
				keys = append(keys, e[:i])
			}
		}
		assert.Contains(t, keys, "PATH")
		assert.Contains(t, keys, "HOME")
		assert.Contains(t, keys, "LC_CTYPE")
	})

	t.Run("LC_ prefixed variables are allowlisted by prefix", func(t *testing.T) {
		t.Setenv("LC_TIME", "en_US.UTF-8")
		t.Setenv("LC_MESSAGES", "en_US.UTF-8")
		env := procenv.BaseEnv()

		var keys []string
		for _, e := range env {
			if i := strings.IndexByte(e, '='); i > 0 {
				keys = append(keys, e[:i])
			}
		}
		assert.Contains(t, keys, "LC_TIME")
		assert.Contains(t, keys, "LC_MESSAGES")
	})

	t.Run("synthesises a PATH entry when none exists in parent env", func(t *testing.T) {
		// SECURITY thread 3291907740: even if the parent never had PATH, the
		// agent still needs one for git/gh.
		t.Setenv("PATH", "")
		env := procenv.BaseEnv()

		var pathVal string
		var pathCount int
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				pathVal = strings.TrimPrefix(e, "PATH=")
				pathCount++
			}
		}
		assert.Equal(t, 1, pathCount, "PATH must be set exactly once")
		assert.NotEmpty(t, pathVal, "synthesised PATH must contain standard bin dirs")
	})
}

func TestResolveBinary(t *testing.T) {
	t.Run("explicit path is returned unchanged", func(t *testing.T) {
		assert.Equal(t, "/custom/claude", resolveBinary("/custom/claude"))
	})

	t.Run("empty path resolves to a non-empty candidate", func(t *testing.T) {
		assert.NotEmpty(t, resolveBinary(""))
	})
}

func TestResolveBinaryWith(t *testing.T) {
	// SF4: resolveBinaryWith takes lookPath + stat as deps so the resolution
	// chain is testable without touching the real filesystem or PATH.
	t.Run("explicit path bypasses lookPath (stat used only for the writable check)", func(t *testing.T) {
		got := resolveBinaryWith(
			"/explicit/claude",
			func(string) (string, error) { t.Fatal("lookPath called"); return "", nil },
			// Return ErrNotExist so the writable check is a no-op and the
			// explicit path is returned unchanged.
			func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		)
		assert.Equal(t, "/explicit/claude", got)
	})

	t.Run("falls back to lookPath when explicit is empty", func(t *testing.T) {
		got := resolveBinaryWith(
			"",
			func(name string) (string, error) {
				assert.Equal(t, "claude", name)
				return "/from/lookpath/claude", nil
			},
			func(string) (os.FileInfo, error) { t.Fatal("stat called"); return nil, nil },
		)
		assert.Equal(t, "/from/lookpath/claude", got)
	})

	t.Run("falls back to procenv.StandardBinDirs when lookPath fails", func(t *testing.T) {
		// First stat call should succeed (any non-nil error indicates not found
		// in production via the os.Stat default). Return a not-found error
		// for all but the second candidate.
		statCalls := 0
		got := resolveBinaryWith(
			"",
			func(string) (string, error) { return "", errors.New("not in PATH") },
			func(string) (os.FileInfo, error) {
				statCalls++
				if statCalls == 2 {
					return nil, nil
				}
				return nil, os.ErrNotExist
			},
		)
		// Second candidate wins; do not assert exact path (depends on
		// procenv.StandardBinDirs order at runtime).
		assert.NotEqual(t, "claude", got, "should return resolved path, not bare default")
		assert.True(t, statCalls >= 2)
	})

	t.Run("returns bare 'claude' when nothing resolves", func(t *testing.T) {
		got := resolveBinaryWith(
			"",
			func(string) (string, error) { return "", errors.New("nope") },
			func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		)
		assert.Equal(t, "claude", got)
	})

	t.Run("explicit path with world-writable mode still resolves (warning logged)", func(t *testing.T) {
		// Consider 2 / thread 3291945444: an explicit BinaryPath that is
		// group/world-writable is accepted (operator may have a legitimate
		// reason) but a slog.Warn is emitted. We verify behaviour by stat
		// returning a mode with world-writable bits set — the call must still
		// return the explicit path.
		got := resolveBinaryWith(
			"/tmp/sketchy/claude",
			func(string) (string, error) { t.Fatal("lookPath called"); return "", nil },
			func(string) (os.FileInfo, error) {
				return fakeFileInfo{mode: 0o777}, nil
			},
		)
		assert.Equal(t, "/tmp/sketchy/claude", got)
	})
}

// fakeFileInfo is a minimal os.FileInfo for tests that only need Mode().
type fakeFileInfo struct {
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return "" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
