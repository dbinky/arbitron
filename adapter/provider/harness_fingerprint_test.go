package provider_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type harnessHome struct {
	home, bin string
}

func newHarnessHome(t *testing.T) harnessHome {
	t.Helper()
	h := harnessHome{home: t.TempDir(), bin: t.TempDir()}
	for _, b := range []string{"claude", "codex", "pi", "opencode"} {
		require.NoError(t, os.WriteFile(filepath.Join(h.bin, b), []byte("#!/bin/sh\n"+b+"-v1"), 0o755))
	}
	write := func(rel, body string) {
		p := filepath.Join(h.home, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	}
	write(".claude/.credentials.json", `{"acct":"a"}`)
	write(".codex/auth.json", `{"acct":"a"}`)
	write(".codex/models_cache.json", `{"models":["m1"]}`)
	write(".pi/agent/auth.json", `{"acct":"a"}`)
	write(".local/share/opencode/auth.json", `{"acct":"a"}`)
	return h
}

func (h harnessHome) fingerprinter() adapterprovider.HarnessFingerprinter {
	return adapterprovider.NewHarnessFileFingerprinter(adapterprovider.HarnessFilesConfig{
		Home: h.home,
		Binaries: map[string]string{
			workflow.BackendClaudeCode: filepath.Join(h.bin, "claude"),
			workflow.BackendCodex:      filepath.Join(h.bin, "codex"),
			workflow.BackendPi:         filepath.Join(h.bin, "pi"),
			workflow.BackendOpencode:   filepath.Join(h.bin, "opencode"),
		},
		Getenv: func(string) string { return "" },
	})
}

func TestHarnessFingerprintChangesWithBinaryAndAccountFiles(t *testing.T) {
	h := newHarnessHome(t)
	fp := h.fingerprinter()
	for _, backend := range []string{workflow.BackendClaudeCode, workflow.BackendCodex, workflow.BackendPi, workflow.BackendOpencode} {
		before, err := fp(backend)
		require.NoError(t, err, backend)
		require.NotEmpty(t, before, backend)
		again, err := fp(backend)
		require.NoError(t, err)
		assert.Equal(t, before, again, "%s: unchanged files are a stable fingerprint", backend)
	}

	// Binary upgrade: same path, new content and mtime.
	claude := filepath.Join(h.bin, "claude")
	before, _ := fp(workflow.BackendClaudeCode)
	require.NoError(t, os.WriteFile(claude, []byte("claude-v2-longer"), 0o755))
	require.NoError(t, os.Chtimes(claude, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))
	after, err := fp(workflow.BackendClaudeCode)
	require.NoError(t, err)
	assert.NotEqual(t, before, after, "an upgraded binary invalidates evidence")

	// Account switch: codex auth.json content changes; a sibling backend does not.
	piBefore, _ := fp(workflow.BackendPi)
	codexBefore, _ := fp(workflow.BackendCodex)
	require.NoError(t, os.WriteFile(filepath.Join(h.home, ".codex", "auth.json"), []byte(`{"acct":"b"}`), 0o600))
	codexAfter, err := fp(workflow.BackendCodex)
	require.NoError(t, err)
	assert.NotEqual(t, codexBefore, codexAfter)
	piAfter, _ := fp(workflow.BackendPi)
	assert.Equal(t, piBefore, piAfter, "another harness's account does not alter this one")

	// models_cache edit is covered too.
	codexBefore = codexAfter
	require.NoError(t, os.WriteFile(filepath.Join(h.home, ".codex", "models_cache.json"), []byte(`{"models":["m2"]}`), 0o600))
	codexAfter, _ = fp(workflow.BackendCodex)
	assert.NotEqual(t, codexBefore, codexAfter)
}

func TestHarnessFingerprintAbsentIsAStateNotAnError(t *testing.T) {
	h := newHarnessHome(t)
	fp := h.fingerprinter()
	present, err := fp(workflow.BackendPi)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(h.bin, "pi")))
	require.NoError(t, os.Remove(filepath.Join(h.home, ".pi", "agent", "auth.json")))
	absent, err := fp(workflow.BackendPi)
	require.NoError(t, err)
	assert.NotEmpty(t, absent)
	assert.NotEqual(t, present, absent, "a missing harness differs from an installed one")
}

func TestHarnessFingerprintHonoursCodexHome(t *testing.T) {
	h := newHarnessHome(t)
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, "auth.json"), []byte(`{"acct":"z"}`), 0o600))
	fp := adapterprovider.NewHarnessFileFingerprinter(adapterprovider.HarnessFilesConfig{
		Home:     h.home,
		Binaries: map[string]string{workflow.BackendCodex: filepath.Join(h.bin, "codex")},
		Getenv: func(k string) string {
			if k == "CODEX_HOME" {
				return other
			}
			return ""
		},
	})
	viaHome, err := fp(workflow.BackendCodex)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(other, "auth.json"), []byte(`{"acct":"y"}`), 0o600))
	changed, err := fp(workflow.BackendCodex)
	require.NoError(t, err)
	assert.NotEqual(t, viaHome, changed, "CODEX_HOME's auth.json is the account file, not ~/.codex")
}

func TestHarnessFingerprintUnreadableFileIsAnError(t *testing.T) {
	h := newHarnessHome(t)
	fp := h.fingerprinter()
	// A directory where the account file should be: stat succeeds, read fails.
	p := filepath.Join(h.home, ".claude", ".credentials.json")
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.Mkdir(p, 0o755))

	b, err := fp(workflow.BackendClaudeCode)

	assert.Error(t, err)
	assert.Empty(t, b, "an unreadable input never yields a fingerprint")
}

func TestHarnessFingerprintUnknownBackendHasNoHarness(t *testing.T) {
	fp := newHarnessHome(t).fingerprinter()

	b, err := fp("")
	assert.NoError(t, err)
	assert.Empty(t, b)
	b, err = fp("bogus")
	assert.NoError(t, err)
	assert.Empty(t, b, "the runtime fails the node; the inventory must not block on it")
}

// Claude reads project settings and MCP servers from its working directory;
// each binds the Claude revision, and no other backend's.
func TestHarnessFingerprintClaudeProjectInputs(t *testing.T) {
	h := newHarnessHome(t)
	project := t.TempDir()
	fp := adapterprovider.NewHarnessFileFingerprinter(adapterprovider.HarnessFilesConfig{
		Home:       h.home,
		Binaries:   map[string]string{workflow.BackendClaudeCode: filepath.Join(h.bin, "claude"), workflow.BackendCodex: filepath.Join(h.bin, "codex")},
		Getenv:     func(string) string { return "" },
		ProjectDir: project,
	})
	for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json", ".mcp.json"} {
		claude, err := fp(workflow.BackendClaudeCode)
		require.NoError(t, err)
		codex, err := fp(workflow.BackendCodex)
		require.NoError(t, err)
		p := filepath.Join(project, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://other.invalid"}}`), 0o600))
		after, err := fp(workflow.BackendClaudeCode)
		require.NoError(t, err)
		assert.NotEqual(t, claude, after, "%s must bind the Claude revision", rel)
		codexAfter, err := fp(workflow.BackendCodex)
		require.NoError(t, err)
		assert.Equal(t, codex, codexAfter, "%s is not a Codex input", rel)
	}
}
