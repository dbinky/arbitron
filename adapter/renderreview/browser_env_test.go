package renderreview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEnvBrowser writes a POSIX script that records, next to itself, only
// whether each sentinel variable reached it (never the values), then exits
// non-zero so the launch fails fast.
func fakeEnvBrowser(t *testing.T) (exe, report string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "fake-chrome")
	report = filepath.Join(dir, "env-report")
	script := `#!/bin/sh
out="$(dirname "$0")/env-report"
: > "$out"
for v in ARBITRON_MASTER_KEY ANTHROPIC_API_KEY OPENAI_API_KEY PATH HOME; do
  if env | grep -q "^$v="; then echo "$v=present" >> "$out"; else echo "$v=absent" >> "$out"; fi
done
exit 3
`
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe, report
}

// SECURITY regression (PR #85 Must 6): the headless browser must not inherit
// the engine's secrets. Removing chromedp.NoInheritEnv (or the explicit Env
// allowlist) from ensureBrowser makes this test fail.
func TestBrowserRenderer_ChildEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "sentinel-master-key-must-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-provider-key-must-not-leak")
	t.Setenv("OPENAI_API_KEY", "sentinel-provider-key-must-not-leak")
	t.Setenv("HOME", t.TempDir())
	exe, report := fakeEnvBrowser(t)

	b, err := NewBrowserRenderer(BrowserConfig{Assets: surfaceOnly(), BrowserPath: exe, ArtifactDir: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	_, err = b.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(), Items: []layout.TextItem{}})
	require.ErrorIs(t, err, ErrBrowserUnavailable)

	raw, err := os.ReadFile(report)
	require.NoError(t, err, "the fake browser never ran")
	got := strings.Fields(string(raw))
	assert.Contains(t, got, "ARBITRON_MASTER_KEY=absent", "master key leaked into the browser environment")
	assert.Contains(t, got, "ANTHROPIC_API_KEY=absent", "provider key leaked into the browser environment")
	assert.Contains(t, got, "OPENAI_API_KEY=absent", "provider key leaked into the browser environment")
	// The allowlist still gives the browser what it needs to start.
	assert.Contains(t, got, "PATH=present")
	assert.Contains(t, got, "HOME=present")
}

func TestBrowserEnv_IsBrowserSpecificAllowlist(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "x")
	t.Setenv("ANTHROPIC_API_KEY", "x")
	t.Setenv("CLAUDE_CONFIG_DIR", "/claude")
	t.Setenv("ANTHROPIC_BASE_URL", "https://example.invalid")
	t.Setenv("TZ", "UTC")
	t.Setenv("LC_ALL", "C")
	names := map[string]bool{}
	for _, e := range browserEnv() {
		name, _, _ := strings.Cut(e, "=")
		names[name] = true
	}
	for _, banned := range []string{"ARBITRON_MASTER_KEY", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "ANTHROPIC_BASE_URL"} {
		assert.False(t, names[banned], banned)
	}
	for _, want := range []string{"PATH", "TZ", "LC_ALL"} {
		assert.True(t, names[want], want)
	}
}
