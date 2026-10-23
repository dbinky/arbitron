package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-browser tests are opt-in, and so is every browser the servers these tests
// boot could launch: saving a workflow queues a layout-arrangement job whose
// render stage auto-detects Chrome. A plain `go test ./...` launched Chrome for
// hours in a ralph loop's whole-suite gate (run 853cf863). The browser-required
// suites run in `make test-render` (ARBITRON_REQUIRE_BROWSER=1).

const noBrowserSentinel = "/nonexistent/arbitron-browser-tests-are-opt-in"

var browserGateRequired bool

func TestMain(m *testing.M) {
	// Root tests observe a settled first discovery cycle after Start, as they
	// did when Start ran it synchronously. Production never waits (see
	// defaultAwaitInitialDiscovery); startup_discovery_test.go disables the wait
	// on its own stacks to prove readiness does not depend on discovery.
	defaultAwaitInitialDiscovery = true
	browserGateRequired = os.Getenv("ARBITRON_REQUIRE_BROWSER") == "1"
	if !browserGateRequired {
		// Env outranks every test's config file, so no booted server can find one.
		os.Setenv("ARBITRON_LAYOUT_BROWSERPATH", noBrowserSentinel)
	}
	os.Exit(m.Run())
}

// HAPPY — outside the gate the pinned browser path never resolves.
func TestBrowserGate_SentinelIsUndetectable(t *testing.T) {
	if browserGateRequired {
		t.Skip("the browser gate is on")
	}
	_, err := renderreview.DetectBrowser(os.Getenv("ARBITRON_LAYOUT_BROWSERPATH"))
	require.ErrorIs(t, err, renderreview.ErrBrowserUnavailable)
}

// EDGE — the inherited sentinel beats a config file's browser path on the way
// from config.Load to the production renderer, so a booted server never runs
// the configured executable.
func TestBrowserGate_ConfigLoadFeedsSentinelToRenderer(t *testing.T) {
	if browserGateRequired {
		t.Skip("the browser gate is on")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable recorder")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "launched")
	recorder := filepath.Join(dir, "recorder.sh")
	require.NoError(t, os.WriteFile(recorder, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755))
	configPath := filepath.Join(dir, "config.yaml")
	yaml := "database:\n  path: " + filepath.Join(dir, "arbitron.db") + "\n" +
		"workspace:\n  path: " + filepath.Join(dir, "workspace") + "\n" +
		"layout:\n  browserpath: " + recorder + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yaml), 0o600))

	cfg, err := configadapter.Load(configPath)
	require.NoError(t, err)
	require.Equal(t, noBrowserSentinel, cfg.Layout.BrowserPath)

	renderer, _, closeFn, err := newLayoutRenderReviewer(cfg, layoutE2EDiscardLogger())
	require.NoError(t, err)
	t.Cleanup(closeFn)
	_, err = renderer.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(), Items: []layout.TextItem{{Key: "k", Text: "Start", Style: layout.StyleNodeLabel, MaxWidth: 240}}})
	require.ErrorIs(t, err, renderreview.ErrBrowserUnavailable)
	assert.NoFileExists(t, marker, "the configured browser must never be executed outside the gate")
}

// FAILURE — a real-browser e2e test skips outside the gate instead of launching.
func TestBrowserGate_RequireBrowserE2ESkipsOutsideTheGate(t *testing.T) {
	if browserGateRequired {
		t.Skip("the browser gate is on")
	}
	ran := false
	t.Run("inner", func(t *testing.T) {
		requireBrowserE2E(t)
		ran = true
	})
	assert.False(t, ran)
}
