package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderWiringConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "arbitron.db")
	cfg.Workspace.Path = t.TempDir()
	cfg.Layout.MaxAttempts = 5
	return cfg
}

func TestLayoutArtifactDir(t *testing.T) {
	cfg := renderWiringConfig(t)
	assert.Equal(t, filepath.Join(filepath.Dir(cfg.Database.Path), "layout-artifacts"), layoutArtifactDir(cfg))
	cfg.Layout.ArtifactDir = filepath.Join(t.TempDir(), "evidence")
	assert.Equal(t, cfg.Layout.ArtifactDir, layoutArtifactDir(cfg))
}

func TestLayoutRenderAssets_IsTheEmbeddedBuildWithTheRenderSurface(t *testing.T) {
	_, err := fs.Stat(layoutRenderAssets(), "render.html")
	require.NoError(t, err, "production assets must contain render.html; run make ui")
	_, err = fs.Stat(dashboard.Assets(), "render.html")
	require.NoError(t, err)
}

func TestNewLayoutRenderReviewer_InstallsTheBrowserAdapter(t *testing.T) {
	cfg := renderWiringConfig(t)
	cfg.Layout.BrowserPath = filepath.Join(t.TempDir(), "no-such-chromium")
	renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, layoutE2EDiscardLogger())
	require.NoError(t, err, "missing browser must not fail boot")
	require.NotNil(t, closeFn)
	t.Cleanup(closeFn)
	require.IsType(t, &renderreview.BrowserRenderer{}, renderer)
	require.IsType(t, &renderreview.ReadabilityReviewer{}, reviewer)
	_, err = renderer.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(), Items: []layout.TextItem{{Key: "k", Text: "Start", Style: layout.StyleNodeLabel, MaxWidth: 240}}})
	require.ErrorIs(t, err, renderreview.ErrBrowserUnavailable)
	closeFn()
	closeFn()
}

func TestNewLayoutRenderReviewer_BrokenBuildFailsBoot(t *testing.T) {
	prev := layoutRenderAssets
	t.Cleanup(func() { layoutRenderAssets = prev })
	layoutRenderAssets = func() fs.FS { return fstest.MapFS{"index.html": {Data: []byte("<!DOCTYPE html>")}} }
	_, _, _, err := newLayoutRenderReviewer(renderWiringConfig(t), layoutE2EDiscardLogger())
	require.ErrorContains(t, err, "render.html")
}

// The executable is a real Chrome producer behind a sandbox-policy gate. If
// composition-root NoSandbox feeding disappears, even otherwise valid text
// measurements fail; the false-policy control must fail at the same sink.
func TestNewLayoutRenderReviewer_RealBrowserNoSandboxIsFed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable wrapper")
	}
	browser := requireBrowserE2E(t)
	wrapper := filepath.Join(t.TempDir(), "chrome-policy")
	script := `#!/bin/sh
case " $* " in
  *" --no-sandbox "*) ;;
  *) echo 'sandbox policy not fed' >&2; exit 1 ;;
esac
exec '` + strings.ReplaceAll(browser, "'", "'\\''") + `' "$@"
`
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
	for _, noSandbox := range []bool{true, false} {
		t.Run(strconv.FormatBool(noSandbox), func(t *testing.T) {
			cfg := renderWiringConfig(t)
			cfg.Layout.BrowserPath = wrapper
			cfg.Layout.BrowserNoSandbox = noSandbox
			renderer, _, closeFn, err := newLayoutRenderReviewer(cfg, layoutE2EDiscardLogger())
			require.NoError(t, err)
			t.Cleanup(closeFn)
			m, err := renderer.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(), Items: []layout.TextItem{{Key: "label", Text: "Actual browser text", Style: layout.StyleNodeLabel, MaxWidth: 240}}})
			if !noSandbox {
				require.ErrorIs(t, err, renderreview.ErrBrowserUnavailable)
				assert.Empty(t, m.Items)
				return
			}
			require.NoError(t, err)
			assert.True(t, m.FontsReady)
			require.Contains(t, m.Items, "label")
			assert.Positive(t, m.Items["label"].Width)
		})
	}
}

func TestNewLayoutRenderReviewer_RequiresConfig(t *testing.T) {
	_, _, _, err := newLayoutRenderReviewer(nil, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, renderreview.ErrBrowserUnavailable)
}
