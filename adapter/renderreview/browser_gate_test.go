package renderreview

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-browser tests are opt-in. Auto-detecting the installed Chrome made every
// plain `go test ./...` launch dozens of Chrome processes. On macOS the full
// Google Chrome.app registers with the Dock on every launch, even headless, and
// a ralph loop's whole-suite gate re-ran them for hours (run 853cf863). They run
// in `make test-render` (ARBITRON_REQUIRE_BROWSER=1).

const noBrowserSentinel = "/nonexistent/arbitron-browser-tests-are-opt-in"

// browserGateEnv records whether the gate is on, captured before the override.
var browserGateEnv = struct{ required bool }{}

func TestMain(m *testing.M) {
	browserGateEnv.required = os.Getenv("ARBITRON_REQUIRE_BROWSER") == "1"
	if !browserGateEnv.required {
		os.Setenv("ARBITRON_LAYOUT_BROWSERPATH", noBrowserSentinel)
	}
	os.Exit(m.Run())
}

// HAPPY — outside the browser gate, nothing in this package can find a browser:
// auto-detection is pinned to a path that never exists.
func TestBrowserGate_PlainRunCannotDetectABrowser(t *testing.T) {
	if browserGateEnv.required {
		t.Skip("the browser gate is on")
	}
	_, err := DetectBrowser(os.Getenv("ARBITRON_LAYOUT_BROWSERPATH"))
	require.ErrorIs(t, err, ErrBrowserUnavailable)
}

// SUCCESS — inside the gate the override is not applied, so a browser resolves.
// Resolution only; launch guarantees stay in the real-browser tests.
func TestBrowserGate_GateResolvesABrowser(t *testing.T) {
	if !browserGateEnv.required {
		t.Skip("the browser gate is off")
	}
	_, err := DetectBrowser(os.Getenv("ARBITRON_LAYOUT_BROWSERPATH"))
	require.NoError(t, err, "the browser gate is on but no browser resolves")
}

// FAILURE — a real-browser test skips (never fails, never launches) outside the gate.
func TestBrowserGate_RequireBrowserSkipsOutsideTheGate(t *testing.T) {
	if browserGateEnv.required {
		t.Skip("the browser gate is on")
	}
	ran := false
	t.Run("inner", func(t *testing.T) {
		requireBrowser(t)
		ran = true
	})
	assert.False(t, ran, "requireBrowser must skip before returning a browser")
}

// EDGE — macOS prefers chrome-headless-shell, which never registers with the Dock,
// over the full Chrome.app; the full app remains a fallback.
func TestBrowserCandidates_DarwinPrefersHeadlessShell(t *testing.T) {
	c := browserCandidates("darwin")
	require.NotEmpty(t, c)
	assert.Equal(t, "chrome-headless-shell", c[0])
	assert.Contains(t, c, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
}

// EDGE — linux also tries the Chrome for Testing headless shell name.
func TestBrowserCandidates_LinuxIncludesHeadlessShell(t *testing.T) {
	assert.Contains(t, browserCandidates("linux"), "chrome-headless-shell")
}

// ERROR — the not-found error points at the headless shell and layout.browserpath.
func TestFindBrowser_NotFoundHintsHeadlessShell(t *testing.T) {
	_, err := findBrowser("", []string{"x"}, func(string) (string, error) { return "", errors.New("not found") })
	require.ErrorIs(t, err, ErrBrowserUnavailable)
	assert.ErrorContains(t, err, "chrome-headless-shell")
	assert.ErrorContains(t, err, "layout.browserpath")
}

// ERROR — the headless shell wins only when it resolves; otherwise detection
// still falls through to Chrome.app.
func TestFindBrowser_HeadlessShellFallsBackToChromeApp(t *testing.T) {
	app := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	lookPath := func(name string) (string, error) {
		if name == app {
			return app, nil
		}
		return "", errors.New("not found")
	}
	p, err := findBrowser("", browserCandidates("darwin"), lookPath)
	require.NoError(t, err)
	assert.Equal(t, app, p)
}
